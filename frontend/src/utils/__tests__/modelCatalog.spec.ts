import { describe, expect, it } from 'vitest'
import type { UserPriceCatalog, UserPriceEntry, UserPriceSet } from '@/api/channels'
import {
  balancePrice,
  buildPriceCatalog,
  creditPrice,
  exceeds,
  formatTokenCount,
  officialPrice,
  planPrice,
  resolveSubscriptionUnit,
  type PricingContext,
} from '../modelCatalog'

const set = (over: Partial<UserPriceSet> = {}): UserPriceSet => ({
  input: null,
  output: null,
  cache_read: null,
  cache_write: null,
  image_output: null,
  unit: null,
  ...over,
})

/** 按后端的口径造一条价格：prices 是 official 乘倍率。 */
function entry(groupId: number, rate: number, official: Partial<UserPriceSet>, over: Partial<UserPriceEntry> = {}): UserPriceEntry {
  const off = set(official)
  const scaled = Object.fromEntries(Object.entries(off).map(([k, v]) => [k, v == null ? null : v * rate])) as unknown as UserPriceSet
  return {
    group_id: groupId,
    rate,
    base_rate: rate,
    has_custom_rate: false,
    billing_mode: 'token',
    kind: 'token',
    official: off,
    prices: scaled,
    tiers: [],
    ...over,
  }
}

const group = (id: number, name: string) => ({
  id,
  name,
  platform: 'openai',
  subscription_type: 'standard',
  is_exclusive: false,
})

const fiat: PricingContext = { isFiat: true, rechargeMultiplier: 13, subscriptionUnit: { min: 0.05, max: 0.1, exact: false } }
const usd: PricingContext = { isFiat: false, rechargeMultiplier: 1, subscriptionUnit: null }

describe('price math', () => {
  it('credit, balance, plan and official prices (the credit price already carries the rate)', () => {
    const credit = 3e-6 * 1.4
    expect(creditPrice(credit, 'token')).toBeCloseTo(4.2)
    expect(balancePrice(credit, 'token', fiat)).toBeCloseTo(4.2 / 13, 9)
    expect(balancePrice(credit, 'token', usd)).toBeCloseTo(4.2)
    expect(officialPrice(3e-6, 'token')).toBe(3)
    const p = planPrice(credit, 'token', fiat)!
    expect(p.min).toBeCloseTo(0.21)
    expect(p.max).toBeCloseTo(0.42)
    expect(p.exact).toBe(false)
  })

  it('plan price is exact with a card and absent in USD mode or without unit', () => {
    const exact = planPrice(2, 'request', { ...fiat, subscriptionUnit: { min: 0.07, max: 0.07, exact: true } })!
    expect(exact).toMatchObject({ min: 0.14, max: 0.14, exact: true })
    expect(planPrice(2, 'request', usd)).toBeNull()
    expect(planPrice(2, 'request', { ...fiat, subscriptionUnit: null })).toBeNull()
  })

  it('m=1 keeps the credit price in fiat context', () => {
    expect(balancePrice(2e-6, 'token', { ...fiat, rechargeMultiplier: 1 })).toBe(2)
  })

  it('exceeds ignores floating point noise', () => {
    expect(exceeds(7, 5)).toBe(true)
    expect(exceeds(5, 7)).toBe(false)
    expect(exceeds(5, 5)).toBe(false)
    expect(exceeds(7.000000000001, 7)).toBe(false)
    expect(exceeds(0, 0)).toBe(false)
  })
})

describe('resolveSubscriptionUnit', () => {
  it('prefers the first active card with a positive fiat_per_credit', () => {
    const u = resolveSubscriptionUnit(
      [{ status: 'expired', fiat_per_credit: 0.01 }, { status: 'active', fiat_per_credit: 0 }, { status: 'active', fiat_per_credit: 0.06 }],
      { u_min: 0.05, u_max: 0.1 },
    )
    expect(u).toEqual({ min: 0.06, max: 0.06, exact: true })
  })

  it('falls back to the purchasable range, ordering min and max', () => {
    expect(resolveSubscriptionUnit([], { u_min: 0.1, u_max: 0.05 })).toEqual({ min: 0.05, max: 0.1, exact: false })
    expect(resolveSubscriptionUnit([], null)).toBeNull()
    expect(resolveSubscriptionUnit([], { u_min: 0, u_max: 0.1 })).toBeNull()
  })
})

describe('buildPriceCatalog', () => {
  const data: UserPriceCatalog = {
    groups: [group(1, 'Stable'), group(2, 'Cheap'), group(3, 'Other')],
    models: [
      {
        name: 'gpt-x',
        platform: 'openai',
        entries: [
          entry(1, 1.4, { input: 2e-6, output: 8e-6 }),
          entry(2, 0.5, { input: 2e-6, output: 8e-6 }),
          entry(3, 1, { input: 4e-6, output: 8e-6 }),
        ],
      },
      { name: 'gpt-nopricing', platform: 'openai', entries: [] },
      {
        name: 'img-x',
        platform: 'openai',
        entries: [
          entry(1, 2, { image_output: 40e-6 }),
          entry(2, 1, { image_output: 40e-6 }),
        ],
      },
    ],
  }

  it('joins groups and sorts them by the backend multiplied price', () => {
    const m = buildPriceCatalog(data).find((x) => x.name === 'gpt-x')!
    // Cheap 2e-6 * 0.5 = 1e-6，Other 4e-6，Stable 2e-6 * 1.4 = 2.8e-6
    expect(m.entries.map((e) => e.group.name)).toEqual(['Cheap', 'Stable', 'Other'])
    expect(m.cheapest?.group.name).toBe('Cheap')
    expect(m.kind).toBe('token')
  })

  it('does not multiply the rate again: first is the backend price, official is the pre-rate price', () => {
    const m = buildPriceCatalog(data).find((x) => x.name === 'gpt-x')!
    const stable = m.entries.find((e) => e.group.id === 1)!
    expect(stable.pricing.first.input).toBe(2e-6 * 1.4)
    expect(stable.pricing.official.input).toBe(2e-6)
    expect(stable.group.rate).toBe(1.4)
  })

  it('carries the per-model rate and the custom-rate flag', () => {
    const custom: UserPriceCatalog = {
      groups: [group(1, 'Stable')],
      models: [{ name: 'm', platform: 'openai', entries: [entry(1, 0.2, { input: 1e-6 }, { base_rate: 1.4, has_custom_rate: true })] }],
    }
    const g = buildPriceCatalog(custom)[0].cheapest?.group
    expect(g).toMatchObject({ name: 'Stable', rate: 0.2, baseRate: 1.4, hasCustomRate: true })
  })

  it('ranks an image-output-only model by its image output price', () => {
    const m = buildPriceCatalog(data).find((x) => x.name === 'img-x')!
    expect(m.kind).toBe('token')
    expect(m.cheapest?.group.name).toBe('Cheap')
    expect(m.cheapest?.pricing.first.imageOutput).toBe(40e-6)
  })

  it('keeps models without pricing but with no entries', () => {
    const m = buildPriceCatalog(data).find((x) => x.name === 'gpt-nopricing')!
    expect(m.entries).toEqual([])
    expect(m.cheapest).toBeNull()
    expect(m.kind).toBeNull()
  })

  it('maps tiers and per-request prices; 0 is free and null is not configured', () => {
    const tiered: UserPriceCatalog = {
      groups: [group(1, 'Stable')],
      models: [
        {
          name: 'req',
          platform: 'openai',
          entries: [
            entry(
              1,
              2,
              { unit: 0.04 },
              {
                billing_mode: 'image',
                kind: 'request',
                tiers: [
                  { min_tokens: 0, max_tokens: 100, label: '1K', official: set({ unit: 0.04 }), prices: set({ unit: 0.08 }) },
                  { min_tokens: 100, max_tokens: null, official: set({ unit: 0.06 }), prices: set({ unit: 0.12 }) },
                ],
              },
            ),
          ],
        },
        { name: 'free', platform: 'openai', entries: [entry(1, 1, { input: 0, output: 0 })] },
      ],
    }
    const [free, req] = buildPriceCatalog(tiered)
    expect(req.kind).toBe('request')
    expect(req.cheapest?.pricing.mode).toBe('image')
    expect(req.cheapest?.pricing.first.unit).toBe(0.08)
    expect(req.cheapest?.pricing.tiers.map((t) => t.prices.unit)).toEqual([0.08, 0.12])
    expect(req.cheapest?.pricing.tiers[0].range).toEqual({ label: '1K', min: 0, max: 100 })
    expect(req.cheapest?.pricing.tiers[1].range.label).toBeUndefined()
    expect(free.cheapest?.pricing.first).toMatchObject({ input: 0, output: 0, cacheRead: null })
  })

  it('returns an empty catalog for no data', () => {
    expect(buildPriceCatalog(null)).toEqual([])
    expect(buildPriceCatalog({ groups: [], models: [] })).toEqual([])
  })

  it('drops entries whose group is not in the group list', () => {
    const orphan: UserPriceCatalog = {
      groups: [group(1, 'Stable')],
      models: [{ name: 'm', platform: 'openai', entries: [entry(9, 1, { input: 1e-6 })] }],
    }
    expect(buildPriceCatalog(orphan)[0].entries).toEqual([])
  })
})

describe('formatTokenCount', () => {
  it('abbreviates round numbers', () => {
    expect(formatTokenCount(200000)).toBe('200K')
    expect(formatTokenCount(1000000)).toBe('1M')
    expect(formatTokenCount(1234)).toBe('1234')
  })
})
