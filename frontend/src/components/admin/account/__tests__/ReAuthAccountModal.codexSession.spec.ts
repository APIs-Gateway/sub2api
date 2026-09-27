import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, beforeEach, vi } from 'vitest'
import type { Account } from '@/types'
import ReAuthAccountModal from '../ReAuthAccountModal.vue'

const mocks = vi.hoisted(() => {
  const oauth = () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    loading: { value: false },
    error: { value: '' },
    oauthState: { value: '' },
    resetState: vi.fn()
  })
  return {
    oauth,
    reauth: vi.fn(),
    showWarning: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn()
  }
})

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showWarning: mocks.showWarning,
    showSuccess: mocks.showSuccess,
    showError: mocks.showError
  })
}))
vi.mock('@/api/admin', () => ({
  adminAPI: { accounts: { reauthCodexSession: mocks.reauth } }
}))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: { value: false }, copyToClipboard: vi.fn() })
}))
vi.mock('@/composables/useAccountOAuth', () => ({ useAccountOAuth: mocks.oauth }))
vi.mock('@/composables/useOpenAIOAuth', () => ({ useOpenAIOAuth: mocks.oauth }))
vi.mock('@/composables/useGeminiOAuth', () => ({ useGeminiOAuth: mocks.oauth }))
vi.mock('@/composables/useAntigravityOAuth', () => ({ useAntigravityOAuth: mocks.oauth }))
vi.mock('@/composables/useGrokOAuth', () => ({ useGrokOAuth: mocks.oauth }))

const account = (authMode = 'chatgpt') => ({
  id: 17,
  name: 'existing',
  platform: 'openai',
  type: 'oauth',
  proxy_id: 9,
  concurrency: 11,
  priority: 37,
  credentials: { auth_mode: authMode }
}) as unknown as Account

function mountModal(selectedAccount = account()) {
  return mount(ReAuthAccountModal, {
    props: { show: true, account: selectedAccount },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true
      }
    }
  })
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('OpenAI Codex auth.json reauthorization', () => {
  it('updates only the selected account and reports success and warnings', async () => {
    const updated = account()
    mocks.reauth.mockResolvedValue({ account: updated, warnings: ['old refresh token not verified'] })
    const wrapper = mountModal()

    expect(wrapper.find('input[value="codex_session"]').exists()).toBe(true)
    await wrapper.get('input[value="codex_session"]').setValue()
    expect(wrapper.text()).toContain('admin.accounts.oauth.openai.codexSessionReauthDesc')
    expect(wrapper.text()).toContain('admin.accounts.oauth.openai.codexSessionReauthSubmit')
    expect(wrapper.findAll('button').some((button) => button.text().includes('admin.accounts.oauth.completeAuth'))).toBe(false)

    await wrapper.get('textarea').setValue('  {"tokens":{"access_token":"at"}}  ')
    const submit = wrapper.findAll('button').find((button) => button.text().includes('codexSessionReauthSubmit'))
    expect(submit).toBeTruthy()
    await submit!.trigger('click')
    await flushPromises()

    expect(mocks.reauth).toHaveBeenCalledTimes(1)
    expect(mocks.reauth).toHaveBeenCalledWith(17, '{"tokens":{"access_token":"at"}}')
    expect(mocks.showWarning).toHaveBeenCalledWith('old refresh token not verified')
    expect(mocks.showSuccess).toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')).toEqual([[updated]])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('keeps the dialog open and shows a server identity rejection', async () => {
    mocks.reauth.mockRejectedValue({ response: { data: { detail: 'identity mismatch' } } })
    const wrapper = mountModal()
    await wrapper.get('input[value="codex_session"]').setValue()
    await wrapper.get('textarea').setValue('bad token')
    const submit = wrapper.findAll('button').find((button) => button.text().includes('codexSessionReauthSubmit'))
    await submit!.trigger('click')
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('identity mismatch')
    expect(wrapper.emitted('reauthorized')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('does not offer OAuth session import for Agent Identity', () => {
    const wrapper = mountModal(account('agentIdentity'))
    expect(wrapper.find('input[value="codex_session"]').exists()).toBe(false)
  })
})
