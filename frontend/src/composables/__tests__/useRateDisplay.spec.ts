import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises } from '@vue/test-utils'

import type { PublicSettings, UserSubscription } from '@/types'

const getSubscriptionPricing = vi.hoisted(() => vi.fn())

vi.mock('@/api/subscriptions', () => ({
  default: { getSubscriptionPricing }
}))

import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useSubscriptionStore } from '@/stores/subscriptions'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '../useCurrencyDisplay'
import {
  PLAN_PRICING_RETRY_COOLDOWN_MS,
  loadPlanPricing,
  resetPlanPricingForTest,
  useRateDisplay
} from '../useRateDisplay'

/** 生产 codex 站的 /subscriptions/pricing：u_min = 0.04（D ≥ 210），u_max = 0.05（D = 30）。 */
const PROD_PRICING = {
  d_min: 30,
  d_max: 510,
  u_min: 0.04,
  u_max: 0.05,
  t_min: 30,
  t_max: 360,
  t_step: 30,
  d_floor: 210
}

function setSettings(settings: Record<string, unknown> | null) {
  useAppStore().cachedPublicSettings = settings as PublicSettings | null
}

/** 登录 / 退出：isAuthenticated 要 token 和 user 都在。 */
function setLoggedIn(loggedIn: boolean) {
  const auth = useAuthStore()
  auth.token = loggedIn ? 'test-token' : null
  auth.user = loggedIn ? ({ id: 1, role: 'user' } as never) : null
}

function setCards(cards: Array<Record<string, unknown>>) {
  useSubscriptionStore().activeSubscriptions = cards as unknown as UserSubscription[]
}

const RATES = [0.585, 1, 1.35, 1.4, 2, 3, 4, 5]

describe('useRateDisplay', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    window.localStorage.clear()
    resetFiatDataMissingForTest()
    resetPlanPricingForTest()
    // 展示模式是模块级单例，逐个用例显式复位。
    useCurrencyDisplay().setMode('fiat')
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
    // 接口需要登录，默认按已登录用户测；未登录的用例单独覆盖。
    setLoggedIn(true)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('m = 1（free 站）', () => {
    beforeEach(() => {
      setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
    })

    it('即使 localStorage 里存了 ¥ 偏好，isFiat 也为假', () => {
      window.localStorage.setItem('currency-display-mode', 'fiat')
      useCurrencyDisplay().setMode('fiat')

      expect(useRateDisplay().isFiat.value).toBe(false)
    })

    it('原样显示 r，套餐倍率恒为 null，不请求 /subscriptions/pricing', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
      expect(RATES.map((r) => rate.rateView(r).main)).toEqual(['0.585', '1', '1.35', '1.4', '2', '3', '4', '5'])
      expect(RATES.map((r) => rate.planLowestRate(r))).toEqual(RATES.map(() => null))
      expect(RATES.map((r) => rate.rateView(r).plan)).toEqual(RATES.map(() => undefined))
      expect(rate.planAvailable.value).toBe(false)
    })

    it('哪怕接口返回了 u_min（free 站 u_min = 1）也不出现「套餐低至」', async () => {
      await loadPlanPricing()
      getSubscriptionPricing.mockClear()

      const rate = useRateDisplay()
      expect(rate.rateView(1.4).plan).toBeUndefined()
      expect(rate.planRateForUnit(1.4, 0.04)).toBeNull()
    })
  })

  describe('m = 13，人民币模式', () => {
    it('请求一次 /subscriptions/pricing，多个组件同时用也只请求一次', async () => {
      useRateDisplay()
      useRateDisplay()
      useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })

    it('余额倍率 r ÷ 13、套餐低至 r × 0.04：方案里的整张表', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(RATES.map((r) => rate.rateView(r).main)).toEqual([
        '0.045',
        '0.0769',
        '0.104',
        '0.108',
        '0.154',
        '0.231',
        '0.308',
        '0.385'
      ])
      expect(RATES.map((r) => rate.rateView(r).plan)).toEqual([
        '0.0234',
        '0.04',
        '0.054',
        '0.056',
        '0.08',
        '0.12',
        '0.16',
        '0.2'
      ])
      expect(rate.planAvailable.value).toBe(true)
      expect(rate.planUnitMin.value).toBe(0.04)
    })

    it('rateView 既收默认倍率，也收带 rate_multiplier 的分组对象', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.rateView({ rate_multiplier: 1.4 })).toEqual(rate.rateView(1.4))
      expect(rate.rateView({ rate_multiplier: 1.4 }, 0.585)).toEqual(rate.rateView(1.4, 0.585))
      expect(rate.rateView({}).main).toBe('-')
      expect(rate.rateView({ rate_multiplier: null }).main).toBe('-')
    })

    it('专属倍率：主 0.045x（划线 0.108x），套餐 0.0234x（划线 0.056x）', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      const view = rate.rateView(1.4, 0.585)
      expect(view).toMatchObject({
        main: '0.045',
        mainStruck: '0.108',
        plan: '0.0234',
        planStruck: '0.056'
      })
    })

    it('接口还没回来时只有主倍率，回来之后响应式补上套餐低至', async () => {
      let resolve: (value: unknown) => void = () => {}
      getSubscriptionPricing.mockReturnValue(new Promise((r) => (resolve = r)))

      const rate = useRateDisplay()
      expect(rate.rateView(1.4)).toMatchObject({ main: '0.108' })
      expect(rate.rateView(1.4).plan).toBeUndefined()

      resolve(PROD_PRICING)
      await flushPromises()
      expect(rate.rateView(1.4).plan).toBe('0.056')
    })

    it('u_min 与 u_max 写反时取较小的（后端按较小的夹）', async () => {
      getSubscriptionPricing.mockResolvedValue({ ...PROD_PRICING, u_min: 0.05, u_max: 0.04 })
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.planUnitMin.value).toBe(0.04)
    })
  })

  describe('美元模式', () => {
    beforeEach(() => {
      useCurrencyDisplay().setMode('usd')
    })

    it('原始 r，没有套餐倍率；专属倍率照旧划线', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.isFiat.value).toBe(false)
      expect(RATES.map((r) => rate.rateView(r).main)).toEqual(['0.585', '1', '1.35', '1.4', '2', '3', '4', '5'])
      expect(RATES.map((r) => rate.rateView(r).plan)).toEqual(RATES.map(() => undefined))
      expect(rate.rateView(1.4, 0.585)).toMatchObject({ main: '0.585', mainStruck: '1.4' })
      expect(rate.planAvailable.value).toBe(false)
    })

    it('加载器与展示模式无关：m ≠ 1 且支付没关就请求，切换 ¥ / $ 不会再请求', async () => {
      const rate = useRateDisplay()
      await flushPromises()
      useCurrencyDisplay().setMode('fiat')
      await flushPromises()

      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
      expect(rate.rateView(1.4).plan).toBe('0.056')
    })
  })

  describe('加载器的触发条件', () => {
    it('payment_enabled = false：不请求，没有套餐倍率', async () => {
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      const rate = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
      expect(rate.rateView(1.4)).toMatchObject({ main: '0.108' })
      expect(rate.rateView(1.4).plan).toBeUndefined()
    })

    it('payment_enabled 缺省视为开启（公开设置里没有这个字段时）', async () => {
      setSettings({ balance_recharge_multiplier: 13 })
      const rate = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
      expect(rate.rateView(1.4).plan).toBe('0.056')
    })

    it('公开设置还没加载：m 按 1，不请求', async () => {
      setSettings(null)
      const rate = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
      expect(rate.isFiat.value).toBe(false)
    })

    it('设置晚到：m 从 1 变成 13 后才请求；支付开关切换时套餐倍率随之出现 / 消失，不重复请求', async () => {
      setSettings(null)
      const rate = useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).not.toHaveBeenCalled()

      setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
      expect(rate.rateView(1.4).plan).toBe('0.056')

      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      await flushPromises()
      expect(rate.rateView(1.4).plan).toBeUndefined()

      setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
      await flushPromises()
      expect(rate.rateView(1.4).plan).toBe('0.056')
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })

    it('loadPlanPricing 重复调用返回同一个请求', async () => {
      const first = loadPlanPricing()
      const second = loadPlanPricing()

      expect(second).toBe(first)
      await first
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })
  })

  describe('失败后的冷却', () => {
    const COOLDOWN = PLAN_PRICING_RETRY_COOLDOWN_MS

    /** 只假冒 Date，不动 setTimeout / 微任务，flushPromises 照常能用。 */
    function freezeClock(startMs = 1_000_000) {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(startMs)
    }

    it('冷却时长是 60 秒', () => {
      expect(COOLDOWN).toBe(60_000)
    })

    it('冷却期内的每一次挂载都不重发，哪怕只差 1 毫秒到点', async () => {
      freezeClock()
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)

      for (const elapsed of [1, 1_000, 30_000, COOLDOWN - 1]) {
        vi.setSystemTime(1_000_000 + elapsed)
        useRateDisplay()
        await loadPlanPricing()
      }
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })

    it('满 60 秒后的下一次挂载重试；重试成功后套餐低至出现，之后不再请求', async () => {
      freezeClock()
      getSubscriptionPricing.mockRejectedValueOnce(new Error('boom')).mockResolvedValue(PROD_PRICING)
      const first = useRateDisplay()
      await flushPromises()
      expect(first.rateView(1.4).plan).toBeUndefined()

      vi.setSystemTime(1_000_000 + COOLDOWN)
      const second = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).toHaveBeenCalledTimes(2)
      // 已经挂着的组件也是同一份缓存，会随之补上。
      expect(first.rateView(1.4).plan).toBe('0.056')
      expect(second.rateView(1.4).plan).toBe('0.056')

      vi.setSystemTime(1_000_000 + COOLDOWN * 10)
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(2)
    })

    it('重试又失败：重新计时，再过 60 秒才有下一次', async () => {
      freezeClock()
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      useRateDisplay()
      await flushPromises()

      vi.setSystemTime(1_000_000 + COOLDOWN)
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(2)

      vi.setSystemTime(1_000_000 + COOLDOWN + COOLDOWN - 1)
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(2)

      vi.setSystemTime(1_000_000 + COOLDOWN * 2)
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(3)
    })

    it('重试还在路上时，再有组件挂载只共用这一次请求', async () => {
      freezeClock()
      getSubscriptionPricing.mockRejectedValueOnce(new Error('boom'))
      useRateDisplay()
      await flushPromises()

      let resolve: (value: unknown) => void = () => {}
      getSubscriptionPricing.mockReturnValue(new Promise((r) => (resolve = r)))
      vi.setSystemTime(1_000_000 + COOLDOWN)
      const a = loadPlanPricing()
      const b = loadPlanPricing()
      expect(b).toBe(a)
      // 请求还没回来时即使再过很久，也不会叠一个新请求。
      vi.setSystemTime(1_000_000 + COOLDOWN * 5)
      expect(loadPlanPricing()).toBe(a)
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(2)

      resolve(PROD_PRICING)
      await a
      expect(useRateDisplay().rateView(1.4).plan).toBe('0.056')
    })

    it('成功过的不会因为时间久了重新请求', async () => {
      freezeClock()
      useRateDisplay()
      await flushPromises()

      vi.setSystemTime(1_000_000 + COOLDOWN * 100)
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })
  })

  describe('登录态', () => {
    it('未登录：不请求，只有主倍率', async () => {
      setLoggedIn(false)
      const rate = useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
      expect(rate.rateView(1.4).main).toBe('0.108')
      expect(rate.rateView(1.4).plan).toBeUndefined()
      expect(rate.planAvailable.value).toBe(false)
    })

    it('只有 token 或只有用户信息都不算登录', async () => {
      const auth = useAuthStore()
      auth.token = 'test-token'
      auth.user = null
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).not.toHaveBeenCalled()

      auth.token = null
      auth.user = { id: 1, role: 'user' } as never
      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('先挂载后登录：登录那一刻补发一次请求，之后不重复', async () => {
      setLoggedIn(false)
      const rate = useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).not.toHaveBeenCalled()

      setLoggedIn(true)
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
      expect(rate.rateView(1.4).plan).toBe('0.056')

      setLoggedIn(false)
      setLoggedIn(true)
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })

    it('已登录但 m = 1 或支付关闭：照旧不请求（与原条件取与）', async () => {
      setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
      useRateDisplay()
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      useRateDisplay()
      await flushPromises()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })
  })

  describe('R2 抑制条件', () => {
    it('/subscriptions/pricing 失败：静默，只有主倍率；冷却期内别的组件再用也不重发', async () => {
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.rateView(1.4)).toMatchObject({ main: '0.108' })
      expect(rate.rateView(1.4).plan).toBeUndefined()
      expect(rate.planAvailable.value).toBe(false)

      useRateDisplay()
      await flushPromises()
      expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    })

    it('接口同步抛错或返回空也静默', async () => {
      getSubscriptionPricing.mockImplementation(() => {
        throw new Error('sync boom')
      })
      expect(() => useRateDisplay()).not.toThrow()
      await flushPromises()
      expect(useRateDisplay().rateView(1.4).plan).toBeUndefined()

      resetPlanPricingForTest()
      getSubscriptionPricing.mockReset().mockResolvedValue(undefined)
      const rate = useRateDisplay()
      await flushPromises()
      expect(rate.planUnitMin.value).toBeNull()
      expect(rate.rateView(1.4).plan).toBeUndefined()
    })

    it('u_min = 0：没有套餐倍率', async () => {
      getSubscriptionPricing.mockResolvedValue({ ...PROD_PRICING, u_min: 0 })
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.planUnitMin.value).toBeNull()
      expect(rate.rateView(1.4).plan).toBeUndefined()
    })

    it('m × u_min ≥ 1：没有套餐倍率（13 × 0.08 = 1.04）', async () => {
      getSubscriptionPricing.mockResolvedValue({ ...PROD_PRICING, u_min: 0.08, u_max: 0.09 })
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.rateView(1.4).plan).toBeUndefined()
      expect(rate.planAvailable.value).toBe(false)
    })

    it('r = 0：套餐倍率与主倍率相等，只剩主倍率', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.rateView(0)).toEqual({ main: '0', mainValue: 0 })
    })
  })

  describe('有卡 / 无卡', () => {
    it('有生效卡：多一个 yourPlan（r × u_card），套餐低至仍是 u_min', async () => {
      setCards([{ status: 'active', fiat_per_credit: 0.0467 }])
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.cardUnit.value).toBe(0.0467)
      expect([1.4, 3, 4, 5].map((r) => rate.rateView(r).yourPlan)).toEqual(['0.0654', '0.14', '0.187', '0.234'])
      expect(rate.rateView(5)).toMatchObject({ main: '0.385', plan: '0.2', yourPlan: '0.234' })
    })

    it('无卡 / 卡过期 / 卡没有单价：没有 yourPlan，其余数字不变', async () => {
      const rate = useRateDisplay()
      await flushPromises()
      const baseline = rate.rateView(5)
      expect(baseline.yourPlan).toBeUndefined()

      for (const cards of [
        [],
        [{ status: 'expired', fiat_per_credit: 0.0467 }],
        [{ status: 'active' }]
      ]) {
        setCards(cards)
        expect(rate.rateView(5)).toEqual(baseline)
      }
    })

    it('卡列表变化时响应式更新', async () => {
      const rate = useRateDisplay()
      await flushPromises()
      expect(rate.rateView(5).yourPlan).toBeUndefined()

      setCards([{ status: 'active', fiat_per_credit: 0.0467 }])
      expect(rate.rateView(5).yourPlan).toBe('0.234')
    })
  })

  describe('购买面板与用量行', () => {
    it('planRateForUnit：D = 210 → 0.056，D = 30 → 0.07，余额倍率不变', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      expect(rate.formatRate(rate.planRateForUnit(1.4, 0.04))).toBe('0.056')
      expect(rate.formatRate(rate.planRateForUnit(1.4, 0.05))).toBe('0.07')
      expect(rate.formatRate(rate.balanceRate(1.4))).toBe('0.108')
    })

    it('planRateForUnit 不依赖 /subscriptions/pricing 是否取到，但受 R2 的 a、c、d 限制', async () => {
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      const rate = useRateDisplay()
      await flushPromises()
      expect(rate.formatRate(rate.planRateForUnit(1.4, 0.04))).toBe('0.056')

      expect(rate.planRateForUnit(1.4, 0.08)).toBeNull()
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      expect(rate.planRateForUnit(1.4, 0.04)).toBeNull()
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
      useCurrencyDisplay().setMode('usd')
      expect(rate.planRateForUnit(1.4, 0.04)).toBeNull()
    })

    it('usageRowRate：余额行 0.108、套餐行 0.0654、老行回落、total_cost = 0 为 null', async () => {
      const rate = useRateDisplay()
      await flushPromises()

      const wallet = { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0.84 / 13 }
      const plan = { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0.84 * 0.0467 }
      expect(rate.formatRate(rate.usageRowRate(wallet))).toBe('0.108')
      expect(rate.formatRate(rate.usageRowRate(plan))).toBe('0.0654')
      expect(rate.formatRate(rate.usageRowRate({ total_cost: 0.6, rate_multiplier: 1.4 }))).toBe('0.108')
      expect(rate.usageRowRate({ total_cost: 0, rate_multiplier: 1.4, fiat_cost: 0 })).toBeNull()
    })

    it('usageRowRate 在美元模式和 free 站原样返回 rate_multiplier', async () => {
      const row = { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0.84 / 13 }
      useCurrencyDisplay().setMode('usd')
      expect(useRateDisplay().usageRowRate(row)).toBe(1.4)

      useCurrencyDisplay().setMode('fiat')
      setSettings({ balance_recharge_multiplier: 1 })
      expect(useRateDisplay().usageRowRate(row)).toBe(1.4)
    })
  })

  it('formatRate / formatRawRate 随入口导出，调用方不用再单独 import', () => {
    const rate = useRateDisplay()

    expect(rate.formatRate(1 / 13)).toBe('0.0769')
    expect(rate.formatRawRate(1.4)).toBe('1.4')
    expect(rate.ctx.value).toMatchObject({ isFiat: true, rechargeMultiplier: 13, paymentEnabled: true })
  })
})
