import { describe, expect, it } from 'vitest'
import type { UserAvailableChannel, UserSupportedModelPricing } from '@/api/channels'
import {
  balancePrice,
  buildCatalog,
  creditPrice,
  exceeds,
  formatTokenCount,
  normalizePricing,
  officialPrice,
  planPrice,
  resolveSubscriptionUnit,
  type PricingContext,
} from '../modelCatalog'

function pricing(p: Partial<UserSupportedModelPricing>): UserSupportedModelPricing {
  return {
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    ...p,
  }
}

const group = (id: number, name: string, rate: number) => ({
  id,
  name,
  platform: 'openai',
  subscription_type: 'standard',
  rate_multiplier: rate,
  is_exclusive: false,
})

const fiat: PricingContext = { isFiat: true, rechargeMultiplier: 13, subscriptionUnit: { min: 0.05, max: 0.1, exact: false } }
const usd: PricingContext = { isFiat: false, rechargeMultiplier: 1, subscriptionUnit: null }

describe('normalizePricing', () => {
  it('uses the base price when there are no intervals', () => {
    const n = normalizePricing(pricing({ input_price: 2e-6, output_price: 8e-6, cache_read_price: 2e-7 }))
    expect(n?.kind).toBe('token')
    expect(n?.first).toMatchObject({ input: 2e-6, output: 8e-6, cacheRead: 2e-7 })
    expect(n?.tiers).toEqual([])
  })

  it('takes the first interval as the headline and keeps all tiers', () => {
    const n = normalizePricing(
      pricing({
        input_price: 9e-6,
        intervals: [
          { min_tokens: 0, max_tokens: 200000, input_price: 1e-6, output_price: 4e-6, cache_read_price: null, cache_write_price: null, per_request_price: null },
          { min_tokens: 200000, max_tokens: null, input_price: 2e-6, output_price: 8e-6, cache_read_price: null, cache_write_price: null, per_request_price: null },
        ],
      }),
    )
    expect(n?.first.input).toBe(1e-6)
    expect(n?.tiers).toHaveLength(2)
    expect(n?.tiers[1].range.max).toBeNull()
  })

  it('shows a per-request model that only has interval pricing', () => {
    const n = normalizePricing(
      pricing({
        billing_mode: 'per_request',
        intervals: [{ min_tokens: 0, max_tokens: null, input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.04 }],
      }),
    )
    expect(n?.kind).toBe('request')
    expect(n?.first.unit).toBe(0.04)
  })

  it('uses image output price for image models', () => {
    // 后端按次 / 按图计费不读 image_output_price；只配了它时按「按 token 的图片输出价」展示。
    const n = normalizePricing(pricing({ billing_mode: 'image', image_output_price: 0.1 }))
    expect(n?.kind).toBe('token')
    expect(n?.mode).toBe('image')
    expect(n?.first).toMatchObject({ imageOutput: 0.1, unit: null, input: null, output: null })
    expect(n?.tiers).toEqual([])
  })

  it('returns null for per-request models without any price', () => {
    expect(normalizePricing(pricing({ billing_mode: 'per_request' }))).toBeNull()
    expect(normalizePricing(pricing({ billing_mode: 'image' }))).toBeNull()
    expect(normalizePricing(null)).toBeNull()
  })

  it('never uses image_output_price as the per-request price', () => {
    const base = normalizePricing(pricing({ billing_mode: 'image', image_output_price: 0.1, per_request_price: 0.04 }))
    expect(base?.kind).toBe('request')
    expect(base?.first.unit).toBe(0.04)
    expect(base?.first.imageOutput).toBeNull()

    const tiered = normalizePricing(
      pricing({
        billing_mode: 'image',
        image_output_price: 0.1,
        per_request_price: 0.04,
        intervals: [{ min_tokens: 0, max_tokens: null, tier_label: '2K', input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, per_request_price: 0.07 }],
      }),
    )
    expect(tiered?.first.unit).toBe(0.07)
  })

  it('takes the first interval as the per-request headline and falls back to the base price', () => {
    const iv = (min: number, max: number | null, price: number | null, label?: string) => ({
      min_tokens: min,
      max_tokens: max,
      tier_label: label,
      input_price: null,
      output_price: null,
      cache_read_price: null,
      cache_write_price: null,
      per_request_price: price,
    })
    const tiered = normalizePricing(
      pricing({
        billing_mode: 'per_request',
        per_request_price: 0.5,
        intervals: [iv(0, 200000, 0.04, 'HD'), iv(200000, null, 0.08)],
      }),
    )
    expect(tiered?.mode).toBe('per_request')
    expect(tiered?.first.unit).toBe(0.04)
    expect(tiered?.tiers.map((t) => t.prices.unit)).toEqual([0.04, 0.08])
    expect(tiered?.tiers[0].range.label).toBe('HD')

    // 首档没配价格时回退到基础价，和 token 模型的取值规则一致。
    const fallback = normalizePricing(
      pricing({ billing_mode: 'per_request', per_request_price: 0.5, intervals: [iv(0, 200000, null), iv(200000, null, 0.08)] }),
    )
    expect(fallback?.first.unit).toBe(0.5)
    expect(fallback?.tiers.map((t) => t.prices.unit)).toEqual([0.5, 0.08])
  })

  it('treats 0 as free and only null as not configured', () => {
    const free = normalizePricing(pricing({ input_price: 0, output_price: 0, cache_read_price: 0 }))
    expect(free?.first).toMatchObject({ input: 0, output: 0, cacheRead: 0, cacheWrite: null })

    const freeRequest = normalizePricing(pricing({ billing_mode: 'per_request', per_request_price: 0 }))
    expect(freeRequest?.kind).toBe('request')
    expect(freeRequest?.first.unit).toBe(0)

    const freeImage = normalizePricing(pricing({ billing_mode: 'image', image_output_price: 0 }))
    expect(freeImage?.first.imageOutput).toBe(0)

    expect(normalizePricing(pricing({ input_price: null, output_price: null }))).toBeNull()
    expect(normalizePricing(pricing({ input_price: -1, output_price: null }))).toBeNull()
  })
})

describe('price math', () => {
  it('credit, balance, plan and official prices', () => {
    expect(creditPrice(3e-6, 1.4, 'token')).toBeCloseTo(4.2)
    expect(balancePrice(3e-6, 1.4, 'token', fiat)).toBeCloseTo(4.2 / 13, 9)
    expect(balancePrice(3e-6, 1.4, 'token', usd)).toBeCloseTo(4.2)
    expect(officialPrice(3e-6, 'token')).toBe(3)
    const p = planPrice(3e-6, 1.4, 'token', fiat)!
    expect(p.min).toBeCloseTo(0.21)
    expect(p.max).toBeCloseTo(0.42)
    expect(p.exact).toBe(false)
  })

  it('plan price is exact with a card and absent in USD mode or without unit', () => {
    const exact = planPrice(1, 2, 'request', { ...fiat, subscriptionUnit: { min: 0.07, max: 0.07, exact: true } })!
    expect(exact).toMatchObject({ min: 0.14, max: 0.14, exact: true })
    expect(planPrice(1, 2, 'request', usd)).toBeNull()
    expect(planPrice(1, 2, 'request', { ...fiat, subscriptionUnit: null })).toBeNull()
  })

  it('m=1 keeps the credit price in fiat context', () => {
    expect(balancePrice(1e-6, 2, 'token', { ...fiat, rechargeMultiplier: 1 })).toBe(2)
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

describe('buildCatalog', () => {
  const channels: UserAvailableChannel[] = [
    {
      name: 'A',
      description: '',
      platforms: [
        {
          platform: 'openai',
          groups: [group(1, 'Stable', 1.4), group(2, 'Cheap', 0.5)],
          supported_models: [
            { name: 'gpt-x', platform: 'openai', pricing: pricing({ input_price: 2e-6, output_price: 8e-6 }) },
            { name: 'gpt-nopricing', platform: 'openai', pricing: null },
          ],
        },
      ],
    },
    {
      name: 'B',
      description: '',
      platforms: [
        {
          platform: 'openai',
          groups: [group(3, 'Other', 1)],
          supported_models: [{ name: 'gpt-x', platform: 'openai', pricing: pricing({ input_price: 4e-6, output_price: 8e-6 }) }],
        },
      ],
    },
  ]

  it('aggregates one model across sections using each group section pricing', () => {
    const catalog = buildCatalog(channels)
    const m = catalog.find((x) => x.name === 'gpt-x')!
    expect(m.entries.map((e) => e.group.name)).toEqual(['Cheap', 'Stable', 'Other'])
    expect(m.cheapest?.group.name).toBe('Cheap')
    // Other 的 section 单价是 4e-6，× 1 = 4e-6；Stable 是 2e-6 × 1.4 = 2.8e-6
    const other = m.entries.find((e) => e.group.id === 3)!
    expect(other.pricing.first.input).toBe(4e-6)
  })

  it('applies the user rate over the group default', () => {
    const m = buildCatalog(channels, { 1: 0.2 }).find((x) => x.name === 'gpt-x')!
    expect(m.cheapest?.group).toMatchObject({ name: 'Stable', rate: 0.2, baseRate: 1.4, hasCustomRate: true })
    const plain = buildCatalog(channels, { 1: 1.4 }).find((x) => x.name === 'gpt-x')!
    expect(plain.entries.find((e) => e.group.id === 1)?.group.hasCustomRate).toBe(false)
  })

  it('ranks an image-output-only model by its image output price', () => {
    const imageChannels: UserAvailableChannel[] = [
      {
        name: 'I',
        description: '',
        platforms: [
          {
            platform: 'openai',
            groups: [group(1, 'Stable', 2), group(2, 'Cheap', 1)],
            supported_models: [
              { name: 'img-x', platform: 'openai', pricing: pricing({ billing_mode: 'image', image_output_price: 40e-6 }) },
            ],
          },
        ],
      },
    ]
    const m = buildCatalog(imageChannels)[0]
    expect(m.kind).toBe('token')
    expect(m.cheapest?.group.name).toBe('Cheap')
    expect(m.cheapest?.pricing.first.imageOutput).toBe(40e-6)
  })

  it('keeps models without pricing but with no entries', () => {
    const m = buildCatalog(channels).find((x) => x.name === 'gpt-nopricing')!
    expect(m.entries).toEqual([])
    expect(m.cheapest).toBeNull()
    expect(m.kind).toBeNull()
  })
})

describe('formatTokenCount', () => {
  it('abbreviates round numbers', () => {
    expect(formatTokenCount(200000)).toBe('200K')
    expect(formatTokenCount(1000000)).toBe('1M')
    expect(formatTokenCount(1234)).toBe('1234')
  })
})
