import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AdminComplianceDialog from '../AdminComplianceDialog.vue'

const { currentLocale, complianceStore, authStore, appStore } = vi.hoisted(() => ({
  currentLocale: { value: 'en' },
  complianceStore: {
    shouldShow: true,
    expectedPhrase: '繁體中文確認短語',
    submitting: false,
    status: {
      version: 'v2026.06.10',
      document_url_zh: 'https://example.com/admin-compliance.zh.md',
      document_url_en: 'https://example.com/admin-compliance.en.md'
    },
    accept: vi.fn()
  },
  authStore: {
    isAuthenticated: true,
    isAdmin: true,
    logout: vi.fn()
  },
  appStore: {
    showSuccess: vi.fn(),
    showError: vi.fn()
  }
}))

vi.mock('@/stores', () => ({
  useAdminComplianceStore: () => complianceStore,
  useAuthStore: () => authStore,
  useAppStore: () => appStore
}))

vi.mock('@/i18n', () => ({
  getLocale: () => currentLocale.value
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('AdminComplianceDialog locale rendering', () => {
  beforeEach(() => {
    currentLocale.value = 'en'
    complianceStore.shouldShow = true
    complianceStore.status.document_url_zh = 'https://example.com/admin-compliance.zh.md'
    complianceStore.status.document_url_en = 'https://example.com/admin-compliance.en.md'
  })

  it('uses the Chinese document link for zh-HK', () => {
    currentLocale.value = 'zh-HK'

    const wrapper = mount(AdminComplianceDialog, {
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          },
          Icon: true,
          Input: {
            template: '<input />'
          }
        }
      }
    })

    expect(wrapper.find('a').attributes('href')).toBe('https://example.com/admin-compliance.zh.md')
    expect(wrapper.find('.legal-document-content').exists()).toBe(true)
  })
})
