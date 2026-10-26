import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { OpsDashboardOverview, OpsMetricThresholds } from '@/api/admin/ops'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'

vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: false }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function overview(changes: Partial<OpsDashboardOverview> = {}): OpsDashboardOverview {
  return {
    start_time: '2026-10-05T00:00:00Z', end_time: '2026-10-05T01:00:00Z', platform: '',
    success_count: 100, error_count_total: 0, business_limited_count: 0, error_count_sla: 0,
    request_count_total: 100, request_count_sla: 100, token_consumed: 100,
    sla: 1, error_rate: 0, upstream_error_rate: 0, upstream_error_count_excl_429_529: 0,
    upstream_429_count: 0, upstream_529_count: 0, qps: { current: 1, peak: 1, avg: 1 },
    tps: { current: 1, peak: 1, avg: 1 }, duration: {}, ttft: {}, health_score: 100,
    ...changes
  }
}

const defaults: OpsMetricThresholds = {
  sla_percent_min: 99.5, ttft_p99_ms_max: 500,
  request_error_rate_percent_max: 5, upstream_error_rate_percent_max: 5
}

const wrappers: ReturnType<typeof mount>[] = []
async function render(data: OpsDashboardOverview, thresholds: OpsMetricThresholds | null = defaults) {
  const wrapper = mount(OpsDashboardHeader, {
    shallow: true,
    props: { overview: data, thresholds, platform: '', groupId: null, timeRange: '1h',
      queryMode: 'raw', loading: false, lastUpdated: null }
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount() })

describe('OpsDashboardHeader configured thresholds', () => {
  it('uses custom SLA threshold for both card, diagnostics and progress', async () => {
    const wrapper = await render(overview({ sla: .9 }), { ...defaults, sla_percent_min: 80 })
    const card = wrapper.get('div[style="order: 2;"]')
    expect(card.get('.text-3xl').classes()).toContain('text-green-600')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.slaCritical')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.slaLow')
    expect(card.get('.h-full').attributes('style')).toBe('width: 50%;')
  })

  it('updates existing card and diagnostics when thresholds change', async () => {
    const wrapper = await render(overview({ sla: .99 }), { ...defaults, sla_percent_min: 98 })
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.slaCritical')
    await wrapper.setProps({ thresholds: { ...defaults, sla_percent_min: 99.5 } })
    expect(wrapper.text()).toContain('admin.ops.diagnosis.slaCritical')
    expect(wrapper.get('div[style="order: 2;"] .text-3xl').classes()).toContain('text-red-600')
    expect(wrapper.get('div[style="order: 2;"] .h-full').attributes('style')).toBe('width: 0%;')
  })

  it.each([
    [99.49, 'slaCritical', 'text-red-600'],
    [99.5, 'slaLow', 'text-yellow-600'],
    [99.59, 'slaLow', 'text-yellow-600'],
    [99.6, null, 'text-green-600']
  ] as const)('honors SLA boundary %s', async (sla, message, color) => {
    const wrapper = await render(overview({ sla: sla / 100 }))
    expect(wrapper.get('div[style="order: 2;"] .text-3xl').classes()).toContain(color)
    for (const key of ['slaCritical', 'slaLow']) {
      expect(wrapper.text().includes(`admin.ops.diagnosis.${key}`)).toBe(key === message)
    }
  })

  it('uses configured error and TTFT warning and critical boundaries', async () => {
    const data = overview({ error_rate: .04, upstream_error_rate: .08, ttft: { p99_ms: 800 } })
    const wrapper = await render(data, { ...defaults, request_error_rate_percent_max: 5,
      upstream_error_rate_percent_max: 10, ttft_p99_ms_max: 1000 })
    expect(wrapper.text()).toContain('admin.ops.diagnosis.errorElevated')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.errorHigh')
    expect(wrapper.text()).toContain('admin.ops.diagnosis.upstreamHigh')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.upstreamCritical')
    expect(wrapper.get('div[style="order: 5;"] .text-3xl').classes()).toContain('text-yellow-600')
    expect(wrapper.get('div[style="order: 6;"] .text-3xl').classes()).toContain('text-yellow-600')
    const ttftWarning = wrapper.findAll('.space-y-3 > .flex.gap-3').find(item => item.text().includes('admin.ops.diagnosis.ttftHigh'))
    expect(ttftWarning?.get('svg').classes()).toContain('text-yellow-500')
    await wrapper.setProps({ overview: overview({ error_rate: .05, upstream_error_rate: .10, ttft: { p99_ms: 1000 } }) })
    expect(wrapper.text()).toContain('admin.ops.diagnosis.errorHigh')
    expect(wrapper.text()).toContain('admin.ops.diagnosis.upstreamCritical')
    const ttftCritical = wrapper.findAll('.space-y-3 > .flex.gap-3').find(item => item.text().includes('admin.ops.diagnosis.ttftHigh'))
    expect(ttftCritical?.get('svg').classes()).toContain('text-red-500')
  })

  it('does not alert for configured higher limits or disabled fields', async () => {
    const data = overview({ sla: .95, error_rate: .05, upstream_error_rate: .06, ttft: { p99_ms: 900 } })
    const wrapper = await render(data, { sla_percent_min: 90, request_error_rate_percent_max: 10,
      upstream_error_rate_percent_max: 10, ttft_p99_ms_max: 2000 })
    for (const key of ['slaLow', 'slaCritical', 'ttftHigh', 'errorHigh', 'errorElevated', 'upstreamHigh', 'upstreamCritical']) {
      expect(wrapper.text()).not.toContain(`admin.ops.diagnosis.${key}`)
    }
    await wrapper.setProps({ thresholds: {} })
    expect(wrapper.get('div[style="order: 2;"] .h-full').attributes('style')).toBe('width: 0%;')
    for (const key of ['slaLow', 'slaCritical', 'ttftHigh', 'errorHigh', 'errorElevated', 'upstreamHigh', 'upstreamCritical']) {
      expect(wrapper.text()).not.toContain(`admin.ops.diagnosis.${key}`)
    }
  })

  it('treats sampled zero threshold as explicit for high metrics', async () => {
    const wrapper = await render(overview({ ttft: { p99_ms: 0 } }), {
      sla_percent_min: 0, ttft_p99_ms_max: 0, request_error_rate_percent_max: 0, upstream_error_rate_percent_max: 0
    })
    expect(wrapper.text()).toContain('admin.ops.diagnosis.ttftHigh')
    expect(wrapper.text()).toContain('admin.ops.diagnosis.errorHigh')
    expect(wrapper.text()).toContain('admin.ops.diagnosis.upstreamCritical')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.slaCritical')
    expect(wrapper.get('div[style="order: 2;"] .h-full').attributes('style')).toBe('width: 100%;')
  })

  it('does not fabricate missing samples as zero or a failing SLA', async () => {
    const data = overview({ sla: 0, request_count_sla: 0 })
    Reflect.deleteProperty(data, 'error_rate')
    Reflect.deleteProperty(data, 'upstream_error_rate')
    const wrapper = await render(data, { ...defaults, ttft_p99_ms_max: 0,
      request_error_rate_percent_max: 0, upstream_error_rate_percent_max: 0 })
    expect(wrapper.get('div[style="order: 2;"] .text-3xl').text()).toBe('-')
    expect(wrapper.get('div[style="order: 2;"] .h-full').attributes('style')).toBe('width: 0%;')
    for (const key of ['slaLow', 'slaCritical', 'ttftHigh', 'errorHigh', 'upstreamCritical']) {
      expect(wrapper.text()).not.toContain(`admin.ops.diagnosis.${key}`)
    }
  })

  it.each([[.999, 0], [1, 100]] as const)('bounds progress at threshold100 for SLA %s', async (sla, percent) => {
    const wrapper = await render(overview({ sla }), { ...defaults, sla_percent_min: 100 })
    expect(wrapper.get('div[style="order: 2;"] .h-full').attributes('style')).toBe(`width: ${percent}%;`)
  })

  it('preserves idle diagnostics with zero limits', async () => {
    const wrapper = await render(overview({ request_count_total: 0, request_count_sla: 0,
      qps: { current: 0, peak: 0, avg: 0 } }), { ttft_p99_ms_max: 0, request_error_rate_percent_max: 0, upstream_error_rate_percent_max: 0 })
    expect(wrapper.text()).toContain('admin.ops.diagnosis.idle')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.errorHigh')
    expect(wrapper.text()).not.toContain('admin.ops.diagnosis.upstreamCritical')
  })
})
