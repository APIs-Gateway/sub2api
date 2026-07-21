import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'

const { queryOpenAIQuotaMock, resetOpenAIQuotaMock } = vi.hoisted(() => ({
  queryOpenAIQuotaMock: vi.fn(),
  resetOpenAIQuotaMock: vi.fn()
}))

vi.mock('@/api/admin/accounts', () => ({
  queryOpenAIQuota: queryOpenAIQuotaMock,
  resetOpenAIQuota: resetOpenAIQuotaMock
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const ConfirmDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false }
  },
  emits: ['confirm', 'cancel'],
  template: `
    <div v-if="show" data-testid="confirm-dialog">
      <button data-testid="cancel" type="button" @click="$emit('cancel')">cancel</button>
      <button data-testid="confirm" type="button" @click="$emit('confirm')">confirm</button>
    </div>
  `
})

const account = {
  id: 1,
  platform: 'openai',
  type: 'oauth'
} as any

describe('OpenAIQuotaResetCell', () => {
  beforeEach(() => {
    queryOpenAIQuotaMock.mockReset()
    resetOpenAIQuotaMock.mockReset()
    queryOpenAIQuotaMock.mockResolvedValue({
      rate_limit_reset_credits: { available_count: 1 }
    })
    resetOpenAIQuotaMock.mockResolvedValue({ windows_reset: 1 })
  })

  const mountCell = () => mount(OpenAIQuotaResetCell, {
    props: { account },
    global: {
      stubs: { ConfirmDialog: ConfirmDialogStub }
    }
  })

  it('requires confirmation before consuming a reset credit', async () => {
    const wrapper = mountCell()

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()
    await wrapper.findAll('button')[1].trigger('click')

    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(true)
    expect(resetOpenAIQuotaMock).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="cancel"]').trigger('click')
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(false)
    expect(resetOpenAIQuotaMock).not.toHaveBeenCalled()

    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.find('[data-testid="confirm"]').trigger('click')
    await flushPromises()

    expect(resetOpenAIQuotaMock).toHaveBeenCalledOnce()
    expect(queryOpenAIQuotaMock).toHaveBeenCalledTimes(2)
  })

  it('closes a pending confirmation when the account changes', async () => {
    const wrapper = mountCell()

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(true)

    await wrapper.setProps({ account: { ...account, id: 2 } })
    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(false)
  })

  it('does not open while a request is already in progress', async () => {
    const wrapper = mountCell()

    ;(wrapper.vm as any).loading = true
    await (wrapper.vm as any).openResetConfirm()

    expect(wrapper.find('[data-testid="confirm-dialog"]').exists()).toBe(false)
  })

  it('reports unavailable credits before opening or confirming a reset', async () => {
    const wrapper = mountCell()

    ;(wrapper.vm as any).data = {
      rate_limit_reset_credits: { available_count: 0 }
    }
    await (wrapper.vm as any).openResetConfirm()
    expect(wrapper.text()).toContain('admin.accounts.openaiQuotaReset.noCreditsAvailable')

    await (wrapper.vm as any).confirmReset()
    expect(resetOpenAIQuotaMock).not.toHaveBeenCalled()

    ;(wrapper.vm as any).resetting = true
    await (wrapper.vm as any).confirmReset()
    expect(resetOpenAIQuotaMock).not.toHaveBeenCalled()
  })
})
