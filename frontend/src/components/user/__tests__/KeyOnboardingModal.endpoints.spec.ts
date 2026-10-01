import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../KeyOnboardingModal.vue'

const { getAvailable } = vi.hoisted(() => ({ getAvailable: vi.fn() }))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: en } = await import('@/i18n/locales/en')
  const lookup = (key: string): string | undefined => {
    let cur: unknown = en
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

const DEFAULT_URL = 'https://api.example.com'
const CDN = 'https://cdn.example.com'
const endpoints = [{ name: 'CDN 加速', endpoint: `${CDN}/v1/`, description: '国内访问更快' }]
const key = (platform: string) => ({ key: 'sk-SECRET-1234567890abcdef', name: 'k', group_id: 7, group: { id: 7, platform } })
const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }

async function mountModal(props: Record<string, unknown> = {}) {
  const w = mount(KeyOnboardingModal, {
    props: { show: true, apiKey: key('openai'), baseUrl: DEFAULT_URL, siteName: 'Hiyo', docUrl: '', customEndpoints: endpoints, ...props } as never,
    global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } } }
  })
  await flushPromises()
  return w
}

/** 逐个页签收集会被复制/展示的全部内容 */
async function collectAll(w: Awaited<ReturnType<typeof mountModal>>): Promise<string[]> {
  const out: string[] = []
  for (const tab of ['install', 'ai', 'ccswitch', 'manual']) {
    await w.get(`[data-test="tab-${tab}"]`).trigger('click')
    await flushPromises()
    out.push(w.html().replace(/id="onboarding-\d+-[a-z-]+"/g, '').replace(/(aria-controls|aria-labelledby|aria-describedby|for|name)="onboarding-\d+[^"]*"/g, ''))
    if (tab === 'install') {
      for (const b of w.findAll('button.onb-tile')) {
        clipboard.writeText.mockClear()
        await b.trigger('click')
        await flushPromises()
        out.push(String(clipboard.writeText.mock.calls[0]?.[0]))
      }
    }
    if (tab === 'ai') out.push((w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value)
    if (tab === 'ccswitch') {
      clipboard.writeText.mockClear()
      await w.get('[data-test="ccs-copy-link"]').trigger('click')
      await flushPromises()
      out.push(String(clipboard.writeText.mock.calls[0]?.[0]))
    }
  }
  return out
}

describe('KeyOnboardingModal 线路选择', () => {
  beforeEach(() => {
    localStorage.clear()
    getAvailable.mockResolvedValue([])
    clipboard.writeText.mockReset().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: clipboard, configurable: true })
  })
  afterEach(() => localStorage.clear())

  it('只有默认地址时不显示线路选择', async () => {
    expect((await mountModal({ customEndpoints: [] })).find('[data-test="endpoints"]').exists()).toBe(false)
    // 非法与重复的端点也不算选项
    const w = await mountModal({ customEndpoints: [{ name: 'a', endpoint: 'ftp://x' }, { name: 'b', endpoint: `${DEFAULT_URL}/v1` }] })
    expect(w.find('[data-test="endpoints"]').exists()).toBe(false)
  })

  it('有备用线路时显示单选组：名称与说明原样，默认地址叫「Default」，带 aria', async () => {
    const w = await mountModal()
    const group = w.get('[role="radiogroup"]')
    expect(group.attributes('aria-labelledby')).toBeTruthy()
    expect(group.attributes('aria-describedby')).toBeTruthy()
    expect(w.findAll('input[type="radio"]').length).toBe(2)
    expect(w.get('[data-test="endpoints"]').text()).toContain('Default')
    expect(w.get('[data-test="endpoints"]').text()).toContain('CDN 加速')
    expect(w.get('[data-test="endpoint-detail"]').text()).toContain(DEFAULT_URL)
    await w.get(`[data-test="endpoint-${CDN}"]`).setValue(true)
    expect(w.get('[data-test="endpoint-detail"]').text()).toContain('国内访问更快')
    expect(w.get('[data-test="endpoint-detail"]').text()).toContain(CDN)
  })

  it('切换后四个页签的内容都换成所选线路，且不再含默认地址', async () => {
    const w = await mountModal()
    await w.get(`[data-test="endpoint-${CDN}"]`).setValue(true)
    const all = (await collectAll(w)).join('\n')
    expect(all).toContain(`${CDN}/v1`)
    expect(all).toContain(CDN)
    expect(all).not.toContain('api.example.com')
    // claude 平台（原生地址不带 /v1）也跟着变
    const w2 = await mountModal({ apiKey: key('anthropic') })
    await w2.get(`[data-test="endpoint-${CDN}"]`).setValue(true)
    expect((await collectAll(w2)).join('\n')).not.toContain('api.example.com')
  })

  it('选默认线路时内容与没有备用线路时逐字节一致（所有页签）', async () => {
    for (const platform of ['openai', 'anthropic', 'gemini']) {
      const plain = await collectAll(await mountModal({ customEndpoints: [], apiKey: key(platform) }))
      const withLines = await collectAll(await mountModal({ apiKey: key(platform) }))
      // 页签 html 里多出的线路选择区域不算；比较复制出去的内容
      const copied = (xs: string[]) => xs.filter((x) => !x.startsWith('<'))
      expect(copied(withLines)).toEqual(copied(plain))
      expect(copied(plain).length).toBeGreaterThan(4)
    }
  })

  it('选择写入 docs_api_endpoint，下次打开仍是它；选回默认则删除', async () => {
    const w = await mountModal()
    await w.get(`[data-test="endpoint-${CDN}"]`).setValue(true)
    expect(localStorage.getItem('docs_api_endpoint')).toBe(CDN)
    const w2 = await mountModal()
    expect((w2.get(`[data-test="endpoint-${CDN}"]`).element as HTMLInputElement).checked).toBe(true)
    await w2.get('[data-test="endpoint-default"]').setValue(true)
    expect(localStorage.getItem('docs_api_endpoint')).toBeNull()
  })

  it('已选线路被站点删掉后回落到默认地址', async () => {
    localStorage.setItem('docs_api_endpoint', 'https://gone.example.com')
    const w = await mountModal()
    expect((w.get('[data-test="endpoint-default"]').element as HTMLInputElement).checked).toBe(true)
    await w.get('[data-test="tab-manual"]').trigger('click')
    expect(w.get('[data-test="panel-manual"]').text()).toContain(DEFAULT_URL)
  })

  it('文档页在弹窗关闭期间改了选择，重新打开时跟随', async () => {
    const w = await mountModal()
    await w.setProps({ show: false })
    localStorage.setItem('docs_api_endpoint', CDN)
    await w.setProps({ show: true })
    expect((w.get(`[data-test="endpoint-${CDN}"]`).element as HTMLInputElement).checked).toBe(true)
  })
})
