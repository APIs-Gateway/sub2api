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
})
