import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { invalidateAdminComplianceSession } from '@/utils/adminComplianceSession'

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
    complianceStore.accept.mockReset()
    authStore.isAuthenticated = true
    authStore.isAdmin = true
    appStore.showSuccess.mockReset()
    appStore.showError.mockReset()
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

  it.each([
    ['success', false],
    ['success', true],
    ['failure', false],
    ['failure', true],
  ] as const)('ignores an old acceptance %s after reset (new admin: %s)', async (outcome, newAdmin) => {
    let resolveAccept!: (value: { required: boolean }) => void
    let rejectAccept!: (error: Error) => void
    complianceStore.accept.mockImplementation(() => new Promise((resolve, reject) => {
      resolveAccept = resolve
      rejectAccept = reject
    }))
    const wrapper = mount(AdminComplianceDialog, {
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          },
          Icon: true,
          Input: {
            props: ['modelValue'],
            template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
          }
        }
      }
    })

    await wrapper.find('input').setValue('繁體中文確認短語')
    await wrapper.find('button.btn-primary').trigger('click')
    expect(complianceStore.accept).toHaveBeenCalledWith('繁體中文確認短語')

    invalidateAdminComplianceSession()
    authStore.isAuthenticated = newAdmin
    authStore.isAdmin = newAdmin
    if (newAdmin) await wrapper.find('input').setValue('new administrator input')
    if (outcome === 'success') resolveAccept({ required: false })
    else rejectAccept(new Error('old administrator failure'))
    await flushPromises()

    expect(appStore.showSuccess).not.toHaveBeenCalled()
    expect(appStore.showError).not.toHaveBeenCalled()
    if (newAdmin) expect((wrapper.find('input').element as HTMLInputElement).value).toBe('new administrator input')
    wrapper.unmount()
  })

  it.each(['success', 'failure'] as const)('keeps current-session acceptance %s behavior', async (outcome) => {
    if (outcome === 'success') complianceStore.accept.mockResolvedValue({ required: false })
    else complianceStore.accept.mockRejectedValue(new Error('current administrator failure'))
    const wrapper = mount(AdminComplianceDialog, {
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          },
          Icon: true,
          Input: {
            props: ['modelValue'],
            template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
          }
        }
      }
    })

    await wrapper.find('input').setValue('繁體中文確認短語')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    if (outcome === 'success') {
      expect(appStore.showSuccess).toHaveBeenCalledWith('adminCompliance.accepted')
      expect((wrapper.find('input').element as HTMLInputElement).value).toBe('')
      expect(appStore.showError).not.toHaveBeenCalled()
    } else {
      expect(appStore.showError).toHaveBeenCalledWith('current administrator failure')
      expect(appStore.showSuccess).not.toHaveBeenCalled()
    }
    wrapper.unmount()
  })
})
