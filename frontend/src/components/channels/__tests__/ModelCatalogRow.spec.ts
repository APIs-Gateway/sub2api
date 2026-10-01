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
        'availableChannels.pricing.cacheWritePrice': 'Cache write',
        'availableChannels.pricing.imageOutputPrice': 'Image output',
        'availableChannels.pricing.perRequestPrice': 'Per request',
        'availableChannels.pricing.perMillion': 'per 1M',
        'availableChannels.pricing.perRequest': 'per request',
        'availableChannels.lowest': 'Lowest',
        'availableChannels.planPrice': 'Plan price',
        'availableChannels.yourPlanPrice': 'Your plan price',
        'availableChannels.tierUpTo': `Up to ${params?.n}`,
        'availableChannels.tierAbove': `Over ${params?.n}`,
        'availableChannels.officialPrice': 'Official price',
        'availableChannels.rateNote': 'rate note',
        'availableChannels.rateCustom': `your rate, default ${params?.base}x`,
        'availableChannels.context': 'Context length',
        'availableChannels.tierName': 'Tier',
        'availableChannels.resolution': 'Resolution',
        'availableChannels.noPricing': 'No pricing',
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

function model(over: Record<string, unknown> = {}, rates: Record<number, number> = {}) {
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
    // 官方价 1 美元 * 7 = ¥7.00，高于展示价，所以加删除线
    const official = w.get('[data-test="official-price"]')
    expect(official.text()).toContain('Official price')
    expect(official.text()).toContain('¥7.00')
    expect(official.find('.line-through').text()).toBe('¥7.00')
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

  it('explains the rate in visible text instead of a hover-only title', () => {
    const w = mount(ModelCatalogRow, { props: { model: model({}, { 1: 1 }), expanded: true } })
    expect(w.get('[data-test="rate-note"]').text()).toBe('rate note')
    const tags = w.findAll('[data-test="rate-tag"]')
    expect(tags.map((t) => t.text()).sort()).toEqual(['0.65x', '1x'])
    expect(tags.every((t) => t.attributes('title') === undefined)).toBe(true)
    // 只有专属倍率才多一句「默认倍率」说明
    const custom = w.findAll('[data-test="rate-custom"]')
    expect(custom).toHaveLength(1)
    expect(custom[0].text()).toBe('your rate, default 1.3x')
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

  it('shows a neutral official price without strike-through when it is not above the shown price', () => {
    publicSettings.value = null
    // 专属倍率 1.3：展示价 $1.3 高于官方价 $1
    const w = mount(ModelCatalogRow, { props: { model: model({}, { 2: 1.3 }), expanded: true } })
    expect(w.get('[data-test="hero-prices"]').text()).toContain('$1.3')
    const official = w.get('[data-test="official-price"]')
    expect(official.text()).toContain('Official price')
    expect(official.text()).toContain('$1')
    expect(official.find('.line-through').exists()).toBe(false)
    expect(official.html()).not.toContain('line-through')
  })

  it('keeps the official price neutral when it equals the shown price', () => {
    publicSettings.value = null
    const w = mount(ModelCatalogRow, { props: { model: model({}, { 2: 1 }), expanded: true } })
    const official = w.get('[data-test="official-price"]')
    expect(official.text()).toContain('$1')
    expect(official.find('.line-through').exists()).toBe(false)
  })

  it('strikes through the official price in USD mode when it is above the shown price', () => {
    publicSettings.value = null
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    // 最低倍率 0.65：展示价 $0.65，官方价 $1 更高
    expect(w.get('[data-test="official-price"] .line-through').text()).toBe('$1')
  })

  it('shows the cache write price in the hero, group table and tier table only when configured', () => {
    const iv = (min: number, max: number | null, input: number, cacheWrite: number | null) => ({
      min_tokens: min,
      max_tokens: max,
      input_price: input,
      output_price: input * 4,
      cache_read_price: null,
      cache_write_price: cacheWrite,
      per_request_price: null,
    })
    const collapsed = mount(ModelCatalogRow, { props: { model: model({ cache_write_price: 2.6e-6 }) } })
    // 列表行只放核心价格
    expect(collapsed.get('[data-test="start-prices"]').text()).not.toContain('Cache write')

    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          cache_write_price: 2.6e-6,
          intervals: [iv(0, 200000, 1e-6, 2.6e-6), iv(200000, null, 2e-6, 5.2e-6)],
        }),
        expanded: true,
      },
    })
    // 2.6e-6 * 1e6 * 0.65 / 13 = 0.13
    const hero = w.get('[data-test="hero-prices"]').text()
    expect(hero).toContain('Cache write')
    expect(hero).toContain('¥0.130')
    const groupHeaders = w.findAll('[data-test="group-table"] thead th').map((th) => th.text())
    expect(groupHeaders).toContain('Cache write')
    expect(w.findAll('[data-test="group-table"] tbody tr')[0].text()).toContain('¥0.130')
    const tierHeaders = w.findAll('[data-test="tier-table"] thead th').map((th) => th.text())
    expect(tierHeaders).toContain('Cache write')
    // 第二档 5.2e-6 * 1e6 * 0.65 / 13 = 0.26
    expect(w.get('[data-test="tier-table"]').text()).toContain('¥0.260')

    const none = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    expect(none.text()).not.toContain('Cache write')
  })

  it('labels the cache column as cache read', () => {
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    const headers = w.findAll('[data-test="group-table"] thead th').map((th) => th.text())
    expect(headers).toContain('Cache read')
    expect(headers).not.toContain('Cache')
  })

  it('names the tier column by billing mode', () => {
    const tiers = [
      { min_tokens: 0, max_tokens: 1000, tier_label: 'A', input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.13 },
      { min_tokens: 1000, max_tokens: null, tier_label: 'B', input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.26 },
    ]
    const header = (mode: string) => {
      const w = mount(ModelCatalogRow, {
        props: {
          model: model({ billing_mode: mode, input_price: null, output_price: null, cache_read_price: null, intervals: tiers }),
          expanded: true,
        },
      })
      return w.get('[data-test="tier-table"] thead th').text()
    }
    expect(header('per_request')).toBe('Tier')
    expect(header('image')).toBe('Resolution')

    const token = mount(ModelCatalogRow, {
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
    expect(token.get('[data-test="tier-table"] thead th').text()).toBe('Context length')
  })

  it('uses the first tier price for per-request models in hero, start price and group table', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          billing_mode: 'per_request',
          input_price: null,
          output_price: null,
          cache_read_price: null,
          per_request_price: 6.5,
          intervals: [
            { min_tokens: 0, max_tokens: 1000, tier_label: 'A', input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.13 },
            { min_tokens: 1000, max_tokens: null, tier_label: 'B', input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.26 },
          ],
        }),
        expanded: true,
      },
    })
    // 0.13 * 0.65 / 13 = 0.0065；不是基础价 6.5 * 0.65 / 13 = 0.325
    expect(w.get('[data-test="start-prices"]').text()).toContain('¥0.0065')
    expect(w.get('[data-test="hero-prices"]').text()).toContain('¥0.0065')
    expect(w.get('[data-test="group-table"] tbody tr').text()).toContain('¥0.0065')
    expect(w.get('[data-test="group-table"]').text()).not.toContain('¥0.325')
  })

  it('shows an image-output-only model as a per-million-token image output price', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          billing_mode: 'image',
          input_price: null,
          output_price: null,
          cache_read_price: null,
          image_output_price: 40e-6,
        }),
        expanded: true,
        subscriptionUnit: unit,
      },
    })
    // 40e-6 * 1e6 * 0.65 / 13 = 2
    const start = w.get('[data-test="start-prices"]').text()
    expect(start).toContain('Image output')
    expect(start).toContain('¥2.00')
    expect(start).toContain('per 1M')
    expect(start).not.toContain('per request')
    const headers = w.findAll('[data-test="group-table"] thead th').map((th) => th.text())
    expect(headers[1]).toBe('Image output')
    expect(headers).not.toContain('Input')
    expect(headers).not.toContain('Output')
    // 套餐价 = 额度价 × u：40e-6 * 1e6 * 0.65 = 26，26 * 0.05 = 1.3 ~ 26 * 0.1 = 2.6
    expect(w.get('[data-test="plan-cell"]').text()).toContain('¥1.30–¥2.60')
  })

  it('shows 0 as free instead of treating it as not configured', () => {
    const free = mount(ModelCatalogRow, {
      props: { model: model({ input_price: 0, output_price: 0, cache_read_price: null }), expanded: true },
    })
    expect(free.find('[data-test="start-prices"]').exists()).toBe(true)
    expect(free.get('[data-test="start-prices"]').text()).toContain('¥0.00')
    expect(free.text()).not.toContain('No pricing')
    expect(free.get('[data-test="group-table"] tbody tr').text()).toContain('¥0.00')

    publicSettings.value = null
    const freeUsd = mount(ModelCatalogRow, {
      props: { model: model({ billing_mode: 'per_request', input_price: null, output_price: null, cache_read_price: null, per_request_price: 0 }) },
    })
    expect(freeUsd.get('[data-test="start-prices"]').text()).toContain('$0')
    expect(freeUsd.get('[data-test="start-prices"]').text()).not.toContain('$0.')
  })

  it('binds aria-controls only while the panel is rendered', () => {
    const collapsed = mount(ModelCatalogRow, { props: { model: model() } })
    expect(collapsed.get('button').attributes('aria-controls')).toBeUndefined()

    const expanded = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    const id = expanded.get('button').attributes('aria-controls')
    expect(id).toBeTruthy()
    expect(expanded.get('[data-test="catalog-panel"]').attributes('id')).toBe(id)
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
