import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { list, getAll, showError } = vi.hoisted(() => ({
  list: vi.fn(),
  getAll: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    announcements: { list, create: vi.fn(), update: vi.fn(), delete: vi.fn() },
    groups: { getAll },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

import AnnouncementsView from '../AnnouncementsView.vue'

function mountView() {
  return mount(AnnouncementsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: { template: '<div data-test="table"><slot name="empty" /></div>' },
        EmptyState: {
          props: ['title', 'description', 'actionText'],
          template: '<div data-test="empty-state">{{ title }}|{{ description }}</div>',
        },
        Pagination: true,
        BaseDialog: true,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        AnnouncementTargetingEditor: true,
        AnnouncementReadStatusDialog: true,
      },
    },
  })
}

describe('admin AnnouncementsView empty state', () => {
  beforeEach(() => {
    list.mockReset()
    getAll.mockReset()
    showError.mockReset()
    list.mockResolvedValue({ items: [], total: 0, pages: 0, page: 1, page_size: 20 })
    getAll.mockResolvedValue([])
  })

  it('shows the create-first-announcement guidance instead of a load error for an empty list', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(list).toHaveBeenCalled()
    const empty = wrapper.get('[data-test="empty-state"]')
    expect(empty.text()).toContain('admin.announcements.createFirstAnnouncement')
    expect(empty.text()).not.toContain('admin.announcements.failedToLoad')
    expect(showError).not.toHaveBeenCalled()
  })
})
