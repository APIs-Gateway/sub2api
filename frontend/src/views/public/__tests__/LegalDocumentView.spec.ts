import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import LegalDocumentView from '../LegalDocumentView.vue'

const { currentLocale, getPublicSettings } = vi.hoisted(() => ({
  currentLocale: { value: 'en' },
  getPublicSettings: vi.fn()
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings
}))

vi.mock('@/i18n', () => ({
  getLocale: () => currentLocale.value
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({
    params: {
      documentId: 'admin-compliance'
    }
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('LegalDocumentView locale rendering', () => {
  beforeEach(() => {
    currentLocale.value = 'en'
    getPublicSettings.mockResolvedValue({
      site_name: 'Sub2API',
      site_logo: '',
      login_agreement_documents: []
    })
  })

  it('uses the Chinese admin compliance document for zh-HK', async () => {
    currentLocale.value = 'zh-HK'

    const wrapper = mount(LegalDocumentView, {
      global: {
        stubs: {
          RouterLink: { template: '<a><slot /></a>' },
          Icon: true
        }
      }
    })

    await flushPromises()

    expect(wrapper.text()).toContain('adminCompliance.title')
    expect(wrapper.find('.legal-document-content').exists()).toBe(true)
  })
})
