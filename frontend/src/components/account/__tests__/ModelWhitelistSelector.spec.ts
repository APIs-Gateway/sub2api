import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { invalidateAuthSession } from '@/utils/authSessionVersion'
import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

const mocks = vi.hoisted(() => ({
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  syncUpstreamModels: vi.fn(),
  syncUpstreamModelsPreview: vi.fn()
}))

const authState = reactive({ authSessionVersion: 0, user: { id: 1 } })
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  accountsAPI: { syncUpstreamModels: mocks.syncUpstreamModels, syncUpstreamModelsPreview: mocks.syncUpstreamModelsPreview }
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values?: Record<string, string>) =>
      key === 'admin.accounts.modelMappingConflict'
        ? `${values?.from} → ${values?.to}`
        : key
  })
}))

const mountSelector = (props: Record<string, unknown> = {}) => mount(ModelWhitelistSelector, {
  props: { modelValue: [], platform: 'openai', ...props },
  global: { stubs: { ModelIcon: true, Icon: true } }
})

const syncButton = (wrapper: ReturnType<typeof mountSelector>) => wrapper.findAll('button')
  .find(button => button.text().includes('admin.accounts.syncUpstreamModels'))!
const deferred = () => {
  let resolve!: (value: { models: string[] }) => void
  let reject!: (error: Error) => void
  const promise = new Promise<{ models: string[] }>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

describe('ModelWhitelistSelector draft preview lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.syncUpstreamModels.mockReset()
    mocks.syncUpstreamModelsPreview.mockReset()
    authState.user = { id: 1 }
    authState.authSessionVersion = invalidateAuthSession()
  })

  it('prefers the draft endpoint over saved account credentials, including an empty saved key and proxy', async () => {
    const credentials = { account_id: 42, platform: 'openai', type: 'apikey', base_url: 'https://draft.example', api_key: '', proxy_id: 8 }
    mocks.syncUpstreamModelsPreview.mockResolvedValue({ models: ['draft-model'] })
    const wrapper = mountSelector({ accountId: 42, syncCredentials: credentials })
    await syncButton(wrapper).trigger('click')
    await flushPromises()
    expect(mocks.syncUpstreamModelsPreview).toHaveBeenCalledWith(credentials)
    expect(mocks.syncUpstreamModels).not.toHaveBeenCalled()
    expect(wrapper.emitted('update:modelValue')).toEqual([[['draft-model']]])
    wrapper.unmount()
  })

  for (const field of ['base_url', 'api_key', 'proxy_id'] as const) {
    it(`discards old ${field} results and keeps a newer request busy`, async () => {
      const old = deferred(), latest = deferred()
      mocks.syncUpstreamModelsPreview.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
      const credentials = { account_id: 42, platform: 'openai', type: 'apikey', base_url: 'https://old.example', api_key: '', proxy_id: 7 }
      const wrapper = mountSelector({ accountId: 42, syncCredentials: credentials })
      await syncButton(wrapper).trigger('click')
      expect(mocks.syncUpstreamModelsPreview).toHaveBeenCalledTimes(1)
      await wrapper.setProps({ syncCredentials: { ...credentials, [field]: field === 'proxy_id' ? 8 : 'new-value' } })
      await syncButton(wrapper).trigger('click')
      old.resolve({ models: ['stale'] })
      await flushPromises()
      expect(wrapper.emitted('update:modelValue')).toBeUndefined()
      expect(mocks.showSuccess).not.toHaveBeenCalled()
      expect(syncButton(wrapper).attributes('disabled')).toBeDefined()
      latest.resolve({ models: ['current'] })
      await flushPromises()
      expect(wrapper.emitted('update:modelValue')).toEqual([[['current']]])
      wrapper.unmount()
    })
  }

  for (const change of ['account', 'close/reopen', 'unmount', 'login session'] as const) {
    for (const result of ['success', 'error'] as const) {
      it(`ignores ${result} after ${change}`, async () => {
        const pending = deferred(), latest = deferred()
        mocks.syncUpstreamModels.mockReturnValueOnce(pending.promise).mockReturnValueOnce(latest.promise)
        const wrapper = mountSelector({ accountId: 42, active: true, syncContext: 1 })
        await syncButton(wrapper).trigger('click')
        expect(mocks.syncUpstreamModels).toHaveBeenCalledTimes(1)
        if (change === 'account') await wrapper.setProps({ accountId: 43 })
        if (change === 'close/reopen') {
          await wrapper.setProps({ active: false, syncContext: 2 })
          await wrapper.setProps({ active: true, syncContext: 3 })
        }
        if (change === 'unmount') wrapper.unmount()
        if (change === 'login session') authState.authSessionVersion = invalidateAuthSession()
        if (change !== 'unmount') { await wrapper.vm.$nextTick(); await syncButton(wrapper).trigger('click') }
        if (change !== 'unmount') expect(mocks.syncUpstreamModels).toHaveBeenCalledTimes(2)
        if (result === 'success') pending.resolve({ models: ['stale'] })
        else pending.reject(new Error('stale failure'))
        await flushPromises()
        expect(wrapper.emitted('update:modelValue')).toBeUndefined()
        expect(mocks.showError).not.toHaveBeenCalled()
        expect(mocks.showSuccess).not.toHaveBeenCalled()
        if (change !== 'unmount') {
          expect(mocks.syncUpstreamModels).toHaveBeenCalledTimes(2)
          expect(syncButton(wrapper).attributes('disabled')).toBeDefined()
          latest.resolve({ models: ['current'] })
          await flushPromises()
          expect(wrapper.emitted('update:modelValue')).toEqual([[['current']]])
          wrapper.unmount()
        }
      })
    }
  }

  it('uses current mappings and selections when the connection draft has not changed', async () => {
    const pending = deferred()
    mocks.syncUpstreamModelsPreview.mockReturnValue(pending.promise)
    const credentials = { platform: 'openai', type: 'apikey', api_key: 'draft' }
    const wrapper = mountSelector({ syncCredentials: credentials })
    await syncButton(wrapper).trigger('click')
    await wrapper.setProps({ modelValue: ['edited'], modelMappings: [{ from: 'blocked', to: 'other' }], syncCredentials: { ...credentials } })
    pending.resolve({ models: ['blocked', 'new'] })
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([[['edited', 'new']]])
    expect(mocks.showInfo).toHaveBeenCalledWith('blocked → other')
    wrapper.unmount()
  })

  it('reports a current preview failure and permits retry without changing the selection', async () => {
    mocks.syncUpstreamModelsPreview.mockRejectedValue(new Error('safe upstream error'))
    const wrapper = mountSelector({ syncCredentials: { platform: 'openai', type: 'apikey', api_key: '' } })
    await syncButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(mocks.showError).toHaveBeenCalled()
    expect(syncButton(wrapper).attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})

const openaiModel = 'gpt-6'

describe('ModelWhitelistSelector mapping conflicts', () => {
  beforeEach(() => { vi.clearAllMocks(); mocks.syncUpstreamModels.mockReset(); mocks.syncUpstreamModelsPreview.mockReset() })

  it('rejects a conflicting custom model and explains the mapping target', async () => {
    const wrapper = mountSelector({ modelMappings: [{ from: 'gpt-latest', to: 'deepseek-chat' }] })
    await wrapper.get('input[placeholder="admin.accounts.enterCustomModelName"]').setValue(' gpt-latest ')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addModel')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(mocks.showInfo).toHaveBeenCalledWith('gpt-latest → deepseek-chat')
  })

  it('allows identity mappings and models without a mapping', async () => {
    const wrapper = mountSelector({ modelMappings: [{ from: 'gpt-latest', to: 'gpt-latest' }] })
    await wrapper.get('input[placeholder="admin.accounts.enterCustomModelName"]').setValue('gpt-latest')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addModel')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-latest']]])
    expect(mocks.showInfo).not.toHaveBeenCalled()
  })

  it('blocks a conflicting dropdown selection', async () => {
    const model = openaiModel
    const wrapper = mountSelector({ modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.get('div.cursor-pointer').trigger('click')
    await wrapper.findAll('button').find(button => button.text().includes(model))!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })

  it('skips conflicting related models while retaining other models', async () => {
    const model = openaiModel
    const wrapper = mountSelector({ modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.fillRelatedModels')!.trigger('click')
    const emitted = wrapper.emitted('update:modelValue')?.[0]?.[0] as string[]
    expect(emitted).not.toContain(model)
    expect(emitted.length).toBeGreaterThan(0)
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })

  it('skips conflicting upstream models and keeps nonconflicting sync results', async () => {
    const model = openaiModel
    mocks.syncUpstreamModels.mockResolvedValue({ models: [model, 'unmapped-model'] })
    const wrapper = mountSelector({ accountId: 42, modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.syncUpstreamModels')!.trigger('click')
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([[['unmapped-model']]])
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })
})
