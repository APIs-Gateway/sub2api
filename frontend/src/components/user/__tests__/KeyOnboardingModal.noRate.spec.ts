/**
 * 接入弹窗里不出现倍率、套餐价：分组倍率只在它前一步的「创建 / 编辑密钥」弹窗里讲，
 * 接入弹窗只管把地址和密钥配进客户端。三种语言、四个页签逐个渲染，防止以后被带进去。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../KeyOnboardingModal.vue'

const { getAvailable, i18nState } = vi.hoisted(() => ({
  getAvailable: vi.fn(),
  i18nState: { lang: 'zh-CN' as 'zh-CN' | 'zh-HK' | 'en' }
}))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))

// 测试环境的 vue-i18n 是 runtime-only 构建，不能编译消息；这里按当前语言的文案表取字再插值，
// 断言的是用户真正看到的字。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: en } = await import('@/i18n/locales/en')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const { default: zhHK } = await import('@/i18n/locales/zh-HK')
  const tables = { en, 'zh-CN': zhCN, 'zh-HK': zhHK } as const
  const lookup = (key: string): string | undefined => {
    let cur: unknown = tables[i18nState.lang]
    for (const seg of key.split('.')) {
      if (typeof cur !== 'object' || cur === null) return undefined
      cur = (cur as Record<string, unknown>)[seg]
    }
    return typeof cur === 'string' ? cur : undefined
  }
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('en'),
      t: (key: string, params: Record<string, unknown> = {}) =>
        (lookup(key) ?? key).replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? ''))
    })
  }
})

const TABS = ['install', 'ai', 'ccswitch', 'manual'] as const
const LANGS = ['zh-CN', 'zh-HK', 'en'] as const

// 倍率、套餐价，以及英文里的 rate（"rate limit" 之类也不该出现在这个弹窗里）。
const FORBIDDEN = /倍率|套餐[价價]|\brates?\b|plan price/i

const channels = [
  {
    name: 'ch',
    description: '',
    platforms: [
      { platform: 'openai', groups: [{ id: 7, name: 'g7' }], supported_models: [{ name: 'gpt-5.6-sol' }] }
    ]
  }
]

describe.each(LANGS)('接入弹窗不出现倍率 / 套餐价（%s）', (lang) => {
  beforeEach(() => {
    i18nState.lang = lang
    getAvailable.mockReset().mockResolvedValue(channels)
  })

  it.each(TABS)('页签 %s', async (tab) => {
    const wrapper = mount(KeyOnboardingModal, {
      props: {
        show: true,
        baseUrl: 'https://codex.hiyo.top/',
        siteName: 'Hiyo',
        docUrl: 'https://docs.example.com',
        apiKey: { key: 'sk-test-0000', name: 'my-key', group_id: 7, group: { id: 7, platform: 'openai' } }
      } as never,
      global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } } }
    })
    await flushPromises()
    await wrapper.get(`[data-test="tab-${tab}"]`).trigger('click')
    await flushPromises()

    const panel = wrapper.get(`[data-test="panel-${tab}"]`)
    // 先确认确实渲染出了内容，避免空页面也算通过。
    expect(panel.text().length).toBeGreaterThan(20)
    // 页面可见文字，加上文本框 / 输入框里的值。
    const fieldValues = panel
      .findAll('textarea, input')
      .map((el) => (el.element as HTMLInputElement | HTMLTextAreaElement).value)
      .join('\n')
    expect(`${wrapper.text()}\n${fieldValues}`).not.toMatch(FORBIDDEN)
    wrapper.unmount()
  })
})
