/**
 * 四个页签组件单独挂载时的 props / emits 约定。
 * 弹窗整体的行为由 ../../__tests__/KeyOnboardingModal*.spec.ts 覆盖；这里只验证页签自己：
 * 拿到 props 就能渲染，复制只通过 copy 事件交出去，选择类状态走 v-model，面板属性落在根元素上。
 */
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import AiTab from '../AiTab.vue'
import CcSwitchTab from '../CcSwitchTab.vue'
import InstallTab from '../InstallTab.vue'
import { CodeBlock } from '../CodeBlock'
import ManualTab from '../ManualTab.vue'
import type { EndpointOption } from '@/utils/apiEndpoints'
import type { CcSwitchForm } from '../useCcSwitchState'

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

const KEY = 'sk-SECRET-1234567890abcdef'
// 配置的地址带 /v1：base（API 根地址）和 configured（原样）不同，用来区分页签取了哪个
const endpoint: EndpointOption = {
  id: 'default',
  base: 'https://api.example.com',
  v1: 'https://api.example.com/v1',
  configured: 'https://api.example.com/v1',
  name: '',
  description: '',
  isDefault: true
}
const panelAttrs = { role: 'tabpanel', id: 'panel-x', 'aria-labelledby': 'tab-x' }

describe('InstallTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(InstallTab, {
      props: { endpoint, fullKey: KEY, platform: 'anthropic', siteName: 'Hiyo', clients: ['claude', 'opencode'], copiedId: '', ...props } as never,
      attrs: panelAttrs
    })

  it('面板属性落在根元素上；每个客户端一张卡片', () => {
    const w = mountTab()
    expect(w.attributes('role')).toBe('tabpanel')
    expect(w.attributes('id')).toBe('panel-x')
    expect(w.attributes('aria-labelledby')).toBe('tab-x')
    expect(w.attributes('data-test')).toBe('panel-install')
    expect(w.findAll('section.onb-card').map((s) => s.attributes('data-test'))).toEqual(['client-claude', 'client-opencode'])
  })

  it('点瓦片：通过 copy 交出脚本和瓦片 id，不自己写剪贴板', async () => {
    const write = vi.fn()
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true })
    const w = mountTab()
    await w.get('[data-test="copy-claude-unix"]').trigger('click')
    const [text, id] = w.emitted('copy')![0] as [string, string]
    expect(id).toBe('claude-unix')
    expect(text).toContain(KEY)
    expect(text).toContain("SUB_ENDPOINT='https://api.example.com'")
    expect(write).not.toHaveBeenCalled()
  })

  it('copiedId 对应的瓦片显示勾，其他不受影响', () => {
    const w = mountTab({ copiedId: 'claude-windows' })
    expect(w.get('[data-test="copy-claude-windows"]').attributes('data-copied')).toBe('true')
    expect(w.find('[data-test="copy-claude-windows"] [data-test="icon-check"]').exists()).toBe(true)
    expect(w.get('[data-test="copy-claude-unix"]').attributes('data-copied')).toBe('false')
  })
})

describe('AiTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(AiTab, {
      props: {
        endpoint,
        platform: 'openai',
        siteName: 'Hiyo',
        models: ['gpt-5.6-sol', 'gpt-5.6-luna'],
        docUrl: 'https://docs.example.com',
        copiedId: '',
        client: 'codex',
        ...props
      } as never,
      attrs: panelAttrs
    })
  const chips = (w: ReturnType<typeof mountTab>) =>
    w.findAll('[data-test^="ai-client-"]').map((c) => c.attributes('data-test')!.replace('ai-client-', ''))
  const text = (w: ReturnType<typeof mountTab>) => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value

  it('选客户端走 v-model:client；提示词随 client 变化，不含密钥', async () => {
    const w = mountTab()
    const codex = text(w)
    expect(codex).toContain('https://api.example.com/v1')
    await w.get('[data-test="ai-client-cursor"]').trigger('click')
    expect(w.emitted('update:client')![0]).toEqual(['cursor'])
    // 外壳把新值传回来之后，提示词跟着变
    await w.setProps({ client: 'cursor' })
    expect(text(w)).toContain('Cursor')
    expect(text(w)).not.toBe(codex)
    expect(text(w)).not.toContain('sk-')
  })

  describe('工具按分组过滤', () => {
    it.each([
      ['openai', false, ['codex', 'cursor', 'chat', 'code', 'other']],
      ['openai', true, ['codex', 'claude', 'cursor', 'chat', 'code', 'other']],
      ['anthropic', false, ['claude', 'cursor', 'chat', 'code', 'other']],
      ['gemini', false, ['chat', 'code', 'other']],
      ['antigravity', false, ['claude', 'chat', 'code', 'other']]
    ] as [string, boolean, string[]][])('%s 分组（开了调度：%s）只显示 %j', (platform, allowMessagesDispatch, expected) => {
      const w = mountTab({ platform, allowMessagesDispatch, client: expected[0] })
      expect(chips(w)).toEqual(expected)
    })

    it('选中的工具不在可选范围里（旧的默认值 Claude Code）：用第一个可用的，并交回外壳', async () => {
      const w = mountTab({ client: 'claude' })
      expect(chips(w)).not.toContain('claude')
      expect(w.emitted('update:client')![0]).toEqual(['codex'])
      expect(w.get('[data-test="ai-client-codex"]').attributes('aria-checked')).toBe('true')
      expect(w.findAll('[role="radio"][aria-checked="true"]')).toHaveLength(1)
      // 提示词用的是 Codex，不是 Claude Code：地址、接口格式、要设置的内容一致
      expect(text(w)).toContain('Codex')
      expect(text(w)).toContain('https://api.example.com/v1')
      expect(text(w)).toContain('OpenAI')
      expect(text(w)).not.toContain('ANTHROPIC')
    })

    it('选中的工具可用时保持不变，不多发更新', () => {
      const w = mountTab({ client: 'cursor' })
      expect(w.emitted('update:client')).toBeUndefined()
      expect(w.get('[data-test="ai-client-cursor"]').attributes('aria-checked')).toBe('true')
    })

    it('openai 分组开了调度：Claude Code 可选（排在 Codex 后面），选中时提示词是根地址 + Anthropic', () => {
      const w = mountTab({ allowMessagesDispatch: true, client: 'claude' })
      expect(chips(w).slice(0, 2)).toEqual(['codex', 'claude'])
      expect(w.emitted('update:client')).toBeUndefined()
      expect(text(w)).toContain('Claude Code')
      expect(text(w)).toContain('The endpoint is https://api.example.com and')
      expect(text(w)).toContain('API format is Anthropic')
      expect(text(w)).toContain('ANTHROPIC_BASE_URL')
    })

    it('换了分组：选中的工具在新分组里没有，就改成第一个可用的', async () => {
      const w = mountTab({ platform: 'anthropic', client: 'cursor' })
      expect(w.emitted('update:client')).toBeUndefined()
      await w.setProps({ platform: 'gemini' })
      expect(chips(w)).toEqual(['chat', 'code', 'other'])
      expect(w.emitted('update:client')!.at(-1)).toEqual(['chat'])
      expect(w.get('[data-test="ai-client-chat"]').attributes('aria-checked')).toBe('true')
    })

    it('分组开关调度之后立刻更新可选工具', async () => {
      const w = mountTab({ allowMessagesDispatch: false })
      expect(chips(w)).not.toContain('claude')
      await w.setProps({ allowMessagesDispatch: true })
      expect(chips(w)).toEqual(['codex', 'claude', 'cursor', 'chat', 'code', 'other'])
    })
  })

  it('复制简短版和详细版通过 copy 交出，按钮 id 固定', async () => {
    const w = mountTab()
    await w.get('[data-test="ai-copy"]').trigger('click')
    await w.get('[data-test="ai-copy-detail"]').trigger('click')
    const [[short, shortId], [detail, detailId]] = w.emitted('copy') as [string, string][]
    expect([shortId, detailId]).toEqual(['ai-short', 'ai-detail'])
    expect(detail).toContain('gpt-5.6-sol, gpt-5.6-luna')
    expect(detail).toContain('https://docs.example.com')
    expect(detail.length).toBeGreaterThan(short.length)
  })

  it('clients、modelsLoading 先只是声明，不改变页面，也不会漏到根元素上', () => {
    const plain = mountTab()
    const w = mountTab({ clients: ['codex', 'opencode'], modelsLoading: true })
    for (const attr of ['clients', 'models-loading']) expect(w.attributes(attr)).toBeUndefined()
    expect(w.html()).toBe(plain.html())
  })

  it('copiedId 对应的按钮显示「已复制」', () => {
    const w = mountTab({ copiedId: 'ai-short' })
    expect(w.get('[data-test="ai-copy"]').text()).toBe('Copied')
    expect(w.get('[data-test="ai-copy-detail"]').text()).not.toBe('Copied')
  })
})

describe('CcSwitchTab', () => {
  // 表单是一个对象，用一个 v-model:form；props 里写不下的字段用第二个参数覆盖
  const mountTab = (props: Record<string, unknown> = {}, form: Partial<CcSwitchForm> = {}) =>
    mount(CcSwitchTab, {
      props: {
        endpoint,
        fullKey: KEY,
        platform: 'openai',
        siteName: 'Hiyo',
        models: ['gpt-5.6-sol', 'gpt-5.6-luna'],
        clients: ['codex'],
        idPrefix: 'onboarding-9',
        copiedId: '',
        form: { client: 'codex', name: '', model: '', ...form },
        ...props
      } as never,
      attrs: panelAttrs
    })
  const linkOf = (text: string) => new URL(text.replace(/^ccswitch:\/\//, 'http://'))

  it('只有一个客户端时不显示客户端选择；有多个时单选组用 idPrefix 关联标签', () => {
    expect(mountTab().find('[role="radiogroup"]').exists()).toBe(false)
    const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'] }, { client: 'claude' })
    expect(w.get('[role="radiogroup"]').attributes('aria-labelledby')).toBe('onboarding-9-ccs-client')
    expect(w.get('#onboarding-9-ccs-client').text()).toBe('Client')
  })

  it('客户端、名称、模型合成一个表单对象，走一个 v-model:form；每次都换成新对象，其余字段原样带上', async () => {
    const form: CcSwitchForm = { client: 'claude', name: 'Old', model: 'gpt-5.6-sol' }
    const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'] }, form)
    await w.get('[data-test="ccs-client-gemini"]').trigger('click')
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    await w.get('[data-test="ccs-model"]').setValue('gpt-5.6-luna')
    // 外壳没有接 update:form 时，页签自己记着最新的值，所以每次都是在上一次的基础上改一个字段
    expect(w.emitted('update:form')).toEqual([
      [{ client: 'gemini', name: 'Old', model: 'gpt-5.6-sol' }],
      [{ client: 'gemini', name: 'Mine', model: 'gpt-5.6-sol' }],
      [{ client: 'gemini', name: 'Mine', model: 'gpt-5.6-luna' }]
    ])
    // 不再有三个独立的 v-model
    expect(w.emitted('update:client')).toBeUndefined()
    expect(w.emitted('update:name')).toBeUndefined()
    expect(w.emitted('update:model')).toBeUndefined()
    // 传进来的对象没有被改动
    expect(form).toEqual({ client: 'claude', name: 'Old', model: 'gpt-5.6-sol' })
  })

  it('外壳把新表单传回来之后，输入框和单选跟着变', async () => {
    const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'] }, { client: 'claude' })
    await w.setProps({ form: { client: 'gemini', name: 'Mine', model: 'gpt-5.6-luna' } })
    expect((w.get('[data-test="ccs-name"]').element as HTMLInputElement).value).toBe('Mine')
    expect((w.get('[data-test="ccs-model"]').element as HTMLSelectElement).value).toBe('gpt-5.6-luna')
    expect(w.get('[data-test="ccs-client-gemini"]').attributes('aria-checked')).toBe('true')
  })

  it('modelsLoading 先只是声明，不改变页面，也不会漏到根元素上', () => {
    const w = mountTab({ modelsLoading: true })
    expect(w.attributes('models-loading')).toBeUndefined()
    expect(w.html()).toBe(mountTab({ modelsLoading: false }).html())
  })

  it('没有模型时不显示模型字段；默认名称是「站点名 - 客户端」', () => {
    const w = mountTab({ models: [] })
    expect(w.find('[data-test="ccs-model"]').exists()).toBe(false)
    expect(w.get('[data-test="ccs-name"]').attributes('placeholder')).toBe('Hiyo - Codex')
  })

  it('复制链接通过 copy 交出；Codex 用配置的地址（带 /v1），其他客户端用 API 根地址', async () => {
    const codex = mountTab({}, { name: 'Mine', model: 'gpt-5.6-luna' })
    await codex.get('[data-test="ccs-copy-link"]').trigger('click')
    const [text, id] = codex.emitted('copy')![0] as [string, string]
    expect(id).toBe('deeplink')
    const url = linkOf(text)
    expect(url.searchParams.get('app')).toBe('codex')
    expect(url.searchParams.get('name')).toBe('Mine')
    expect(url.searchParams.get('model')).toBe('gpt-5.6-luna')
    expect(url.searchParams.get('apiKey')).toBe(KEY)
    expect(url.searchParams.get('endpoint')).toBe('https://api.example.com/v1')

    const claude = mountTab({ platform: 'anthropic', clients: ['claude'] }, { client: 'claude' })
    await claude.get('[data-test="ccs-copy-link"]').trigger('click')
    expect(linkOf((claude.emitted('copy')![0] as [string])[0]).searchParams.get('endpoint')).toBe('https://api.example.com')
  })

  it('打开 CC Switch：用 window.open(链接, _self)', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const w = mountTab()
    await w.get('[data-test="ccs-open"]').trigger('click')
    expect(open).toHaveBeenCalledTimes(1)
    expect(String(open.mock.calls[0][0]).startsWith('ccswitch://')).toBe(true)
    expect(open.mock.calls[0][1]).toBe('_self')
    open.mockRestore()
  })
})

describe('ManualTab', () => {
  const cdn: EndpointOption = {
    id: 'https://cdn.example.com',
    base: 'https://cdn.example.com',
    v1: 'https://cdn.example.com/v1',
    configured: 'https://cdn.example.com',
    name: 'Fast line',
    description: '',
    isDefault: false
  }
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(ManualTab, {
      props: {
        endpoint,
        endpointOptions: [endpoint],
        endpointId: 'default',
        fullKey: KEY,
        maskedKey: 'sk-SEC…cdef',
        platform: 'anthropic',
        siteName: 'Hiyo',
        clients: ['claude', 'opencode'],
        models: [],
        docUrl: '',
        copiedId: '',
        ...props
      } as never,
      attrs: panelAttrs
    })
  const codeTabs = (w: ReturnType<typeof mountTab>) => w.findAll('[data-test^="manual-tab-"]').map((b) => b.attributes('data-test')!.replace('manual-tab-', ''))
  const shownCode = (w: ReturnType<typeof mountTab>) => w.findAll('[data-test="manual-code"] pre').map((p) => p.text())

  it('面板属性落在根元素上', () => {
    const w = mountTab()
    expect(w.attributes('role')).toBe('tabpanel')
    expect(w.attributes('id')).toBe('panel-x')
    expect(w.attributes('aria-labelledby')).toBe('tab-x')
    expect(w.attributes('data-test')).toBe('panel-manual')
  })

  describe('地址卡片', () => {
    it('只有默认线路：一张卡片，没有单选；每个地址一个复制按钮，说明是通用的一句', async () => {
      const w = mountTab()
      expect(w.findAll('[data-test^="manual-line-"]:not([data-test^="manual-line-radio"]):not([data-test="manual-line-note"])').length).toBe(1)
      expect(w.find('input[type="radio"]').exists()).toBe(false)
      expect(w.find('[role="radiogroup"][data-test="manual-lines"]').exists()).toBe(false)
      expect(w.get('[data-test="manual-line-0"]').text()).toContain('Default')
      expect(w.get('[data-test="manual-line-note"]').text()).toBe('Enter this address in your client.')
      await w.get('[data-test="copy-line-0-v1"]').trigger('click')
      await w.get('[data-test="copy-line-0-base"]').trigger('click')
      expect(w.emitted('copy')).toEqual([
        ['https://api.example.com/v1', 'm-0-v1'],
        ['https://api.example.com', 'm-0-base']
      ])
    })

    it('卡片上只列代码里用得到的地址：/v1 给 OpenAI 兼容的客户端，接入地址给 Claude Code / Gemini CLI；没有分组时两个都给', () => {
      const rows = (props: Record<string, unknown>) => {
        const w = mountTab(props)
        return [w.find('[data-test="copy-line-0-v1"]').exists(), w.find('[data-test="copy-line-0-base"]').exists()]
      }
      expect(rows({ platform: 'openai', clients: ['codex', 'opencode'] })).toEqual([true, false])
      expect(rows({ platform: 'openai', clients: ['codex', 'claude', 'opencode'] })).toEqual([true, true])
      expect(rows({ platform: 'gemini', clients: ['gemini', 'opencode'] })).toEqual([true, true])
      expect(rows({ platform: 'antigravity', clients: ['claude', 'gemini'] })).toEqual([false, true])
      expect(rows({ platform: null, clients: [] })).toEqual([true, true])
    })

    it('antigravity：卡片上的接入地址带 /antigravity，和代码里用的一致，也是复制出去的那个', async () => {
      const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'] })
      expect(w.get('[data-test="manual-line-0"]').text()).toContain('https://api.example.com/antigravity')
      expect(w.get('[data-test="manual-line-0"]').text()).not.toContain('https://api.example.com/v1')
      expect(shownCode(w).join('\n')).toContain('https://api.example.com/antigravity')
      await w.get('[data-test="copy-line-0-base"]').trigger('click')
      expect(w.emitted('copy')).toEqual([['https://api.example.com/antigravity', 'm-0-base']])
    })

    it('多条线路：每条一张卡片，是单选组；选中的写明「下方代码使用此地址」，备用线路用管理员的名称，没有说明时给通用的一句', () => {
      const w = mountTab({ endpointOptions: [endpoint, cdn] })
      expect(w.get('[data-test="manual-lines"]').attributes('role')).toBe('radiogroup')
      expect(w.get('[data-test="manual-lines"]').attributes('aria-label')).toBe('API Endpoints')
      const radios = w.findAll('input[type="radio"]')
      expect(radios.map((r) => (r.element as HTMLInputElement).checked)).toEqual([true, false])
      const cards = [w.get('[data-test="manual-line-0"]'), w.get('[data-test="manual-line-1"]')]
      expect(cards[0].text()).toContain('Default')
      expect(cards[0].text()).toContain('Used in the code below')
      expect(cards[1].text()).toContain('Fast line')
      expect(cards[1].text()).toContain('https://cdn.example.com/v1')
      expect(cards[1].text()).not.toContain('Used in the code below')
      expect(cards[1].get('[data-test="manual-line-note"]').text()).toBe('If the default address is slow, use this one instead.')
    })

    it('管理员给备用线路写了说明就原样显示', () => {
      const w = mountTab({ endpointOptions: [endpoint, { ...cdn, description: '国内访问更快' }] })
      expect(w.get('[data-test="manual-line-1"] [data-test="manual-line-note"]').text()).toBe('国内访问更快')
    })

    it('点卡片或选单选就换线路（v-model:endpointId）；点复制按钮只复制，不换线路', async () => {
      const w = mountTab({ endpointOptions: [endpoint, cdn] })
      await w.get('[data-test="copy-line-1-v1"]').trigger('click')
      expect(w.emitted('update:endpointId')).toBeUndefined()
      expect(w.emitted('copy')![0]).toEqual(['https://cdn.example.com/v1', 'm-1-v1'])
      await w.get('[data-test="manual-line-1"]').trigger('click')
      expect(w.emitted('update:endpointId')![0]).toEqual(['https://cdn.example.com'])
      await w.get('[data-test="manual-line-radio-0"]').setValue(true)
      expect(w.emitted('update:endpointId')!.at(-1)).toEqual(['default'])
    })

    it('选中哪条线路，代码就用哪条的地址；卡片仍列出全部线路', async () => {
      const w = mountTab({ platform: 'openai', clients: ['codex', 'opencode'], endpointOptions: [endpoint, cdn] })
      expect(shownCode(w)[0]).toContain('https://api.example.com/v1')
      await w.setProps({ endpoint: cdn, endpointId: cdn.id })
      expect(shownCode(w)[0]).toContain('https://cdn.example.com/v1')
      expect(shownCode(w)[0]).not.toContain('api.example.com')
      expect(w.get('[data-test="manual-line-0"]').text()).toContain('https://api.example.com/v1')
    })

    it('复制过的按钮显示「已复制」', () => {
      const w = mountTab({ copiedId: 'm-0-v1' })
      expect(w.get('[data-test="copy-line-0-v1"]').text()).toBe('Copied')
      expect(w.get('[data-test="copy-line-0-base"]').text()).toBe('Copy')
    })
  })

  describe('密钥', () => {
    it('密钥行显示掩码，复制的是完整密钥', async () => {
      const w = mountTab()
      const row = w.get('[data-test="manual-key"]')
      expect(row.text()).toContain('sk-SEC…cdef')
      expect(row.text()).not.toContain(KEY)
      await row.get('button').trigger('click')
      expect(w.emitted('copy')).toEqual([[KEY, 'm-key']])
    })

    it('拿到完整密钥：标题写「密钥已填入下方」，代码里是真密钥', () => {
      const w = mountTab()
      expect(w.get('[data-test="manual-key-state"]').text()).toBe('Your key is filled in below')
      expect(shownCode(w).join('\n')).toContain(KEY)
      expect(shownCode(w).join('\n')).not.toContain('YOUR_API_KEY')
    })

    it('拿不到完整密钥：给提示，代码里用占位符，没有密钥行', () => {
      const w = mountTab({ fullKey: '', maskedKey: '' })
      expect(w.find('[data-test="manual-key"]').exists()).toBe(false)
      expect(w.get('[data-test="manual-key-state"]').text()).toContain('YOUR_API_KEY')
      expect(shownCode(w).join('\n')).toContain('YOUR_API_KEY')
    })

    it('只有打码的密钥时，提示里带上它', () => {
      const w = mountTab({ fullKey: '', maskedKey: 'sk-SEC…cdef' })
      expect(w.get('[data-test="manual-key-state"]').text()).toContain('sk-SEC…cdef')
    })
  })

  describe('代码页签：随分组能力显示', () => {
    it.each([
      ['openai', ['codex', 'opencode'], ['openai', 'curl', 'codex']],
      ['openai 开了 Messages 调度', ['codex', 'claude', 'opencode'], ['openai', 'curl', 'codex', 'claude']],
      ['anthropic', ['claude', 'opencode'], ['openai', 'curl', 'claude']],
      ['gemini', ['gemini', 'opencode'], ['openai', 'curl', 'gemini']],
      ['antigravity', ['claude', 'gemini'], ['claude', 'gemini']],
      ['没有分组', [], []]
    ])('%s', (_name, clients, expected) => {
      const w = mountTab({ clients, platform: clients.length ? 'x' : null })
      expect(codeTabs(w)).toEqual(expected)
    })

    it('没有分组：不显示代码，提示先选分组；地址和密钥照常显示', () => {
      const w = mountTab({ platform: null, clients: [] })
      expect(w.find('[data-test="manual-code"]').exists()).toBe(false)
      expect(w.get('[data-test="manual-no-group"]').text()).toBe('Choose a group for this key before connecting it.')
      expect(w.find('[data-test="manual-key"]').exists()).toBe(true)
    })

    it('点页签换代码；选中的页签对新分组不可用时回到默认页签', async () => {
      const w = mountTab({ platform: 'openai', clients: ['codex', 'claude', 'opencode'] })
      expect(w.get('[data-test="manual-tab-openai"]').attributes('aria-checked')).toBe('true')
      await w.get('[data-test="manual-tab-claude"]').trigger('click')
      expect(w.get('[data-test="manual-tab-claude"]').attributes('aria-checked')).toBe('true')
      expect(shownCode(w)[0]).toContain('ANTHROPIC_BASE_URL')
      await w.setProps({ platform: 'openai', clients: ['codex', 'opencode'] })
      expect(w.get('[data-test="manual-tab-openai"]').attributes('aria-checked')).toBe('true')
      expect(shownCode(w)[0]).toContain('from openai import OpenAI')
    })

    describe('默认选中的页签', () => {
      const checked = (w: ReturnType<typeof mountTab>) =>
        w.findAll('[data-test^="manual-tab-"][aria-checked="true"]').map((b) => b.attributes('data-test')!.replace('manual-tab-', ''))

      it.each([
        ['openai', ['codex', 'opencode'], 'openai'],
        ['openai 开了 Messages 调度', ['codex', 'claude', 'opencode'], 'openai'],
        ['anthropic / grok', ['claude', 'opencode'], 'claude'],
        ['gemini', ['gemini', 'opencode'], 'gemini'],
        ['antigravity', ['claude', 'gemini'], 'claude']
      ] as const)('%s：默认 %s 页签，不是 OpenAI SDK（openai 分组除外）', (_name, clients, expected) => {
        const w = mountTab({ platform: 'x', clients: [...clients] })
        expect(checked(w)).toEqual([expected])
        expect(shownCode(w).length).toBeGreaterThan(0)
      })

      it('原生客户端的默认页签显示的是那个客户端的命令', () => {
        expect(shownCode(mountTab({ platform: 'anthropic', clients: ['claude', 'opencode'] }))[0]).toContain('ANTHROPIC_BASE_URL')
        expect(shownCode(mountTab({ platform: 'gemini', clients: ['gemini', 'opencode'] }))[0]).toContain('GOOGLE_GEMINI_BASE_URL')
        expect(shownCode(mountTab({ platform: 'openai', clients: ['codex', 'opencode'] }))[0]).toContain('from openai import OpenAI')
      })

      it('外壳通过 v-model:codeTab 记着选择：点页签发出 update:codeTab，传进来的值被采用', async () => {
        const w = mountTab({ platform: 'anthropic', clients: ['claude', 'opencode'], codeTab: 'curl' })
        expect(checked(w)).toEqual(['curl'])
        await w.get('[data-test="manual-tab-openai"]').trigger('click')
        expect(w.emitted('update:codeTab')).toEqual([['openai']])
      })

      it('传进来的页签对当前分组不可用时用默认页签', () => {
        expect(checked(mountTab({ platform: 'anthropic', clients: ['claude', 'opencode'], codeTab: 'gemini' }))).toEqual(['claude'])
      })
    })

    it('代码块的复制按钮走 copy，id 带页签名', async () => {
      const w = mountTab({ platform: 'openai', clients: ['codex', 'opencode'] })
      await w.get('[data-test="manual-tab-curl"]').trigger('click')
      const blocks = w.findAllComponents(CodeBlock).filter((b) => b.element.closest('[data-test="manual-code"]'))
      expect(blocks.map((b) => b.props('label'))).toEqual(['macOS / Linux', 'Windows PowerShell (curl.exe)'])
      await blocks[1].get('button').trigger('click')
      const [text, id] = w.emitted('copy')![0] as [string, string]
      expect(id).toBe('code-curl-windows')
      expect(text).toContain('curl.exe')
      expect(text).toContain(KEY)
    })

    it('示例模型取自分组：anthropic 取 sonnet 档，openai 没有默认模型时取第一个 gpt-*', async () => {
      const claude = mountTab({ platform: 'anthropic', models: ['claude-opus-5', 'claude-sonnet-4-5', 'claude-haiku-5'] })
      await claude.get('[data-test="manual-tab-openai"]').trigger('click')
      expect(shownCode(claude)[0]).toContain('"claude-sonnet-4-5"')
      const gpt = mountTab({ platform: 'openai', clients: ['codex', 'opencode'], models: ['gpt-image-2', 'gpt-5.5', 'gpt-5.6-luna'] })
      expect(shownCode(gpt)[0]).toContain('"gpt-5.5"')
    })
  })

  describe('配置文件片段', () => {
    const titles = (w: ReturnType<typeof mountTab>) => w.findAll('details > summary').map((s) => s.text())

    it('每个可用客户端一段，包括 OpenCode', () => {
      expect(titles(mountTab())).toEqual(['Claude Code', 'OpenCode'])
      expect(titles(mountTab({ platform: 'openai', clients: ['codex', 'opencode'] }))).toEqual(['Codex CLI', 'OpenCode'])
      expect(titles(mountTab({ platform: 'gemini', clients: ['gemini', 'opencode'] }))).toEqual(['Gemini CLI', 'OpenCode'])
    })

    it('代码块的复制按钮也走 copy', async () => {
      const w = mountTab()
      const block = w.findAllComponents(CodeBlock).find((b) => b.element.closest('details'))!
      await block.get('button').trigger('click')
      const [text, id] = w.emitted('copy')![0] as [string, string]
      expect(id.startsWith('claude-')).toBe(true)
      expect(text).toContain("export ANTHROPIC_BASE_URL='https://api.example.com'")
      expect(text).toContain(KEY)
    })

    it('OpenCode 片段按平台选 provider，写当前线路的地址', () => {
      const w = mountTab({ endpointOptions: [endpoint, cdn], endpoint: cdn, endpointId: cdn.id })
      const text = w.findAll('details pre').map((p) => p.text()).find((t) => t.includes('opencode.ai/config.json'))!
      expect(JSON.parse(text)).toEqual({
        $schema: 'https://opencode.ai/config.json',
        provider: { anthropic: { options: { baseURL: 'https://cdn.example.com/v1', apiKey: KEY } } }
      })
    })
  })

  it('有文档地址才显示「查看文档」链接', () => {
    expect(mountTab().find('a[target="_blank"]').exists()).toBe(false)
    const w = mountTab({ docUrl: 'https://docs.example.com' })
    expect(w.get('a[target="_blank"]').attributes('href')).toBe('https://docs.example.com')
  })

  it('保留「连不上时检查」四条，页脚提醒妥善保管密钥', () => {
    const w = mountTab()
    expect(w.findAll('ul li').length).toBe(4)
    expect(w.get('[data-test="manual-footer"]').text()).toBe('Keep your key safe. Anyone who has it can spend your balance or plan.')
  })
})
