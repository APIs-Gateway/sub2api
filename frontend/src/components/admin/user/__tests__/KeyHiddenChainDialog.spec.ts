import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { getChain, putChain, deleteChain, showSuccess } = vi.hoisted(() => ({
  getChain: vi.fn(), putChain: vi.fn(), deleteChain: vi.fn(), showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { apiKeys: { getFallbackChain: getChain, putHiddenFallbackChain: putChain, deleteHiddenFallbackChain: deleteChain } }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError: vi.fn() }) }))
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

import KeyHiddenChainDialog from '../KeyHiddenChainDialog.vue'
import type { AdminGroup, ApiKey } from '@/types'

const group = (id: number, name: string, rate: number, extra: Partial<AdminGroup> = {}) =>
  ({ id, name, platform: 'openai', rate_multiplier: rate, status: 'active', ...extra }) as AdminGroup

const groups = [
  group(16, 'Codex Plus', 1),
  group(88, 'hovy-513', 2.5),
  group(89, 'Codex Pro', 3),
  group(90, 'Claude X', 1, { platform: 'anthropic' }),
  group(91, 'Old', 1, { status: 'inactive' })
]
const apiKey = { id: 1908, name: 'k1', group_id: 16 } as ApiKey

const emptyChain = {
  key_id: 1908, user_id: 513, platform: 'openai',
  primary_group: { group_id: 16, name: 'Codex Plus' },
  user_items: [{ group_id: 21, position: 0 }],
  hidden_head: [], hidden_tail: [], effective: [{ hop: 0, group_id: 16, source: 'primary', eligible: true }]
}

async function open(chain: unknown = emptyChain) {
  getChain.mockResolvedValue(chain)
  const w = mount(KeyHiddenChainDialog, {
    props: { show: true, apiKey, groups },
    global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>' } } }
  })
  await flushPromises()
  return w
}

async function pick(w: Awaited<ReturnType<typeof open>>, part: 'head' | 'tail', id: number) {
  const sel = w.get(`[data-test="add-${part}"]`)
  ;(sel.element as HTMLSelectElement).value = String(id)
  await sel.trigger('change')
}

describe('KeyHiddenChainDialog', () => {
  beforeEach(() => {
    getChain.mockReset(); putChain.mockReset(); deleteChain.mockReset(); showSuccess.mockReset()
  })

  it('只列同平台、启用中、不是主分组的候选', async () => {
    const w = await open()
    const opts = w.get('[data-test="add-head"]').findAll('option').map((o) => o.text())
    expect(opts.some((t) => t.includes('hovy-513'))).toBe(true)
    expect(opts.some((t) => t.includes('Codex Pro'))).toBe(true)
    expect(opts.some((t) => t.includes('Codex Plus'))).toBe(false)
    expect(opts.some((t) => t.includes('Claude X'))).toBe(false)
    expect(opts.some((t) => t.includes('Old'))).toBe(false)
  })

  it('写 head 项：并排显示 head 与主分组的倍率，并强制填备注', async () => {
    const w = await open()
    expect(w.find('[data-test="rate-compare"]').exists()).toBe(false)
    await pick(w, 'head', 88)
    expect(w.get('[data-test="rate-compare"]').text()).toContain('头部分组倍率 2.5x，主分组倍率 1x')

    // 没填备注：不发请求，提示必填
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(putChain).not.toHaveBeenCalled()
    expect(w.get('[data-test="note-hint"]').text()).toBe('请填写备注，至少 4 个字符')

    // 备注太短也不行
    await w.get('[data-test="note"]').setValue('abc')
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(putChain).not.toHaveBeenCalled()

    putChain.mockResolvedValue(emptyChain)
    await w.get('[data-test="note"]').setValue('513 专属号')
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(putChain).toHaveBeenCalledWith(1908, { head: [88], tail: [], note: '513 专属号' })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('只写尾部分组时备注可以不填；顺序可上下调整', async () => {
    const w = await open()
    await pick(w, 'tail', 88)
    await pick(w, 'tail', 89)
    await w.get('[data-test="tail-item-89"] [data-test="move-up"]').trigger('click')
    putChain.mockResolvedValue(emptyChain)
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(putChain).toHaveBeenCalledWith(1908, { head: [], tail: [89, 88] })
  })

  it('头部和尾部都空时保存等价于清空', async () => {
    const w = await open({ ...emptyChain, hidden_tail: [{ group_id: 88, position: 0 }] })
    await w.get('[data-test="tail-item-88"] [data-test="remove"]').trigger('click')
    deleteChain.mockResolvedValue(undefined)
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(putChain).not.toHaveBeenCalled()
    expect(deleteChain).toHaveBeenCalledWith(1908)
  })

  it('已有隐藏链时回填；清空走 DELETE', async () => {
    const w = await open({
      ...emptyChain,
      hidden_head: [{ group_id: 88, position: 0, note: '513 专属号' }],
      effective: [
        { hop: 0, group_id: 88, source: 'admin_head', eligible: true },
        { hop: 1, group_id: 16, source: 'primary', eligible: true }
      ],
      skipped: [
        { group_id: 21, source: 'user', skip_reason: 'group_inactive' },
        { group_id: 22, source: 'user', skip_reason: 'not_allowed' },
        { group_id: 23, source: 'admin', skip_reason: 'group_missing' },
        { group_id: 24, source: 'user', skip_reason: 'invalid_platform' },
        { group_id: 25, source: 'user', skip_reason: 'some_future_reason' }
      ]
    })
    expect((w.get('[data-test="note"]').element as HTMLTextAreaElement).value).toBe('513 专属号')
    expect(w.get('[data-test="head-item-88"]').text()).toContain('hovy-513')
    const skipped = w.get('[data-test="skipped"]').text()
    expect(skipped).toContain('已跳过：分组已停用')
    expect(skipped).toContain('已跳过：用户无权使用')
    expect(skipped).toContain('已跳过：分组已删除')
    expect(skipped).toContain('已跳过：与主分组平台不一致')
    // 未知值回落到通用文案，不显示原始代码
    expect(skipped).toContain('已跳过：不可用')
    expect(skipped).not.toMatch(/group_|not_allowed|invalid_platform|some_future_reason/)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    deleteChain.mockResolvedValue(undefined)
    await w.get('[data-test="clear"]').trigger('click')
    await flushPromises()
    expect(deleteChain).toHaveBeenCalledWith(1908)
  })

  it('保存失败时显示后端给出的原因', async () => {
    const w = await open()
    await pick(w, 'head', 88)
    await w.get('[data-test="note"]').setValue('513 专属号')
    putChain.mockRejectedValue({ status: 400, message: 'platform mismatch' })
    await w.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="hidden-error"]').text()).toBe('platform mismatch')
  })

  it('加载失败时提示并可重试', async () => {
    getChain.mockRejectedValueOnce(new Error('x'))
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const w = mount(KeyHiddenChainDialog, {
      props: { show: true, apiKey, groups },
      global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } } }
    })
    await flushPromises()
    expect(w.get('[data-test="hidden-load-error"]').text()).toContain('加载失败')
  })
})
