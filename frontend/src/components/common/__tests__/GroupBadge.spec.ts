import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, ref } from 'vue'

const getSubscriptionPricing = vi.hoisted(() => vi.fn())
vi.mock('@/api/subscriptions', () => ({ default: { getSubscriptionPricing } }))

// 测试环境用的是 vue-i18n 的 runtime 构建，不能现场编译消息；
// 这里直接按 zh-CN 语言包的点路径取文案并替换 {占位符}，断言的是用户真正看到的字。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const translate = (key: string, params: Record<string, unknown> = {}) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    if (typeof hit !== 'string') return key
    return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
  }
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate }) }
})

import { useGroupRateView } from '@/composables/useGroupRateView'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { PublicSettings } from '@/types'
import GroupBadge from '../GroupBadge.vue'

const PROD_PRICING = { d_min: 30, d_max: 510, u_min: 0.04, u_max: 0.05, t_min: 30, t_max: 360, t_step: 30, d_floor: 210 }

/** 去掉注释占位、图标和标签之间的空白，只比较用户看得到的结构。 */
const norm = (html: string) =>
  html
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/<svg[\s\S]*?<\/svg>/g, '<svg/>')
    .replace(/>\s+</g, '><')
    .trim()

const OPENAI_BOX =
  'inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400'
const PILL = 'px-1.5 py-0.5 rounded text-[10px] font-semibold bg-black/10 dark:bg-white/10'

/**
 * 改动前（origin/main 975bdf6f4 的 GroupBadge）逐个配置渲染出来的结构，去掉注释和图标后原样抄在这里。
 * 后台调用点不传 rateView、free 站和美元模式的用户端传了 rateView，都必须和它逐字相同。
 */
const LEGACY = {
  standard: {
    props: { name: 'codex特惠分组', platform: 'openai', rateMultiplier: 1.4 },
    html: `<span class="${OPENAI_BOX}"><svg/><span class="truncate">codex特惠分组</span><span class="${PILL}">1.4x</span></span>`
  },
  custom: {
    props: { name: 'kiro cc', platform: 'anthropic', rateMultiplier: 2, userRateMultiplier: 1.5 },
    html:
      '<span class="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400"><svg/><span class="truncate">kiro cc</span>' +
      `<span class="${PILL}"><span class="line-through opacity-50 mr-0.5">2x</span><span class="font-bold">1.5x</span></span></span>`
  },
  subscriptionDays: {
    props: { name: 'Pro 订阅', platform: 'openai', subscriptionType: 'subscription', daysRemaining: 5, rateMultiplier: 3 },
    html:
      '<span class="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400"><svg/><span class="truncate">Pro 订阅</span>' +
      '<span class="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-amber-200/80 text-amber-800 dark:bg-amber-800/50 dark:text-amber-300">5天</span></span>'
  },
  subscriptionAlwaysRate: {
    props: { name: 'Pro 订阅', platform: 'openai', subscriptionType: 'subscription', alwaysShowRate: true, rateMultiplier: 3 },
    html:
      '<span class="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400"><svg/><span class="truncate">Pro 订阅</span>' +
      '<span class="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-emerald-200/60 text-emerald-800 dark:bg-emerald-800/40 dark:text-emerald-300">3x</span></span>'
  },
  rateHidden: {
    props: { name: 'g', platform: 'openai', rateMultiplier: 1, showRate: false },
    html: `<span class="${OPENAI_BOX}"><svg/><span class="truncate">g</span></span>`
  }
} as const

type BadgeProps = Record<string, unknown>

/** 和用户端页面一样，用 useGroupRateView 生成 rateView 再交给徽标。 */
const Harness = defineComponent({
  components: { GroupBadge },
  props: { badge: { type: Object, required: true } },
  setup() {
    return useGroupRateView()
  },
  template: `<GroupBadge v-bind="badge" :rate-view="groupRateView(badge.rateMultiplier, badge.userRateMultiplier)" />`
})

function setSettings(settings: Record<string, unknown> | null) {
  useAppStore().cachedPublicSettings = settings as PublicSettings | null
}

function mountWithRateView(badge: BadgeProps) {
  return mount(Harness, { props: { badge } })
}

describe('GroupBadge', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    window.localStorage.clear()
    resetFiatDataMissingForTest()
    resetPlanPricingForTest()
    useCurrencyDisplay().setMode('fiat')
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    const auth = useAuthStore()
    auth.token = 'test-token'
    auth.user = { id: 1, role: 'user' } as never
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
  })

  describe('不传 rateView（后台调用点）', () => {
    it.each(Object.entries(LEGACY))('%s：与改动前逐字相同，没有悬停提示', (_name, { props, html }) => {
      const wrapper = mount(GroupBadge, { props: props as never })

      expect(norm(wrapper.html())).toBe(html)
      expect(wrapper.attributes('title')).toBeUndefined()
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
    })

    it('站点是 13 倍充值、人民币模式，后台徽标仍是原始倍率', () => {
      const wrapper = mount(GroupBadge, { props: LEGACY.standard.props })

      expect(wrapper.text()).toContain('1.4x')
      expect(wrapper.text()).not.toContain('0.108')
      expect(wrapper.text()).not.toContain('套餐低至')
    })
  })

  describe('free 站（充值倍率 1）：逐字不变', () => {
    beforeEach(() => {
      setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
    })

    it.each(Object.entries(LEGACY))('%s：传了 rateView 也与改动前逐字相同', async (_name, { props, html }) => {
      const wrapper = mountWithRateView(props)
      await flushPromises()

      expect(norm(wrapper.html())).toBe(html)
      expect(wrapper.attributes('title')).toBeUndefined()
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('localStorage 里存着 ¥ 偏好也一样', async () => {
      window.localStorage.setItem('currency-display-mode', 'fiat')
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(norm(wrapper.html())).toBe(LEGACY.standard.html)
    })
  })

  describe('其他站点的美元模式：倍率照旧，没有套餐低至', () => {
    beforeEach(() => {
      useCurrencyDisplay().setMode('usd')
    })

    it.each(Object.entries(LEGACY))('%s：与改动前逐字相同', async (_name, { props, html }) => {
      const wrapper = mountWithRateView(props)
      await flushPromises()

      expect(norm(wrapper.html())).toBe(html)
      expect(wrapper.attributes('title')).toBeUndefined()
    })
  })

  describe('人民币模式（充值倍率 13）', () => {
    it('余额等效倍率 0.108x，旁边是「套餐低至 0.056x」', async () => {
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(wrapper.get('span.truncate').text()).toBe('codex特惠分组')
      expect(wrapper.get(`span[class="${PILL}"]`).text()).toBe('0.108x')
      expect(wrapper.get('[data-test="plan-rate"]').text()).toBe('套餐低至 0.056x')
      // 文字颜色 gray-700 / 深色 gray-300，在徽标底色上够读
      expect(wrapper.get('[data-test="plan-rate"]').classes()).toEqual(
        expect.arrayContaining(['text-gray-700', 'dark:text-gray-300'])
      )
    })

    it('窄屏收起套餐低至（hidden sm:inline），悬停提示里有完整说明', async () => {
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(wrapper.get('[data-test="plan-rate"]').classes()).toEqual(expect.arrayContaining(['hidden', 'sm:inline']))
      expect(wrapper.attributes('title')).toBe(
        '倍率 0.108x：官方价每 $1 的用量，实付 ¥0.108。倍率越低越省。\n开通套餐后，同样的用量最低按 0.056x 扣费。'
      )
    })

    it('inlinePlan=false（表格行把套餐低至放在徽标下一行）：徽标里没有，悬停提示照旧', async () => {
      const wrapper = mountWithRateView({ ...LEGACY.standard.props, inlinePlan: false })
      await flushPromises()

      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
      expect(wrapper.get(`span[class="${PILL}"]`).text()).toBe('0.108x')
      expect(wrapper.attributes('title')).toContain('开通套餐后，同样的用量最低按 0.056x 扣费。')
    })

    it('专属倍率：默认值划掉，专属值高亮，套餐低至也一样', async () => {
      const wrapper = mountWithRateView({ ...LEGACY.standard.props, userRateMultiplier: 0.585 })
      await flushPromises()

      const pill = wrapper.get(`span[class="${PILL}"]`)
      expect(pill.get('.line-through').text()).toBe('0.108x')
      expect(pill.get('.font-bold').text()).toBe('0.045x')
      const plan = wrapper.get('[data-test="plan-rate"]')
      expect(plan.get('.line-through').text()).toBe('0.056x')
      expect(plan.get('.font-bold').text()).toBe('0.0234x')
      expect(plan.text()).toBe('套餐低至 0.056x0.0234x')
    })

    it('套餐定价还没回来时只有主倍率、没有悬停；回来之后补上', async () => {
      let resolve: (value: unknown) => void = () => {}
      getSubscriptionPricing.mockReturnValue(new Promise((r) => (resolve = r)))
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(wrapper.text()).toContain('0.108x')
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
      expect(wrapper.attributes('title')).toBeUndefined()

      resolve(PROD_PRICING)
      await flushPromises()
      expect(wrapper.get('[data-test="plan-rate"]').text()).toBe('套餐低至 0.056x')
      expect(wrapper.attributes('title')).toContain('0.056x')
    })

    it('套餐定价取不到：只有主倍率', async () => {
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(wrapper.text()).toContain('0.108x')
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
    })

    it('支付关闭：只有主倍率', async () => {
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()

      expect(wrapper.text()).toContain('0.108x')
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
    })

    it('有生效套餐卡：悬停多一行「你的套餐当前按 x 扣费」，徽标上的数字不变', async () => {
      const wrapper = mountWithRateView(LEGACY.standard.props)
      await flushPromises()
      const before = wrapper.text()

      const { useSubscriptionStore } = await import('@/stores/subscriptions')
      useSubscriptionStore().activeSubscriptions = [{ status: 'active', fiat_per_credit: 0.0467 }] as never
      await flushPromises()

      expect(wrapper.text()).toBe(before)
      expect(wrapper.attributes('title')).toBe(
        '倍率 0.108x：官方价每 $1 的用量，实付 ¥0.108。倍率越低越省。\n' +
          '开通套餐后，同样的用量最低按 0.056x 扣费。\n你的套餐当前按 0.0654x 扣费。'
      )
    })

    it('订阅分组显示「订阅 / 剩余天数」，不加套餐低至，与改动前相同', async () => {
      const wrapper = mountWithRateView(LEGACY.subscriptionDays.props)
      await flushPromises()

      expect(norm(wrapper.html())).toBe(LEGACY.subscriptionDays.html)
    })

    it('订阅分组开了 alwaysShowRate：显示等效倍率和套餐低至', async () => {
      const wrapper = mountWithRateView(LEGACY.subscriptionAlwaysRate.props)
      await flushPromises()

      expect(wrapper.get('span[class*="bg-emerald-200/60"]').text()).toBe('0.231x')
      expect(wrapper.get('[data-test="plan-rate"]').text()).toBe('套餐低至 0.12x')
    })

    it('showRate = false 时什么倍率都不出现', async () => {
      const wrapper = mountWithRateView(LEGACY.rateHidden.props)
      await flushPromises()

      expect(norm(wrapper.html())).toBe(LEGACY.rateHidden.html)
      expect(wrapper.attributes('title')).toBeUndefined()
    })
  })
})
