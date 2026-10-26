import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { OpsIngressRejectListResponse } from '@/api/admin/ops'
import OpsIngressRejectTable from '../OpsIngressRejectTable.vue'

const listIngressRejects = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: { listIngressRejects: (...args: unknown[]) => listIngressRejects(...args) }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})
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
  props: {
    page: { type: Number, default: 1 },
    pageSize: { type: Number, default: 20 },
    total: { type: Number, default: 0 }
  },
  emits: ['update:page', 'update:pageSize'],
  template: '<div class="pagination-stub" />'
})

function deferredResponse() {
  let resolve!: (response: OpsIngressRejectListResponse) => void
  let reject!: (error: Error) => void
  const promise = new Promise<OpsIngressRejectListResponse>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function aggregateResponse(rejectReason: string, total = 100, page = 1): OpsIngressRejectListResponse {
  return {
    items: [{
      id: page,
      bucket_start: '2026-09-22T00:00:00Z',
      reject_reason: rejectReason,
      route_family: 'responses',
      protocol: 'openai',
      client_ip: '203.0.113.0/24',
      request_count: 7,
      first_seen: '2026-09-22T00:00:01Z',
      last_seen: '2026-09-22T00:00:59Z'
    }],
    total,
    page,
    page_size: 20
  }
}

beforeEach(() => { listIngressRejects.mockReset() })

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

  it('keeps the latest filtered rows when an older request resolves last', async () => {
    const older = deferredResponse()
    const latest = deferredResponse()
    listIngressRejects.mockReturnValueOnce(older.promise).mockReturnValueOnce(latest.promise)
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'ip_restricted')
    await flushPromises()

    latest.resolve(aggregateResponse('ip_restricted'))
    await flushPromises()
    expect(wrapper.text()).toContain('ip_restricted')

    older.resolve(aggregateResponse('invalid_api_key', 40))
    await flushPromises()
    expect(wrapper.text()).toContain('ip_restricted')
    expect(wrapper.text()).not.toContain('invalid_api_key')
    expect(wrapper.findAllComponents(SelectControlStub)[1].props('modelValue')).toBe('ip_restricted')
    expect(wrapper.findComponent(PaginationStub).props('total')).toBe(100)
    wrapper.unmount()
  })

  it('ignores an older error after the latest filter succeeds', async () => {
    const older = deferredResponse()
    const latest = deferredResponse()
    listIngressRejects.mockReturnValueOnce(older.promise).mockReturnValueOnce(latest.promise)
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'ip_restricted')
    await flushPromises()
    latest.resolve(aggregateResponse('ip_restricted'))
    await flushPromises()

    older.reject(new Error('obsolete query failed'))
    await flushPromises()
    expect(wrapper.text()).toContain('ip_restricted')
    expect(wrapper.text()).not.toContain('obsolete query failed')
    expect(wrapper.find('.empty-state').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps the latest request loading when an older request settles', async () => {
    const older = deferredResponse()
    const latest = deferredResponse()
    listIngressRejects.mockReturnValueOnce(older.promise).mockReturnValueOnce(latest.promise)
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'ip_restricted')
    await flushPromises()

    older.resolve(aggregateResponse('invalid_api_key'))
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ops.loadingText')
    expect(wrapper.text()).not.toContain('invalid_api_key')

    latest.reject(new Error('current query failed'))
    await flushPromises()
    expect(wrapper.text()).toContain('current query failed')
    expect(wrapper.text()).not.toContain('admin.ops.loadingText')
    expect(wrapper.find('.empty-state').exists()).toBe(true)
    wrapper.unmount()
  })

  it('does not use an older total to rewind the current filtered page', async () => {
    const olderPage = deferredResponse()
    const filteredFirstPage = deferredResponse()
    const latestPage = deferredResponse()
    listIngressRejects
      .mockResolvedValueOnce(aggregateResponse('invalid_api_key'))
      .mockReturnValueOnce(olderPage.promise)
      .mockReturnValueOnce(filteredFirstPage.promise)
      .mockReturnValueOnce(latestPage.promise)
    const wrapper = mount(OpsIngressRejectTable, {
      global: { stubs: { Select: SelectControlStub, EmptyState: EmptyStateStub, Pagination: PaginationStub } }
    })
    await flushPromises()
    await wrapper.findComponent(PaginationStub).vm.$emit('update:page', 4)
    await flushPromises()
    await wrapper.findAllComponents(SelectControlStub)[1].vm.$emit('update:modelValue', 'ip_restricted')
    await flushPromises()
    expect(listIngressRejects).toHaveBeenLastCalledWith(expect.objectContaining({ reason: 'ip_restricted', page: 1 }))

    filteredFirstPage.resolve(aggregateResponse('ip_restricted'))
    await flushPromises()
    await wrapper.findComponent(PaginationStub).vm.$emit('update:page', 4)
    await flushPromises()
    latestPage.resolve(aggregateResponse('ip_restricted', 100, 4))
    await flushPromises()

    olderPage.resolve(aggregateResponse('invalid_api_key', 20, 4))
    await flushPromises()
    expect(wrapper.text()).toContain('ip_restricted')
    expect(wrapper.text()).not.toContain('invalid_api_key')
    expect(wrapper.findComponent(PaginationStub).props('page')).toBe(4)
    expect(wrapper.findComponent(PaginationStub).props('total')).toBe(100)
    expect(listIngressRejects).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })
})
