import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { derives, installApiMocks } from './fixtures'
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
import MatrixView from '../MatrixView.vue'

type Wrapper = Awaited<ReturnType<typeof mountPricingView>>
const rowKeys = (w: Wrapper) => w.findAll('[data-test^="row-"]').map((r) => r.attributes('data-test')!.replace('row-', ''))
const kindOf = (w: Wrapper, group: number, model: string) => w.find(`[data-test="cell-${group}|${model}"] [data-kind]`).attributes('data-kind')

describe('MatrixView', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
  })

  it('按平台分页签，默认落在 OpenAI；行是模型，列是该平台的分组', async () => {
    const wrapper = await mountPricingView(MatrixView)
    expect(wrapper.findAll('[role="tab"]').map((t) => t.attributes('data-test'))).toEqual(['tab-openai', 'tab-anthropic'])
    expect(wrapper.find('[data-test="tab-openai"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-test="tab-openai"]').text()).toContain('2 个分组')

    // 分组列：按名称排序，显示倍率与开放方式
    const cols = wrapper.findAll('[data-test^="col-"]')
    expect(cols.map((c) => c.attributes('data-test'))).toEqual(['col-2', 'col-1'])
    expect(cols[0].text()).toContain('倍率 ×5')
    expect(cols[0].text()).toContain('白名单')
    expect(cols[1].text()).toContain('倍率 ×1.4')
    expect(cols[1].text()).toContain('开放')
    expect(rowKeys(wrapper)).toEqual(['gpt-5.4', 'gpt-5.5', 'gpt-6.2-sol', 'minimax-m3', 'qwen3-max'])

    await wrapper.find('[data-test="tab-anthropic"]').trigger('click')
    expect(rowKeys(wrapper)).toEqual(['claude-opus-5-5'])
    expect(wrapper.findAll('[data-test^="col-"]').map((c) => c.attributes('data-test'))).toEqual(['col-3'])
  })

  it('明确写出现在展示的是由渠道配置推出的结果，分组标「尚未切换」', async () => {
    const wrapper = await mountPricingView(MatrixView)
    const notice = wrapper.find('[data-test="derived-notice"]')
    expect(notice.text()).toContain('现在展示的是由渠道配置推出的结果')
    expect(notice.text()).toContain('还有 2 / 3 个分组没有切换')
    expect(notice.find('a').attributes('href')).toBe('/admin/channels/pricing')
    expect(wrapper.find('[data-test="stage-1"]').text()).toContain('尚未切换')
    expect(wrapper.find('[data-test="stage-2"]').exists()).toBe(true)

    // 已切换的分组不再标
    await wrapper.find('[data-test="tab-anthropic"]').trigger('click')
    expect(wrapper.find('[data-test="stage-3"]').exists()).toBe(false)
  })

  it('所有分组都没切换时，提示写「所有分组」', async () => {
    api.pricing.getGroupDerive.mockImplementation(async (id: number) => ({ ...derives[id], stored_config: null }))
    const wrapper = await mountPricingView(MatrixView)
    expect(wrapper.find('[data-test="derived-notice"]').text()).toContain('所有分组还没有切换')
  })

  it('部分分组已切换时，提示里说清楚还有几个没切', async () => {
    api.pricing.getGroupDerive.mockImplementation(async (id: number) => ({
      ...derives[id],
      stored_config: id === 1 ? { pricing_stage: 'v2' } : derives[id].stored_config
    }))
    const wrapper = await mountPricingView(MatrixView)
    expect(wrapper.find('[data-test="derived-notice"]').text()).toContain('还有 1 / 3 个分组没有切换')
  })

  it('全部分组都已切换时不再显示这条提示', async () => {
    api.pricing.getGroupDerive.mockImplementation(async (id: number) => ({
      ...derives[id],
      stored_config: { pricing_stage: 'v2' }
    }))
    const wrapper = await mountPricingView(MatrixView)
    expect(wrapper.find('[data-test="derived-notice"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="stage-1"]').exists()).toBe(false)
  })

  it('格子状态映射：开放、额外倍率、自定义价、未定价、关闭', async () => {
    const wrapper = await mountPricingView(MatrixView)
    expect(kindOf(wrapper, 1, 'gpt-5.5')).toBe('open')
    expect(kindOf(wrapper, 2, 'gpt-5.5')).toBe('extra')
    expect(wrapper.find('[data-test="cell-2|gpt-5.5"]').text()).toContain('×1.2')
    expect(kindOf(wrapper, 2, 'minimax-m3')).toBe('custom')
    expect(kindOf(wrapper, 1, 'minimax-m3')).toBe('unpriced')
    expect(wrapper.find('[data-test="cell-1|minimax-m3"]').text()).toContain('调用不扣费')
    expect(wrapper.find('[data-test="cell-1|minimax-m3"]').classes()).toContain('is-unpriced')
    expect(kindOf(wrapper, 1, 'gpt-6.2-sol')).toBe('closed')
    expect(wrapper.find('[data-test="cell-1|gpt-6.2-sol"]').classes()).toContain('is-closed')
  })

  it('格子显示用户实付价，¥ 为主，$ 放在提示里', async () => {
    const wrapper = await mountPricingView(MatrixView)
    // 7 美元额度按 13 倍充值倍率折成 ¥0.5385；42 → ¥3.23
    const cell = wrapper.find('[data-test="cell-1|gpt-5.5"]')
    expect(cell.text()).toContain('开放')
    expect(cell.text()).toContain('¥0.5385 / ¥3.23')
    expect(cell.find('[data-kind]').attributes('title')).toContain('输入 $7.00 / 输出 $42.00')
    // 关闭的原因写在提示里
    expect(wrapper.find('[data-test="cell-2|gpt-6.2-sol"] [data-kind]').attributes('title')).toContain('白名单')
  })

  it('美元口径下只显示 $', async () => {
    const wrapper = await mountPricingView(MatrixView, { recharge: 1 })
    expect(wrapper.find('[data-test="cell-1|gpt-5.5"]').text()).toContain('$7.00 / $42.00')
    expect(wrapper.find('[data-test="cell-1|gpt-5.5"] [data-kind]').attributes('title')).toContain('输入 $7.00 / 输出 $42.00')
  })

  it('按次计费的格子显示每次价格', async () => {
    const wrapper = await mountPricingView(MatrixView)
    await wrapper.find('[data-test="tab-anthropic"]').trigger('click')
    expect(wrapper.find('[data-test="cell-3|claude-opus-5-5"]').text()).toContain('/ 次')
  })

  it('没报出价的格子显示无法取价', async () => {
    api.pricing.quoteBatch.mockImplementation(async (g: number[], m: string[]) => ({
      models: m.map((model) => ({ model, priced: true, source: 'litellm', per_mtok: { input: 1, output: 2 } })),
      cells: g.flatMap((gid) => m.map((model) => ({ group_id: gid, model, error: 'QUOTE_FAILED', access: { ok: false }, priced: false, source: 'none', billing_mode: '', group_multiplier: 0, effective_multiplier: 0 })))
    }))
    const wrapper = await mountPricingView(MatrixView)
    expect(kindOf(wrapper, 1, 'gpt-5.5')).toBe('error')
    expect(wrapper.find('[data-test="cell-1|gpt-5.5"]').text()).toContain('无法取价')
  })

  it('行头显示官方价，草稿 / 下线 / 未登记的模型带标记，无价的写明', async () => {
    const wrapper = await mountPricingView(MatrixView)
    const head = (k: string) => wrapper.find(`[data-test="row-${k}"] th`).text()
    expect(head('gpt-5.5')).toContain('¥35.00 / ¥210.00')
    expect(head('gpt-5.5')).toContain('$5.00 / $30.00')
    expect(head('gpt-6.2-sol')).toContain('草稿')
    expect(head('gpt-5.4')).toContain('下线')
    expect(head('qwen3-max')).toContain('未登记')
    expect(head('minimax-m3')).toContain('没有官方价')
  })

  it('搜索与「隐藏全部关闭的模型」', async () => {
    const wrapper = await mountPricingView(MatrixView)
    await wrapper.find('[data-test="matrix-search"]').setValue('minimax')
    expect(rowKeys(wrapper)).toEqual(['minimax-m3'])
    await wrapper.find('[data-test="matrix-search"]').setValue('')

    await wrapper.find('[data-test="hide-closed"]').trigger('click')
    expect(rowKeys(wrapper)).toEqual(['gpt-5.5', 'minimax-m3', 'qwen3-max'])
    expect(wrapper.find('[data-test="hide-closed"]').attributes('aria-pressed')).toBe('true')

    await wrapper.find('[data-test="matrix-search"]').setValue('不存在')
    expect(wrapper.find('[data-test="matrix-empty"]').exists()).toBe(true)
  })

  it('图例六种状态齐全；没有分组切换到新配置时，批量调整按钮置灰并说明原因', async () => {
    // 全部分组都还在用渠道配置
    api.pricing.getGroupDerive.mockImplementation(async (id: number) => ({ ...derives[id], stored_config: { pricing_stage: 'legacy' } }))
    const wrapper = await mountPricingView(MatrixView)
    const legend = wrapper.find('[data-test="legend"]').text()
    for (const word of ['按官方价开放', '额外倍率', '自定义价', '没有任何价格', '不开放', '分组还没切换']) expect(legend).toContain(word)
    const edit = wrapper.find('[data-test="edit"]')
    expect(edit.attributes('disabled')).toBeDefined()
    expect(edit.attributes('title')).toContain('还没有分组切换')
  })

  it('点行头打开模型抽屉', async () => {
    const wrapper = await mountPricingView(MatrixView)
    await wrapper.find('[data-test="row-gpt-5.5"] button').trigger('click')
    expect(wrapper.find('[data-test="model-drawer"]').exists()).toBe(true)
    expect(wrapper.find('aside h2').text()).toBe('gpt-5.5')
    await wrapper.find('button[aria-label="关闭"]').trigger('click')
    expect(wrapper.find('[data-test="model-drawer"]').exists()).toBe(false)
  })

  it('没有分组的平台给出空状态', async () => {
    api.groups.getAll.mockResolvedValue([groupOf(3)])
    const wrapper = await mountPricingView(MatrixView)
    // 只有 anthropic 分组：默认落在它；目录里 openai 的页签仍在，点进去是空状态
    expect(wrapper.find('[data-test="tab-anthropic"]').attributes('aria-selected')).toBe('true')
    await wrapper.find('[data-test="tab-openai"]').trigger('click')
    expect(wrapper.find('[data-test="no-groups"]').text()).toContain('OpenAI 还没有分组')
  })

  it('读不到开放方式时提示；加载失败时可以重试', async () => {
    api.pricing.getGroupDerive.mockRejectedValue(new Error('x'))
    let wrapper = await mountPricingView(MatrixView)
    expect(wrapper.find('[data-test="derive-failed"]').text()).toContain('3 个分组')
    wrapper.unmount()

    resetPricingDataForTest()
    installApiMocks(api)
    api.pricing.listModelCatalog.mockRejectedValueOnce({ message: '目录没取到' })
    wrapper = await mountPricingView(MatrixView)
    expect(wrapper.find('[data-test="load-error"]').text()).toContain('目录没取到')
    await wrapper.find('[data-test="load-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="matrix"]').exists()).toBe(true)
  })

  it('刷新按钮重新取数', async () => {
    const wrapper = await mountPricingView(MatrixView)
    await wrapper.find('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(api.groups.getAll).toHaveBeenCalledTimes(2)
  })
})

function groupOf(id: number) {
  return { id, name: 'kiro cc', platform: 'anthropic', rate_multiplier: 2 }
}
