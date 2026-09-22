import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsIngressRejectTable from '../OpsIngressRejectTable.vue'

const listIngressRejects = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: { listIngressRejects: (...args: unknown[]) => listIngressRejects(...args) }
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@vueuse/core', () => ({ useMediaQuery: () => true }))

const SelectStub = defineComponent({
  name: 'Select',
  props: { modelValue: { type: String, default: '' } },
  emits: ['update:modelValue'],
  template: '<div class="select-stub" />'
})
const EmptyStateStub = defineComponent({ name: 'EmptyState', template: '<div class="empty-state" />' })
const PaginationStub = defineComponent({ name: 'Pagination', template: '<div class="pagination-stub" />' })

describe('OpsIngressRejectTable', () => {
  it('loads sanitized aggregate rows from the existing admin endpoint', async () => {
    listIngressRejects.mockResolvedValue({
      items: [{
        id: 1,
        bucket_start: '2026-09-22T00:00:00Z',
        reject_reason: 'invalid_api_key',
        route_family: 'responses',
        protocol: 'openai',
        client_ip: '203.0.113.0/24',
        request_count: 7,
        first_seen: '2026-09-22T00:00:01Z',
        last_seen: '2026-09-22T00:00:59Z'
      }],
      total: 1,
      page: 1,
      page_size: 20
    })

    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    expect(listIngressRejects).toHaveBeenCalledWith(expect.objectContaining({ time_range: '1h', page: 1, page_size: 20 }))
    expect(wrapper.text()).toContain('invalid_api_key')
    expect(wrapper.text()).toContain('203.0.113.0/24')
  })

  it('passes the selected reason filter to the aggregate endpoint', async () => {
    listIngressRejects.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    await wrapper.findAllComponents(SelectStub)[1].vm.$emit('update:modelValue', 'invalid_api_key')
    await flushPromises()

    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ reason: 'invalid_api_key', page: 1 }))
  })
})
