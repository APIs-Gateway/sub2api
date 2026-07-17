import { describe, expect, it, vi } from 'vitest'
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
