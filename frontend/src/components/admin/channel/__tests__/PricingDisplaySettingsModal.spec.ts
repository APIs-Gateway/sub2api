import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import PricingDisplaySettingsModal from '../PricingDisplaySettingsModal.vue'

const { getPricingDisplayMock, listChannelsMock, getGroupsMock } = vi.hoisted(() => ({
  getPricingDisplayMock: vi.fn(),
  listChannelsMock: vi.fn(),
  getGroupsMock: vi.fn(),
}))

vi.mock('@/api/admin/channels', () => ({
  default: {
    getPricingDisplay: getPricingDisplayMock,
    list: listChannelsMock,
    updatePricingDisplay: vi.fn(),
  },
}))

vi.mock('@/api/admin/groups', () => ({
  default: {
    getAllIncludingInactive: getGroupsMock,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: () => 'error',
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

describe('PricingDisplaySettingsModal', () => {
  it('offers exact mapped models as public-display candidates', async () => {
    getPricingDisplayMock.mockResolvedValue({ group_ids: [], models: [] })
    getGroupsMock.mockResolvedValue([])
    listChannelsMock.mockResolvedValue({
      items: [
        {
          model_pricing: [],
          model_mapping: {
            openai: {
              'gpt-5.6-sol': 'gpt-5.6-sol',
              'gpt-5.6-terra': 'gpt-5.6-terra',
              'gpt-5.6-luna': 'gpt-5.6-luna',
              'gpt-*': 'gpt-*',
            },
          },
        },
      ],
      total: 1,
    })

    const wrapper = mount(PricingDisplaySettingsModal, {
      props: { show: false },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          GroupSelector: true,
          Icon: true,
        },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const content = wrapper.text()
    expect(content).toContain('gpt-5.6-sol')
    expect(content).toContain('gpt-5.6-terra')
    expect(content).toContain('gpt-5.6-luna')
    expect(content).not.toContain('gpt-*')
  })
})
