import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'

const { list, getAllWithCount, checkProxyQuality } = vi.hoisted(() => ({
  list: vi.fn(),
  getAllWithCount: vi.fn(),
  checkProxyQuality: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, getAllWithCount, checkProxyQuality } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))
const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show" class="dialog"><slot /><slot name="footer" /></div>' },
  } },
})
let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({ items: [{ id: 9, name: 'proxy', protocol: 'http', host: 'proxy.example', port: 8080, status: 'active' }], total: 1, pages: 1 })
  getAllWithCount.mockResolvedValue([])
  checkProxyQuality.mockResolvedValue({
    proxy_id: 9,
    score: 90,
    grade: 'A',
    summary: 'ok',
    exit_ip: '203.0.113.9',
    country: 'US',
    country_code: 'US',
    base_latency_ms: 42,
    passed_count: 3,
    warn_count: 0,
    failed_count: 0,
    challenge_count: 0,
    checked_at: 1700000000,
    items: [
      { target: 'base_connectivity', status: 'pass', latency_ms: 42, message: 'ok' },
      { target: 'grok', status: 'pass', http_status: 401, latency_ms: 120, message: 'reachable' },
      { target: 'custom_target', status: 'warn', message: 'unknown' },
    ],
  })
})
afterEach(() => wrapper?.unmount())

describe('proxy quality report', () => {
  it('labels the grok target and keeps unknown targets as-is', async () => {
    wrapper = mountView(); await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'admin.proxies.qualityCheck')!.trigger('click')
    await flushPromises()

    expect(checkProxyQuality).toHaveBeenCalledWith(9)
    const targetCells = wrapper.findAll('.dialog tbody tr').map(row => row.find('td').text())
    expect(targetCells).toContain('Grok')
    expect(targetCells).toContain('custom_target')
    expect(targetCells).not.toContain('grok')
  })
})
