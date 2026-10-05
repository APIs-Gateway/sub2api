import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { invalidateAuthSession } from '@/utils/authSessionVersion'
import CreateAccountModal from '../CreateAccountModal.vue'

const mocks = vi.hoisted(() => ({ preview: vi.fn(), create: vi.fn(), showInfo: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
const auth = reactive({ isSimpleMode: true, authSessionVersion: 0, user: { id: 1 } })
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: { syncUpstreamModelsPreview: mocks.preview },
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue({})
}))
vi.mock('@/api/admin', () => ({ adminAPI: {
  accounts: { create: mocks.create },
  settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({}), getSettings: vi.fn().mockResolvedValue({}) },
  tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
} }))

const BaseDialog = defineComponent({ props: { show: Boolean }, template: '<div v-if="show"><slot /><slot name="footer" /></div>' })
const ProxySelector = defineComponent({
  name: 'ProxySelector', props: ['modelValue'], emits: ['update:modelValue'],
  template: '<select data-testid="proxy" :value="modelValue ?? 0" @change="$emit(\'update:modelValue\', Number($event.target.value) || null)"><option value="0">direct</option><option value="8">proxy</option></select>'
})
const mountCreate = () => mount(CreateAccountModal, { props: { show: true, proxies: [], groups: [] }, global: { stubs: {
    BaseDialog, ProxySelector, Select: true, Toggle: true, Icon: true, PlatformIcon: true, ModelIcon: true,
    GroupSelector: true, QuotaLimitCard: true, OAuthAuthorizationFlow: true, ConfirmDialog: true, ProxyAdBanner: true
  } } })
const openCreate = async (platform = 'anthropic') => {
  const wrapper = mountCreate()
  if (platform !== 'anthropic') await wrapper.get('[data-tour="account-form-platform"]').findAll('button')
    .find(button => button.text().toLowerCase() === platform)!.trigger('click')
  await wrapper.get('[data-tour="account-form-type"]').findAll('button')
    .find(button => button.text().includes('admin.accounts.apiKey') || button.text().includes('accountType.apiKeyTitle') || button.text().includes('API Key'))!.trigger('click')
  await wrapper.get('input[type="password"]').setValue('draft-key')
  const whitelist = wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelWhitelist')
  if (whitelist) await whitelist.trigger('click')
  return wrapper
}
const syncButton = (wrapper: Awaited<ReturnType<typeof openCreate>>) => wrapper.findAll('button')
  .find(button => button.text().includes('admin.accounts.syncUpstreamModels'))!
const deferred = () => {
  let resolve!: (value: { models: string[] }) => void
  let reject!: (error: Error) => void
  const promise = new Promise<{ models: string[] }>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

describe('CreateAccountModal real model preview', () => {
  beforeEach(() => { vi.clearAllMocks(); mocks.preview.mockReset(); auth.authSessionVersion = invalidateAuthSession() })

  it('keeps Create mapping saves independent and retains its existing default whitelist reset after deletion', async () => {
    mocks.create.mockResolvedValue({ id: 99 })
    const wrapper = await openCreate('openai')
    await wrapper.get('input[data-tour="account-form-name"]').setValue('mapping account')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelMapping')!.trigger('click')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addMapping')!.trigger('click')
    await wrapper.get('input[placeholder="admin.accounts.requestModel"]').setValue('gpt-6')
    await wrapper.get('input[placeholder="admin.accounts.actualModel"]').setValue('different-target')
    const row = wrapper.get('input[placeholder="admin.accounts.requestModel"]').element.parentElement!
    await wrapper.findAll('button').find(button => button.element.parentElement === row)!.trigger('click')
    await flushPromises()
    await wrapper.get('#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledTimes(1)
    expect(mocks.create.mock.calls[0]?.[0]?.credentials).not.toHaveProperty('model_mapping')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.modelWhitelist')!.trigger('click')
    await wrapper.get('#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledTimes(2)
    expect(mocks.create.mock.calls[1]?.[0]?.credentials?.model_mapping).toMatchObject({ 'gpt-6': 'gpt-6' })
    expect(mocks.showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  for (const platform of ['anthropic', 'openai', 'gemini']) {
    it(`sends the selected and cleared draft proxy for ${platform} without saving`, async () => {
      mocks.preview.mockResolvedValue({ models: ['draft-model'] })
      const wrapper = await openCreate(platform)
      await wrapper.get('[data-testid="proxy"]').setValue('8')
      await syncButton(wrapper).trigger('click')
      await flushPromises()
      expect(mocks.preview).toHaveBeenLastCalledWith(expect.objectContaining({ platform, type: 'apikey', api_key: 'draft-key', proxy_id: 8 }))
      await wrapper.get('[data-testid="proxy"]').setValue('0')
      await syncButton(wrapper).trigger('click')
      await flushPromises()
      expect(mocks.preview).toHaveBeenLastCalledWith(expect.objectContaining({ proxy_id: 0 }))
      expect(mocks.create).not.toHaveBeenCalled()
      wrapper.unmount()
    })
  }

  it('keeps Grok creation on OAuth without a draft API-key preview', async () => {
    const wrapper = mountCreate()
    await wrapper.get('[data-tour="account-form-platform"]').findAll('button').find(button => button.text() === 'Grok')!.trigger('click')
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    expect(syncButton(wrapper)).toBeUndefined()
    expect(mocks.preview).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  for (const result of ['success', 'error'] as const) {
    it(`ignores old ${result} after clearing the proxy and keeps the new preview busy`, async () => {
      const old = deferred(), latest = deferred()
      mocks.preview.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
      const wrapper = await openCreate()
      await wrapper.get('[data-testid="proxy"]').setValue('8')
      await syncButton(wrapper).trigger('click')
      expect(mocks.preview).toHaveBeenCalledTimes(1)
      await wrapper.get('[data-testid="proxy"]').setValue('0')
      await syncButton(wrapper).trigger('click')
      expect(mocks.preview).toHaveBeenCalledTimes(2)
      if (result === 'success') old.resolve({ models: ['stale-create'] })
      else old.reject(new Error('stale failure'))
      await flushPromises()
      expect(wrapper.text()).not.toContain('stale-create')
      expect(mocks.showError).not.toHaveBeenCalled()
      expect(mocks.showSuccess).not.toHaveBeenCalled()
      expect(syncButton(wrapper).attributes('disabled')).toBeDefined()
      latest.resolve({ models: ['current-create'] })
      await flushPromises()
      expect(wrapper.text()).toContain('current-create')
      expect(mocks.create).not.toHaveBeenCalled()
      wrapper.unmount()
    })
  }
})
