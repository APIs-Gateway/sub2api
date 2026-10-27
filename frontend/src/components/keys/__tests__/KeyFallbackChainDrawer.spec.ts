import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

// 抽屉在 document 上挂了键盘监听，用例之间必须卸载，否则上一个用例的实例会接着响应
const mounted: Array<{ unmount: () => void }> = []
afterEach(() => {
  while (mounted.length) mounted.pop()!.unmount()
})

function mountDrawer(props: Record<string, unknown> = {}) {
  const w = mount(KeyFallbackChainDrawer, {
    props: { show: true, initialKeyId: null, ...props },
    global: { stubs: { KeyFallbackChainEditor: EditorStub, Teleport: true, Transition: false } }
  })
  mounted.push(w)
  return w
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
    expect(w.get('[data-test="intro"]').text()).toContain('就按该分组的价格计费')
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

  describe('焦点陷阱与焦点归还', () => {
    const mountAttached = async (props: Record<string, unknown> = {}) => {
      const w = mount(KeyFallbackChainDrawer, {
        attachTo: document.body,
        props: { show: true, initialKeyId: null, ...props },
        global: { stubs: { KeyFallbackChainEditor: EditorStub, Teleport: true, Transition: false } }
      })
      mounted.push(w)
      await flushPromises()
      return w
    }
    const tab = (w: ReturnType<typeof mount>, shiftKey = false) =>
      w.get('[data-test="drawer"]').trigger('keydown', { key: 'Tab', shiftKey })

    it('打开后焦点进入抽屉（关闭按钮）', async () => {
      const w = await mountAttached()
      expect(document.activeElement).toBe(w.get('[data-test="drawer-close"]').element)
      w.unmount()
    })

    it('Tab 在最后一个可聚焦元素上回到第一个', async () => {
      const w = await mountAttached()
      const toggles = w.findAll('[data-test="key-toggle"]')
      ;(toggles[toggles.length - 1].element as HTMLElement).focus()
      await tab(w)
      expect(document.activeElement).toBe(w.get('[data-test="drawer-close"]').element)
      w.unmount()
    })

    it('Shift+Tab 在第一个可聚焦元素上跳到最后一个', async () => {
      const w = await mountAttached()
      ;(w.get('[data-test="drawer-close"]').element as HTMLElement).focus()
      await tab(w, true)
      const toggles = w.findAll('[data-test="key-toggle"]')
      expect(document.activeElement).toBe(toggles[toggles.length - 1].element)
      w.unmount()
    })

    it('中间的 Tab 不拦截，交给浏览器', async () => {
      const w = await mountAttached()
      ;(w.get('[data-test="drawer-close"]').element as HTMLElement).focus()
      const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
      w.get('[data-test="drawer-close"]').element.dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(false)
      w.unmount()
    })

    it('焦点在抽屉外面时按 Tab，被拉回抽屉里', async () => {
      const outside = document.createElement('button')
      document.body.appendChild(outside)
      const w = await mountAttached()
      outside.focus()
      const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
      outside.dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(true)
      expect(w.get('[data-test="drawer"]').element.contains(document.activeElement)).toBe(true)
      w.unmount()
      outside.remove()
    })

    it('被样式隐藏的元素（窄屏下的拖动手柄）不算可聚焦元素', async () => {
      const w = await mountAttached()
      const toggles = w.findAll('[data-test="key-toggle"]')
      const last = toggles[toggles.length - 1].element as HTMLElement
      last.style.display = 'none'
      ;(toggles[toggles.length - 2].element as HTMLElement).focus()
      await tab(w)
      expect(document.activeElement).toBe(w.get('[data-test="drawer-close"]').element)
      w.unmount()
    })

    it('关闭后焦点回到打开它的按钮', async () => {
      const trigger = document.createElement('button')
      document.body.appendChild(trigger)
      trigger.focus()
      const w = await mountAttached({ show: false })
      await w.setProps({ show: true })
      await flushPromises()
      expect(document.activeElement).not.toBe(trigger)
      await w.setProps({ show: false })
      await flushPromises()
      expect(document.activeElement).toBe(trigger)
      w.unmount()
      trigger.remove()
    })

    it('按钮在点击时没拿到焦点（如 Safari）：用 returnFocus 指定的元素', async () => {
      const trigger = document.createElement('button')
      document.body.appendChild(trigger)
      const w = await mountAttached({ show: false, returnFocus: trigger })
      await w.setProps({ show: true })
      await flushPromises()
      await w.setProps({ show: false })
      await flushPromises()
      expect(document.activeElement).toBe(trigger)
      w.unmount()
      trigger.remove()
    })
  })

  it('Esc 已被里面（如选择列表）处理过时，不再关闭抽屉', async () => {
    const w = mountDrawer()
    await flushPromises()
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    ev.preventDefault()
    document.dispatchEvent(ev)
    expect(w.emitted('close')).toBeUndefined()
    w.unmount()
  })
})
