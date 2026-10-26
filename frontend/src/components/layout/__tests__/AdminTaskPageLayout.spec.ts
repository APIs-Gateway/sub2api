import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'

import AdminTaskPageLayout from '../AdminTaskPageLayout.vue'

const TableWorklist = defineComponent({
  template: `
    <div data-test="worklist-card">
      <div class="table-wrapper" data-test="table-scrollport">rows</div>
    </div>
  `
})

const originalMatchMedia = Object.getOwnPropertyDescriptor(window, 'matchMedia')
const mountedWrappers = new Set<{ unmount: () => void }>()

afterEach(() => {
  try {
    for (const wrapper of mountedWrappers) wrapper.unmount()
  } finally {
    mountedWrappers.clear()
    if (originalMatchMedia) {
      Object.defineProperty(window, 'matchMedia', originalMatchMedia)
    } else {
      Reflect.deleteProperty(window, 'matchMedia')
    }
  }
})

function mountLayout(desktop: boolean) {
  let changeListener: ((event: MediaQueryListEvent) => void) | undefined
  const mediaQuery = {
    matches: desktop,
    addEventListener: vi.fn((event: string, listener: (event: MediaQueryListEvent) => void) => {
      if (event === 'change') changeListener = listener
    }),
    removeEventListener: vi.fn((event: string, listener: (event: MediaQueryListEvent) => void) => {
      if (event === 'change' && changeListener === listener) changeListener = undefined
    })
  }

  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockReturnValue(mediaQuery)
  })

  const wrapper = mount(AdminTaskPageLayout, {
    props: { title: 'Redeem codes' },
    slots: {
      filters: '<input aria-label="Search codes" />',
      worklist: () => h(TableWorklist),
      footer: '<nav aria-label="Pagination">page controls</nav>'
    }
  })
  mountedWrappers.add(wrapper)

  return {
    wrapper,
    setDesktop(matches: boolean) {
      if (!changeListener) throw new Error('Expected the layout to register a media-query listener')
      mediaQuery.matches = matches
      changeListener({ matches } as MediaQueryListEvent)
    }
  }
}

describe('AdminTaskPageLayout worklist containment', () => {
  it('keeps the DataTable scroll port inside a bounded desktop flex chain', () => {
    const { wrapper } = mountLayout(true)
    const worklist = wrapper.get('.admin-task-page__worklist').element as HTMLElement
    const worklistContent = wrapper.get('.admin-task-page__worklist-content').element as HTMLElement
    const footer = wrapper.get('.admin-task-page__footer').element as HTMLElement

    expect((wrapper.element as HTMLElement).style.height).toBe('calc(100vh - (64px + 4rem))')
    expect(worklist.style.display).toBe('flex')
    expect(worklist.style.flex).toBe('1 1 0%')
    expect(worklist.style.minHeight).toBe('0')
    expect(worklist.style.overflow).toBe('hidden')
    expect(worklistContent.style.display).toBe('flex')
    expect(worklistContent.style.flex).toBe('1 1 0%')
    expect(footer.style.flex).toBe('0 0 auto')
    expect(wrapper.get('[data-test="table-scrollport"]').classes()).toContain('table-wrapper')
  })

  it('releases the worklist to normal document flow below the desktop breakpoint', () => {
    const { wrapper } = mountLayout(false)
    const worklist = wrapper.get('.admin-task-page__worklist').element as HTMLElement
    const worklistContent = wrapper.get('.admin-task-page__worklist-content').element as HTMLElement

    expect((wrapper.element as HTMLElement).style.height).toBe('')
    expect(worklist.style.display).toBe('block')
    expect(worklist.style.overflow).toBe('visible')
    expect(worklistContent.style.display).toBe('block')
    expect(worklistContent.style.overflow).toBe('visible')
  })

  it('removes and restores desktop bounds when the media-query listener crosses the breakpoint', async () => {
    const { wrapper, setDesktop } = mountLayout(true)
    const page = wrapper.element as HTMLElement
    const worklists = [
      wrapper.get('.admin-task-page__worklist').element as HTMLElement,
      wrapper.get('.admin-task-page__worklist-content').element as HTMLElement
    ]
    const fixedSections = [
      wrapper.get('.admin-task-page__header').element as HTMLElement,
      wrapper.get('.admin-task-page__filters').element as HTMLElement,
      wrapper.get('.admin-task-page__footer').element as HTMLElement
    ]

    const expectDesktop = () => {
      expect(page.style.height).toBe('calc(100vh - (64px + 4rem))')
      for (const worklist of worklists) {
        expect(worklist.style.display).toBe('flex')
        expect(worklist.style.flex).toBe('1 1 0%')
        expect(worklist.style.flexDirection).toBe('column')
        expect(worklist.style.minHeight).toBe('0')
        expect(worklist.style.overflow).toBe('hidden')
      }
      for (const section of fixedSections) expect(section.style.flex).toBe('0 0 auto')
    }

    expectDesktop()

    setDesktop(false)
    await nextTick()

    expect(page.style.height).toBe('')
    for (const worklist of worklists) {
      expect(worklist.style.display).toBe('block')
      expect(worklist.style.flex).toBe('')
      expect(worklist.style.flexDirection).toBe('')
      expect(worklist.style.minHeight).toBe('0')
      expect(worklist.style.overflow).toBe('visible')
    }
    for (const section of fixedSections) expect(section.style.flex).toBe('')
    expect(wrapper.get('[data-test="table-scrollport"]').classes()).toContain('table-wrapper')

    setDesktop(true)
    await nextTick()

    expectDesktop()
  })
})
