/**
 * Wrap a sensitive action with a short-lived TOTP step-up grant.
 */
import { ref } from 'vue'
import { extractApiErrorCode } from '@/utils/apiError'

const STEP_UP_REQUIRED = 'STEP_UP_REQUIRED'
const STEP_UP_TOTP_NOT_ENABLED = 'STEP_UP_TOTP_NOT_ENABLED'
const STEP_UP_ADMIN_API_KEY_FORBIDDEN = 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'

/** Thrown when the user dismisses the verification prompt. */
export class StepUpCancelledError extends Error {
  readonly code = 'STEP_UP_CANCELLED'

  constructor() {
    super('step-up verification cancelled by user')
    this.name = 'StepUpCancelledError'
  }
}

export function isStepUpCancelled(err: unknown): boolean {
  return err instanceof StepUpCancelledError
}

function markerOf(err: unknown): string {
  const marker = extractApiErrorCode(err)
  return marker?.startsWith('STEP_UP') ? marker : ''
}

export function isStepUpRequired(err: unknown): boolean {
  return markerOf(err) === STEP_UP_REQUIRED
}

export function isStepUpBlocked(err: unknown): boolean {
  const marker = markerOf(err)
  return marker === STEP_UP_TOTP_NOT_ENABLED || marker === STEP_UP_ADMIN_API_KEY_FORBIDDEN
}

export function stepUpBlockReason(err: unknown): string {
  return markerOf(err)
}

export type StepUpController = ReturnType<typeof useStepUp>

export function useStepUp() {
  const visible = ref(false)
  const blockedReason = ref('')
  let resolver: ((verified: boolean) => void) | null = null

  function prompt(): Promise<boolean> {
    visible.value = true
    return new Promise<boolean>((resolve) => {
      resolver = resolve
    })
  }

  function onVerified() {
    visible.value = false
    resolver?.(true)
    resolver = null
  }

  function onCancel() {
    visible.value = false
    resolver?.(false)
    resolver = null
  }

  async function run<T>(action: () => Promise<T>): Promise<T> {
    try {
      return await action()
    } catch (err) {
      if (isStepUpBlocked(err)) {
        blockedReason.value = markerOf(err)
        throw err
      }
      if (!isStepUpRequired(err)) {
        throw err
      }

      const verified = await prompt()
      if (!verified) {
        throw new StepUpCancelledError()
      }

      // The grant is session-bound; retry the original operation once.
      return await action()
    }
  }

  return {
    visible,
    blockedReason,
    prompt,
    onVerified,
    onCancel,
    run
  }
}
