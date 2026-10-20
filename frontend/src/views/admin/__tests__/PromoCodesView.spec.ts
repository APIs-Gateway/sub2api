import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { PromoCode } from '@/types'
import PromoCodesView from '../PromoCodesView.vue'

const { listPromoCodes } = vi.hoisted(() => ({
  listPromoCodes: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    promo: {
      list: listPromoCodes,
      getById: vi.fn(),
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      getUsages: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
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

const promoCode: PromoCode = {
  id: 1,
  code: 'LOCAL-TIME',
  bonus_amount: 10,
  max_uses: 0,
  used_count: 0,
  status: 'active',
  expires_at: '2026-07-31T00:00:00Z',
  notes: null,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z'
}

describe('PromoCodesView edit expiry', () => {
  beforeEach(() => {
    listPromoCodes.mockReset()
    listPromoCodes.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
  })

  it('prefills edit expiry in the browser local timezone', async () => {
    const previousTZ = process.env.TZ
    process.env.TZ = 'Asia/Shanghai'

    try {
      const wrapper = mount(PromoCodesView, { shallow: true })
      await flushPromises()

      ;(wrapper.vm as any).handleEdit(promoCode)

      expect((wrapper.vm as any).editForm.expires_at_str).toBe('2026-07-31T08:00')
      expect((wrapper.vm as any).showEditDialog).toBe(true)

      ;(wrapper.vm as any).handleEdit({ ...promoCode, expires_at: null })
      expect((wrapper.vm as any).editForm.expires_at_str).toBe('')

      wrapper.unmount()
    } finally {
      if (previousTZ === undefined) {
        delete process.env.TZ
      } else {
        process.env.TZ = previousTZ
      }
    }
  })
})
