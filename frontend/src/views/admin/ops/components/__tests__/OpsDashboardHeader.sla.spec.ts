import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { flushPromises, shallowMount } from '@vue/test-utils'
import type { OpsDashboardOverview } from '@/api/admin/ops'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'

vi.mock('@vueuse/core', () => ({ useMediaQuery: () => ref(true) }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { listRequestDetails: vi.fn() } }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
  useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: false }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

async function slaValue(overview: Partial<OpsDashboardOverview>) {
  const wrapper = shallowMount(OpsDashboardHeader, {
    props: {
      overview: overview as OpsDashboardOverview,
      platform: '',
      groupId: null,
      timeRange: '1h',
      queryMode: 'auto',
      loading: false,
      lastUpdated: null,
    },
  })
  await flushPromises()
  const card = wrapper
    .findAll('div.rounded-2xl')
    .find((el) => el.find('span').exists() && el.text().startsWith('admin.ops.sla'))
  expect(card).toBeDefined()
  const value = card!.get('.text-3xl').text()
  wrapper.unmount()
  return value
}

describe('OpsDashboardHeader SLA card', () => {
  it('shows a neutral placeholder when the window has no SLA requests', async () => {
    expect(await slaValue({ sla: 0, request_count_sla: 0, success_count: 0 })).toBe('-')
  })

  it('shows the SLA percentage when the window has requests', async () => {
    expect(await slaValue({ sla: 0.995, request_count_sla: 200, success_count: 199 })).toBe('99.500%')
  })
})
