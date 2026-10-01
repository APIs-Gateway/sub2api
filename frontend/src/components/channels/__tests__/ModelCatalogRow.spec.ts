import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelCatalogRow from '../ModelCatalogRow.vue'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { buildCatalog } from '@/utils/modelCatalog'
import type { UserAvailableChannel } from '@/api/channels'

const publicSettings: { value: Record<string, unknown> | null } = { value: null }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({ activeSubscriptions: [] }),
}))
vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string>) =>
      ({
        'availableChannels.pricing.inputPrice': 'Input',
        'availableChannels.pricing.outputPrice': 'Output',
        'availableChannels.pricing.cacheReadPrice': 'Cache read',
        'availableChannels.pricing.perRequestPrice': 'Per request',
        'availableChannels.pricing.perMillion': 'per 1M',
        'availableChannels.pricing.perRequest': 'per request',
        'availableChannels.lowest': 'Lowest',
        'availableChannels.planPrice': 'Plan price',
        'availableChannels.yourPlanPrice': 'Your plan price',
        'availableChannels.tierUpTo': `Up to ${params?.n}`,
        'availableChannels.tierAbove': `Over ${params?.n}`,
        'availableChannels.rateTooltip': 'rate tip',
        'availableChannels.rateTooltipCustom': `custom tip ${params?.base}`,
      })[key] ?? key,
  }),
}))

const g = (id: number, name: string, rate: number) => ({
  id, name, platform: 'openai', subscription_type: 'standard', rate_multiplier: rate, is_exclusive: false,
})

const tokenPricing = {
  billing_mode: 'token' as const,
  input_price: 1e-6,
  output_price: 4e-6,
  cache_read_price: 1e-7,
  cache_write_price: null,
  image_output_price: null,
  per_request_price: null,
  intervals: [],
}

function model(over: Partial<typeof tokenPricing> & { intervals?: unknown[] } = {}, rates: Record<number, number> = {}) {
  const channels: UserAvailableChannel[] = [
    {
      name: 'c',
      description: '',
      platforms: [
        {
          platform: 'openai',
          groups: [g(1, 'Stable', 1.3), g(2, 'Budget', 0.65)],
          supported_models: [{ name: 'gpt-x', platform: 'openai', pricing: { ...tokenPricing, ...over } as never }],
        },
      ],
    },
  ]
  return buildCatalog(channels, rates)[0]
}

const unit = { min: 0.05, max: 0.1, exact: false }

describe('ModelCatalogRow', () => {
  beforeEach(() => {
    publicSettings.value = { balance_recharge_multiplier: 13, official_price_cny_rate: 7 }
    window.localStorage.clear()
    useCurrencyDisplay().setMode('fiat')
  })

  it('shows the cheapest group balance price in the collapsed row', () => {
    const w = mount(ModelCatalogRow, { props: { model: model() } })
    // 1e-6 * 1e6 * 0.65 / 13 = 0.05
    expect(w.get('[data-test="start-prices"]').text()).toContain('¥0.050')
    expect(w.find('[data-test="catalog-panel"]').exists()).toBe(false)
  })

  it('expands to hero prices, struck-through official price, and the group table with lowest tag', () => {
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true, subscriptionUnit: unit } })
    expect(w.get('[data-test="hero-prices"]').text()).toContain('¥0.050')
    // 官方价 1 美元 * 7 = ¥7.00
    expect(w.get('[data-test="official-price"]').text()).toContain('¥7.00')
    const rows = w.findAll('[data-test="group-table"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('Budget')
    expect(rows[0].text()).toContain('Lowest')
    expect(rows[0].attributes('data-lowest')).toBe('true')
    expect(rows[1].text()).not.toContain('Lowest')
    // 套餐价区间：0.65 * 0.05 = 0.0325 ~ 0.065
    expect(rows[0].get('[data-test="plan-cell"]').text()).toContain('¥0.033–¥0.065')
  })

  it('hides official price when the backend does not provide a rate', () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    expect(w.find('[data-test="official-price"]').exists()).toBe(false)
    expect(w.text()).not.toContain('$')
  })

  it('shows USD credit prices without plan column when the multiplier is 1', () => {
    publicSettings.value = null
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true, subscriptionUnit: unit } })
    expect(w.get('[data-test="hero-prices"]').text()).toContain('$0.65')
    expect(w.find('[data-test="plan-cell"]').exists()).toBe(false)
  })

  it('renders the custom rate tag with its own tooltip', () => {
    const w = mount(ModelCatalogRow, { props: { model: model({}, { 1: 1 }), expanded: true } })
    const tags = w.findAll('[data-test="rate-tag"]')
    const custom = tags.find((t) => t.text() === '1x')!
    expect(custom.attributes('title')).toBe('custom tip 1.3')
    expect(tags.find((t) => t.text() === '0.65x')!.attributes('title')).toBe('rate tip')
  })

  it('lists tiers for the cheapest group', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          intervals: [
            { min_tokens: 0, max_tokens: 200000, input_price: 1e-6, output_price: 4e-6, cache_read_price: null, cache_write_price: null, per_request_price: null },
            { min_tokens: 200000, max_tokens: null, input_price: 2e-6, output_price: 8e-6, cache_read_price: null, cache_write_price: null, per_request_price: null },
          ],
        }),
        expanded: true,
      },
    })
    const tier = w.get('[data-test="tier-table"]')
    expect(tier.text()).toContain('Up to 200K')
    expect(tier.text()).toContain('Over 200K')
    // 2e-6*1e6*0.65/13 = 0.1
    expect(tier.text()).toContain('¥0.100')
  })

  it('shows a per-request model that only has interval pricing', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          billing_mode: 'per_request' as never,
          input_price: null,
          output_price: null,
          cache_read_price: null,
          intervals: [{ min_tokens: 0, max_tokens: null, input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.13 }],
        }),
      },
    })
    // 0.13 * 0.65 / 13 = 0.0065
    expect(w.get('[data-test="start-prices"]').text()).toContain('¥0.0065')
    expect(w.get('[data-test="start-prices"]').text()).toContain('per request')
  })

  it('emits toggle and is not expandable without pricing', async () => {
    const w = mount(ModelCatalogRow, { props: { model: model() } })
    await w.get('button').trigger('click')
    expect(w.emitted('toggle')).toHaveLength(1)
    expect(w.get('button').attributes('aria-expanded')).toBe('false')
    const empty = mount(ModelCatalogRow, { props: { model: { key: 'k', name: 'n', platform: 'openai', kind: null, entries: [], cheapest: null } } })
    expect(empty.get('button').attributes('disabled')).toBeDefined()
  })
})
