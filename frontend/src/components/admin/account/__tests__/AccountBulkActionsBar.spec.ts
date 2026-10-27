import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountBulkActionsBar from '../AccountBulkActionsBar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}|${Object.values(params).join(',')}` : key
    })
  }
})

const mountBar = (props: Record<string, unknown> = {}) =>
  mount(AccountBulkActionsBar, {
    props: { selectedIds: [1, 2, 3], ...props } as any
  })

describe('AccountBulkActionsBar select-all-filtered banner', () => {
  it('does not show the banner by default', () => {
    const wrapper = mountBar()
    expect(wrapper.find('[data-testid="select-all-filtered-banner"]').exists()).toBe(false)
  })

  it('offers to select all filtered results with the page and total counts', async () => {
    const wrapper = mountBar({ pageSelectedCount: 20, totalCount: 245, canSelectAllFiltered: true })

    const banner = wrapper.get('[data-testid="select-all-filtered-banner"]')
    expect(banner.text()).toContain('admin.accounts.bulkActions.pageSelected|20')
    expect(banner.text()).toContain('admin.accounts.bulkActions.selectAllFiltered|245')

    await wrapper.get('[data-testid="select-all-filtered"]').trigger('click')
    expect(wrapper.emitted('select-all-filtered')).toHaveLength(1)
  })

  it('disables the button while the full result is loading', async () => {
    const wrapper = mountBar({ canSelectAllFiltered: true, totalCount: 245, selectingAllFiltered: true })

    const button = wrapper.get('[data-testid="select-all-filtered"]')
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(wrapper.emitted('select-all-filtered')).toBeUndefined()
  })

  it('states how many accounts are selected once all filtered results are selected', () => {
    const wrapper = mountBar({
      selectedIds: Array.from({ length: 245 }, (_, index) => index + 1),
      allFilteredSelected: true,
      totalCount: 245
    })

    const banner = wrapper.get('[data-testid="select-all-filtered-banner"]')
    expect(banner.text()).toContain('admin.accounts.bulkActions.allFilteredSelected|245')
    expect(wrapper.find('[data-testid="select-all-filtered"]').exists()).toBe(false)
  })
})
