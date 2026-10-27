import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  duplicate,
  createGroup,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates,
  showError,
  showSuccess
} = vi.hoisted(() => ({
    listGroups: vi.fn(),
    duplicate: vi.fn(),
    createGroup: vi.fn(),
    getUsageSummary: vi.fn(),
    getCapacitySummary: vi.fn(),
    getModelsListCandidates: vi.fn(),
    showError: vi.fn(),
    showSuccess: vi.fn()
  }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate,
      getUsageSummary,
      getCapacitySummary,
      getModelsListCandidates,
      create: createGroup,
      update: vi.fn(),
      delete: vi.fn(),
      getAll: vi.fn(),
      updateSortOrder: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(), nextStep: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.name ? `${key}:${params.name}` : key
    })
  }
})

const DataTableStub = {
  props: ['data'],
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
}

function mountView() {
  return mount(GroupsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        EmptyState: true,
        PlatformIcon: true,
        Icon: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        GroupCapacityBadge: true,
        VueDraggable: true
      }
    }
  })
}

const mounted: ReturnType<typeof mountView>[] = []
afterEach(() => {
  mounted.splice(0).forEach((wrapper) => wrapper.unmount())
})

beforeEach(() => {
  vi.clearAllMocks()
  listGroups.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
  getUsageSummary.mockResolvedValue([])
  getCapacitySummary.mockResolvedValue([])
  getModelsListCandidates.mockResolvedValue([])
  createGroup.mockResolvedValue({ id: 99 })
})

async function openCreate() {
  const wrapper = mountView()
  mounted.push(wrapper)
  await flushPromises()
  const open = wrapper.findAll('button').find((button) => button.text().trim() === 'admin.groups.createGroup')!
  await open.trigger('click')
  await wrapper.get('[data-tour="group-form-name"]').setValue('Dispatch contract')
  return wrapper
}

async function choosePlatform(wrapper: ReturnType<typeof mountView>, label: string) {
  const select = wrapper.get('[data-tour="group-form-platform"]')
  await select.get('button[aria-label="Select option"]').trigger('click')
  await flushPromises()
  const option = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]')).find((node) => node.textContent?.trim() === label)!
  expect(option).toBeDefined()
  option.click()
  await flushPromises()
}

async function enableDispatch(wrapper: ReturnType<typeof mountView>) {
  const label = wrapper.findAll('label').find((node) => node.text() === 'admin.groups.openaiMessages.allowDispatch')!
  label.element.parentElement!.querySelector('button')!.click()
  await flushPromises()
}

const familyInput = (wrapper: ReturnType<typeof mountView>, family: string) => wrapper.get(`input[placeholder="admin.groups.openaiMessages.${family}ModelPlaceholder"]`)

describe('GroupsView create Grok Messages platform transitions', () => {
  it('submits current OpenAI family defaults after selecting Grok then OpenAI', async () => {
    const wrapper = await openCreate()
    await choosePlatform(wrapper, 'Grok')
    await enableDispatch(wrapper)
    expect((familyInput(wrapper, 'opus').element as HTMLInputElement).value).toBe('grok-4.3')
    await choosePlatform(wrapper, 'OpenAI')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({
      platform: 'openai', allow_messages_dispatch: true,
      messages_dispatch_model_config: { opus_mapped_model: 'gpt-5.4', sonnet_mapped_model: 'gpt-5.3-codex', haiku_mapped_model: 'gpt-5.4-mini', exact_model_mappings: {} },
    }))
  })

  it('preserves an explicit family value, exact row conversion and enabled dispatch while correcting only two automatic defaults', async () => {
    const wrapper = await openCreate()
    await choosePlatform(wrapper, 'Grok')
    await enableDispatch(wrapper)
    await familyInput(wrapper, 'opus').setValue(' custom-explicit-model ')
    const add = wrapper.findAll('button').find((button) => button.text().trim() === 'admin.groups.openaiMessages.addExactMapping')!
    await add.trigger('click')
    await wrapper.get('input[placeholder="admin.groups.openaiMessages.claudeModelPlaceholder"]').setValue(' claude-custom-exact ')
    await wrapper.get('input[placeholder="admin.groups.openaiMessages.targetModelPlaceholder"]').setValue(' custom-exact-target ')
    // A second empty row remains ignored by the existing converter.
    await wrapper.findAll('button').find((button) => button.text().trim() === 'admin.groups.openaiMessages.addExactMapping')!.trigger('click')
    await choosePlatform(wrapper, 'OpenAI')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({
      platform: 'openai', allow_messages_dispatch: true,
      messages_dispatch_model_config: { opus_mapped_model: 'custom-explicit-model', sonnet_mapped_model: 'gpt-5.3-codex', haiku_mapped_model: 'gpt-5.4-mini', exact_model_mappings: { 'claude-custom-exact': 'custom-exact-target' } },
    }))
  })

  it('retains routable Grok defaults when the submitted create platform stays Grok', async () => {
    const wrapper = await openCreate()
    await choosePlatform(wrapper, 'Grok')
    await enableDispatch(wrapper)
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({ platform: 'grok', allow_messages_dispatch: true, messages_dispatch_model_config: { opus_mapped_model: 'grok-4.3', sonnet_mapped_model: 'grok-4.3', haiku_mapped_model: 'grok-4.3', exact_model_mappings: {} } }))
  })
})
