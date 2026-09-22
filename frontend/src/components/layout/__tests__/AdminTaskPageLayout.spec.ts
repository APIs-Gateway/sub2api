import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import AdminTaskPageLayout from '../AdminTaskPageLayout.vue'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AdminTaskPageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('AdminTaskPageLayout worklist containment', () => {
  it('renders a bounded desktop worklist flex chain around its contents', () => {
    const wrapper = mount(AdminTaskPageLayout, {
      props: { title: 'Redeem codes' },
      slots: {
        filters: '<input aria-label="Search codes" />',
        worklist: '<div class="redeem-card"><div class="table-wrapper">rows</div></div>',
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
    expect(wrapper.get('.table-wrapper').exists()).toBe(true)

    expect(componentSource).toContain('height: calc(100vh - 64px - 4rem);')
    expect(componentSource).toContain('@media (max-width: 1023px)')
    expect(componentSource).toContain('.admin-task-page__worklist-content > :deep(*)')
  })
})
