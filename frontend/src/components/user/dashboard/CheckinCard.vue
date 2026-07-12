<template>
  <!-- 每日签到条：仅在功能开启时显示 -->
  <div v-if="status?.enabled" class="card p-4">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <!-- 左：标题 + 说明 -->
      <div>
        <div class="flex items-center gap-2">
          <h3 class="font-serif text-base text-gray-900 dark:text-white">{{ t('checkin.title') }}</h3>
          <span
            v-if="status.bonus_available > 0"
            class="rounded-full border border-primary-300 px-2 py-0.5 text-xs font-medium text-primary-600 dark:border-primary-700 dark:text-primary-300"
          >
            {{ t('checkin.bonusBadge', { n: status.bonus_available }) }}
          </span>
        </div>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ subtitle }}</p>
      </div>

      <!-- 右：进度 + 领取按钮 -->
      <div class="flex items-center gap-4">
        <!-- 当日活跃度未达标：只做笼统提示，不暴露具体计算口径。 -->
        <div v-if="notActive" class="hidden text-right sm:block">
          <p class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('checkin.activityNotMet') }}
          </p>
          <p class="text-[11px] text-gray-400 dark:text-gray-500">
            {{ t('checkin.activityHint') }}
          </p>
        </div>
        <div v-else-if="status.spend_per_extra > 0" class="hidden text-right sm:block">
          <p class="font-mono text-xs text-gray-500 dark:text-gray-400">
            {{ t('checkin.todaySpend') }}
            <span class="text-gray-700 dark:text-gray-300">${{ formatUsd(status.today_spend) }}</span>
          </p>
          <p class="text-[11px] text-gray-400 dark:text-gray-500">
            {{ t('checkin.nextBonusHint', { amount: formatUsd(status.spend_to_next_bonus) }) }}
          </p>
        </div>
        <button
          class="btn btn-primary whitespace-nowrap"
          :disabled="claimDisabled"
          @click="claim"
        >
          <span v-if="claiming">{{ t('checkin.claiming') }}</span>
          <span v-else-if="status.can_claim">{{ claimLabel }}</span>
          <span v-else-if="notActive">{{ t('checkin.notActiveButton') }}</span>
          <span v-else>{{ t('checkin.doneToday') }}</span>
        </button>
      </div>
    </div>

    <!-- Turnstile 人机校验：开启且仍可领取时显示 -->
    <div v-if="showTurnstile" class="mt-3 flex justify-end">
      <TurnstileWidget
        ref="turnstileRef"
        :site-key="turnstileSiteKey"
        size="flexible"
        @verify="onTurnstileVerify"
        @expire="onTurnstileReset"
        @error="onTurnstileReset"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { getCheckinStatus, claimCheckin, type CheckinStatus } from '@/api/user'
import TurnstileWidget from '@/components/TurnstileWidget.vue'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const status = ref<CheckinStatus | null>(null)
const claiming = ref(false)

const turnstileRef = ref<InstanceType<typeof TurnstileWidget> | null>(null)
const turnstileToken = ref('')
const turnstileEnabled = computed(() => !!appStore.cachedPublicSettings?.turnstile_enabled)
const turnstileSiteKey = computed(() => appStore.cachedPublicSettings?.turnstile_site_key || '')
const showTurnstile = computed(
  () => !!status.value?.can_claim && turnstileEnabled.value && !!turnstileSiteKey.value
)
const claimDisabled = computed(
  () =>
    !status.value?.can_claim ||
    claiming.value ||
    (turnstileEnabled.value && !!turnstileSiteKey.value && !turnstileToken.value)
)

const formatUsd = (v: number) => (Number.isFinite(v) ? v : 0).toFixed(2)

// notActive：基础签到被"当日活跃度门槛"拦住（未领、当日 Token 未达标、且无其他可领项）。
const notActive = computed(() => {
  const s = status.value
  return !!s && !s.can_claim && !s.daily_claimed && !s.tokens_met && s.min_tokens > 0
})

const subtitle = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.can_claim) {
    return t('checkin.rewardRange', {
      min: formatUsd(s.amount_min),
      max: formatUsd(s.amount_max)
    })
  }
  if (notActive.value) {
    return t('checkin.notActiveEnough')
  }
  return t('checkin.doneSubtitle')
})

const claimLabel = computed(() => {
  const s = status.value
  if (!s) return t('checkin.claimDaily')
  return s.daily_available ? t('checkin.claimDaily') : t('checkin.claimBonus')
})

function onTurnstileVerify(token: string) {
  turnstileToken.value = token
}

function onTurnstileReset() {
  turnstileToken.value = ''
}

function resetTurnstile() {
  turnstileToken.value = ''
  if (turnstileRef.value) {
    turnstileRef.value.reset()
  }
}

async function load() {
  try {
    const nextStatus = await getCheckinStatus()
    status.value = nextStatus
    return nextStatus
  } catch (error) {
    console.warn('Failed to load checkin status:', error)
    status.value = null
    return null
  }
}

function claimAdvanced(before: CheckinStatus | null, after: CheckinStatus | null) {
  if (!before || !after) return false
  return (
    (!before.daily_claimed && after.daily_claimed) ||
    after.bonus_claimed_today > before.bonus_claimed_today
  )
}

async function claim() {
  if (claimDisabled.value) return
  const beforeClaim = status.value
  claiming.value = true
  try {
    const res = await claimCheckin(turnstileEnabled.value ? turnstileToken.value : undefined)
    if (res.status) {
      status.value = res.status
    } else {
      await load()
    }
    appStore.showSuccess(t('checkin.claimedToast', { amount: formatUsd(res.amount) }))
    // 签到领取已经在服务端提交。余额刷新是后续同步动作，即使失败也不能把
    // 已成功的签到误报为失败；用户信息会在下一次常规刷新时再次同步。
    void authStore.refreshUser().catch((error) => {
      console.warn('Failed to refresh user after successful checkin:', error)
    })
  } catch (error) {
    console.warn('Checkin claim failed:', error)
    // POST 可能已在服务端提交，只是成功响应在弱网或重启时丢失。先回查
    // 原子签到状态；确认领取计数前进时按成功处理，绝不重放 POST。
    const reconciledStatus = await load()
    if (claimAdvanced(beforeClaim, reconciledStatus)) {
      appStore.showSuccess(t('checkin.claimRecoveredToast'))
      void authStore.refreshUser().catch((refreshError) => {
        console.warn('Failed to refresh user after reconciled checkin:', refreshError)
      })
    } else {
      appStore.showError(t('checkin.claimFailed'))
    }
  } finally {
    claiming.value = false
    // Turnstile token 单次有效，无论成败都重置以便下次领取
    resetTurnstile()
  }
}

onMounted(load)
</script>
