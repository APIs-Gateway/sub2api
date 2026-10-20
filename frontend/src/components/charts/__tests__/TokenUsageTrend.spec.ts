import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { watchEffect } from 'vue'

import TokenUsageTrend from '../TokenUsageTrend.vue'

const messages: Record<string, string> = {
  'admin.dashboard.tokenUsageTrend': 'Token Usage Trend',
  'admin.dashboard.noDataAvailable': 'No data available',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    setup(props: { options: unknown }) {
      const g = globalThis as any
      watchEffect(() => {
        g.__lastLineOptions = props.options
      })
      return {}
    },
    template: '<div class="chart-data">{{ JSON.stringify(data) }}</div>',
  },
}))

describe('TokenUsageTrend', () => {
  it('calculates cache hit rate against all prompt tokens', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 500,
            output_tokens: 100,
            cache_creation_tokens: 0,
            cache_read_tokens: 1500,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 1500 / (500 + 1500 + 0) * 100 = 75%
    expect(hitRateDataset.data[0]).toBe(75)
  })

  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0,
            actual_cost: 0,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    expect(hitRateDataset.data[0]).toBe(0)
  })

  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 200,
            output_tokens: 50,
            cache_creation_tokens: 300,
            cache_read_tokens: 500,
            cost: 0.02,
            actual_cost: 0.01,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 500 / (200 + 500 + 300) * 100 = 50%
    expect(hitRateDataset.data[0]).toBe(50)
  })

  describe('token formatting', () => {
    const point = {
      date: '2026-05-08',
      requests: 1,
      input_tokens: 1,
      output_tokens: 1,
      cache_creation_tokens: 0,
      cache_read_tokens: 0,
      cost: 0.0123,
      actual_cost: 0.5,
    }

    function opts(props: Record<string, unknown>) {
      mount(TokenUsageTrend, { props: { trendData: [point], ...props }, global: { stubs: { LoadingSpinner: true } } })
      return (globalThis as any).__lastLineOptions
    }

    it('admin keeps the 2-decimal axis labels and the default chart font', () => {
      const o = opts({})
      expect(o.scales.y.ticks.callback(1_230_000)).toBe('1.23M')
      expect(o.font).toBeUndefined()
    })

    it('user side uses 1-decimal axis labels, full tooltip counts and the site font', () => {
      const o = opts({ unifiedTypography: true })
      expect(o.scales.y.ticks.callback(1_230_000)).toBe('1.2M')
      expect(o.plugins.tooltip.callbacks.label({ dataset: { label: 'Input', yAxisID: 'y' }, raw: 1_234_567 })).toBe('Input: 1,234,567')
      expect(o.font.family).toContain('Space Grotesk')
    })
  })

  describe('tooltip footer', () => {
    const point = {
      date: '2026-05-08',
      requests: 1,
      input_tokens: 1,
      output_tokens: 1,
      cache_creation_tokens: 0,
      cache_read_tokens: 0,
      cost: 0.0123,
      actual_cost: 0.5,
    }

    function footer(props: Record<string, unknown>): string {
      mount(TokenUsageTrend, { props: { trendData: [point], ...props }, global: { stubs: { LoadingSpinner: true } } })
      const opts = (globalThis as any).__lastLineOptions
      return opts.plugins.tooltip.callbacks.footer([{ dataIndex: 0 }])
    }

    it('keeps the USD standard price when no formatter is passed (admin)', () => {
      expect(footer({})).toBe('Actual: $0.500 | Standard: $0.012')
    })

    it('unified typography (user side) uses the shared money rule, admin default unchanged', () => {
      expect(footer({ unifiedTypography: true })).toBe('Actual: $0.50 | Standard: $0.0123')
    })

    it('uses the user-side formatter for the standard price', () => {
      expect(footer({ formatActualCost: () => '¥0.04', formatStandardCost: () => '¥0.09' })).toBe(
        'Actual: ¥0.04 | Standard: ¥0.09',
      )
    })

    it('drops the standard price when the formatter cannot provide one', () => {
      expect(footer({ formatActualCost: () => '¥0.04', formatStandardCost: () => null })).toBe('Actual: ¥0.04')
    })
  })
})
