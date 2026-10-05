import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'

import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { KeyFallbackChain, KeyFallbackChainItem } from '@/types'

const { getChain, replaceChain, getSubscriptionPricing } = vi.hoisted(() => ({
  getChain: vi.fn(),
  replaceChain: vi.fn(),
  getSubscriptionPricing: vi.fn()
}))
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
  return { ...actual, useI18n: () => ({ t: translate }) }
})

vi.mock('@/api/keyFallback', () => ({
  keyFallbackAPI: { getChain, replaceChain, listChains: vi.fn() }
}))

// jsdom 里 SortableJS 拖不起来：用一个会把 v-model / end 事件透出来的替身，
// 测试里直接触发这两个事件，等价于用户拖完一次。
vi.mock('vue-draggable-plus', () => ({
  VueDraggable: defineComponent({
    name: 'VueDraggable',
    props: { modelValue: { type: Array, default: () => [] }, disabled: Boolean },
    emits: ['update:modelValue', 'end'],
    setup(_, { slots }) {
      return () => h('div', { 'data-stub': 'draggable' }, slots.default?.())
    }
  })
}))

import KeyFallbackChainEditor from '../KeyFallbackChainEditor.vue'

// cny 是服务端按余额价口径给的人民币价（这里取美元价 / 7.2），前端不再自己换算
const price = (input: number, output: number) => ({
  priced: true,
  input_usd_per_mtok: input,
  output_usd_per_mtok: output,
  cny: { input_per_mtok: +(input / 7.2).toFixed(4), output_per_mtok: +(output / 7.2).toFixed(4) }
})

function item(
  id: number,
  name: string,
  position: number,
  extra: Partial<KeyFallbackChainItem> = {}
): KeyFallbackChainItem {
  return {
    group_id: id,
    name,
    role: position === 0 ? 'primary' : 'fallback',
    position,
    status: 'active',
    usable: true,
    rate_multiplier: 1,
    user_rate_multiplier: null,
    effective_multiplier: 1,
    reference_price: price(1.25, 10),
    ...extra
  }
}

function makeChain(overrides: Partial<KeyFallbackChain> = {}): KeyFallbackChain {
  return {
    key_id: 7,
    platform: 'openai',
    reference_model: 'gpt-5.5',
    max_fallbacks: 5,
    items: [
      item(16, 'Codex Plus', 0),
      item(21, 'Codex 稳定', 1, { rate_multiplier: 1.8, user_rate_multiplier: 1.5, reference_price: price(1.875, 15) }),
      item(22, 'Codex 备用', 2, { status: 'disabled', usable: false }),
      item(23, 'Codex 专属', 3, { status: 'unavailable', usable: false, reference_price: { priced: false } })
    ],
    available: [
      { group_id: 30, name: 'Codex Pro', status: 'active', rate_multiplier: 3, effective_multiplier: 3, reference_price: price(3.75, 30) },
      { group_id: 31, name: 'Codex Max', status: 'active', rate_multiplier: 5, effective_multiplier: 5, reference_price: { priced: false } }
    ],
    ...overrides
  }
}

/** 按 groupIds 重建服务端的返回：主分组 + 这些兜底分组 */
function replyFor(base: KeyFallbackChain, ids: number[]): KeyFallbackChain {
  const all = [...base.items, ...base.available.map((a) => item(a.group_id, a.name, 9, a))]
  return {
    ...base,
    items: [
      base.items[0],
      ...ids.map((id, i) => ({ ...all.find((x) => x.group_id === id)!, role: 'fallback' as const, position: i + 1 }))
    ],
    available: base.available.filter((a) => !ids.includes(a.group_id))
  }
}

function setRecharge(multiplier: number) {
  const store = useAppStore()
  store.cachedPublicSettings = { balance_recharge_multiplier: multiplier } as never
}

async function mountEditor(chain = makeChain()) {
  getChain.mockResolvedValue(chain)
  const wrapper = mount(KeyFallbackChainEditor, {
    props: { keyId: 7 },
    global: { stubs: { GroupBadge: { props: ['name', 'rateMultiplier', 'userRateMultiplier'], template: '<span data-test="badge">{{ name }} {{ userRateMultiplier ?? rateMultiplier }}x</span>' } } }
  })
  await flushPromises()
  return wrapper
}

const draggable = (w: ReturnType<typeof mount>) => w.findComponent({ name: 'VueDraggable' })
const ids = (w: ReturnType<typeof mount>) =>
  w.findAll('[data-test^="fallback-item-"]').map((n) => Number(n.attributes('data-test')!.replace('fallback-item-', '')))

describe('KeyFallbackChainEditor', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getChain.mockReset()
    replaceChain.mockReset()
    setRecharge(7.2)
  })

  it('第一项是主要分组：标「主要」，没有拖动手柄和删除按钮', async () => {
    const w = await mountEditor()
    const primary = w.get('[data-test="primary-item"]')
    expect(primary.text()).toContain('Codex Plus')
    expect(primary.text()).toContain('主要')
    expect(primary.find('[data-test="drag-handle"]').exists()).toBe(false)
    expect(primary.find('[data-test="remove"]').exists()).toBe(false)
    // 兜底项才有
    expect(w.findAll('[data-test="drag-handle"]')).toHaveLength(3)
    expect(w.findAll('[data-test="remove"]')).toHaveLength(3)
  })

  it('每项显示分组徽标、人民币参考价和状态', async () => {
    const w = await mountEditor()
    expect(w.get('[data-test="reference-model"]').text()).toContain('gpt-5.5')
    // 服务端给的 cny：1.875 / 7.2 = 0.2604，15 / 7.2 = 2.0833，按单价规则原样保留，不再收成两位
    const row = w.get('[data-test="fallback-item-21"]')
    expect(row.get('[data-test="badge"]').text()).toContain('Codex 稳定')
    expect(row.get('[data-test="price"]').text()).toBe('输入 ¥0.2604 / 输出 ¥2.0833')
    expect(row.get('[data-test="status"]').text()).toContain('可用')
  })

  it('不可用的项仍然展示，带状态和原因，并且可以删除', async () => {
    const w = await mountEditor()
    const disabled = w.get('[data-test="fallback-item-22"]')
    expect(disabled.get('[data-test="status"]').text()).toContain('已停用')
    expect(disabled.get('[data-test="reason"]').text()).toContain('该分组已停用')
    const gone = w.get('[data-test="fallback-item-23"]')
    expect(gone.get('[data-test="status"]').text()).toContain('不可用')
    expect(gone.get('[data-test="reason"]').text()).toContain('已无权使用')
    expect(gone.get('[data-test="price"]').text()).toBe('未定价')

    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    await gone.get('[data-test="remove"]').trigger('click')
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledWith(7, [21, 22])
  })

  it('主分组被停用时给单独一句指引，不说「建议移除」（主分组不能移除）', async () => {
    const chain = makeChain()
    chain.items[0] = item(16, 'Codex Plus', 0, { status: 'disabled', usable: false })
    const w = await mountEditor(chain)
    const primary = w.get('[data-test="primary-item"]')
    expect(primary.get('[data-test="reason"]').text()).toBe('主分组已停用，请在编辑密钥时更换')
    expect(primary.text()).not.toContain('建议移除')
    expect(primary.find('[data-test="remove"]').exists()).toBe(false)
    // 兜底项的原因文案不受影响
    expect(w.get('[data-test="fallback-item-22"] [data-test="reason"]').text()).toContain('建议移除')
  })

  it('充值倍率为 1（free 站）时跟着全站口径显示美元', async () => {
    setRecharge(1)
    const w = await mountEditor()
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('输入 $1.875 / 输出 $15.00')
    expect(w.text()).not.toContain('¥')
  })

  it('人民币价只读服务端给的 cny，不自己乘倍率或汇率', async () => {
    const chain = makeChain()
    chain.items[1].reference_price = { priced: true, input_usd_per_mtok: 9, output_usd_per_mtok: 9, cny: { input_per_mtok: 0.5, output_per_mtok: 4 } }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('输入 ¥0.50 / 输出 ¥4.00')
  })

  it('没有 cny 时回落到同一价格的美元口径，不拿美元数字套 ¥', async () => {
    const chain = makeChain()
    chain.items[1].reference_price = { priced: true, input_usd_per_mtok: 1.875, output_usd_per_mtok: 15 }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('输入 $1.875 / 输出 $15.00')
  })

  it('美元价按单价规则显示：不固定 3 位小数，也不把 ≥ 1 的价格收成两位', async () => {
    setRecharge(1)
    const chain = makeChain()
    // 固定 3 位会把 0.0375 写成 $0.038、12.3456 写成 $12.346；统一金额规则又会把 12.3456 收成 $12.35。
    chain.items[1].reference_price = { priced: true, input_usd_per_mtok: 0.0375, output_usd_per_mtok: 12.3456 }
    chain.items[2].reference_price = { priced: true, input_usd_per_mtok: 0.5, output_usd_per_mtok: 1234.5 }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('输入 $0.0375 / 输出 $12.3456')
    expect(w.get('[data-test="fallback-item-22"] [data-test="price"]').text()).toBe('输入 $0.50 / 输出 $1,234.50')
  })

  it('总开关关着（enabled=false）时编辑器照常工作，不出现任何开关提示', async () => {
    const w = await mountEditor(makeChain({ enabled: false }))
    expect(w.find('[data-test="add-button"]').attributes('disabled')).toBeUndefined()
    expect(w.text()).not.toMatch(/未开启|关闭|功能/)
  })

  it('拖拽排序后整条替换，顺序按新位置提交', async () => {
    const w = await mountEditor()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    const d = draggable(w)
    const list = (d.props('modelValue') as KeyFallbackChainItem[]).map((i) => i)
    d.vm.$emit('update:modelValue', [list[2], list[0], list[1]])
    await flushPromises()
    d.vm.$emit('end')
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledTimes(1)
    expect(replaceChain).toHaveBeenCalledWith(7, [23, 21, 22])
    expect(ids(w)).toEqual([23, 21, 22])
  })

  it('拖了一圈又回到原位置时不发请求', async () => {
    const w = await mountEditor()
    const d = draggable(w)
    d.vm.$emit('end')
    await flushPromises()
    expect(replaceChain).not.toHaveBeenCalled()
  })

  it('在手柄上按方向键也能调整顺序', async () => {
    const w = await mountEditor()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    await w.get('[data-test="fallback-item-21"] [data-test="drag-handle"]').trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledWith(7, [22, 21, 23])
  })

  it('添加：从选择列表挑一个分组，追加到链末尾', async () => {
    const w = await mountEditor()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    expect(w.find('[data-test="picker"]').exists()).toBe(false)
    await w.get('[data-test="add-button"]').trigger('click')
    const picker = w.get('[data-test="picker"]')
    expect(picker.text()).toContain('Codex Pro')
    expect(picker.text()).toContain('Codex Max')
    // 参考价 3.75/7.2 = 0.5208
    expect(picker.get('[data-test="pick-30"]').text()).toContain('输入 ¥0.5208')
    await picker.get('[data-test="pick-30"]').trigger('click')
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledWith(7, [21, 22, 23, 30])
    expect(w.find('[data-test="picker"]').exists()).toBe(false)
    expect(ids(w)).toEqual([21, 22, 23, 30])
  })

  it('删除一项后按剩余顺序整条替换', async () => {
    const w = await mountEditor()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledWith(7, [22, 23])
    expect(ids(w)).toEqual([22, 23])
  })

  it('达到上限时添加按钮禁用，并说明原因', async () => {
    const base = makeChain({ max_fallbacks: 3 })
    const w = await mountEditor(base)
    const btn = w.get('[data-test="add-button"]')
    expect(btn.attributes('disabled')).toBeDefined()
    expect(w.get('[data-test="add-hint"]').text()).toBe('最多添加 3 个兜底分组，请先移除一个')
    expect(btn.attributes('aria-describedby')).toBe(w.get('[data-test="add-hint"]').attributes('id'))
    await btn.trigger('click')
    expect(w.find('[data-test="picker"]').exists()).toBe(false)
  })

  it('没有更多可添加的分组时按钮禁用并说明', async () => {
    const w = await mountEditor(makeChain({ available: [] }))
    expect(w.get('[data-test="add-button"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-test="add-hint"]').text()).toBe('没有更多可添加的分组')
  })

  it('保存失败：恢复原顺序，并按错误码给出用户能看懂的提示', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 400, code: 400, reason: 'FALLBACK_GROUP_NOT_ALLOWED', message: 'internal detail' })
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="action-error"]').text()).toBe('你没有该分组的使用权限')
    expect(w.text()).not.toContain('internal detail')
    expect(ids(w)).toEqual([21, 22, 23])
  })

  it('链里有不可用项时，拖动其他项照常整条提交，不会悄悄丢掉不可用项', async () => {
    const w = await mountEditor()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    const d = draggable(w)
    const list = d.props('modelValue') as KeyFallbackChainItem[]
    d.vm.$emit('update:modelValue', [list[1], list[0], list[2]])
    await flushPromises()
    d.vm.$emit('end')
    await flushPromises()
    // 22（已停用）、23（不可用）都还在提交里
    expect(replaceChain).toHaveBeenCalledWith(7, [22, 21, 23])
    expect(w.find('[data-test="action-error"]').exists()).toBe(false)
    expect(w.find('[data-test="item-error"]').exists()).toBe(false)
    expect(ids(w)).toEqual([22, 21, 23])
  })

  it('不可用项有明确的「移除」入口，点了才移除；可用项没有', async () => {
    const w = await mountEditor()
    expect(w.find('[data-test="fallback-item-21"] [data-test="remove-unusable"]').exists()).toBe(false)
    expect(w.get('[data-test="fallback-item-22"] [data-test="reason"]').text()).toContain('建议移除')
    expect(replaceChain).not.toHaveBeenCalled()
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    await w.get('[data-test="fallback-item-22"] [data-test="remove-unusable"]').trigger('click')
    await flushPromises()
    expect(replaceChain).toHaveBeenCalledWith(7, [21, 23])
  })

  it('后端在错误里带 group_id 时，原因标在对应那一项上，不出现整体提示', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({
      status: 404,
      reason: 'FALLBACK_GROUP_UNAVAILABLE',
      message: 'internal detail',
      metadata: { group_id: '22' }
    })
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="fallback-item-22"] [data-test="item-error"]').text()).toBe('该分组已停用')
    expect(w.find('[data-test="fallback-item-21"] [data-test="item-error"]').exists()).toBe(false)
    expect(w.find('[data-test="fallback-item-23"] [data-test="item-error"]').exists()).toBe(false)
    expect(w.find('[data-test="action-error"]').exists()).toBe(false)
    // 不替用户删：链仍是服务器上的 21、22、23
    expect(ids(w)).toEqual([21, 22, 23])
    // 下一次保存成功后错误消失
    replaceChain.mockImplementation((_id: number, g: number[]) => Promise.resolve(replyFor(makeChain(), g)))
    await w.get('[data-test="fallback-item-22"] [data-test="remove-unusable"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="item-error"]').exists()).toBe(false)
  })

  it('错误没有 group_id、或 group_id 不在链里（刚加的被拒）时，退回整体提示', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 404, reason: 'FALLBACK_GROUP_UNAVAILABLE', metadata: { group_id: 30 } })
    await w.get('[data-test="add-button"]').trigger('click')
    await w.get('[data-test="pick-30"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="action-error"]').text()).toBe('该分组已停用')
    expect(w.find('[data-test="item-error"]').exists()).toBe(false)

    replaceChain.mockRejectedValue({ status: 403, reason: 'FALLBACK_GROUP_NOT_ALLOWED' })
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="action-error"]').text()).toBe('你没有该分组的使用权限')
    expect(w.find('[data-test="item-error"]').exists()).toBe(false)
  })

  it('保存失败后向服务器重新拉取真实的链：请求其实已落库时界面跟着服务器走', async () => {
    const w = await mountEditor()
    expect(getChain).toHaveBeenCalledTimes(1)
    replaceChain.mockRejectedValue({ status: 0, message: 'Network error' })
    // 服务器上其实已经删掉了 21
    getChain.mockResolvedValueOnce(replyFor(makeChain(), [22, 23]))
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(getChain).toHaveBeenCalledTimes(2)
    expect(ids(w)).toEqual([22, 23])
    expect(w.get('[data-test="action-error"]').text()).toBe('保存失败，请稍后重试')
    // 静默刷新：没有骨架屏
    expect(w.find('[data-test="editor-loading"]').exists()).toBe(false)
  })

  it('拖动保存失败：回到服务器上的顺序', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 500, message: 'boom' })
    const d = draggable(w)
    const list = d.props('modelValue') as KeyFallbackChainItem[]
    d.vm.$emit('update:modelValue', [list[2], list[0], list[1]])
    await flushPromises()
    d.vm.$emit('end')
    await flushPromises()
    expect(getChain).toHaveBeenCalledTimes(2)
    expect(ids(w)).toEqual([21, 22, 23])
  })

  it('409 主分组被改：重新拉 Key 和链，主分组以服务器为准，并提示已刷新', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 409, reason: 'FALLBACK_KEY_CHANGED', message: 'key changed' })
    const fresh = makeChain({
      items: [item(40, 'Codex Team', 0), item(21, 'Codex 稳定', 1)],
      available: [{ group_id: 30, name: 'Codex Pro', status: 'active', rate_multiplier: 3, effective_multiplier: 3, reference_price: price(3.75, 30) }]
    })
    getChain.mockResolvedValueOnce(fresh)
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(getChain).toHaveBeenCalledTimes(2)
    expect(w.get('[data-test="primary-item"]').text()).toContain('Codex Team')
    expect(w.get('[data-test="primary-item"]').text()).not.toContain('Codex Plus')
    expect(ids(w)).toEqual([21])
    expect(w.get('[data-test="action-error"]').text()).toBe('密钥刚刚被修改，已为你刷新，请重试')
    // 之后的保存基于新的主分组继续可用
    replaceChain.mockResolvedValue(replyFor(fresh, []))
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(replaceChain).toHaveBeenLastCalledWith(7, [])
  })

  it('刷新本身失败时保持本地快照，仍给出错误提示', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 409, reason: 'FALLBACK_KEY_CHANGED' })
    getChain.mockRejectedValueOnce({ status: 500, message: 'boom' })
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(ids(w)).toEqual([21, 22, 23])
    expect(w.get('[data-test="action-error"]').text()).toBe('密钥刚刚被修改，请重新打开后再试')
  })

  it('保存进行中再次触发，不会发出第二个请求', async () => {
    const w = await mountEditor()
    let resolve!: (v: KeyFallbackChain) => void
    replaceChain.mockReturnValue(new Promise<KeyFallbackChain>((r) => (resolve = r)))
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await w.get('[data-test="fallback-item-22"] [data-test="remove"]').trigger('click')
    expect(replaceChain).toHaveBeenCalledTimes(1)
    resolve(replyFor(makeChain(), [22, 23]))
    await flushPromises()
  })

  it('priced=true 但缺美元价时按「未定价」显示，不出现 $0.00', async () => {
    const chain = makeChain()
    chain.items[1].reference_price = { priced: true }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('未定价')
  })

  it('人民币口径只看 cny：美元字段缺失也照常显示人民币价', async () => {
    const chain = makeChain()
    chain.items[1].reference_price = { priced: true, cny: { input_per_mtok: 0.5, output_per_mtok: 4 } }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('输入 ¥0.50 / 输出 ¥4.00')
  })

  it('美元口径只看美元字段：只有 cny 时按「未定价」，不拿人民币数字套 $', async () => {
    setRecharge(1)
    const chain = makeChain()
    chain.items[1].reference_price = { priced: true, cny: { input_per_mtok: 0.5, output_per_mtok: 4 } }
    const w = await mountEditor(chain)
    expect(w.get('[data-test="fallback-item-21"] [data-test="price"]').text()).toBe('未定价')
  })

  it('未知错误统一提示保存失败，不透出后端 message', async () => {
    const w = await mountEditor()
    replaceChain.mockRejectedValue({ status: 500, message: 'pq: deadlock detected' })
    await w.get('[data-test="fallback-item-21"] [data-test="remove"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="action-error"]').text()).toBe('保存失败，请稍后重试')
    expect(w.text()).not.toContain('deadlock')
  })

  it('加载失败时显示提示，可以重试', async () => {
    getChain.mockRejectedValueOnce({ status: 500, message: 'boom' })
    const w = mount(KeyFallbackChainEditor, { props: { keyId: 7 },  })
    await flushPromises()
    expect(w.get('[data-test="editor-load-error"]').text()).toContain('加载失败，请重试')
    getChain.mockResolvedValue(makeChain())
    await w.get('[data-test="editor-load-error"] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="primary-item"]').exists()).toBe(true)
  })
})

// 徽标上的倍率与下面的参考价同一口径：人民币模式显示等效倍率（r 除以充值倍率）并补「套餐低至」，
// 美元模式和 free 站（充值倍率 1）原样。这里用真实的 GroupBadge 渲染，断言用户看到的字。
describe('KeyFallbackChainEditor 分组倍率', () => {
  const PROD_PRICING = { d_min: 30, d_max: 510, u_min: 0.04, u_max: 0.05, t_min: 30, t_max: 360, t_step: 30, d_floor: 210 }

  async function mountReal(chain = makeChain()) {
    getChain.mockResolvedValue(chain)
    const wrapper = mount(KeyFallbackChainEditor, { props: { keyId: 7 } })
    await flushPromises()
    return wrapper
  }

  /** 卡片里的倍率药丸（徽标右侧那一小块）和「套餐低至」。 */
  const rateOf = (root: { find: (s: string) => { exists(): boolean; text(): string } }) => root.find('span.rounded.text-\\[10px\\]').text()
  const planOf = (root: { findAll: (s: string) => Array<{ text(): string }> }) => root.findAll('[data-test="plan-rate"]').map((n) => n.text())

  beforeEach(() => {
    setActivePinia(createPinia())
    window.localStorage.clear()
    resetFiatDataMissingForTest()
    resetPlanPricingForTest()
    useCurrencyDisplay().setMode('fiat')
    getChain.mockReset()
    replaceChain.mockReset()
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    const auth = useAuthStore()
    auth.token = 'test-token'
    auth.user = { id: 1, role: 'user' } as never
    setRecharge(13)
  })

  it('人民币模式：主分组 0.0769x + 套餐低至 0.04x，价格行仍是 ¥ 参考价', async () => {
    const w = await mountReal()
    const primary = w.get('[data-test="primary-item"]')

    expect(rateOf(primary)).toBe('0.0769x')
    expect(planOf(primary)).toEqual(['套餐低至 0.04x'])
    expect(primary.get('[data-test="price"]').text()).toMatch(/^输入 ¥/)
  })

  it('徽标变长换行时，状态和拖动、删除按钮留在右侧', async () => {
    const w = await mountReal()

    expect(w.get('[data-test="primary-item"] [data-test="status"]').classes()).toContain('ml-auto')
    expect(w.get('[data-test="fallback-item-21"] [data-test="status"]').element.parentElement?.classList.contains('ml-auto')).toBe(true)
  })

  it('兜底项带专属倍率：默认值划线，专属值高亮，套餐低至也一样', async () => {
    const w = await mountReal()
    const row = w.get('[data-test="fallback-item-21"]')

    // 默认 1.8 → 0.138，专属 1.5 → 0.115；套餐低至 0.072 / 0.06
    expect(row.findAll('.line-through').map((n) => n.text())).toEqual(['0.138x', '0.072x'])
    expect(row.findAll('.font-bold').map((n) => n.text())).toEqual(['0.115x', '0.06x'])
  })

  it('添加分组的选择列表：每个候选也用等效倍率', async () => {
    const w = await mountReal()
    await w.get('[data-test="add-button"]').trigger('click')
    const picker = w.get('[data-test="picker"]')

    expect(rateOf(picker.get('[data-test="pick-30"]'))).toBe('0.231x')
    expect(planOf(picker.get('[data-test="pick-30"]'))).toEqual(['套餐低至 0.12x'])
    expect(rateOf(picker.get('[data-test="pick-31"]'))).toBe('0.385x')
    expect(planOf(picker.get('[data-test="pick-31"]'))).toEqual(['套餐低至 0.2x'])
    expect(picker.get('[data-test="pick-30"] [data-test="price"]').text()).toContain('输入 ¥0.5208')
  })

  it('套餐定价取不到：只剩主倍率', async () => {
    getSubscriptionPricing.mockRejectedValue(new Error('boom'))
    const w = await mountReal()

    expect(rateOf(w.get('[data-test="primary-item"]'))).toBe('0.0769x')
    expect(w.findAll('[data-test="plan-rate"]')).toHaveLength(0)
  })

  it('美元模式（充值倍率 13，选了 $）：原始倍率，没有套餐低至', async () => {
    useCurrencyDisplay().setMode('usd')
    const w = await mountReal()

    expect(rateOf(w.get('[data-test="primary-item"]'))).toBe('1x')
    const row = w.get('[data-test="fallback-item-21"]')
    expect(row.findAll('.line-through').map((n) => n.text())).toEqual(['1.8x'])
    expect(row.findAll('.font-bold').map((n) => n.text())).toEqual(['1.5x'])
    expect(w.findAll('[data-test="plan-rate"]')).toHaveLength(0)
    expect(w.get('[data-test="primary-item"] [data-test="price"]').text()).toMatch(/^输入 \$/)
  })

  it('free 站（充值倍率 1）：与改动前逐字相同，不请求套餐定价', async () => {
    setRecharge(1)
    window.localStorage.setItem('currency-display-mode', 'fiat')
    const w = await mountReal()

    expect(rateOf(w.get('[data-test="primary-item"]'))).toBe('1x')
    const row = w.get('[data-test="fallback-item-21"]')
    expect(row.findAll('.line-through').map((n) => n.text())).toEqual(['1.8x'])
    expect(row.findAll('.font-bold').map((n) => n.text())).toEqual(['1.5x'])
    expect(w.findAll('[data-test="plan-rate"]')).toHaveLength(0)
    // 徽标本身没有悬停提示（只有带套餐低至的人民币模式才有）
    expect(w.findAll('span.rounded-md.px-2').filter((n) => n.attributes('title') !== undefined)).toHaveLength(0)
    expect(getSubscriptionPricing).not.toHaveBeenCalled()
  })

  it('未登录：不请求套餐定价，只有主倍率', async () => {
    const auth = useAuthStore()
    auth.token = null
    auth.user = null
    const w = await mountReal()

    expect(getSubscriptionPricing).not.toHaveBeenCalled()
    expect(rateOf(w.get('[data-test="primary-item"]'))).toBe('0.0769x')
    expect(w.findAll('[data-test="plan-rate"]')).toHaveLength(0)
  })
})
