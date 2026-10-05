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

type Status = 'operational' | 'degraded' | 'failed' | 'error'

// 与后端对普通用户的响应一致：没有 provider / primary_model / PING。
const card = (id: number, name: string, primary: Status, timeline: Status[] = [primary]) => ({
  id,
  name,
  group_name: '',
  primary_status: primary,
  primary_latency_ms: 820,
  availability_7d: 99.5,
  timeline: timeline.map(s => ({ status: s, latency_ms: 820, checked_at: checkedAt })),
})

const BaseDialogStub = {
  props: ['show', 'title'],
  template: '<div v-if="show" class="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
}
const mountView = () => mount(ChannelStatusView, {
  global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, BaseDialog: BaseDialogStub } },
})

let wrapper: ReturnType<typeof mountView>
beforeEach(() => { localStorage.clear(); list.mockReset(); status.mockReset() })
afterEach(() => { wrapper?.unmount(); localStorage.clear() })

// 页面顶部的总状态胶囊（MonitorHero 里唯一一个带 uppercase 的 span）。
const overallChip = () => wrapper.get('section span.uppercase').text()

async function show(items: ReturnType<typeof card>[]) {
  list.mockResolvedValue({ items })
  wrapper = mountView()
  await flushPromises()
}

describe('渠道状态页：总状态只看红卡', () => {
  it('全绿 → 正常', async () => {
    await show([card(1, 'Alpha', 'operational'), card(2, 'Bravo', 'operational')])
    expect(overallChip()).toBe('正常')
  })

  it('有黄卡但没有红卡 → 总状态仍是正常；黄卡只在卡片本身显示降级', async () => {
    await show([card(1, 'Alpha', 'operational'), card(2, 'Bravo', 'degraded', ['degraded', 'operational'])])
    expect(overallChip()).toBe('正常')
    const cardB = wrapper.findAll('button').find(b => b.text().includes('Bravo'))!
    expect(cardB.text()).toContain('降级')
  })

  it('有红卡 → 降级', async () => {
    await show([card(1, 'Alpha', 'operational'), card(2, 'Bravo', 'failed'), card(3, 'Charlie', 'degraded')])
    expect(overallChip()).toBe('降级')
  })

  it('failed 和 error 都算红卡', async () => {
    await show([card(1, 'Alpha', 'operational'), card(2, 'Bravo', 'error')])
    expect(overallChip()).toBe('降级')
  })

  it('每张卡都是红的 → 不可用', async () => {
    await show([card(1, 'Alpha', 'failed'), card(2, 'Bravo', 'error')])
    expect(overallChip()).toBe('不可用')
  })

  it('全红的颜色与降级不同，不会被读成「降级」', async () => {
    await show([card(1, 'Alpha', 'failed'), card(2, 'Bravo', 'error')])
    const unavailableChip = wrapper.get('section span.uppercase')
    expect(unavailableChip.classes().join(' ')).toMatch(/red/)
    expect(unavailableChip.get('span').classes()).toContain('bg-red-500')
    wrapper.unmount()

    await show([card(1, 'Alpha', 'failed'), card(2, 'Bravo', 'operational')])
    expect(wrapper.get('section span.uppercase').classes().join(' ')).not.toMatch(/red/)
    expect(wrapper.get('section span.uppercase').get('span').classes()).not.toContain('bg-red-500')
  })

  it('没有任何渠道 → 正常，不会误报不可用', async () => {
    await show([])
    expect(overallChip()).toBe('正常')
  })
})

describe('渠道状态页：普通用户看到的硬失败统一是「不可用」', () => {
  it('卡片、时间线提示里都没有「失败」「错误」', async () => {
    await show([
      card(1, 'Alpha', 'failed', ['failed', 'operational']),
      card(2, 'Bravo', 'error', ['error', 'degraded', 'operational']),
      card(3, 'Charlie', 'operational'),
    ])
    const text = wrapper.text()
    const html = wrapper.html()
    expect(text).not.toContain('失败')
    expect(text).not.toContain('错误')
    expect(html).not.toMatch(/title="[^"]*(失败|错误)[^"]*"/)

    const cardA = wrapper.findAll('button').find(b => b.text().includes('Alpha'))!
    const cardB = wrapper.findAll('button').find(b => b.text().includes('Bravo'))!
    expect(cardA.text()).toContain('不可用')
    expect(cardB.text()).toContain('不可用')
    // 时间线每一格仍对应一次探测，悬浮提示里用同一个词
    expect(cardA.html()).toMatch(/title="[^"]*· 不可用 ·[^"]*"/)
    expect(cardA.html()).toMatch(/title="[^"]*· 正常 ·[^"]*"/)
    expect(cardB.html()).toMatch(/title="[^"]*· 降级 ·[^"]*"/)
  })

  it('详情弹窗里的状态也是「不可用」', async () => {
    status.mockResolvedValue({
      id: 2, name: 'Bravo', group_name: '',
      models: [{
        latest_status: 'error', latest_latency_ms: null,
        availability_7d: 41.2, availability_15d: 40, availability_30d: 39, avg_latency_7d_ms: null,
      }],
    })
    await show([card(2, 'Bravo', 'error')])
    await wrapper.findAll('button').find(b => b.text().includes('Bravo'))!.trigger('click')
    await flushPromises()

    const dialog = wrapper.get('.dialog')
    expect(dialog.text()).toContain('不可用')
    expect(dialog.text()).not.toContain('错误')
    expect(dialog.text()).not.toContain('失败')
    // error 和 failed 一样是红色，不是灰色的「未知」样式
    const badge = dialog.findAll('span').find(s => s.text() === '不可用')!
    expect(badge.classes().join(' ')).toMatch(/red/)
  })
})

describe('渠道状态页：管理员保留失败 / 错误的细分', () => {
  const adminCard = (id: number, name: string, primary: Status) => ({
    ...card(id, name, primary),
    provider: 'openai',
    primary_model: 'gpt-5.6-secret-model',
    primary_ping_latency_ms: 37,
    extra_models: [],
    timeline: [{ status: primary, latency_ms: 820, ping_latency_ms: 37, checked_at: checkedAt }],
  })

  it('卡片和时间线仍然显示「失败」「错误」', async () => {
    list.mockResolvedValue({ items: [adminCard(1, 'Alpha', 'failed'), adminCard(2, 'Bravo', 'error')] })
    wrapper = mountView(); await flushPromises()

    const cardA = wrapper.findAll('button').find(b => b.text().includes('Alpha'))!
    const cardB = wrapper.findAll('button').find(b => b.text().includes('Bravo'))!
    expect(cardA.text()).toContain('失败')
    expect(cardB.text()).toContain('错误')
    expect(cardA.html()).toMatch(/title="[^"]*· 失败 ·[^"]*"/)
    expect(cardB.html()).toMatch(/title="[^"]*· 错误 ·[^"]*"/)
    // 顶部总状态与普通用户同一口径：两张都是红卡 → 不可用
    expect(overallChip()).toBe('不可用')
  })
})
