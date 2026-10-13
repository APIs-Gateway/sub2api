<template>
  <AppLayout>
    <div class="space-y-6">
      <DashboardLoadError
        v-if="loadError"
        :title="t('dashboard.loadFailed')"
        :description="t('dashboard.loadFailedDescription')"
        :retry-label="t('common.refresh')"
        :loading="loading"
        @retry="loadStats"
      />
      <div v-if="loading && !stats" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <template v-else-if="stats">
        <UserDashboardStats :stats="stats" :balance="user?.balance || 0" :is-simple="authStore.isSimpleMode" :platform-quotas="platformQuotas" />
        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <!-- 概览补充块：需要处理 + API 接入卡 -->
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <UserDashboardNeedsAttention :api-keys="apiKeys" :estimated-days="estimatedDays" />
          <UserDashboardApiEntry />
        </div>
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import DashboardLoadError from '@/components/common/DashboardLoadError.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import UserDashboardNeedsAttention from '@/components/user/dashboard/UserDashboardNeedsAttention.vue'
import UserDashboardApiEntry from '@/components/user/dashboard/UserDashboardApiEntry.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem, ApiKey } from '@/types'
import { getMyPlatformQuotas } from '@/api/user'
import keysAPI from '@/api/keys'

const { t } = useI18n()
const authStore = useAuthStore()
const user = computed(() => authStore.user)

const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const loadError = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)
const apiKeys = ref<ApiKey[]>([])

const formatLD = (d: Date) => d.toISOString().split('T')[0]
const startDate = ref(formatLD(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatLD(new Date()))
const granularity = ref('day')

/**
 * 余额预计续航天数：取最近 7 个日桶的实际扣费求日均，然后 balance ÷ 日均。
 * 返回 -1 表示无法计算（无消耗或数据不足）；999 以上显示 >999。
 * 只统计 actual_cost（余额扣费+订阅扣费都算），因为我们拿不到按 billing_type 拆分的
 * 日粒度数据；续航偏保守，订阅用户看到小数字时自然知道余额不是主要扣费来源。
 */
const estimatedDays = computed<number>(() => {
  const balance = user.value?.balance ?? 0
  if (balance <= 0) return -1
  // 取 day 粒度最近 7 个点
  const dayPoints = [...trendData.value]
    .filter((p) => p.actual_cost >= 0)
    .slice(-7)
  if (dayPoints.length === 0) return -1
  const totalCost = dayPoints.reduce((sum, p) => sum + (p.actual_cost ?? 0), 0)
  if (totalCost < 0.0001) return 999
  const dailyAvg = totalCost / dayPoints.length
  const days = Math.round(balance / dailyAvg)
  return Math.min(days, 9999)
})

const loadStats = async () => {
  loading.value = true
  loadError.value = false
  try {
    const [dashboardStats] = await Promise.all([
      usageAPI.getDashboardStats(),
      authStore.refreshUser().catch((error) => { console.warn('Failed to refresh user on dashboard load:', error) }),
    ])
    stats.value = dashboardStats
  } catch (error) {
    loadError.value = true
    console.error('Failed to load dashboard stats:', error)
  } finally {
    loading.value = false
  }
}

const loadCharts = async () => {
  loadingCharts.value = true
  try {
    const res = await Promise.all([
      usageAPI.getDashboardTrend({ start_date: startDate.value, end_date: endDate.value, granularity: granularity.value as any }),
      usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value }),
    ])
    trendData.value = res[0].trend || []
    modelStats.value = res[1].models || []
  } catch (error) {
    console.error('Failed to load charts:', error)
  } finally {
    loadingCharts.value = false
  }
}

const loadRecent = async () => {
  loadingUsage.value = true
  try {
    const res = await usageAPI.getByDateRange(startDate.value, endDate.value)
    recentUsage.value = res.items.slice(0, 5)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    loadingUsage.value = false
  }
}

const loadPlatformQuotas = async () => {
  try {
    const data = await getMyPlatformQuotas()
    platformQuotas.value = data.platform_quotas ?? []
  } catch (error) {
    console.warn('Failed to load platform quotas:', error)
    platformQuotas.value = []
  }
}

const loadApiKeys = async () => {
  try {
    const res = await keysAPI.list(1, 100)
    apiKeys.value = res.items ?? []
  } catch (error) {
    console.warn('Failed to load API keys for attention check:', error)
    apiKeys.value = []
  }
}

const refreshAll = () => {
  loadStats()
  loadCharts()
  loadRecent()
  loadPlatformQuotas()
  loadApiKeys()
}

onMounted(() => { refreshAll() })
</script>
