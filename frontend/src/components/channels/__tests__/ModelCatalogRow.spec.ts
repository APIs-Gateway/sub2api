import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelCatalogRow from '../ModelCatalogRow.vue'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { buildPriceCatalog } from '@/utils/modelCatalog'
import type { UserPriceCatalog, UserPriceSet } from '@/api/channels'

const publicSettings: { value: Record<string, unknown> | null } = { value: null }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
const activeCards: { value: Array<Record<string, unknown>> } = { value: [] }
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    get activeSubscriptions() {
      return activeCards.value
    },
  }),
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: true }),
}))
const getSubscriptionPricing = vi.hoisted(() => vi.fn())
vi.mock('@/api/subscriptions', () => ({
  default: { getSubscriptionPricing },
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
        'availableChannels.planRate': 'Plan rate',
        'availableChannels.yourPlanRate': 'Your plan rate',
        'availableChannels.planRateLead': 'As low as',
        'availableChannels.tierUpTo': `Up to ${params?.n}`,
        'availableChannels.tierAbove': `Over ${params?.n}`,
        'availableChannels.officialPrice': 'Official price',
        'availableChannels.rateNote': 'rate note',
        'availableChannels.rateNoteFiat': 'rate note fiat with plan',
        'availableChannels.rateNoteFiatNoPlan': 'rate note fiat no plan',
        'availableChannels.rateCustom': `your rate, default ${params?.base}x`,
        'availableChannels.context': 'Context length',
        'availableChannels.tierName': 'Tier',
        'availableChannels.resolution': 'Resolution',
        'availableChannels.noPricing': 'No pricing',
        'availableChannels.peakNote': 'peak note',
        'availableChannels.peakShort': `peak ${params?.n}x`,
      })[key] ?? key,
  }),
}))

const g = (id: number, name: string) => ({
  id, name, platform: 'openai', subscription_type: 'standard', is_exclusive: false,
})

const set = (over: Partial<UserPriceSet> = {}): UserPriceSet => ({
  input: null, output: null, cache_read: null, cache_write: null, image_output: null, unit: null, ...over,
})

/** 按后端的口径放大：prices = official × 倍率。 */
function scaled(s: UserPriceSet, rate: number): UserPriceSet {
  return Object.fromEntries(Object.entries(s).map(([k, v]) => [k, v == null ? null : v * rate])) as unknown as UserPriceSet
}

interface TierSpec {
  min: number
  max: number | null
  label?: string
  official: Partial<UserPriceSet>
}

interface Over {
  billing_mode?: 'token' | 'per_request' | 'image'
  kind?: 'token' | 'request'
  official?: Partial<UserPriceSet>
  tiers?: TierSpec[]
}

const baseRates: Record<number, number> = { 1: 1.3, 2: 0.65 }

/** 与后端 /channels/prices 的结构一致：两个分组（Stable 1.3x、Budget 0.65x），价格已乘倍率。 */
function model(over: Over = {}, rates: Record<number, number> = {}) {
  const official = set(over.official ?? { input: 1e-6, output: 4e-6, cache_read: 1e-7 })
  const entries = [1, 2].map((id) => {
    const rate = rates[id] ?? baseRates[id]
    return {
      group_id: id,
      rate,
      base_rate: baseRates[id],
      has_custom_rate: rate !== baseRates[id],
      billing_mode: over.billing_mode ?? 'token',
      kind: over.kind ?? 'token',
      official,
      prices: scaled(official, rate),
      tiers: (over.tiers ?? []).map((t) => ({
        min_tokens: t.min,
        max_tokens: t.max,
        label: t.label,
        official: set(t.official),
        prices: scaled(set(t.official), rate),
      })),
    }
  })
  const data: UserPriceCatalog = {
    groups: [g(1, 'Stable'), g(2, 'Budget')],
    models: [{ name: 'gpt-x', platform: 'openai', entries: entries as never }],
  }
  return buildPriceCatalog(data)[0]
}

/** 生产 codex 站的 /subscriptions/pricing：u_min = 0.04，u_max = 0.05。 */
const PROD_PRICING = { d_min: 30, d_max: 510, u_min: 0.04, u_max: 0.05, t_min: 30, t_max: 360, t_step: 30, d_floor: 210 }

describe('ModelCatalogRow', () => {
  beforeEach(() => {
    publicSettings.value = { balance_recharge_multiplier: 13, official_price_cny_rate: 7, payment_enabled: true }
    window.localStorage.clear()
    activeCards.value = []
    resetPlanPricingForTest()
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    useCurrencyDisplay().setMode('fiat')
  })

  it('shows the cheapest group balance price in the collapsed row', () => {
    const w = mount(ModelCatalogRow, { props: { model: model() } })
    // 1e-6 * 1e6 * 0.65 / 13 = 0.05
    expect(w.get('[data-test="start-prices"]').text()).toContain('¥0.05')
    expect(w.find('[data-test="catalog-panel"]').exists()).toBe(false)
  })

  it('expands to hero prices, struck-through official price, and the group table with lowest tag', async () => {
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    await flushPromises()
    expect(w.get('[data-test="hero-prices"]').text()).toContain('¥0.05')
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
    // 套餐倍率：0.65 * 0.04 = 0.026
    expect(rows[0].get('[data-test="plan-cell"]').text()).toBe('As low as0.026x')
  })

  it('hides official price when the backend does not provide a rate', () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    expect(w.find('[data-test="official-price"]').exists()).toBe(false)
    expect(w.text()).not.toContain('$')
  })

  it('shows USD credit prices without plan column when the multiplier is 1', async () => {
    publicSettings.value = null
    const w = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    await flushPromises()
    expect(w.get('[data-test="hero-prices"]').text()).toContain('$0.65')
    expect(w.find('[data-test="plan-cell"]').exists()).toBe(false)
  })

  it('explains the rate in visible text instead of a hover-only title', async () => {
    const w = mount(ModelCatalogRow, { props: { model: model({}, { 1: 1 }), expanded: true } })
    await flushPromises()
    expect(w.get('[data-test="rate-note"]').text()).toBe('rate note fiat with plan')
    const tags = w.findAll('[data-test="rate-tag"]')
    // 等效倍率：0.65 ÷ 13 = 0.05，1 ÷ 13 = 0.0769
    expect(tags.map((t) => t.text()).sort()).toEqual(['0.05x', '0.0769x'])
    expect(tags.every((t) => t.attributes('title') === undefined)).toBe(true)
    // 只有专属倍率才多一句「默认倍率」说明
    const custom = w.findAll('[data-test="rate-custom"]')
    expect(custom).toHaveLength(1)
    // 默认倍率 1.3 ÷ 13 = 0.1
    expect(custom[0].text()).toBe('your rate, default 0.1x')
  })

  it('lists tiers for the cheapest group', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          tiers: [
            { min: 0, max: 200000, official: { input: 1e-6, output: 4e-6 } },
            { min: 200000, max: null, official: { input: 2e-6, output: 8e-6 } },
          ],
        }),
        expanded: true,
      },
    })
    const tier = w.get('[data-test="tier-table"]')
    expect(tier.text()).toContain('Up to 200K')
    expect(tier.text()).toContain('Over 200K')
    // 2e-6*1e6*0.65/13 = 0.1
    expect(tier.text()).toContain('¥0.10')
  })

  it('shows a per-request model that only has interval pricing', () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({ billing_mode: 'per_request', kind: 'request', official: { unit: 0.13 } }),
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
    expect(w.get('[data-test="official-price"] .line-through').text()).toBe('$1.00')
  })

  it('shows the cache write price in the hero, group table and tier table only when configured', () => {
    const collapsed = mount(ModelCatalogRow, {
      props: { model: model({ official: { input: 1e-6, output: 4e-6, cache_write: 2.6e-6 } }) },
    })
    // 列表行只放核心价格
    expect(collapsed.get('[data-test="start-prices"]').text()).not.toContain('Cache write')

    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          official: { input: 1e-6, output: 4e-6, cache_write: 2.6e-6 },
          tiers: [
            { min: 0, max: 200000, official: { input: 1e-6, output: 4e-6, cache_write: 2.6e-6 } },
            { min: 200000, max: null, official: { input: 2e-6, output: 8e-6, cache_write: 5.2e-6 } },
          ],
        }),
        expanded: true,
      },
    })
    // 2.6e-6 * 1e6 * 0.65 / 13 = 0.13
    const hero = w.get('[data-test="hero-prices"]').text()
    expect(hero).toContain('Cache write')
    expect(hero).toContain('¥0.13')
    const groupHeaders = w.findAll('[data-test="group-table"] thead th').map((th) => th.text())
    expect(groupHeaders).toContain('Cache write')
    expect(w.findAll('[data-test="group-table"] tbody tr')[0].text()).toContain('¥0.13')
    const tierHeaders = w.findAll('[data-test="tier-table"] thead th').map((th) => th.text())
    expect(tierHeaders).toContain('Cache write')
    // 第二档 5.2e-6 * 1e6 * 0.65 / 13 = 0.26
    expect(w.get('[data-test="tier-table"]').text()).toContain('¥0.26')

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
    const tiers: TierSpec[] = [
      { min: 0, max: 1000, label: 'A', official: { unit: 0.13 } },
      { min: 1000, max: null, label: 'B', official: { unit: 0.26 } },
    ]
    const header = (mode: 'per_request' | 'image') => {
      const w = mount(ModelCatalogRow, {
        props: { model: model({ billing_mode: mode, kind: 'request', official: { unit: 0.13 }, tiers }), expanded: true },
      })
      return w.get('[data-test="tier-table"] thead th').text()
    }
    expect(header('per_request')).toBe('Tier')
    expect(header('image')).toBe('Resolution')

    const token = mount(ModelCatalogRow, {
      props: {
        model: model({
          tiers: [
            { min: 0, max: 200000, official: { input: 1e-6, output: 4e-6 } },
            { min: 200000, max: null, official: { input: 2e-6, output: 8e-6 } },
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
          kind: 'request',
          official: { unit: 0.13 },
          tiers: [
            { min: 0, max: 1000, label: 'A', official: { unit: 0.13 } },
            { min: 1000, max: null, label: 'B', official: { unit: 0.26 } },
          ],
        }),
        expanded: true,
      },
    })
    // 0.13 * 0.65 / 13 = 0.0065
    expect(w.get('[data-test="start-prices"]').text()).toContain('¥0.0065')
    expect(w.get('[data-test="hero-prices"]').text()).toContain('¥0.0065')
    expect(w.get('[data-test="group-table"] tbody tr').text()).toContain('¥0.0065')
    expect(w.get('[data-test="group-table"]').text()).not.toContain('¥0.325')
  })

  it('shows an image-output-only model as a per-million-token image output price', async () => {
    const w = mount(ModelCatalogRow, {
      props: {
        model: model({
          billing_mode: 'image',
          official: { image_output: 40e-6 },
        }),
        expanded: true,
      },
    })
    await flushPromises()
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
    // 套餐倍率只看分组倍率：0.65 * 0.04 = 0.026
    expect(w.get('[data-test="plan-cell"]').text()).toBe('As low as0.026x')
  })

  it('shows 0 as free instead of treating it as not configured', () => {
    const free = mount(ModelCatalogRow, {
      props: { model: model({ official: { input: 0, output: 0 } }), expanded: true },
    })
    expect(free.find('[data-test="start-prices"]').exists()).toBe(true)
    expect(free.get('[data-test="start-prices"]').text()).toContain('¥0.00')
    expect(free.text()).not.toContain('No pricing')
    expect(free.get('[data-test="group-table"] tbody tr').text()).toContain('¥0.00')

    publicSettings.value = null
    const freeUsd = mount(ModelCatalogRow, {
      props: { model: model({ billing_mode: 'per_request', kind: 'request', official: { unit: 0 } }) },
    })
    expect(freeUsd.get('[data-test="start-prices"]').text()).toContain('$0.00')
    expect(freeUsd.text()).not.toContain('No pricing')
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
  it('shows each group by its own billing mode when one model mixes token and per-request groups', () => {
    const off = set({ input: 1e-6, output: 4e-6 })
    const req = set({ unit: 0.5 })
    const mk = (id: number, rate: number, over: Record<string, unknown>, official: UserPriceSet) => ({
      group_id: id, rate, base_rate: rate, has_custom_rate: false, official, prices: scaled(official, rate), tiers: [], ...over,
    })
    const data = {
      groups: [g(1, 'TokenG'), g(2, 'ReqG'), g(3, 'ReqCheap')],
      models: [{
        name: 'mixed', platform: 'openai',
        entries: [
          mk(1, 1, { billing_mode: 'token', kind: 'token' }, off),
          // 每次价低于每 token 价换算后的数字，但不应因此排到前面
          mk(2, 1, { billing_mode: 'per_request', kind: 'request' }, req),
          mk(3, 0.4, { billing_mode: 'per_request', kind: 'request' }, req),
        ],
      }],
    } as unknown as UserPriceCatalog
    const m = buildPriceCatalog(data)[0]
    expect(m.kinds).toEqual(['token', 'request'])
    expect(m.entries.map((e) => e.group.name)).toEqual(['TokenG', 'ReqCheap', 'ReqG'])
    const w = mount(ModelCatalogRow, { props: { model: m, expanded: true } })
    const sections = w.findAll('[data-test="group-section"]')
    expect(sections).toHaveLength(2)
    expect(sections[0].text()).toContain('TokenG')
    expect(sections[0].text()).not.toContain('Req')
    expect(sections[0].text()).toContain('per 1M')
    expect(sections[1].text()).toContain('ReqCheap')
    expect(sections[1].text()).toContain('ReqG')
    expect(sections[1].text()).toContain('per request')
    expect(sections[1].findAll('[data-lowest="true"]')).toHaveLength(1)
    expect(sections[1].findAll('td').some((td) => td.text() === '-')).toBe(false)
    expect(sections[0].findAll('td').some((td) => td.text() === '-')).toBe(false)
  })

  it('marks the peak multiplier per group: only default-card groups are tagged', () => {
    const off = set({ input: 1e-6, output: 4e-6 })
    const mk = (id: number, rate: number, peak?: number) => ({
      group_id: id, rate, base_rate: rate, has_custom_rate: false, billing_mode: 'token', kind: 'token',
      official: off, prices: scaled(off, rate), tiers: [], peak_multiplier: peak,
    })
    const build = (cheapPeak: boolean) =>
      buildPriceCatalog({
        groups: [g(1, 'Stable'), g(2, 'Budget')],
        // Budget 更便宜（起价所在分组）；Stable 走默认价卡带峰时倍率。
        models: [{ name: 'ds', platform: 'openai', entries: [mk(1, 1.3, 2), mk(2, 0.65, cheapPeak ? 2 : undefined)] }],
      } as unknown as UserPriceCatalog)[0]

    const mixed = mount(ModelCatalogRow, { props: { model: build(false), expanded: true } })
    const tags = mixed.findAll('[data-test="group-table"] tbody tr').map((r) => r.find('[data-test="peak-tag"]').exists())
    expect(tags).toEqual([false, true]) // Budget（无）、Stable（有）
    expect(mixed.get('[data-test="peak-note"]').text()).toBe('peak note')
    // 起价所在分组没有峰时倍率：列表行不标
    expect(mixed.find('[data-test="peak-note-row"]').exists()).toBe(false)

    const cheapPeak = mount(ModelCatalogRow, { props: { model: build(true), expanded: true } })
    expect(cheapPeak.get('[data-test="peak-note-row"]').text()).toBe('peak 2x')
    expect(cheapPeak.findAll('[data-test="peak-tag"]')).toHaveLength(2)
    const plain = mount(ModelCatalogRow, { props: { model: model(), expanded: true } })
    expect(plain.find('[data-test="peak-note-row"]').exists()).toBe(false)
    expect(plain.find('[data-test="peak-note"]').exists()).toBe(false)
  })
})

/** 生产 codex 站的 4 个分组：官方价输入 $5 / 输出 $30 每百万 token。customs 是专属倍率。 */
function fourGroups(customs: Record<number, number> = {}) {
  const official = set({ input: 5e-6, output: 30e-6 })
  const base: Array<[number, string, number]> = [
    [1, 'codex特惠分组', 1.4],
    [2, 'codex pro+plus', 3],
    [3, '稳定 luna 专用', 4],
    [4, 'codex 满血pro', 5],
  ]
  const entries = base.map(([id, , baseRate]) => {
    const rate = customs[id] ?? baseRate
    return {
      group_id: id, rate, base_rate: baseRate, has_custom_rate: rate !== baseRate,
      billing_mode: 'token', kind: 'token', official, prices: scaled(official, rate), tiers: [],
    }
  })
  return buildPriceCatalog({
    groups: base.map(([id, name]) => g(id, name)),
    models: [{ name: 'gpt-5.5', platform: 'openai', entries: entries as never }],
  } as unknown as UserPriceCatalog)[0]
}

function groupRows(w: ReturnType<typeof mount>) {
  return w.findAll('[data-test="group-table"] tbody tr')
}
function headers(w: ReturnType<typeof mount>) {
  return w.findAll('[data-test="group-table"] thead th').map((th) => th.text())
}

describe('ModelCatalogRow 等效倍率与套餐倍率列（DR-3）', () => {
  beforeEach(() => {
    publicSettings.value = { balance_recharge_multiplier: 13, official_price_cny_rate: 7.2, payment_enabled: true }
    window.localStorage.clear()
    activeCards.value = []
    resetPlanPricingForTest()
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    useCurrencyDisplay().setMode('fiat')
  })

  async function open(m = fourGroups()) {
    const w = mount(ModelCatalogRow, { props: { model: m, expanded: true } })
    await flushPromises()
    return w
  }

  it('¥ 模式：分组旁是等效倍率，套餐倍率列写「低至」', async () => {
    const w = await open()
    const rows = groupRows(w)
    expect(rows.map((r) => r.get('[data-test="rate-tag"]').text())).toEqual(['0.108x', '0.231x', '0.308x', '0.385x'])
    expect(rows.map((r) => r.get('[data-test="plan-cell"]').text())).toEqual([
      'As low as0.056x',
      'As low as0.12x',
      'As low as0.16x',
      'As low as0.2x',
    ])
    expect(headers(w)).toEqual(['availableChannels.group', 'Input', 'Output', 'Plan rate'])
    expect(rows[0].text()).toContain('¥0.54')
    expect(rows[0].text()).toContain('¥3.23')
    expect(rows[0].attributes('data-lowest')).toBe('true')
    expect(w.get('[data-test="rate-note"]').text()).toBe('rate note fiat with plan')
  })

  it('「低至」是单独的小字前缀，和数字之间有间距', async () => {
    const w = await open()
    const cell = w.get('[data-test="plan-cell"]')
    const lead = cell.get('[data-test="plan-lead"]')
    expect(lead.text()).toBe('As low as')
    expect(lead.classes()).toContain('mr-1')
    // 前缀在数字之前，数字部分不再带前缀
    expect(cell.html().indexOf('plan-lead')).toBeLessThan(cell.html().indexOf('0.056'))
    expect(cell.text()).toBe('As low as0.056x')
    // 有生效卡时没有前缀
    activeCards.value = [{ status: 'active', fiat_per_credit: 0.0467 }]
    const withCard = await open()
    expect(withCard.find('[data-test="plan-lead"]').exists()).toBe(false)
  })

  it('¥ 模式价格：≥ ¥0.01 固定 2 位小数，< ¥0.01 保留 4 位有效数字', async () => {
    const official = set({ input: 0.538462e-6, output: 3.230769e-6, cache_read: 0.0038e-6 })
    const m = buildPriceCatalog({
      groups: [g(1, 'codex特惠分组')],
      models: [{
        name: 'gpt-5.5', platform: 'openai',
        entries: [{ group_id: 1, rate: 13, base_rate: 13, has_custom_rate: false, billing_mode: 'token', kind: 'token', official, prices: scaled(official, 13), tiers: [] }] as never,
      }],
    } as unknown as UserPriceCatalog)[0]
    const w = await open(m)
    const hero = w.get('[data-test="hero-prices"]').text()
    expect(hero).toContain('¥0.54')
    expect(hero).not.toContain('¥0.538')
    expect(hero).toContain('¥3.23')
    expect(hero).not.toContain('¥3.230')
    expect(hero).toContain('¥0.0038')
    expect(groupRows(w)[0].text()).toContain('¥0.54')
  })

  it('官方价仍是 ¥ 划线，价格只有一套', async () => {
    const w = await open()
    const official = w.get('[data-test="official-price"]')
    // $5 × 7.2 = ¥36.00，高于展示价，加删除线
    expect(official.find('.line-through').text()).toBe('¥36.00')
    expect(w.get('[data-test="hero-prices"]').text()).toContain('¥0.54')
  })

  it('有生效套餐卡：列头「你的套餐倍率」，值用卡的精确单价，不带「低至」', async () => {
    activeCards.value = [{ status: 'active', fiat_per_credit: 0.0467 }]
    const w = await open()
    expect(headers(w)).toContain('Your plan rate')
    expect(headers(w)).not.toContain('Plan rate')
    // 1.4 × 0.0467 = 0.06538；5 × 0.0467 = 0.2335 → half up 0.234
    expect(groupRows(w).map((r) => r.get('[data-test="plan-cell"]').text())).toEqual([
      '0.0654x',
      '0.14x',
      '0.187x',
      '0.234x',
    ])
  })

  it('专属倍率：行倍率用专属值，默认倍率与套餐倍率都按专属值换算', async () => {
    // 分组 1 专属倍率 0.585
    const w = await open(fourGroups({ 1: 0.585 }))
    const row = groupRows(w).find((r) => r.text().includes('codex特惠分组'))!
    expect(row.get('[data-test="rate-tag"]').text()).toBe('0.045x')
    expect(row.get('[data-test="rate-custom"]').text()).toBe('your rate, default 0.108x')
    expect(row.get('[data-test="plan-cell"]').text()).toBe('As low as0.0234x')
    // 其余分组没有专属倍率说明
    expect(w.findAll('[data-test="rate-custom"]')).toHaveLength(1)
  })

  it('按次计费同样显示等效倍率和套餐倍率', async () => {
    const official = set({ unit: 0.13 })
    const m = buildPriceCatalog({
      groups: [g(1, 'codex特惠分组')],
      models: [{
        name: 'img', platform: 'openai',
        entries: [{ group_id: 1, rate: 1.4, base_rate: 1.4, has_custom_rate: false, billing_mode: 'per_request', kind: 'request', official, prices: scaled(official, 1.4), tiers: [] }] as never,
      }],
    } as unknown as UserPriceCatalog)[0]
    const w = await open(m)
    expect(w.get('[data-test="rate-tag"]').text()).toBe('0.108x')
    expect(w.get('[data-test="plan-cell"]').text()).toBe('As low as0.056x')
  })

  describe('不显示套餐倍率列', () => {
    function expectNoPlanColumn(w: ReturnType<typeof mount>) {
      expect(w.find('[data-test="plan-cell"]').exists()).toBe(false)
      expect(w.find('[data-test="plan-header"]').exists()).toBe(false)
      expect(headers(w)).toEqual(['availableChannels.group', 'Input', 'Output'])
    }

    it('美元模式：原始倍率，原来的说明', async () => {
      useCurrencyDisplay().setMode('usd')
      const w = await open()
      expectNoPlanColumn(w)
      expect(groupRows(w).map((r) => r.get('[data-test="rate-tag"]').text())).toEqual(['1.4x', '3x', '4x', '5x'])
      expect(w.get('[data-test="rate-note"]').text()).toBe('rate note')
      expect(w.get('[data-test="hero-prices"]').text()).toContain('$7')
    })

    it('free 站（m = 1）：原始倍率，不请求套餐定价', async () => {
      publicSettings.value = { balance_recharge_multiplier: 1, payment_enabled: true }
      const w = await open()
      expectNoPlanColumn(w)
      expect(groupRows(w).map((r) => r.get('[data-test="rate-tag"]').text())).toEqual(['1.4x', '3x', '4x', '5x'])
      expect(w.get('[data-test="rate-note"]').text()).toBe('rate note')
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('支付关闭：倍率仍是等效倍率，只有说明短一句', async () => {
      publicSettings.value = { balance_recharge_multiplier: 13, official_price_cny_rate: 7.2, payment_enabled: false }
      const w = await open()
      expectNoPlanColumn(w)
      expect(groupRows(w).map((r) => r.get('[data-test="rate-tag"]').text())).toEqual(['0.108x', '0.231x', '0.308x', '0.385x'])
      expect(w.get('[data-test="rate-note"]').text()).toBe('rate note fiat no plan')
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('/subscriptions/pricing 请求失败：整列隐藏，其余照常', async () => {
      getSubscriptionPricing.mockReset().mockRejectedValue(new Error('boom'))
      const w = await open()
      expectNoPlanColumn(w)
      expect(groupRows(w).map((r) => r.get('[data-test="rate-tag"]').text())).toEqual(['0.108x', '0.231x', '0.308x', '0.385x'])
      expect(w.get('[data-test="rate-note"]').text()).toBe('rate note fiat no plan')
    })
  })

  it('用户端文案里不再有「套餐价」', async () => {
    const w = await open()
    expect(w.text()).not.toMatch(/套餐[价價]|plan price/i)
  })
})
