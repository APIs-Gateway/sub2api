import { describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import {
  isStepUpBlocked,
  isStepUpCancelled,
  isStepUpRequired,
  stepUpBlockReason,
  StepUpCancelledError,
  useStepUp
} from '../useStepUp'

describe('useStepUp error classification', () => {
  it('detects step-up codes from the shared API error shapes', () => {
    expect(isStepUpRequired({ status: 403, code: 'STEP_UP_REQUIRED' })).toBe(true)
    expect(isStepUpRequired({ status: 403, reason: 'STEP_UP_REQUIRED' })).toBe(true)
    expect(isStepUpRequired({ response: { data: { code: 'STEP_UP_REQUIRED' } } })).toBe(true)
    expect(isStepUpRequired({ status: 500, code: 'INTERNAL' })).toBe(false)
    expect(isStepUpRequired(null)).toBe(false)
  })

  it('detects blocked step-up errors', () => {
    expect(isStepUpBlocked({ code: 'STEP_UP_TOTP_NOT_ENABLED' })).toBe(true)
    expect(isStepUpBlocked({ reason: 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' })).toBe(true)
    expect(isStepUpBlocked({ code: 'STEP_UP_REQUIRED' })).toBe(false)
  })

  it('returns the block reason marker', () => {
    expect(stepUpBlockReason({ reason: 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' })).toBe('STEP_UP_ADMIN_API_KEY_FORBIDDEN')
    expect(stepUpBlockReason({ code: 'OTHER' })).toBe('')
  })
})

describe('useStepUp concurrent prompts', () => {
  it.each([
    ['verification', true],
    ['cancellation', false],
  ] as const)('settles all waiters after %s and starts a fresh prompt', async (_outcome, verified) => {
    const stepUp = useStepUp()
    const first = stepUp.prompt()
    const second = stepUp.prompt()
    expect(second).toBe(first)
    expect(stepUp.visible.value).toBe(true)

    if (verified) stepUp.onVerified()
    else stepUp.onCancel()

    await expect(first).resolves.toBe(verified)
    await expect(second).resolves.toBe(verified)
    expect(stepUp.visible.value).toBe(false)

    const next = stepUp.prompt()
    expect(next).not.toBe(first)
    expect(stepUp.visible.value).toBe(true)
    stepUp.onCancel()
    await expect(next).resolves.toBe(false)
  })

  it('retries each concurrent sensitive action once after shared verification', async () => {
    const stepUp = useStepUp()
    const calls = [0, 0]
    const action = (index: number) => async () => {
      calls[index] += 1
      if (calls[index] === 1) throw { status: 403, code: 'STEP_UP_REQUIRED' }
      return `action-${index}`
    }

    const first = stepUp.run(action(0))
    const second = stepUp.run(action(1))
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onVerified()

    await expect(Promise.all([first, second])).resolves.toEqual(['action-0', 'action-1'])
    expect(calls).toEqual([2, 2])
    expect(stepUp.visible.value).toBe(false)
  })

  it('cancels each concurrent sensitive action without retrying it', async () => {
    const stepUp = useStepUp()
    const calls = [0, 0]
    const action = (index: number) => async () => {
      calls[index] += 1
      throw { status: 403, code: 'STEP_UP_REQUIRED' }
    }

    const first = stepUp.run(action(0))
    const second = stepUp.run(action(1))
    const results = Promise.allSettled([first, second])
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onCancel()

    const settled = await results
    expect(settled).toHaveLength(2)
    for (const result of settled) {
      expect(result.status).toBe('rejected')
      if (result.status === 'rejected') expect(result.reason).toBeInstanceOf(StepUpCancelledError)
    }
    expect(calls).toEqual([1, 1])
    expect(stepUp.visible.value).toBe(false)
  })

  it('cancels both waiting actions when the owning view scope is disposed', async () => {
    const scope = effectScope()
    const stepUp = scope.run(() => useStepUp())!
    const calls = [0, 0]
    const action = (index: number) => async () => {
      calls[index] += 1
      throw { status: 403, code: 'STEP_UP_REQUIRED' }
    }

    const first = stepUp.run(action(0))
    const second = stepUp.run(action(1))
    const results = Promise.allSettled([first, second])
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    scope.stop()

    for (const result of await results) {
      expect(result.status).toBe('rejected')
      if (result.status === 'rejected') expect(result.reason).toBeInstanceOf(StepUpCancelledError)
    }
    expect(calls).toEqual([1, 1])
    expect(stepUp.visible.value).toBe(false)
  })

  it('cancels a step-up response that arrives after its view scope is disposed', async () => {
    const scope = effectScope()
    const stepUp = scope.run(() => useStepUp())!
    let rejectAction!: (reason: unknown) => void
    const action = vi.fn(() => new Promise<string>((_resolve, reject) => {
      rejectAction = reject
    }))
    const pending = stepUp.run(action)

    scope.stop()
    rejectAction({ status: 403, code: 'STEP_UP_REQUIRED' })

    await expect(pending).rejects.toBeInstanceOf(StepUpCancelledError)
    expect(action).toHaveBeenCalledTimes(1)
    expect(stepUp.visible.value).toBe(false)
  })
})

describe('useStepUp.run', () => {
  it('returns the action result directly on success', async () => {
    const stepUp = useStepUp()
    await expect(stepUp.run(async () => 42)).resolves.toBe(42)
    expect(stepUp.visible.value).toBe(false)
  })

  it('rethrows non-step-up and blocked errors without prompting', async () => {
    const stepUp = useStepUp()
    const internalError = { status: 500, code: 'INTERNAL' }
    await expect(stepUp.run(async () => { throw internalError })).rejects.toBe(internalError)

    const blockedError = { status: 403, code: 'STEP_UP_TOTP_NOT_ENABLED' }
    await expect(stepUp.run(async () => { throw blockedError })).rejects.toBe(blockedError)
    expect(stepUp.visible.value).toBe(false)
  })

  it('prompts on STEP_UP_REQUIRED and retries after verification', async () => {
    const stepUp = useStepUp()
    let calls = 0
    const action = async () => {
      calls += 1
      if (calls === 1) throw { status: 403, code: 'STEP_UP_REQUIRED' }
      return 'ok'
    }

    const promise = stepUp.run(action)
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onVerified()

    await expect(promise).resolves.toBe('ok')
    expect(calls).toBe(2)
  })

  it('throws a cancellation sentinel when the prompt is cancelled', async () => {
    const stepUp = useStepUp()
    const promise = stepUp.run(async () => {
      throw { status: 403, code: 'STEP_UP_REQUIRED' }
    })

    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onCancel()

    await expect(promise).rejects.toBeInstanceOf(StepUpCancelledError)
    expect(isStepUpCancelled(new StepUpCancelledError())).toBe(true)
  })
})
