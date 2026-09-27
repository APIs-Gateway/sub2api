import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

const mocks = vi.hoisted(() => ({
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  syncUpstreamModels: vi.fn()
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  accountsAPI: { syncUpstreamModels: mocks.syncUpstreamModels }
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values?: Record<string, string>) =>
      key === 'admin.accounts.modelMappingConflict'
        ? `${values?.from} → ${values?.to}`
        : key
  })
}))

const mountSelector = (props: Record<string, unknown> = {}) => mount(ModelWhitelistSelector, {
  props: { modelValue: [], platform: 'openai', ...props },
  global: { stubs: { ModelIcon: true, Icon: true } }
})

const openaiModel = 'gpt-6'

describe('ModelWhitelistSelector mapping conflicts', () => {
  beforeEach(() => vi.clearAllMocks())

  it('rejects a conflicting custom model and explains the mapping target', async () => {
    const wrapper = mountSelector({ modelMappings: [{ from: 'gpt-latest', to: 'deepseek-chat' }] })
    await wrapper.get('input[placeholder="admin.accounts.enterCustomModelName"]').setValue(' gpt-latest ')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addModel')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(mocks.showInfo).toHaveBeenCalledWith('gpt-latest → deepseek-chat')
  })

  it('allows identity mappings and models without a mapping', async () => {
    const wrapper = mountSelector({ modelMappings: [{ from: 'gpt-latest', to: 'gpt-latest' }] })
    await wrapper.get('input[placeholder="admin.accounts.enterCustomModelName"]').setValue('gpt-latest')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.addModel')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-latest']]])
    expect(mocks.showInfo).not.toHaveBeenCalled()
  })

  it('blocks a conflicting dropdown selection', async () => {
    const model = openaiModel
    const wrapper = mountSelector({ modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.get('div.cursor-pointer').trigger('click')
    await wrapper.findAll('button').find(button => button.text().includes(model))!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })

  it('skips conflicting related models while retaining other models', async () => {
    const model = openaiModel
    const wrapper = mountSelector({ modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.fillRelatedModels')!.trigger('click')
    const emitted = wrapper.emitted('update:modelValue')?.[0]?.[0] as string[]
    expect(emitted).not.toContain(model)
    expect(emitted.length).toBeGreaterThan(0)
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })

  it('skips conflicting upstream models and keeps nonconflicting sync results', async () => {
    const model = openaiModel
    mocks.syncUpstreamModels.mockResolvedValue({ models: [model, 'unmapped-model'] })
    const wrapper = mountSelector({ accountId: 42, modelMappings: [{ from: model, to: 'different-target' }] })
    await wrapper.findAll('button').find(button => button.text() === 'admin.accounts.syncUpstreamModels')!.trigger('click')
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([[['unmapped-model']]])
    expect(mocks.showInfo).toHaveBeenCalledWith(`${model} → different-target`)
  })
})
