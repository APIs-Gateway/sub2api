import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import MonitorCard from '../MonitorCard.vue'
import ProviderIcon from '../ProviderIcon.vue'

// 用真实的 zh-CN 文案渲染，这样才能断言页面上到底出现了哪些字。
vi.mock('vue-i18n', async () => {
  const zhCN = (await import('@/i18n/locales/zh-CN')).default as Record<string, unknown>
  const t = (key: string, params?: Record<string, unknown>) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    const text = typeof hit === 'string' ? hit : key
    return text.replace(/\{(\w+)\}/g, (_, k: string) => String(params?.[k] ?? ''))
  }
  return { useI18n: () => ({ t }) }
})

const checkedAt = new Date().toISOString()

// 与后端对普通用户返回的字段完全一致：没有 provider、primary_model、PING、附加模型。
const nonAdminItem = {
  id: 7,
  name: 'GPT 通道',
  group_name: 'GPT',
  primary_status: 'operational' as const,
  primary_latency_ms: 820,
  availability_7d: 99.5,
  timeline: [{ status: 'operational' as const, latency_ms: 820, checked_at: checkedAt }],
}

// 管理员拿到的完整视图。
const adminItem = {
  ...nonAdminItem,
  provider: 'openai' as const,
  primary_model: 'gpt-5.6-secret-model',
  primary_ping_latency_ms: 37,
  extra_models: [{ model: 'gpt-extra-model', status: 'operational' as const, latency_ms: 900 }],
  timeline: [{ ...nonAdminItem.timeline[0], ping_latency_ms: 37 }],
}

const mountCard = (item: typeof nonAdminItem | typeof adminItem) =>
  mount(MonitorCard, { props: { item, window: '7d', availabilityValue: 99.5, countdownSeconds: 60 } })

describe('MonitorCard：普通用户的卡片', () => {
  const wrapper = mountCard(nonAdminItem)
  const html = wrapper.html()
  const text = wrapper.text()

  it('不渲染供应商、主模型和 PING', () => {
    expect(html).not.toMatch(/openai/i)
    expect(html).not.toMatch(/anthropic|gemini/i)
    expect(html).not.toMatch(/gpt-5\.6-secret-model/)
    expect(text).not.toContain('PING')
    expect(text).not.toContain('端点')
    expect(wrapper.findComponent(ProviderIcon).exists()).toBe(false) // 供应商图标
    expect(text).not.toContain('+ ') // 附加模型计数
  })

  it('只显示本地化后的状态和名称', () => {
    expect(text).toContain('GPT 通道')
    expect(text).toContain('正常')
    expect(text).toContain('对话延迟')
    expect(text).toContain('820')
    expect(text).toContain('99.50')
    expect(text).toContain('过去')
    expect(text).toContain('现在')
  })

  it('没有写死的英文状态词', () => {
    expect(text).not.toMatch(/\b(OPERATIONAL|DEGRADED|UNAVAILABLE|PAST|NOW)\b/)
  })

  it('没有分组名时仍保留标签行的高度，同一排的卡片不会错位', () => {
    const w = mountCard({ ...nonAdminItem, group_name: '' })
    const spacer = w.find('span.invisible[aria-hidden="true"]')
    expect(spacer.exists()).toBe(true)
    expect(w.text()).not.toContain('GPT 通道 GPT')
    expect(wrapper.find('span.invisible[aria-hidden="true"]').exists()).toBe(false) // 有分组名时不需要
  })

  it('延迟卡片占满整行，不留空位', () => {
    const tiles = wrapper.findAll('.grid.gap-2 > div')
    expect(tiles).toHaveLength(1)
    expect(wrapper.find('.grid.gap-2').classes()).toContain('grid-cols-1')
  })
})

describe('MonitorCard：管理员的卡片保持不变', () => {
  const wrapper = mountCard(adminItem)
  const text = wrapper.text()

  it('仍显示供应商、主模型、PING 和附加模型计数', () => {
    expect(text).toContain('OpenAI')
    expect(text).toContain('gpt-5.6-secret-model')
    expect(text).toContain('端点 PING')
    expect(text).toContain('37')
    expect(text).toContain('+ 1 模型')
    expect(wrapper.findComponent(ProviderIcon).exists()).toBe(true)
  })

  it('两个指标并排', () => {
    expect(wrapper.findAll('.grid.gap-2 > div')).toHaveLength(2)
    expect(wrapper.find('.grid.gap-2').classes()).toContain('grid-cols-2')
  })

  it('PING 为 null（暂无数据）时仍显示 PING 卡片', () => {
    const w = mountCard({ ...adminItem, primary_ping_latency_ms: null as unknown as number })
    expect(w.text()).toContain('端点 PING')
  })
})
