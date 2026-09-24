import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useAdminComplianceStore } from '../adminCompliance'

const { currentLocale } = vi.hoisted(() => ({
  currentLocale: { value: 'en' }
}))

vi.mock('@/i18n', () => ({
  getLocale: () => currentLocale.value
}))

vi.mock('@/api/admin/compliance', () => ({
  default: {
    getStatus: vi.fn(),
    accept: vi.fn()
  }
}))

describe('useAdminComplianceStore locale phrases', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    currentLocale.value = 'en'
  })

  it('uses the Chinese acknowledgement phrase for zh-HK', () => {
    currentLocale.value = 'zh-HK'
    const store = useAdminComplianceStore()

    store.requireAcknowledgement({
      ack_phrase_zh: '繁體中文確認短語',
      ack_phrase_en: 'English confirmation phrase'
    })

    expect(store.expectedPhrase).toBe('繁體中文確認短語')
  })

  it('keeps the current neutral acknowledgement contract when the API only requests acknowledgement', () => {
    const store = useAdminComplianceStore()

    store.requireAcknowledgement()

    expect(store.status).toMatchObject({
      required: true,
      version: 'v2026.09.22',
      document_url_zh: '/legal/admin-compliance',
      document_url_en: '/legal/admin-compliance',
      ack_phrase_zh: '我已阅读、理解并同意本服务部署与运营合规承诺',
      ack_phrase_en: "I have read, understood, and agree to this service's Deployment and Operation Compliance Commitment"
    })
    expect(store.expectedPhrase).toBe(
      "I have read, understood, and agree to this service's Deployment and Operation Compliance Commitment"
    )

    currentLocale.value = 'zh-CN'
    expect(store.expectedPhrase).toBe('我已阅读、理解并同意本服务部署与运营合规承诺')
  })
})
