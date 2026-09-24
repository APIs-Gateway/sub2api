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

const SelectControlStub = defineComponent({
  name: 'SelectControlStub',
  props: { modelValue: { type: String, default: '' } },
  emits: ['update:modelValue'],
  template: '<div class="select-stub" />'
})
const EmptyStateStub = defineComponent({ name: 'EmptyState', template: '<div class="empty-state" />' })
const PaginationStub = defineComponent({
  name: 'PaginationStub',
  props: { page: { type: Number, default: 1 }, pageSize: { type: Number, default: 20 } },
  emits: ['update:page', 'update:pageSize'],
  template: '<div class="pagination-stub" />'
})

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
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    expect(listIngressRejects).toHaveBeenCalledWith(expect.objectContaining({ time_range: '1h', page: 1, page_size: 20 }))
    expect(wrapper.text()).toContain('invalid_api_key')
    expect(wrapper.text()).toContain('203.0.113.0/24')
  })

  it('passes the selected reason filter to the aggregate endpoint', async () => {
    listIngressRejects.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'invalid_api_key')
    await flushPromises()

    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ reason: 'invalid_api_key', page: 1 }))
  })

  it('allows a later page, then resets to the first page when page size changes', async () => {
    listIngressRejects.mockImplementation(async (params: { page?: number; page_size?: number }) => ({
      items: [{
        id: params.page ?? 1,
        bucket_start: '2026-09-22T00:00:00Z',
        reject_reason: 'invalid_api_key',
        route_family: 'responses',
        protocol: 'openai',
        client_ip: '203.0.113.0/24',
        request_count: 7,
        first_seen: '2026-09-22T00:00:01Z',
        last_seen: '2026-09-22T00:00:59Z'
      }],
      total: 100,
      page: params.page ?? 1,
      page_size: params.page_size ?? 20
    }))
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    const pagination = wrapper.findComponent(PaginationStub)
    await pagination.vm.$emit('update:page', 4)
    await flushPromises()
    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ page: 4, page_size: 20 }))

    await pagination.vm.$emit('update:pageSize', 50)
    await flushPromises()

    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ page: 1, page_size: 50 }))
    expect(wrapper.find('.pagination-stub').exists()).toBe(true)
  })

  it('resets a later page before loading a narrowed filter result', async () => {
    listIngressRejects.mockImplementation(async (params: { page?: number; page_size?: number; reason?: string }) => ({
      items: params.reason && params.page !== 1
        ? []
        : [{
            id: params.page ?? 1,
            bucket_start: '2026-09-22T00:00:00Z',
            reject_reason: 'invalid_api_key',
            route_family: 'responses',
            protocol: 'openai',
            client_ip: '203.0.113.0/24',
            request_count: 7,
            first_seen: '2026-09-22T00:00:01Z',
            last_seen: '2026-09-22T00:00:59Z'
          }],
      total: params.reason ? 40 : 100,
      page: params.page ?? 1,
      page_size: params.page_size ?? 20
    }))
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()

    await wrapper.findComponent(PaginationStub).vm.$emit('update:page', 4)
    await flushPromises()
    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ page: 4 }))

    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'invalid_api_key')
    await flushPromises()

    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ reason: 'invalid_api_key', page: 1 }))
    expect(wrapper.find('.pagination-stub').exists()).toBe(true)
  })
})
