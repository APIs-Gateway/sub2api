import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAppStore } from '@/stores/app'

import LegalDocumentView from '../LegalDocumentView.vue'

const { currentLocale, getPublicSettings } = vi.hoisted(() => ({
  currentLocale: { value: 'en' },
  getPublicSettings: vi.fn()
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings
}))

vi.mock('@/i18n', async () => ({
  ...(await vi.importActual<typeof import('@/i18n')>('@/i18n')),
  getLocale: () => currentLocale.value
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({
    params: {
      documentId: 'admin-compliance'
    }
  })
}))

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string) => key
  })
}))

enableAutoUnmount(afterEach)

let pinia: ReturnType<typeof createPinia>
let injectedConfig: PropertyDescriptor | undefined

function mountDocument() {
  return mount(LegalDocumentView, {
    global: {
      plugins: [pinia],
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        Icon: true
      }
    }
  })
}

describe('LegalDocumentView locale rendering', () => {
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    injectedConfig = Object.getOwnPropertyDescriptor(window, '__APP_CONFIG__')
    delete window.__APP_CONFIG__
    currentLocale.value = 'en'
    getPublicSettings.mockReset().mockResolvedValue({
      site_name: 'Sub2API',
      site_logo: '',
      login_agreement_documents: []
    })
  })

  afterEach(() => {
    if (injectedConfig) {
      Object.defineProperty(window, '__APP_CONFIG__', injectedConfig)
    } else {
      delete window.__APP_CONFIG__
    }
  })

  it('uses the Chinese admin compliance document for zh-HK', async () => {
    currentLocale.value = 'zh-HK'

    const wrapper = mountDocument()

    await flushPromises()

    expect(wrapper.text()).toContain('adminCompliance.title')
    expect(wrapper.find('.legal-document-content').exists()).toBe(true)
    expect(wrapper.get('.legal-document-content').text()).toContain('部署与运营合规承诺')
    expect(getPublicSettings).toHaveBeenCalledTimes(1)
    expect(useAppStore(pinia).cachedPublicSettings?.site_name).toBe('Sub2API')
  })

  it('renders the configured name from the actual public settings cache', async () => {
    getPublicSettings.mockResolvedValue({
      site_name: 'Configured Portal',
      site_logo: '',
      login_agreement_documents: []
    })

    const wrapper = mountDocument()
    await flushPromises()

    expect(getPublicSettings).toHaveBeenCalledTimes(1)
    expect(useAppStore(pinia).cachedPublicSettings?.site_name).toBe('Configured Portal')
    expect(wrapper.get('header').text()).toContain('Configured Portal')
  })
})
