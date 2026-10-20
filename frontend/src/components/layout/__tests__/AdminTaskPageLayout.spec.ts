import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

import AdminTaskPageLayout from '../AdminTaskPageLayout.vue'

const TableWorklist = defineComponent({
  template: `
    <div data-test="worklist-card">
      <div class="table-wrapper" data-test="table-scrollport">rows</div>
    </div>
  `
})

function mountLayout(desktop: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockReturnValue({
      matches: desktop,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })
  })

  return mount(AdminTaskPageLayout, {
    props: { title: 'Redeem codes' },
    slots: {
      filters: '<input aria-label="Search codes" />',
      worklist: () => h(TableWorklist),
      footer: '<nav aria-label="Pagination">page controls</nav>'
    }
  })
}

describe('AdminTaskPageLayout worklist containment', () => {
  it('keeps the DataTable scroll port inside a bounded desktop flex chain', () => {
    const wrapper = mountLayout(true)
    const worklist = wrapper.get('.admin-task-page__worklist').element as HTMLElement
    const worklistContent = wrapper.get('.admin-task-page__worklist-content').element as HTMLElement
    const footer = wrapper.get('.admin-task-page__footer').element as HTMLElement

    expect((wrapper.element as HTMLElement).style.height).toBe('calc(100vh - 64px - 4rem)')
    expect(worklist.style.display).toBe('flex')
    expect(worklist.style.flex).toBe('1 1 0%')
    expect(worklist.style.minHeight).toBe('0')
    expect(worklist.style.overflow).toBe('hidden')
    expect(worklistContent.style.display).toBe('flex')
    expect(worklistContent.style.flex).toBe('1 1 0%')
    expect(footer.style.flex).toBe('0 0 auto')
    expect(wrapper.get('[data-test="table-scrollport"]').classes()).toContain('table-wrapper')

    wrapper.unmount()
  })

  it('releases the worklist to normal document flow below the desktop breakpoint', () => {
    const wrapper = mountLayout(false)
    const worklist = wrapper.get('.admin-task-page__worklist').element as HTMLElement
    const worklistContent = wrapper.get('.admin-task-page__worklist-content').element as HTMLElement

    expect((wrapper.element as HTMLElement).style.height).toBe('')
    expect(worklist.style.display).toBe('block')
    expect(worklist.style.overflow).toBe('visible')
    expect(worklistContent.style.display).toBe('block')
    expect(worklistContent.style.overflow).toBe('visible')

    wrapper.unmount()
  })
})
