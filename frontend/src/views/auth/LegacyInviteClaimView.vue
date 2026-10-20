<template>
  <AuthLayout>
    <div class="space-y-6">
      <!-- Title -->
      <div class="text-center">
        <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('legacyInvite.title') }}
        </h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ subtitleText }}
        </p>
      </div>

      <!-- 入口未开放：直接给结论，不再展示任何表单 -->
      <div
        v-if="!isLoadingStatus && !status.enabled"
        class="rounded-xl border border-gray-200 bg-gray-50 p-6 text-center dark:border-dark-700 dark:bg-dark-800/50"
      >
        <div
          class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700"
        >
          <Icon name="lock" size="lg" class="text-gray-400 dark:text-dark-500" />
        </div>
        <h3 class="mt-4 text-lg font-semibold text-gray-800 dark:text-dark-200">
          {{ t('legacyInvite.disabledTitle') }}
        </h3>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ t('legacyInvite.disabledHint') }}
        </p>
      </div>

      <!-- 领取成功 -->
      <div v-else-if="claimedCode" class="space-y-6">
        <div
          class="rounded-xl border border-green-200 bg-green-50 p-6 dark:border-green-800/50 dark:bg-green-900/20"
        >
          <div class="flex flex-col items-center gap-4 text-center">
            <div
              class="flex h-12 w-12 items-center justify-center rounded-full bg-green-100 dark:bg-green-800/50"
            >
              <Icon name="checkCircle" size="lg" class="text-green-600 dark:text-green-400" />
            </div>
            <div class="w-full">
              <h3 class="text-lg font-semibold text-green-800 dark:text-green-200">
                {{ t('legacyInvite.successTitle') }}
              </h3>
              <p class="mt-2 text-sm text-green-700 dark:text-green-300">
                {{ alreadyClaimed ? t('legacyInvite.alreadyClaimedHint') : t('legacyInvite.successHint') }}
              </p>

              <div
                class="mt-4 flex items-center gap-2 rounded-lg border border-green-300 bg-white px-3 py-2.5 dark:border-green-700 dark:bg-dark-800"
              >
                <code
                  class="flex-1 select-all break-all text-left font-mono text-base font-semibold text-gray-900 dark:text-white"
                >
                  {{ claimedCode }}
                </code>
                <button
                  type="button"
                  class="btn btn-secondary shrink-0 px-3 py-1.5 text-sm"
                  @click="copyCode"
                >
                  {{ isCopied ? t('legacyInvite.copied') : t('legacyInvite.copy') }}
                </button>
              </div>

              <p v-if="expiresAtText" class="mt-2 text-xs text-green-700 dark:text-green-300">
                {{ t('legacyInvite.expiresAt', { date: expiresAtText }) }}
              </p>
            </div>
          </div>
        </div>

        <router-link to="/register" class="btn btn-primary w-full">
          {{ t('legacyInvite.goRegister') }}
        </router-link>
      </div>

      <!-- 领取表单 -->
      <form v-else-if="!isLoadingStatus" class="space-y-5" @submit.prevent="handleClaim">
        <!-- 主站邮箱 -->
        <div>
          <label for="legacy-email" class="input-label">
            {{ t('legacyInvite.emailLabel') }}
          </label>
          <div class="relative">
            <div class="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3.5">
              <Icon name="mail" size="md" class="text-gray-400 dark:text-dark-500" />
            </div>
            <input
              id="legacy-email"
              v-model="formData.email"
              type="email"
              required
              autofocus
              autocomplete="email"
              :disabled="isSending || isClaiming"
              class="input pl-11"
              :placeholder="t('legacyInvite.emailPlaceholder')"
            />
          </div>
        </div>

        <!-- Turnstile：发信接口对外开放，人机校验不能省 -->
        <div v-if="turnstileEnabled && turnstileSiteKey && !isCodeSent">
          <TurnstileWidget
            ref="turnstileRef"
            :site-key="turnstileSiteKey"
            @verify="onTurnstileVerify"
            @expire="onTurnstileExpire"
            @error="onTurnstileError"
          />
        </div>

        <!-- 发送验证码 -->
        <button
          type="button"
          class="btn btn-secondary w-full"
          :disabled="isSending || countdown > 0 || (turnstileEnabled && !turnstileToken)"
          @click="handleSendCode"
        >
          {{ sendCodeButtonText }}
        </button>

        <!-- 验证码：发过之后才出现，避免用户对着空表单猜流程 -->
        <div v-if="isCodeSent">
          <label for="legacy-code" class="input-label">
            {{ t('legacyInvite.codeLabel') }}
          </label>
          <input
            id="legacy-code"
            v-model="formData.code"
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="6"
            :disabled="isClaiming"
            class="input tracking-widest"
            :placeholder="t('legacyInvite.codePlaceholder')"
          />
        </div>

        <button
          v-if="isCodeSent"
          type="submit"
          class="btn btn-primary w-full"
          :disabled="isClaiming || formData.code.trim().length === 0"
        >
          {{ isClaiming ? t('legacyInvite.claiming') : t('legacyInvite.claim') }}
        </button>
      </form>
    </div>

    <template #footer>
      <p class="text-gray-500 dark:text-dark-400">
        {{ t('legacyInvite.haveAccount') }}
        <router-link
          to="/login"
          class="font-medium text-primary-600 transition-colors hover:text-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
        >
          {{ t('auth.signIn') }}
        </router-link>
      </p>
    </template>
  </AuthLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AuthLayout } from '@/components/layout'
import Icon from '@/components/icons/Icon.vue'
import TurnstileWidget from '@/components/TurnstileWidget.vue'
import { useAppStore } from '@/stores'
import { getPublicSettings } from '@/api/auth'
import {
  claimLegacyInvite,
  getLegacyInviteStatus,
  sendLegacyInviteCode,
  type LegacyInviteStatus
} from '@/api/legacyInvite'

const { t, locale } = useI18n()
const appStore = useAppStore()

// ==================== State ====================

const isLoadingStatus = ref<boolean>(true)
const isSending = ref<boolean>(false)
const isClaiming = ref<boolean>(false)
const isCodeSent = ref<boolean>(false)
const isCopied = ref<boolean>(false)

const status = reactive<LegacyInviteStatus>({ enabled: false, min_paid_amount: 0, min_usage_cost: 0 })

const claimedCode = ref<string>('')
const alreadyClaimed = ref<boolean>(false)
const expiresAt = ref<string>('')

const turnstileEnabled = ref<boolean>(false)
const turnstileSiteKey = ref<string>('')
const turnstileRef = ref<InstanceType<typeof TurnstileWidget> | null>(null)
const turnstileToken = ref<string>('')

const countdown = ref<number>(0)
let countdownTimer: ReturnType<typeof setInterval> | null = null

const formData = reactive({
  email: '',
  code: ''
})

// ==================== Computed ====================

// 用量口径开着的时候必须把两条门槛都写出来：付费没达标的重度用户如果只看到
// 「满 {amount} 元」，会以为自己没资格而直接走人。
const subtitleText = computed(() =>
  status.min_usage_cost > 0
    ? t('legacyInvite.subtitleWithUsage', {
        amount: formatAmount(status.min_paid_amount),
        usage: formatAmount(status.min_usage_cost)
      })
    : t('legacyInvite.subtitle', { amount: formatAmount(status.min_paid_amount) })
)

const sendCodeButtonText = computed(() => {
  if (isSending.value) return t('legacyInvite.sending')
  if (countdown.value > 0) return t('legacyInvite.resendIn', { seconds: countdown.value })
  return isCodeSent.value ? t('legacyInvite.resendCode') : t('legacyInvite.sendCode')
})

const expiresAtText = computed(() => {
  if (!expiresAt.value) return ''
  const parsed = new Date(expiresAt.value)
  if (Number.isNaN(parsed.getTime())) return ''
  return parsed.toLocaleDateString(locale.value)
})

// ==================== Lifecycle ====================

onMounted(async () => {
  try {
    const [inviteStatus, settings] = await Promise.all([
      getLegacyInviteStatus(),
      getPublicSettings().catch(() => null)
    ])
    status.enabled = inviteStatus.enabled
    status.min_paid_amount = inviteStatus.min_paid_amount
    status.min_usage_cost = inviteStatus.min_usage_cost ?? 0
    if (settings) {
      turnstileEnabled.value = settings.turnstile_enabled
      turnstileSiteKey.value = settings.turnstile_site_key || ''
    }
  } catch (error) {
    console.error('Failed to load legacy invite status:', error)
    // 状态查不到时保持关闭态：与其让用户填完表单才失败，不如先说"暂未开放"
    status.enabled = false
  } finally {
    isLoadingStatus.value = false
  }
})

onBeforeUnmount(() => {
  stopCountdown()
})

// ==================== Turnstile ====================

function onTurnstileVerify(token: string): void {
  turnstileToken.value = token
}

function onTurnstileExpire(): void {
  turnstileToken.value = ''
}

function onTurnstileError(): void {
  turnstileToken.value = ''
}

// ==================== Helpers ====================

/** 门槛金额是整数时不显示小数点，避免出现「满 300.00 元」这种别扭文案 */
function formatAmount(amount: number): string {
  return Number.isInteger(amount) ? String(amount) : amount.toFixed(2)
}

/** 「不达标」得把当前生效的口径都列出来，否则用户不知道还有第二条路可走 */
function notEligibleMessage(): string {
  if (status.min_usage_cost > 0) {
    return t('legacyInvite.errors.notEligibleWithUsage', {
      amount: formatAmount(status.min_paid_amount),
      usage: formatAmount(status.min_usage_cost)
    })
  }
  return t('legacyInvite.errors.notEligible', { amount: formatAmount(status.min_paid_amount) })
}

function startCountdown(seconds: number): void {
  stopCountdown()
  countdown.value = seconds
  countdownTimer = setInterval(() => {
    countdown.value -= 1
    if (countdown.value <= 0) {
      stopCountdown()
    }
  }, 1000)
}

function stopCountdown(): void {
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
  countdown.value = 0
}

function isValidEmail(email: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)
}

/**
 * 把后端的错误 reason 翻成本地化文案。
 * 认不出来的 reason 一律走通用兜底，不要把英文原文直接甩给用户。
 */
function resolveErrorMessage(error: unknown, fallbackKey: string): string {
  const err = error as { code?: string; message?: string }
  switch (err?.code) {
    case 'LEGACY_INVITE_DISABLED':
      return t('legacyInvite.errors.disabled')
    case 'LEGACY_INVITE_NOT_ELIGIBLE':
      return notEligibleMessage()
    case 'LEGACY_INVITE_ALREADY_CLAIMED':
      return t('legacyInvite.errors.alreadyClaimed')
    case 'LEGACY_INVITE_LOOKUP_FAILED':
      return t('legacyInvite.errors.lookupFailed')
    case 'INVALID_VERIFY_CODE':
      return t('legacyInvite.errors.invalidCode')
    case 'VERIFY_CODE_MAX_ATTEMPTS':
      return t('legacyInvite.errors.tooManyAttempts')
    case 'VERIFY_CODE_TOO_FREQUENT':
      return t('legacyInvite.errors.tooFrequent')
    case 'INVALID_EMAIL':
      return t('legacyInvite.errors.invalidEmail')
    default:
      return t(fallbackKey)
  }
}

// ==================== Handlers ====================

async function handleSendCode(): Promise<void> {
  if (!isValidEmail(formData.email.trim())) {
    appStore.showError(t('legacyInvite.errors.invalidEmail'))
    return
  }

  isSending.value = true
  try {
    const result = await sendLegacyInviteCode({
      email: formData.email.trim(),
      turnstile_token: turnstileEnabled.value ? turnstileToken.value : undefined
    })
    isCodeSent.value = true
    startCountdown(result.countdown || 60)
    appStore.showSuccess(t('legacyInvite.codeSent'))
  } catch (error: unknown) {
    // Turnstile token 是一次性的，失败后必须重置，否则重试必然再失败一次
    if (turnstileRef.value) {
      turnstileRef.value.reset()
      turnstileToken.value = ''
    }
    appStore.showError(resolveErrorMessage(error, 'legacyInvite.errors.sendFailed'))
  } finally {
    isSending.value = false
  }
}

async function handleClaim(): Promise<void> {
  if (!formData.code.trim()) {
    return
  }

  isClaiming.value = true
  try {
    const result = await claimLegacyInvite({
      email: formData.email.trim(),
      code: formData.code.trim()
    })
    claimedCode.value = result.invitation_code
    alreadyClaimed.value = result.already_claimed
    expiresAt.value = result.expires_at || ''
    stopCountdown()
  } catch (error: unknown) {
    appStore.showError(resolveErrorMessage(error, 'legacyInvite.errors.generic'))
  } finally {
    isClaiming.value = false
  }
}

async function copyCode(): Promise<void> {
  try {
    await navigator.clipboard.writeText(claimedCode.value)
    isCopied.value = true
    setTimeout(() => {
      isCopied.value = false
    }, 2000)
  } catch {
    appStore.showError(t('legacyInvite.errors.copyFailed'))
  }
}
</script>
