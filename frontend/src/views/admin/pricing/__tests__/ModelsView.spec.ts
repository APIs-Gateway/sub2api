import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { installApiMocks } from './fixtures'
import { mountPricingView } from './mountHelpers'

const { api } = vi.hoisted(() => ({
  api: {
    groups: { getAll: vi.fn() },
    pricing: { listModelCatalog: vi.fn(), getGroupDerive: vi.fn(), quoteBatch: vi.fn() }
  }
}))

vi.mock('@/api/admin', () => ({ adminAPI: api }))

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

import { resetPricingDataForTest } from '../usePricingData'
import ModelsView from '../ModelsView.vue'

const rowKeys = (wrapper: Awaited<ReturnType<typeof mountPricingView>>) =>
  wrapper.findAll('[data-test^="model-row-"]').map((r) => r.attributes('data-test')!.replace('model-row-', ''))

describe('ModelsView', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
  })

  it('渲染目录里的模型，未登记的排在最后，新建按钮可用', async () => {
    const wrapper = await mountPricingView(ModelsView)
    expect(rowKeys(wrapper)).toEqual(['gpt-5.4', 'gpt-5.5', 'gpt-6.2-sol', 'minimax-m3', 'qwen3-max', 'claude-opus-5-5'])
    expect(wrapper.find('[data-test="create-model"]').attributes('disabled')).toBeUndefined()
    // 表头四个必要列
    const head = wrapper.find('thead').text()
    for (const col of ['模型', '平台', '状态', '官方参考价', '价格来源', '已开放分组']) expect(head).toContain(col)
  })

  it('官方参考价以 ¥ 为主、$ 为小字，价格来源和状态各自显示', async () => {
    const wrapper = await mountPricingView(ModelsView)
    const official = wrapper.find('[data-test="official-gpt-5.5"]')
    expect(official.text()).toBe('¥35.00 / ¥210.00')
    expect(official.element.nextElementSibling?.textContent).toBe('$5.00 / $30.00')

    const row = wrapper.find('[data-test="model-row-gpt-5.5"]').text()
    expect(row).toContain('价目表')
    expect(row).toContain('上线')
    expect(wrapper.find('[data-test="model-row-gpt-6.2-sol"]').text()).toContain('内置价')
    expect(wrapper.find('[data-test="model-row-gpt-6.2-sol"]').text()).toContain('草稿')
    expect(wrapper.find('[data-test="model-row-gpt-5.4"]').text()).toContain('官方定价')
    expect(wrapper.find('[data-test="model-row-gpt-5.4"]').text()).toContain('下线')
    expect(wrapper.find('[data-test="model-row-qwen3-max"]').text()).toContain('未登记')
  })

  it('已开放分组数来自报价', async () => {
    const wrapper = await mountPricingView(ModelsView)
    expect(wrapper.find('[data-test="open-count-gpt-5.5"]').text().replace(/\s/g, '')).toBe('2/2')
    expect(wrapper.find('[data-test="open-count-gpt-6.2-sol"]').text().replace(/\s/g, '')).toBe('0/2')
    expect(wrapper.find('[data-test="open-count-qwen3-max"]').text().replace(/\s/g, '')).toBe('1/2')
    expect(wrapper.find('[data-test="open-count-claude-opus-5-5"]').text().replace(/\s/g, '')).toBe('1/1')
  })

  it('没有官方价的模型标「无价」，筛选「无价」只剩它', async () => {
    const wrapper = await mountPricingView(ModelsView)
    expect(wrapper.find('[data-test="unpriced-minimax-m3"]').text()).toBe('无价')
    expect(wrapper.find('[data-test="model-row-minimax-m3"]').text()).toContain('—')

    await wrapper.find('[data-test="filter-unpriced"]').trigger('click')
    expect(rowKeys(wrapper)).toEqual(['minimax-m3'])
    expect(wrapper.find('[data-test="filter-unpriced"]').attributes('aria-pressed')).toBe('true')
    await wrapper.find('[data-test="filter-unpriced"]').trigger('click')
    expect(rowKeys(wrapper)).toHaveLength(6)
  })

  it('筛选：未登记、搜索、平台、状态', async () => {
    const wrapper = await mountPricingView(ModelsView)

    await wrapper.find('[data-test="filter-unregistered"]').trigger('click')
    expect(rowKeys(wrapper)).toEqual(['qwen3-max'])
    await wrapper.find('[data-test="filter-unregistered"]').trigger('click')

    await wrapper.find('[data-test="model-search"]').setValue('SOL')
    expect(rowKeys(wrapper)).toEqual(['gpt-6.2-sol'])
    // 显示名也能搜
    await wrapper.find('[data-test="model-search"]').setValue('GPT 6.2')
    expect(rowKeys(wrapper)).toEqual(['gpt-6.2-sol'])
    await wrapper.find('[data-test="model-search"]').setValue('')

    // 平台、状态是两个下拉：直接改它们的值
    const selects = wrapper.findAllComponents({ name: 'Select' })
    expect(selects).toHaveLength(2)
    await selects[0].vm.$emit('update:modelValue', 'anthropic')
    expect(rowKeys(wrapper)).toEqual(['claude-opus-5-5'])
    await selects[0].vm.$emit('update:modelValue', '')
    await selects[1].vm.$emit('update:modelValue', 'draft')
    expect(rowKeys(wrapper)).toEqual(['gpt-6.2-sol'])
    await selects[1].vm.$emit('update:modelValue', 'retired')
    expect(rowKeys(wrapper)).toEqual(['gpt-5.4'])

    await wrapper.find('[data-test="model-search"]').setValue('不存在的模型')
    expect(wrapper.find('[data-test="models-empty"]').exists()).toBe(true)
  })

  it('点一行打开抽屉，列出各分组的开放情况与价格；关闭后收起', async () => {
    const wrapper = await mountPricingView(ModelsView)
    expect(wrapper.find('[data-test="model-drawer"]').exists()).toBe(false)

    await wrapper.find('[data-test="model-row-gpt-5.5"]').trigger('click')
    const drawer = wrapper.find('[data-test="model-drawer"]')
    expect(drawer.exists()).toBe(true)
    expect(wrapper.find('[data-test="drawer-official"]').text()).toContain('输入 ¥35.00 / 输出 ¥210.00')
    expect(wrapper.find('[data-test="drawer-official"]').text()).toContain('$5.00 / $30.00')
    const mini = wrapper.find('[data-test="mini-matrix"]')
    expect(mini.findAll('li')).toHaveLength(2)
    expect(mini.find('[data-test="mini-row-1"]').text()).toContain('开放')
    expect(mini.find('[data-test="mini-row-1"]').text()).toContain('尚未切换')
    expect(mini.find('[data-test="mini-row-2"]').text()).toContain('白名单')
    expect(mini.find('[data-test="mini-row-2"]').text()).toContain('×1.2')
    const action = wrapper.find('[data-test="drawer-action"]')
    expect(action.text()).toBe('下线…')
    expect(action.attributes('disabled')).toBeUndefined()

    await wrapper.find('button[aria-label="关闭"]').trigger('click')
    expect(wrapper.find('[data-test="model-drawer"]').exists()).toBe(false)
  })

  it('抽屉按模型状态给出对应的操作，并解释关闭的原因', async () => {
    const wrapper = await mountPricingView(ModelsView)

    await wrapper.find('[data-test="model-row-gpt-6.2-sol"]').trigger('click')
    expect(wrapper.find('[data-test="drawer-action"]').text()).toBe('上线…')
    expect(wrapper.find('[data-test="drawer-status"]').text()).toBe('草稿')
    expect(wrapper.find('aside').text()).toContain('GPT 6.2 Sol')
    expect(wrapper.find('[data-test="model-drawer"]').text()).toContain('参考模型')
    expect(wrapper.find('[data-test="mini-row-1"] [title]').attributes('title')).toContain('模型还是草稿')

    await wrapper.find('[data-test="model-row-gpt-5.4"]').trigger('keydown.enter')
    expect(wrapper.find('[data-test="drawer-action"]').text()).toBe('重新上线')

    await wrapper.find('[data-test="model-row-qwen3-max"]').trigger('click')
    expect(wrapper.find('[data-test="drawer-action"]').text()).toBe('加入目录并上线…')
    expect(wrapper.find('[data-test="model-drawer"]').text()).toContain('还没加入模型目录')

    await wrapper.find('[data-test="model-row-minimax-m3"]').trigger('click')
    expect(wrapper.find('[data-test="drawer-official"]').text()).toBe('没有官方价')
  })

  it('站点没有充值倍率时只显示美元', async () => {
    const wrapper = await mountPricingView(ModelsView, { recharge: 1 })
    expect(wrapper.find('[data-test="official-gpt-5.5"]').text()).toBe('$5.00 / $30.00')
    expect(wrapper.find('[data-test="official-gpt-5.5"]').element.nextElementSibling).toBeNull()
  })

  it('读不到开放方式的分组会提示', async () => {
    api.pricing.getGroupDerive.mockRejectedValue(new Error('x'))
    const wrapper = await mountPricingView(ModelsView)
    expect(wrapper.find('[data-test="derive-failed"]').text()).toContain('3 个分组')
  })

  it('加载失败时显示错误和重试，重试后恢复', async () => {
    api.groups.getAll.mockRejectedValueOnce({ message: '加载炸了' })
    const wrapper = await mountPricingView(ModelsView)
    expect(wrapper.find('[data-test="load-error"]').text()).toContain('加载炸了')
    expect(wrapper.find('[data-test="models-table"]').exists()).toBe(false)

    await wrapper.find('[data-test="load-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="models-table"]').exists()).toBe(true)
  })

  it('刷新按钮重新取数', async () => {
    const wrapper = await mountPricingView(ModelsView)
    expect(api.groups.getAll).toHaveBeenCalledTimes(1)
    await wrapper.find('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(api.groups.getAll).toHaveBeenCalledTimes(2)
  })
})
