import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsDashboard from '../OpsDashboard.vue'

const mocks = vi.hoisted(() => ({
  fetchAdminSettings: vi.fn(),
  getAdvancedSettings: vi.fn(),
  getDashboardSnapshotV2: vi.fn(),
  getDashboardOverview: vi.fn(),
  getErrorTrend: vi.fn(),
  getErrorDistribution: vi.fn(),
  getLatencyHistogram: vi.fn(),
  getMetricThresholds: vi.fn(),
  getThroughputTrend: vi.fn(),
  routerReplace: vi.fn(),
  showError: vi.fn(),
  route: { query: {} as Record<string, unknown> }
}))

vi.mock('@/api/admin/ops', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/ops')>('@/api/admin/ops')
  return {
    ...actual,
    opsAPI: {
      ...actual.opsAPI,
      getAdvancedSettings: mocks.getAdvancedSettings,
      getDashboardSnapshotV2: mocks.getDashboardSnapshotV2,
      getDashboardOverview: mocks.getDashboardOverview,
      getErrorTrend: mocks.getErrorTrend,
      getErrorDistribution: mocks.getErrorDistribution,
      getLatencyHistogram: mocks.getLatencyHistogram,
      getMetricThresholds: mocks.getMetricThresholds,
      getThroughputTrend: mocks.getThroughputTrend
    }
  }
})

vi.mock('@/stores', () => ({
  useAdminSettingsStore: () => ({
    fetch: mocks.fetchAdminSettings,
    opsMonitoringEnabled: true,
    opsQueryModeDefault: 'auto'
  }),
  useAppStore: () => ({
    showError: mocks.showError
  })
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({
    replace: mocks.routerReplace
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@vueuse/core', () => ({
  useDebounceFn: <T extends (...args: any[]) => any>(fn: T) => fn,
  useIntervalFn: () => ({
    pause: vi.fn(),
    resume: vi.fn()
  })
}))

const LayoutStub = defineComponent({
  name: 'AppLayout',
  template: '<div><slot /></div>'
})

describe('OpsDashboard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.route.query = {}
    mocks.fetchAdminSettings.mockResolvedValue(undefined)
    mocks.getAdvancedSettings.mockResolvedValue({
      auto_refresh_enabled: false,
      auto_refresh_interval_seconds: 30,
      display_alert_events: false,
      display_openai_token_stats: false
    })
    mocks.getDashboardSnapshotV2.mockResolvedValue({
      error_trend: null,
      overview: null,
      throughput_trend: null
    })
    mocks.getErrorDistribution.mockResolvedValue(null)
    mocks.getDashboardOverview.mockResolvedValue(null)
    mocks.getErrorTrend.mockResolvedValue(null)
    mocks.getLatencyHistogram.mockResolvedValue(null)
    mocks.getMetricThresholds.mockResolvedValue(null)
    mocks.getThroughputTrend.mockResolvedValue(null)
    mocks.routerReplace.mockResolvedValue(undefined)
  })

  it('gives responsive trend cards a fixed height', async () => {
    const wrapper = mount(OpsDashboard, {
      shallow: true,
      global: {
        stubs: {
          AppLayout: LayoutStub
        }
      }
    })

    await flushPromises()

    const cards = wrapper.findAll('.lg\\:grid-cols-4 > div')
    expect(cards).toHaveLength(3)
    expect(cards[0].classes()).toContain('min-h-[360px]')
    expect(cards[1].classes()).toContain('h-[360px]')
    expect(cards[2].classes()).toContain('h-[360px]')

    wrapper.unmount()
  })

  it('does not use legacy fallback endpoints when ops monitoring is disabled', async () => {
    mocks.getDashboardSnapshotV2.mockRejectedValue({ code: 'OPS_DISABLED' })

    const wrapper = mount(OpsDashboard, {
      shallow: true,
      global: { stubs: { AppLayout: LayoutStub } }
    })
    await flushPromises()

    expect(mocks.getDashboardOverview).not.toHaveBeenCalled()
    // One call belongs to the independent five-hour switch trend, not the
    // legacy snapshot fallback.
    expect(mocks.getThroughputTrend).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not dereference a cleared controller after unmount', async () => {
    let resolveSnapshot!: (value: { error_trend: null; overview: null; throughput_trend: null }) => void
    mocks.getDashboardSnapshotV2.mockImplementation(() => new Promise((resolve) => {
      resolveSnapshot = resolve
    }))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const wrapper = mount(OpsDashboard, {
      shallow: true,
      global: { stubs: { AppLayout: LayoutStub } }
    })
    await flushPromises()
    expect(mocks.getDashboardSnapshotV2).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    resolveSnapshot({ error_trend: null, overview: null, throughput_trend: null })
    await flushPromises()

    expect(consoleError.mock.calls.flat().join(' ')).not.toContain("reading 'signal'")
    consoleError.mockRestore()
  })
})
