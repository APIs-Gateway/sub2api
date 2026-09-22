import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, h } from 'vue'

import AdminTaskPageLayout from '../AdminTaskPageLayout.vue'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AdminTaskPageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

const TableWorklist = defineComponent({
  template: `
    <div data-test="worklist-card">
      <div class="table-wrapper" data-test="table-scrollport">rows</div>
    </div>
  `
})

const styleBlock = (selector: string) => {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = componentSource.match(new RegExp(`${escapedSelector}\\s*\\{([^}]*)\\}`))
  return match?.[1] ?? ''
}

describe('AdminTaskPageLayout worklist containment', () => {
  it('keeps the DataTable scroll port inside a bounded desktop flex chain', () => {
    const wrapper = mount(AdminTaskPageLayout, {
      props: { title: 'Redeem codes' },
      slots: {
        filters: '<input aria-label="Search codes" />',
        worklist: () => h(TableWorklist),
        footer: '<nav aria-label="Pagination">page controls</nav>'
      }
    })

    expect(wrapper.classes()).toContain('admin-task-page')
    expect(wrapper.get('.admin-task-page__worklist').classes()).toEqual(
      expect.arrayContaining(['flex', 'flex-1', 'min-h-0', 'flex-col'])
    )
    expect(wrapper.get('.admin-task-page__worklist-content').classes()).toEqual(
      expect.arrayContaining(['flex', 'flex-1', 'min-h-0', 'flex-col'])
    )
    expect(wrapper.get('.admin-task-page__footer').classes()).toContain('flex-none')
    expect(
      wrapper.get('.admin-task-page__worklist-content').get('[data-test="worklist-card"]').exists()
    ).toBe(true)
    expect(wrapper.get('[data-test="table-scrollport"]').classes()).toContain('table-wrapper')

    expect(componentSource).toMatch(
      /@media \(min-width: 1024px\)[\s\S]*?\.admin-task-page\s*\{\s*height: calc\(100vh - 64px - 4rem\);/
    )
    expect(styleBlock('.admin-task-page__worklist')).toContain('overflow-hidden')
    expect(styleBlock('.admin-task-page__worklist-content')).toContain('overflow-hidden')
    expect(styleBlock('.admin-task-page__worklist-content > :deep(*)')).toContain('overflow-hidden')

    const scrollportContract = styleBlock('.admin-task-page__worklist-content :deep(.table-wrapper)')
    expect(scrollportContract).toContain('min-h-0')
    expect(scrollportContract).toContain('flex-1')
    expect(scrollportContract).toContain('overflow-x-auto')
    expect(scrollportContract).toContain('overflow-y-auto')

    expect(componentSource).toMatch(/@media \(max-width: 1023px\)[\s\S]*overflow-visible/)
  })
})
