import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'
import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import { adminAPI } from '@/api/admin'
import { parseDateTimeLocalInput } from '@/utils/format'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      bulkUpdate: vi.fn(),
      checkMixedChannelRisk: vi.fn()
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountModal(extraProps: Record<string, unknown> = {}) {
  return mount(BulkEditAccountModal, {
    props: {
      show: true,
      accountIds: [1, 2],
      selectedPlatforms: ['antigravity'],
      selectedTypes: ['apikey'],
      proxies: [],
      groups: [],
      ...extraProps
    } as any,
    global: {
      plugins: [createPinia()],
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        Select: {
          props: ['modelValue', 'options'],
          emits: ['update:modelValue'],
          template: `
            <select
              v-bind="$attrs"
              :value="modelValue"
              @change="$emit('update:modelValue', $event.target.value)"
            >
              <option v-for="option in options" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
          `
        },
        ProxySelector: true,
        GroupSelector: true,
        Icon: true
      }
    }
  })
}

describe('BulkEditAccountModal', () => {
  beforeEach(() => {
    vi.mocked(adminAPI.accounts.bulkUpdate).mockReset()
    vi.mocked(adminAPI.accounts.checkMixedChannelRisk).mockReset()

    vi.mocked(adminAPI.accounts.bulkUpdate).mockResolvedValue({
      success: 2,
      failed: 0,
      results: []
    } as any)
    vi.mocked(adminAPI.accounts.checkMixedChannelRisk).mockResolvedValue({
      has_risk: false
    } as any)
  })

  for (const mode of ['whitelist', 'mapping'] as const) {
    it(`retains the deleted rewrite source only in the selected ${mode} save mode`, async () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey'] })
      await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
      await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelMapping')!.trigger('click')
      await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addMapping')!.trigger('click')
      await wrapper.get('input[placeholder="admin.accounts.requestModel"]').setValue('retained-alias')
      await wrapper.get('input[placeholder="admin.accounts.actualModel"]').setValue('gpt-6')
      const row = wrapper.get('input[placeholder="admin.accounts.requestModel"]').element.parentElement!
      await wrapper.findAll('button').find(button => button.element.parentElement === row)!.trigger('click')
      await flushPromises()
      expect(wrapper.find('input[placeholder="admin.accounts.requestModel"]').exists()).toBe(false)
      if (mode === 'whitelist') {
        await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelWhitelist')!.trigger('click')
        expect(wrapper.getComponent(ModelWhitelistSelector).props('modelValue')).toEqual(['retained-alias'])
      }
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: { model_mapping: mode === 'whitelist' ? { 'retained-alias': 'retained-alias' } : {} }
      })
      wrapper.unmount()
    })
  }

  it('antigravity 白名单包含 Gemini 图片模型且过滤掉普通 GPT 模型', async () => {
    const wrapper = mountModal()
    const selector = wrapper.findComponent(ModelWhitelistSelector)
    expect(selector.exists()).toBe(true)

    await selector.find('div.cursor-pointer').trigger('click')

    expect(wrapper.text()).toContain('gemini-3.1-flash-image')
    expect(wrapper.text()).toContain('gemini-2.5-flash-image')
    expect(wrapper.text()).not.toContain('gpt-5.3-codex')
  })

  it('antigravity 映射预设包含图片映射并过滤 OpenAI 预设', async () => {
    const wrapper = mountModal()

    const mappingTab = wrapper.findAll('button').find((btn) => btn.text().includes('admin.accounts.modelMapping'))
    expect(mappingTab).toBeTruthy()
    await mappingTab!.trigger('click')

    expect(wrapper.text()).toContain('3.1-Flash-Image透传')
    expect(wrapper.text()).toContain('3-Pro-Image→3.1')
    expect(wrapper.text()).not.toContain('GPT-5.3 Codex Spark')
  })

  it('仅勾选模型限制且白名单留空时，应提交空 model_mapping 以支持所有模型', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['anthropic'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      credentials: {
        model_mapping: {}
      }
    })
  })

  it('keeps whitelist save independent of mapping rows left in the other mode', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
    const mappingTab = wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelMapping')
    await mappingTab!.trigger('click')
    const addMapping = wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addMapping')
    await addMapping!.trigger('click')
    await wrapper.get('input[placeholder="admin.accounts.requestModel"]').setValue('gpt-6')
    await wrapper.get('input[placeholder="admin.accounts.actualModel"]').setValue('different-target')

    const whitelistTab = wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelWhitelist')
    await whitelistTab!.trigger('click')
    const selector = wrapper.getComponent(ModelWhitelistSelector)
    expect(selector.props('modelMappings')).toBeUndefined()
    await selector.get('input[placeholder="admin.accounts.enterCustomModelName"]').setValue('gpt-6')
    const addModel = selector.findAll('button').find(button => button.text() === 'admin.accounts.addModel')
    await addModel!.trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      credentials: { model_mapping: { 'gpt-6': 'gpt-6' } }
    })
  })

  it('OpenAI 账号批量编辑可开启自动透传', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-passthrough-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: true
      }
    })
  })

  it('OpenAI OAuth 批量编辑可开启 namespace 摊平兼容开关', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-flatten-namespaces-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-flatten-namespaces-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_responses_flatten_namespaces: true
      }
    })
  })

  it('namespace 摊平开关不对 API Key 等非 OAuth 选择展示', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth', 'apikey']
    })

    expect(wrapper.find('#bulk-edit-openai-flatten-namespaces-enabled').exists()).toBe(false)
  })

  it('OpenAI OAuth 批量编辑应提交 OAuth 专属 WS mode 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-ws-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-ws-mode-select"]').setValue('passthrough')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_oauth_responses_websockets_v2_mode: 'passthrough',
        openai_oauth_responses_websockets_v2_enabled: true
      }
    })
  })

  it('OpenAI API Key 批量编辑不显示 WS mode 入口', () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    expect(wrapper.find('#bulk-edit-openai-ws-mode-enabled').exists()).toBe(false)
  })

  it('OpenAI API Key 批量编辑可设置上游倍率自动探测开关', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-upstream-billing-auto-probe-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-upstream-billing-auto-probe-select"]').setValue('disabled')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      upstream_billing_probe_enabled: false
    })
  })

  it('OpenAI OAuth 批量编辑应提交 codex_cli_only 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-codex-cli-only-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-codex-cli-only-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_cli_only: true
      }
    })
  })

  it('OpenAI OAuth 批量编辑应提交 codex_cli_only_allowed_clients 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-codex-allow-claude-code-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-codex-allow-claude-code-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_cli_only_allowed_clients: ['claude_code']
      }
    })
  })

  it('OpenAI API Key 批量编辑应提交 API Key 专属 WS mode 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-openai-apikey-ws-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-apikey-ws-mode-select"]').setValue('ctx_pool')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_apikey_responses_websockets_v2_mode: 'ctx_pool',
        openai_apikey_responses_websockets_v2_enabled: true
      }
    })
  })

  it('筛选 OpenAI 账号批量编辑应提交 Compact 模式和专属模型映射', async () => {
    const wrapper = mountModal({
      accountIds: [],
      selectedPlatforms: [],
      selectedTypes: [],
      target: {
        mode: 'filtered',
        filters: { platform: 'openai' },
        previewCount: 12,
        selectedPlatforms: ['openai'],
        selectedTypes: ['oauth', 'apikey']
      }
    })

    await wrapper.get('#bulk-edit-openai-compact-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-compact-mode-select"]').setValue('force_on')
    await wrapper.get('#bulk-edit-openai-compact-model-mapping-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-compact-model-mapping-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-testid="bulk-edit-openai-compact-model-mapping-input"]')
    await inputs[0].setValue('gpt-5.4')
    await inputs[1].setValue('gpt-5.4-openai-compact')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith({
      filters: { platform: 'openai' },
      extra: {
        openai_compact_mode: 'force_on'
      },
      credentials: {
        compact_model_mapping: {
          'gpt-5.4': 'gpt-5.4-openai-compact'
        }
      }
    })
  })

  it('OpenAI 账号批量编辑可关闭自动透传', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: false,
        openai_oauth_passthrough: false
      }
    })
  })

  it('开启 OpenAI 自动透传时不再同时提交模型限制', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-passthrough-toggle').trigger('click')
    await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: true
      }
    })
    expect(wrapper.text()).toContain('admin.accounts.openai.modelRestrictionDisabledByPassthrough')
  })

  it('filtered-results 模式下应提交 filters 而不是 account_ids', async () => {
    const wrapper = mountModal({
      accountIds: [],
      target: {
        mode: 'filtered',
        filters: {
          platform: 'openai',
          type: 'oauth',
          status: 'active',
          group: '12',
          search: 'bulk-target',
          privacy_mode: 'training_set_cf_blocked'
        },
        previewCount: 5,
        selectedPlatforms: ['openai'],
        selectedTypes: ['oauth']
      }
    })

    await wrapper.get('#bulk-edit-status-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith({
      filters: {
        platform: 'openai',
        type: 'oauth',
        status: 'active',
        group: '12',
        search: 'bulk-target',
        privacy_mode: 'training_set_cf_blocked'
      },
      status: 'active'
    })
  })

  describe('分组修改方式', () => {
    async function enableGroupsWith(wrapper: ReturnType<typeof mountModal>, ids: number[]) {
      await wrapper.get('#bulk-edit-groups-enabled').setValue(true)
      wrapper.findComponent(GroupSelector).vm.$emit('update:modelValue', ids)
      await flushPromises()
    }

    async function submit(wrapper: ReturnType<typeof mountModal>) {
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()
    }

    it('默认选中追加，并显式发送 group_mode=append', async () => {
      const wrapper = mountModal()

      expect(wrapper.get('[data-testid="bulk-edit-group-mode-append"]').attributes('aria-checked')).toBe('true')
      expect(wrapper.get('[data-testid="bulk-edit-group-mode-replace"]').attributes('aria-checked')).toBe('false')
      expect(wrapper.find('[data-testid="bulk-edit-group-replace-warning"]').exists()).toBe(false)

      await enableGroupsWith(wrapper, [5, 6])
      await submit(wrapper)

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        group_ids: [5, 6],
        group_mode: 'append'
      })
    })

    it('切换到移除后发送 group_mode=remove', async () => {
      const wrapper = mountModal()
      await enableGroupsWith(wrapper, [5])
      await wrapper.get('[data-testid="bulk-edit-group-mode-remove"]').trigger('click')
      await submit(wrapper)

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        group_ids: [5],
        group_mode: 'remove'
      })
    })

    it('替换模式显示醒目提示，并发送 group_mode=replace', async () => {
      const wrapper = mountModal()
      await enableGroupsWith(wrapper, [5])
      await wrapper.get('[data-testid="bulk-edit-group-mode-replace"]').trigger('click')

      const warning = wrapper.get('[data-testid="bulk-edit-group-replace-warning"]')
      expect(warning.text()).toContain('admin.accounts.bulkEdit.groupModeReplaceWarning')

      await submit(wrapper)

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        group_ids: [5],
        group_mode: 'replace'
      })
    })

    it('替换且未选分组时提示会清空全部分组，仍允许提交', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-groups-enabled').setValue(true)
      await wrapper.get('[data-testid="bulk-edit-group-mode-replace"]').trigger('click')

      expect(wrapper.get('[data-testid="bulk-edit-group-replace-warning"]').text()).toContain(
        'admin.accounts.bulkEdit.groupModeReplaceEmptyWarning'
      )

      await submit(wrapper)

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        group_ids: [],
        group_mode: 'replace'
      })
    })

    it('追加或移除时未选分组不提交', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-groups-enabled').setValue(true)
      await submit(wrapper)
      expect(adminAPI.accounts.bulkUpdate).not.toHaveBeenCalled()

      await wrapper.get('[data-testid="bulk-edit-group-mode-remove"]').trigger('click')
      await submit(wrapper)
      expect(adminAPI.accounts.bulkUpdate).not.toHaveBeenCalled()
    })

    it('没勾选分组时不发送 group_ids 和 group_mode', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-status-enabled').setValue(true)
      await submit(wrapper)

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], { status: 'active' })
    })

    it('关闭弹窗后分组方式恢复为追加', async () => {
      const wrapper = mountModal()
      await wrapper.get('[data-testid="bulk-edit-group-mode-replace"]').trigger('click')
      expect(wrapper.get('[data-testid="bulk-edit-group-mode-replace"]').attributes('aria-checked')).toBe('true')

      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })

      expect(wrapper.get('[data-testid="bulk-edit-group-mode-append"]').attributes('aria-checked')).toBe('true')
    })

    it('移除分组不做混合渠道预检，追加分组才做', async () => {
      const wrapper = mountModal({
        selectedPlatforms: ['anthropic'],
        selectedTypes: ['apikey']
      })
      await enableGroupsWith(wrapper, [5])

      await wrapper.get('[data-testid="bulk-edit-group-mode-remove"]').trigger('click')
      await submit(wrapper)
      expect(adminAPI.accounts.checkMixedChannelRisk).not.toHaveBeenCalled()
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)

      await wrapper.get('[data-testid="bulk-edit-group-mode-append"]').trigger('click')
      await submit(wrapper)
      expect(adminAPI.accounts.checkMixedChannelRisk).toHaveBeenCalledTimes(1)
    })
  })

  describe('池模式', () => {
    it('目标账号含 OAuth 账号时不显示池模式', () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey', 'oauth'] })
      expect(wrapper.find('#bulk-edit-pool-mode-enabled').exists()).toBe(false)
    })

    it('勾选但保持关闭时只发送 pool_mode=false', async () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey'] })
      await wrapper.get('#bulk-edit-pool-mode-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: { pool_mode: false }
      })
    })

    it('开启后发送重试次数和状态码', async () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey', 'bedrock'] })
      await wrapper.get('#bulk-edit-pool-mode-enabled').setValue(true)
      await wrapper.get('#bulk-edit-pool-mode-toggle').trigger('click')
      await wrapper.get('#bulk-edit-pool-mode-retry-count').setValue('5')
      await wrapper.get('#bulk-edit-pool-mode-retry-status-codes').setValue('429, 401, 999, 401')
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: {
          pool_mode: true,
          pool_mode_retry_count: 5,
          pool_mode_retry_status_codes: [401, 429]
        }
      })
    })

    it('状态码留空时不发送，保持各账号原有设置', async () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey'] })
      await wrapper.get('#bulk-edit-pool-mode-enabled').setValue(true)
      await wrapper.get('#bulk-edit-pool-mode-toggle').trigger('click')
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: { pool_mode: true, pool_mode_retry_count: 3 }
      })
    })

    it('没勾选「是否修改」时不发送池模式', async () => {
      const wrapper = mountModal({ selectedPlatforms: ['openai'], selectedTypes: ['apikey'] })
      await wrapper.get('#bulk-edit-pool-mode-toggle').trigger('click')
      await wrapper.get('#bulk-edit-status-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], { status: 'active' })
    })
  })

  describe('临时不可调度', () => {
    it('勾选但保持关闭时只发送 temp_unschedulable_enabled=false', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-temp-unsched-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: { temp_unschedulable_enabled: false }
      })
    })

    it('开启但没有可用规则时不提交', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-temp-unsched-enabled').setValue(true)
      await wrapper.get('#bulk-edit-temp-unsched-toggle').trigger('click')
      await wrapper.get('[data-testid="temp-unsched-add-rule"]').trigger('click')
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).not.toHaveBeenCalled()
    })

    it('开启并添加预设规则后发送规则', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-temp-unsched-enabled').setValue(true)
      await wrapper.get('#bulk-edit-temp-unsched-toggle').trigger('click')
      await wrapper.get('[data-testid="temp-unsched-preset-1"]').trigger('click')
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        credentials: {
          temp_unschedulable_enabled: true,
          temp_unschedulable_rules: [
            {
              error_code: 429,
              keywords: ['rate limit', 'too many requests'],
              duration_minutes: 10,
              description: 'admin.accounts.tempUnschedulable.presets.rateLimitDesc'
            }
          ]
        }
      })
    })
  })

  describe('过期时间', () => {
    it('勾选后留空表示清除过期时间', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-expires-at-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], { expires_at: 0 })
    })

    it('填写时间后发送秒级时间戳', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-expires-at-enabled').setValue(true)
      await wrapper.get('#bulk-edit-expires-at').setValue('2030-01-02T03:04')
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
        expires_at: parseDateTimeLocalInput('2030-01-02T03:04')
      })
    })

    it('过期后自动暂停需单独勾选才发送', async () => {
      const wrapper = mountModal()
      await wrapper.get('#bulk-edit-auto-pause-toggle').trigger('click')
      await wrapper.get('#bulk-edit-status-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenLastCalledWith([1, 2], { status: 'active' })

      await wrapper.get('#bulk-edit-auto-pause-enabled').setValue(true)
      await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
      await flushPromises()
      expect(adminAPI.accounts.bulkUpdate).toHaveBeenLastCalledWith([1, 2], {
        status: 'active',
        auto_pause_on_expired: true
      })
    })
  })
})
