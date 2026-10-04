/**
 * 外壳用的几个 composable：模型加载状态（useGroupModels）、CC Switch 的表单状态（useCcSwitchState）、手动配置选中的代码页签（useManualCodeTab）。
 * 弹窗整体的行为由 ../../__tests__/KeyOnboardingModal*.spec.ts 覆盖；这里只验证它们对页签暴露的接口。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, ref } from 'vue'

import { clientsForPlatform } from '@/utils/keyOnboarding'
import { useCcSwitchState } from '../useCcSwitchState'
import { useGroupModels } from '../useGroupModels'
import { useManualCodeTab } from '../useManualCodeTab'

const { getAvailable } = vi.hoisted(() => ({ getAvailable: vi.fn() }))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))

const channels = [
  {
    name: 'ch',
    description: '',
    platforms: [
      { platform: 'openai', groups: [{ id: 7, name: 'g7' }], supported_models: [{ name: 'gpt-5.6-sol' }, { name: 'gpt-5.6-luna' }] },
      { platform: 'openai', groups: [{ id: 99, name: 'other' }], supported_models: [{ name: 'not-mine' }] }
    ]
  }
]

describe('useGroupModels', () => {
  beforeEach(() => {
    getAvailable.mockReset()
  })

  it('loading：请求在路上时为 true，成功后回到 false，models 才有内容', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const { models, loading, load } = useGroupModels(() => 7)
    expect(loading.value).toBe(false)
    expect(models.value).toEqual([])

    const pending = load()
    expect(loading.value).toBe(true)
    expect(models.value).toEqual([])

    resolve(channels)
    await pending
    expect(loading.value).toBe(false)
    expect(models.value).toEqual(['gpt-5.6-sol', 'gpt-5.6-luna'])
  })

  it('请求失败也回到 false，models 为空；之后不重试', async () => {
    getAvailable.mockRejectedValue(new Error('x'))
    const { models, loading, load } = useGroupModels(() => 7)
    await load()
    expect(loading.value).toBe(false)
    expect(models.value).toEqual([])
    await load()
    expect(getAvailable).toHaveBeenCalledTimes(1)
  })

  it('加载中再调用 load 不会重复请求', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const { loading, load } = useGroupModels(() => 7)
    const first = load()
    await load()
    expect(getAvailable).toHaveBeenCalledTimes(1)
    expect(loading.value).toBe(true)
    resolve(channels)
    await first
    expect(loading.value).toBe(false)
  })
})

describe('useCcSwitchState', () => {
  const setup = (platform: string | null, show = true, groupId: number | undefined = 7) => {
    const state = { platform: ref(platform), show: ref(show), groupId: ref<number | undefined>(groupId) }
    const scope = effectScope()
    const result = scope.run(() =>
      useCcSwitchState({ platform: state.platform, show: () => state.show.value, groupId: () => state.groupId.value })
    )!
    return { ...state, ...result, stop: () => scope.stop() }
  }

  it('表单是一个对象：客户端、名称、模型；初始是该平台的第一个客户端，名称和模型为空', () => {
    expect(setup('openai').form.value).toEqual({ client: 'codex', name: '', model: '' })
    expect(setup('anthropic').form.value).toEqual({ client: 'claude', name: '', model: '' })
    expect(setup('gemini').form.value).toEqual({ client: 'gemini', name: '', model: '' })
    expect(setup('antigravity').form.value).toEqual({ client: 'claude', name: '', model: '' })
  })

  it('选中的客户端不在新平台的可选范围内时，改成第一个；在范围内就保留', async () => {
    const s = setup('antigravity')
    s.form.value = { ...s.form.value, client: 'gemini' }
    s.platform.value = 'gemini'
    await nextTick()
    expect(s.form.value.client).toBe('gemini')
    s.platform.value = 'openai'
    await nextTick()
    expect(s.form.value.client).toBe('codex')
  })

  it('关闭再打开、换分组：名称和模型清空，客户端保留', async () => {
    const s = setup('antigravity')
    s.form.value = { client: 'gemini', name: 'Mine', model: 'gemini-3-pro' }
    s.show.value = false
    await nextTick()
    expect(s.form.value).toEqual({ client: 'gemini', name: '', model: '' })

    s.form.value = { client: 'gemini', name: 'Again', model: 'gemini-3-pro' }
    s.groupId.value = 10
    await nextTick()
    expect(s.form.value).toEqual({ client: 'gemini', name: '', model: '' })
  })

  it('更新表单时换成新对象', () => {
    const s = setup('anthropic')
    const before = s.form.value
    s.form.value = { ...before, name: 'Mine' }
    expect(s.form.value).not.toBe(before)
    expect(before.name).toBe('')
  })
})

describe('useManualCodeTab', () => {
  const setup = (platform: string | null, dispatch: boolean | undefined = undefined) => {
    const state = { platform: ref(platform), dispatch: ref<boolean | undefined>(dispatch) }
    const clients = computed(() => clientsForPlatform(state.platform.value, { allowMessagesDispatch: state.dispatch.value }))
    const scope = effectScope()
    const tab = scope.run(() => useManualCodeTab({ platform: state.platform, allowMessagesDispatch: () => state.dispatch.value, clients }))!
    return { ...state, tab, stop: () => scope.stop() }
  }

  it('初始值是这个分组的默认页签：openai 是 OpenAI SDK，其余是原生客户端', () => {
    expect(setup('openai').tab.value).toBe('openai')
    expect(setup('anthropic').tab.value).toBe('claude')
    expect(setup('grok').tab.value).toBe('claude')
    expect(setup('gemini').tab.value).toBe('gemini')
    expect(setup('antigravity').tab.value).toBe('claude')
    expect(setup(null).tab.value).toBe('openai')
  })

  it('换了分组（平台变了）回到新分组的默认页签', async () => {
    const s = setup('anthropic')
    s.tab.value = 'curl'
    s.platform.value = 'gemini'
    await nextTick()
    expect(s.tab.value).toBe('gemini')
    s.platform.value = 'openai'
    await nextTick()
    expect(s.tab.value).toBe('openai')
  })

  it('Messages 调度开关变了也算换了分组；没有这个字段和 false 是一回事，不算', async () => {
    const s = setup('openai', true)
    s.tab.value = 'claude'
    s.dispatch.value = false
    await nextTick()
    expect(s.tab.value).toBe('openai')

    s.tab.value = 'curl'
    s.dispatch.value = undefined
    await nextTick()
    expect(s.tab.value).toBe('curl')
  })

  it('平台没变（同类分组之间换密钥）时，用户选的保留', async () => {
    const s = setup('anthropic')
    s.tab.value = 'curl'
    s.platform.value = 'anthropic'
    await nextTick()
    expect(s.tab.value).toBe('curl')
  })
})
