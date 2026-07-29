import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsRequestDetailsModal from '../OpsRequestDetailsModal.vue'

const isDesktopViewport = ref(false)
const mockListRequestDetails = vi.fn()

vi.mock('@vueuse/core', () => ({
  useMediaQuery: () => isDesktopViewport,
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listRequestDetails: (...args: unknown[]) => mockListRequestDetails(...args),
  },
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn(), showWarning: vi.fn() }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const BaseDialogStub = defineComponent({
  template: '<div class="base-dialog"><slot /></div>',
})

const PaginationStub = defineComponent({
  template: '<div class="pagination-stub" />',
})

function mountModal() {
  return mount(OpsRequestDetailsModal, {
    props: {
      modelValue: false,
      timeRange: '60m',
      preset: { title: 'Request details' },
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Pagination: PaginationStub,
      },
    },
  })
}

describe('OpsRequestDetailsModal mobile layout', () => {
  beforeEach(() => {
    isDesktopViewport.value = false
    mockListRequestDetails.mockReset()
    mockListRequestDetails.mockResolvedValue({
      total: 2,
      items: [
        {
          kind: 'error',
          platform: 'openai',
          created_at: '2026-07-20T12:00:00Z',
          model: 'gpt-5',
          duration_ms: 123,
          status_code: 502,
          request_id: 'req-mobile-error',
          error_id: 9,
        },
        {
          kind: 'success',
          platform: 'anthropic',
          created_at: '2026-07-20T12:01:00Z',
          model: 'claude-sonnet',
          duration_ms: 456,
          status_code: 200,
          request_id: '',
        },
      ],
    })
  })

  it('renders request rows as cards on mobile while keeping request and error actions', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ modelValue: true })
    await flushPromises()

    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.text()).toContain('OPENAI')
    expect(wrapper.text()).toContain('gpt-5')
    expect(wrapper.text()).toContain('123 ms')
    expect(wrapper.text()).toContain('502')
    expect(wrapper.text()).toContain('req-mobile-error')
    expect(wrapper.text()).toContain('admin.ops.requestDetails.copy')
    expect(wrapper.text()).toContain('admin.ops.requestDetails.viewError')
    expect(wrapper.text()).toContain('ANTHROPIC')

    await wrapper.get('button[class*="bg-red-50"]').trigger('click')
    expect(wrapper.emitted('openErrorDetail')).toEqual([[9]])
    expect(wrapper.emitted('update:modelValue')).toEqual([[false]])
  })

  it('keeps the existing table layout on desktop', async () => {
    isDesktopViewport.value = true
    const wrapper = mountModal()
    await wrapper.setProps({ modelValue: true })
    await flushPromises()

    expect(wrapper.find('table').exists()).toBe(true)
    expect(wrapper.text()).toContain('OPENAI')
    expect(wrapper.text()).toContain('req-mobile-error')
  })
})
