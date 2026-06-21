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

      <!-- 右：消费进度 + 领取按钮 -->
      <div class="flex items-center gap-4">
        <div v-if="status.spend_per_extra > 0" class="hidden text-right sm:block">
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

const subtitle = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.can_claim) {
    return t('checkin.rewardRange', {
      min: formatUsd(s.amount_min),
      max: formatUsd(s.amount_max)
    })
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
    status.value = await getCheckinStatus()
  } catch (error) {
    console.warn('Failed to load checkin status:', error)
    status.value = null
  }
}

async function claim() {
  if (claimDisabled.value) return
  claiming.value = true
  try {
    const res = await claimCheckin(turnstileEnabled.value ? turnstileToken.value : undefined)
    if (res.status) {
      status.value = res.status
    } else {
      await load()
    }
    // 余额已变动，刷新用户信息让仪表盘余额同步更新
    await authStore.refreshUser()
    appStore.showSuccess(t('checkin.claimedToast', { amount: formatUsd(res.amount) }))
  } catch (error) {
    console.warn('Checkin claim failed:', error)
    appStore.showError(t('checkin.claimFailed'))
    await load()
  } finally {
    claiming.value = false
    // Turnstile token 单次有效，无论成败都重置以便下次领取
    resetTurnstile()
  }
}

onMounted(load)
</script>
