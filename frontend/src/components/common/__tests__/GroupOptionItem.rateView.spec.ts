import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, ref } from 'vue'

const getSubscriptionPricing = vi.hoisted(() => vi.fn())
const i18nState = vi.hoisted(() => ({ lang: 'zh-CN' as 'zh-CN' | 'zh-HK' | 'en' }))
vi.mock('@/api/subscriptions', () => ({ default: { getSubscriptionPricing } }))

// 测试环境用的是 vue-i18n 的 runtime 构建，不能现场编译消息；
// 这里按当前语言的文案表取字再替换 {占位符}，断言的是用户真正看到的字。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const { default: zhHK } = await import('@/i18n/locales/zh-HK')
  const { default: en } = await import('@/i18n/locales/en')
  const tables = { 'zh-CN': zhCN, 'zh-HK': zhHK, en } as const
  const translate = (key: string, params: Record<string, unknown> = {}) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], tables[i18nState.lang])
    if (typeof hit !== 'string') return key
    return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
  }
  return { ...actual, useI18n: () => ({ locale: ref(i18nState.lang), t: translate }) }
})

import { useGroupRateView } from '@/composables/useGroupRateView'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { PublicSettings } from '@/types'
import GroupOptionItem from '../GroupOptionItem.vue'

const PROD_PRICING = { d_min: 30, d_max: 510, u_min: 0.04, u_max: 0.05, t_min: 30, t_max: 360, t_step: 30, d_floor: 210 }

/** 改动前（origin/main 975bdf6f4）free 站药丸的悬停原文，一个字不能变。 */
const LEGACY_TIP = {
  'zh-CN':
    '「Nx 倍率」是该分组相对官方价的扣额度速度：数字越大、池子越稳定，同样的用量扣得也越多；它是系统扣费的系数，不是最终价格。叠加套餐折扣后你实际相当于官方价几折，见分组描述。',
  'zh-HK':
    '「Nx 倍率」是該分組相對官方價的扣額度速度：數字越大、池子越穩定，同樣的用量扣得也越多；它是系統扣費的系數，不是最終價格。疊加套餐折扣後你實際相當於官方價幾折，見分組描述。',
  en: 'The "Nx rate" is how fast this group burns your balance relative to official pricing: a higher number means a more stable pool but also more balance consumed for the same usage. It is the billing coefficient, not the final price. See the group description for your effective fraction of official pricing after the plan discount.'
} as const

const OPTION = {
  name: 'codex特惠分组',
  platform: 'openai',
  rateMultiplier: 1.4,
  description: '不保证稳定性，挂了切其它分组。出问题会积极修复。'
}
const CUSTOM = { ...OPTION, userRateMultiplier: 0.585 }

type OptionProps = Record<string, unknown>

const Harness = defineComponent({
  components: { GroupOptionItem },
  props: { option: { type: Object, required: true } },
  setup() {
    return useGroupRateView()
  },
  template: `<GroupOptionItem v-bind="option" :rate-view="groupRateView(option.rateMultiplier, option.userRateMultiplier)" />`
})

function setSettings(settings: Record<string, unknown> | null) {
  useAppStore().cachedPublicSettings = settings as PublicSettings | null
}

const mountPlain = (option: OptionProps) => mount(GroupOptionItem, { props: option as never })
const mountWithView = (option: OptionProps) => mount(Harness, { props: { option } })

/** 药丸：带 rounded-full 的那个 span。 */
const pillOf = (wrapper: ReturnType<typeof mount>) => wrapper.get('span.rounded-full')

describe('GroupOptionItem 倍率药丸', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    window.localStorage.clear()
    i18nState.lang = 'zh-CN'
    resetFiatDataMissingForTest()
    resetPlanPricingForTest()
    useCurrencyDisplay().setMode('fiat')
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    const auth = useAuthStore()
    auth.token = 'test-token'
    auth.user = { id: 1, role: 'user' } as never
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
  })

  describe('不传 rateView', () => {
    it('药丸「1.4x 倍率」，悬停是原文案；没有套餐低至', () => {
      const wrapper = mountPlain(OPTION)

      expect(pillOf(wrapper).text()).toBe('1.4x 倍率')
      expect(pillOf(wrapper).attributes('title')).toBe(LEGACY_TIP['zh-CN'])
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
    })

    it('专属倍率：划掉默认值，高亮专属值', () => {
      const wrapper = mountPlain({ ...OPTION, rateMultiplier: 2, userRateMultiplier: 1.5 })

      expect(pillOf(wrapper).get('.line-through').text()).toBe('2x')
      expect(pillOf(wrapper).get('.font-bold').text()).toBe('1.5x')
    })

    it('没有 rateMultiplier 就没有药丸', () => {
      const wrapper = mountPlain({ name: 'g', platform: 'openai' })

      expect(wrapper.find('span.rounded-full').exists()).toBe(false)
    })
  })

  describe.each(['zh-CN', 'zh-HK', 'en'] as const)('free 站（充值倍率 1）逐字不变（%s）', (lang) => {
    beforeEach(() => {
      i18nState.lang = lang
      setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
    })

    it('药丸文字和悬停与改动前一模一样，也没有套餐低至', async () => {
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).text()).toBe(lang === 'en' ? '1.4x rate' : '1.4x 倍率')
      expect(pillOf(wrapper).attributes('title')).toBe(LEGACY_TIP[lang])
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('专属倍率照旧划线，悬停同样是原文', async () => {
      const wrapper = mountWithView({ ...OPTION, rateMultiplier: 2, userRateMultiplier: 1.5 })
      await flushPromises()

      expect(pillOf(wrapper).get('.line-through').text()).toBe('2x')
      expect(pillOf(wrapper).get('.font-bold').text()).toBe('1.5x')
      expect(pillOf(wrapper).attributes('title')).toBe(LEGACY_TIP[lang])
    })
  })

  it('free 站即使 localStorage 里存着 ¥ 偏好，也是美元口径的原文', async () => {
    setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
    window.localStorage.setItem('currency-display-mode', 'fiat')
    const wrapper = mountWithView(OPTION)
    await flushPromises()

    expect(pillOf(wrapper).text()).toBe('1.4x 倍率')
    expect(pillOf(wrapper).attributes('title')).toBe(LEGACY_TIP['zh-CN'])
  })

  describe('其他站点的美元模式', () => {
    beforeEach(() => {
      useCurrencyDisplay().setMode('usd')
    })

    it('药丸仍是原始倍率，悬停说明每 $1 官方价扣多少额度，没有套餐低至', async () => {
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).text()).toBe('1.4x 倍率')
      expect(pillOf(wrapper).attributes('title')).toBe('倍率 1.4x：官方价每 $1 的用量，扣 $1.4 额度。倍率越低越省。')
      expect(wrapper.find('[data-test="plan-rate"]').exists()).toBe(false)
    })

    it.each([
      ['zh-HK', '倍率 1.4x：官方價每 $1 的用量，扣 $1.4 額度。倍率越低越省。'],
      ['en', 'Rate 1.4x: every $1 of usage at official pricing deducts $1.4 of quota. A lower rate costs less.']
    ] as const)('%s 的悬停文案', async (lang, tip) => {
      i18nState.lang = lang
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).attributes('title')).toBe(tip)
    })
  })

  describe('人民币模式（充值倍率 13）', () => {
    it('药丸 0.108x 倍率，下面一行「套餐低至 0.056x」', async () => {
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).text()).toBe('0.108x 倍率')
      const plan = wrapper.get('[data-test="plan-rate"]')
      expect(plan.text()).toBe('套餐低至 0.056x')
      // 药丸和套餐低至在同一列、右对齐，窄屏也不收起
      expect(plan.element.parentElement).toBe(pillOf(wrapper).element.parentElement)
      expect(plan.element.parentElement?.className).toContain('items-end')
      expect(plan.classes()).not.toContain('hidden')
      expect(plan.classes()).toEqual(expect.arrayContaining(['text-gray-700', 'dark:text-gray-300']))
    })

    it('悬停：每 $1 官方价实付多少元，再加套餐说明', async () => {
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).attributes('title')).toBe(
        '倍率 0.108x：官方价每 $1 的用量，实付 ¥0.108。倍率越低越省。\n开通套餐后，同样的用量最低按 0.056x 扣费。'
      )
    })

    it.each([
      [
        'zh-HK',
        '倍率 0.108x：官方價每 $1 的用量，實付 ¥0.108。倍率越低越省。\n開通套餐後，同樣的用量最低按 0.056x 扣費。',
        '套餐低至 0.056x',
        '0.108x 倍率'
      ],
      [
        'en',
        'Rate 0.108x: you pay ¥0.108 for every $1 of usage at official pricing. A lower rate costs less.\nWith a plan, the same usage is charged at 0.056x or lower.',
        'Plan as low as 0.056x',
        '0.108x rate'
      ]
    ] as const)('%s 的文案', async (lang, tip, plan, pill) => {
      i18nState.lang = lang
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(pillOf(wrapper).attributes('title')).toBe(tip)
      expect(wrapper.get('[data-test="plan-rate"]').text()).toBe(plan)
      expect(pillOf(wrapper).text()).toBe(pill)
    })

    it('专属倍率：药丸和套餐低至都是「默认划掉 + 专属高亮」', async () => {
      const wrapper = mountWithView(CUSTOM)
      await flushPromises()

      expect(pillOf(wrapper).get('.line-through').text()).toBe('0.108x')
      expect(pillOf(wrapper).get('.font-bold').text()).toBe('0.045x')
      const plan = wrapper.get('[data-test="plan-rate"]')
      expect(plan.get('.line-through').text()).toBe('0.056x')
      expect(plan.get('.font-bold').text()).toBe('0.0234x')
      expect(pillOf(wrapper).attributes('title')).toContain('倍率 0.045x：官方价每 $1 的用量，实付 ¥0.045。')
    })

    it('套餐定价取不到或支付关闭：只有药丸，没有套餐低至', async () => {
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      const failed = mountWithView(OPTION)
      await flushPromises()
      expect(pillOf(failed).text()).toBe('0.108x 倍率')
      expect(failed.find('[data-test="plan-rate"]').exists()).toBe(false)
      expect(pillOf(failed).attributes('title')).toBe('倍率 0.108x：官方价每 $1 的用量，实付 ¥0.108。倍率越低越省。')

      resetPlanPricingForTest()
      getSubscriptionPricing.mockResolvedValue(PROD_PRICING)
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      const closed = mountWithView(OPTION)
      await flushPromises()
      expect(closed.find('[data-test="plan-rate"]').exists()).toBe(false)
    })
  })

  describe('其余部分不受影响', () => {
    it('描述照常显示，悬停仍是描述；选中时勾号与药丸对齐', async () => {
      const wrapper = mountWithView({ ...OPTION, selected: true })
      await flushPromises()

      expect(wrapper.text()).toContain(OPTION.description)
      expect(wrapper.find('[title]').attributes('title')).toBe(OPTION.description)
      const check = wrapper.get('svg.text-primary-600')
      expect(check.classes()).toContain('mt-1')
      expect(wrapper.text()).toContain('codex特惠分组')
    })

    it('不选中就没有勾号', async () => {
      const wrapper = mountWithView(OPTION)
      await flushPromises()

      expect(wrapper.find('svg.text-primary-600').exists()).toBe(false)
    })
  })
})
