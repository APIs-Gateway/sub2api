import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { listChains } = vi.hoisted(() => ({ listChains: vi.fn() }))

vi.mock('@/api/keyFallback', () => ({ keyFallbackAPI: { listChains, getChain: vi.fn(), replaceChain: vi.fn() } }))
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

import KeyFallbackChainDrawer from '../KeyFallbackChainDrawer.vue'

const item = (group_id: number, name: string, role: 'primary' | 'fallback', position: number) => ({
  group_id, name, role, position, status: 'active' as const, usable: true
})

const summary = {
  platforms: [
    {
      platform: 'openai',
      keys: [
        { key_id: 1, name: 'my codex', has_available: true, items: [item(16, 'Codex Plus', 'primary', 0), item(21, 'Codex 稳定', 'fallback', 1)] },
        { key_id: 2, name: 'second', has_available: true, items: [item(16, 'Codex Plus', 'primary', 0)] }
      ]
    },
    { platform: 'anthropic', keys: [] }
  ]
}

const EditorStub = { props: ['keyId'], template: '<div data-test="editor-stub">editor {{ keyId }}</div>' }

function mountDrawer(props: Record<string, unknown> = {}) {
  return mount(KeyFallbackChainDrawer, {
    props: { show: true, initialKeyId: null, ...props },
    global: { stubs: { KeyFallbackChainEditor: EditorStub, Teleport: true, Transition: false } }
  })
}

describe('KeyFallbackChainDrawer', () => {
  beforeEach(() => {
    listChains.mockReset()
    listChains.mockResolvedValue(summary)
  })

  it('按平台分区列出 Key，没有 Key 的平台不显示，摘要显示兜底个数', async () => {
    const w = mountDrawer()
    await flushPromises()
    expect(w.find('[data-test="platform-openai"]').exists()).toBe(true)
    expect(w.find('[data-test="platform-anthropic"]').exists()).toBe(false)
    expect(w.get('[data-test="key-1"] [data-test="key-summary"]').text()).toBe('1 个兜底分组')
    expect(w.get('[data-test="key-2"] [data-test="key-summary"]').text()).toBe('未设置兜底')
    expect(w.get('[data-test="intro"]').text()).toContain('按哪个分组的价格计费')
  })

  it('从行内入口打开时直接展开那把 Key，点别的 Key 切换', async () => {
    const w = mountDrawer({ initialKeyId: 2 })
    await flushPromises()
    expect(w.get('[data-test="key-2"]').text()).toContain('editor 2')
    expect(w.find('[data-test="key-1"] [data-test="editor-stub"]').exists()).toBe(false)
    await w.get('[data-test="key-1"] [data-test="key-toggle"]').trigger('click')
    expect(w.get('[data-test="key-1"]').text()).toContain('editor 1')
    expect(w.find('[data-test="key-2"] [data-test="editor-stub"]').exists()).toBe(false)
  })

  it('没有已绑定分组的密钥时给出下一步', async () => {
    listChains.mockResolvedValue({ platforms: [] })
    const w = mountDrawer()
    await flushPromises()
    expect(w.get('[data-test="drawer-empty"]').text()).toContain('先给密钥选择分组')
  })

  it('接口报错时显示提示并可重试', async () => {
    listChains.mockRejectedValueOnce({ status: 500, message: 'boom' })
    const w = mountDrawer()
    await flushPromises()
    expect(w.get('[data-test="drawer-error"]').text()).toContain('加载失败，请重试')
    expect(w.text()).not.toContain('boom')
    await w.get('[data-test="drawer-error"] button').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="platform-openai"]').exists()).toBe(true)
  })

  it('点关闭按钮和按 Esc 都会触发 close', async () => {
    const w = mountDrawer()
    await flushPromises()
    await w.get('[data-test="drawer-close"]').trigger('click')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(w.emitted('close')).toHaveLength(2)
  })
})
