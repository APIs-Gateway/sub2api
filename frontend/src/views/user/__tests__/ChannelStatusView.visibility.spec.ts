import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ChannelStatusView from '../ChannelStatusView.vue'

const { list, status } = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn() }))
vi.mock('@/api/channelMonitor', () => ({ list, status }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: { channel_monitor_enabled: true }, showError: vi.fn() }),
}))
vi.mock('vue-i18n', async () => {
  const zhCN = (await import('@/i18n/locales/zh-CN')).default as Record<string, unknown>
  const t = (key: string, params?: Record<string, unknown>) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    const text = typeof hit === 'string' ? hit : key
    return text.replace(/\{(\w+)\}/g, (_, k: string) => String(params?.[k] ?? ''))
  }
  return { ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t }) }
})

const checkedAt = new Date().toISOString()

// 与后端对普通用户的响应一致（backend 单测 TestChannelMonitorUserList_NonAdminHidesUpstreamInfo 锁定了这份结构）。
const nonAdminList = {
  items: [
    {
      id: 7,
      name: 'GPT 通道',
      group_name: 'GPT',
      primary_status: 'operational',
      primary_latency_ms: 820,
      availability_7d: 99.5,
      timeline: [{ status: 'operational', latency_ms: 820, checked_at: checkedAt }],
    },
    {
      id: 8,
      name: 'Claude 通道',
      group_name: '',
      primary_status: 'failed',
      primary_latency_ms: null,
      availability_7d: 41.2,
      timeline: [],
    },
  ],
}
const nonAdminDetail = {
  id: 7,
  name: 'GPT 通道',
  group_name: 'GPT',
  models: [{
    latest_status: 'operational', latest_latency_ms: 820,
    availability_7d: 99.5, availability_15d: 98.1, availability_30d: 97.7, avg_latency_7d_ms: 900,
  }],
}
const adminList = {
  items: [{
    ...nonAdminList.items[0],
    provider: 'openai',
    primary_model: 'gpt-5.6-secret-model',
    primary_ping_latency_ms: 37,
    extra_models: [],
    timeline: [{ status: 'operational', latency_ms: 820, ping_latency_ms: 37, checked_at: checkedAt }],
  }],
}

const BaseDialogStub = {
  props: ['show', 'title'],
  template: '<div v-if="show" class="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
}
const mountView = () => mount(ChannelStatusView, {
  global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, BaseDialog: BaseDialogStub } },
})

const LEAKS = [/openai/i, /anthropic/i, /gemini/i, /secret-model/, /PING/, /端点/]

let wrapper: ReturnType<typeof mountView>
beforeEach(() => { localStorage.clear(); list.mockReset(); status.mockReset() })
afterEach(() => { wrapper?.unmount(); localStorage.clear() })

describe('渠道状态页：普通用户', () => {
  it('列表和详情弹窗里都看不到供应商、主模型和 PING', async () => {
    list.mockResolvedValue(nonAdminList)
    status.mockResolvedValue(nonAdminDetail)
    wrapper = mountView(); await flushPromises()

    const listHtml = wrapper.html()
    for (const re of LEAKS) expect(listHtml).not.toMatch(re)
    expect(wrapper.text()).toContain('GPT 通道')
    expect(wrapper.text()).toContain('Claude 通道')
    expect(wrapper.text()).toContain('失败')

    await wrapper.findAll('button').find(b => b.text().includes('GPT 通道'))!.trigger('click')
    await flushPromises()

    const dialog = wrapper.get('.dialog')
    expect(dialog.find('table').exists()).toBe(false)
    expect(dialog.text()).toContain('15 天可用率')
    expect(dialog.text()).toContain('98.10%')
    for (const re of LEAKS) expect(dialog.html()).not.toMatch(re)
  })

  it('顶部总状态是中文，不是写死的英文', async () => {
    list.mockResolvedValue(nonAdminList)
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('降级') // 其中一个渠道失败 → 总状态降级
    expect(wrapper.text()).not.toMatch(/\b(OPERATIONAL|DEGRADED|UNAVAILABLE|PAST|NOW)\b/)
  })

  it('15 天窗口也能取到主模型的可用率（详情里没有模型名）', async () => {
    list.mockResolvedValue({ items: [nonAdminList.items[0]] })
    status.mockResolvedValue(nonAdminDetail)
    wrapper = mountView(); await flushPromises()

    await wrapper.findAll('[role="tab"]').find(b => b.text() === '15 天')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('98.10')
  })
})

describe('渠道状态页：管理员', () => {
  it('仍能看到供应商、主模型和 PING', async () => {
    list.mockResolvedValue(adminList)
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('OpenAI')
    expect(wrapper.text()).toContain('gpt-5.6-secret-model')
    expect(wrapper.text()).toContain('端点 PING')
  })
})
