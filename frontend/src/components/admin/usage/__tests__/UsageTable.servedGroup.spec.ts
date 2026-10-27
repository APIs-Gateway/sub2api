import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageTable from '../UsageTable.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'keyFallback.usage.sourceUser': 'User chain',
    'keyFallback.usage.sourceAdmin': 'Hidden chain'
  }
  return { ...actual, useI18n: () => ({ t: (key: string) => messages[key] ?? key }) }
})

const DataTableStub = {
  props: ['data'],
  template: '<div><div v-for="row in data" :key="row.request_id"><slot name="cell-group" :row="row" /></div></div>'
}

const mountRows = (rows: Record<string, unknown>[]) =>
  mount(UsageTable, {
    props: { data: rows as never, loading: false, columns: [] },
    global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } }
  })

describe('admin UsageTable served group', () => {
  it('没有兜底时只显示主分组', () => {
    const w = mountRows([{ request_id: 'a', group: { name: 'Codex Plus' } }])
    expect(w.text()).toContain('Codex Plus')
    expect(w.find('[data-test="served-group"]').exists()).toBe(false)
  })

  it('显示实际服务的分组和来源，主分组仍保留', () => {
    const w = mountRows([
      { request_id: 'a', group: { name: 'Codex Plus' }, served_group_id: 21, served_group: { id: 21, name: 'Codex 稳定' }, served_route_source: 1 },
      { request_id: 'b', group: { name: 'Codex Plus' }, served_group_id: 88, served_group: { id: 88, name: 'hovy-513' }, served_route_source: 2 }
    ])
    const served = w.findAll('[data-test="served-group"]')
    expect(served).toHaveLength(2)
    expect(served[0].text()).toContain('Codex 稳定')
    expect(served[0].get('[data-test="served-source"]').text()).toBe('User chain')
    expect(served[1].text()).toContain('hovy-513')
    expect(served[1].get('[data-test="served-source"]').text()).toBe('Hidden chain')
    expect(w.text()).toContain('Codex Plus')
  })
})
