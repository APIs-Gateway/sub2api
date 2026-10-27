import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: {}, showError: vi.fn(), showSuccess: vi.fn() }),
}))
vi.mock('@/api/admin', () => ({
  adminAPI: { channelMonitor: {}, channelMonitorTemplate: { list: vi.fn().mockResolvedValue({ items: [] }) } },
}))
vi.mock('@/api/keys', () => ({ keysAPI: { list: vi.fn() } }))
vi.mock('@/api/groups', () => ({ userGroupsAPI: { getUserGroupRates: vi.fn() } }))
vi.mock('vue-i18n', async () => {
  const zhCN = (await import('@/i18n/locales/zh-CN')).default as Record<string, unknown>
  const t = (key: string) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    return typeof hit === 'string' ? hit : key
  }
  return { useI18n: () => ({ t }) }
})

import MonitorFormDialog from '../MonitorFormDialog.vue'

const stub = { template: '<div />' }
const mountForm = () => mount(MonitorFormDialog, {
  props: { show: true, monitor: null },
  global: {
    stubs: {
      BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
      Toggle: stub, Select: stub, ModelTagInput: stub, MonitorKeyPickerDialog: stub,
      MonitorAdvancedRequestConfig: stub, ProviderIcon: stub,
    },
  },
})

describe('监控表单：名称和分组名称会展示给用户，旁边有提示', () => {
  it('名称、分组名称旁各有一句提示，要求不写上游厂商、上游模型或内部账号名', () => {
    const w = mountForm()
    for (const id of ['monitor-name-hint', 'monitor-group-hint']) {
      const hint = w.get(`[data-testid="${id}"]`).text()
      expect(hint).toContain('显示给所有用户')
      expect(hint).toContain('上游厂商')
      expect(hint).toContain('上游模型')
      expect(hint).toContain('内部账号名')
    }
  })

  it('提示紧跟在对应的输入框后面', () => {
    const w = mountForm()
    const nameInput = w.get('input[required]')
    expect(nameInput.element.nextElementSibling?.getAttribute('data-testid')).toBe('monitor-name-hint')
    const groupHint = w.get('[data-testid="monitor-group-hint"]')
    expect(groupHint.element.previousElementSibling?.tagName).toBe('INPUT')
  })
})
