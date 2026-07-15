import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import DataTable from '@/components/common/DataTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const columns = [{ key: 'name', label: 'Name' }]

function mountTable(data: any[], rowKey?: string) {
  return mount(DataTable, {
    props: { columns, data, rowKey },
    global: { stubs: { Icon: true } }
  })
}

beforeEach(() => {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === '(min-width: 768px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn()
    }))
  })
})

describe('DataTable virtualizer row identity', () => {
  it('clears caches when a page replaces rows with new stable IDs', async () => {
    const wrapper = mountTable(
      [{ id: 1, name: 'First' }, { id: 2, name: 'Second' }],
      'id'
    )
    await nextTick()

    const virtualizer = (wrapper.vm as any).virtualizer
    const measureElement = vi.spyOn(virtualizer, 'measureElement')
    const measure = vi.spyOn(virtualizer, 'measure')

    await wrapper.setProps({
      data: [{ id: 101, name: 'Next first' }, { id: 102, name: 'Next second' }]
    })
    await nextTick()

    expect(measureElement).toHaveBeenCalledWith(null)
    expect(measure).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('clears caches when equal-length pages replace rows without stable IDs', async () => {
    const wrapper = mountTable([{ name: 'First' }, { name: 'Second' }])
    await nextTick()

    const virtualizer = (wrapper.vm as any).virtualizer
    const measure = vi.spyOn(virtualizer, 'measure')

    await wrapper.setProps({ data: [{ name: 'Next first' }, { name: 'Next second' }] })
    await nextTick()

    expect(measure).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('preserves caches when stable rows are only reordered', async () => {
    const firstPage = [{ id: 1, name: 'First' }, { id: 2, name: 'Second' }]
    const wrapper = mountTable(firstPage, 'id')
    await nextTick()

    const virtualizer = (wrapper.vm as any).virtualizer
    const measureElement = vi.spyOn(virtualizer, 'measureElement')
    const measure = vi.spyOn(virtualizer, 'measure')

    await wrapper.setProps({ data: [...firstPage].reverse() })
    await nextTick()

    expect(measureElement).not.toHaveBeenCalledWith(null)
    expect(measure).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
