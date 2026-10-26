import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'
import PricingEntryCard from '../PricingEntryCard.vue'
import ModelTagInput from '../ModelTagInput.vue'
import channelsAPI from '@/api/admin/channels'
import type { PricingFormEntry } from '../types'

vi.mock('@/api/admin/channels', () => ({ default: { getModelDefaultPricing: vi.fn() } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

const getPricing = vi.mocked(channelsAPI.getModelDefaultPricing)
const price = { found: true, input_price: 3e-6, output_price: 15e-6, cache_write_price: 4e-6, cache_read_price: 0, image_output_price: 7e-6 }
const priceFields = ['input_price', 'output_price', 'cache_write_price', 'cache_read_price', 'image_output_price', 'per_request_price'] as const

function entry(models: string[] = []): PricingFormEntry {
  return { models, billing_mode: 'token', input_price: null, output_price: null,
    cache_write_price: null, cache_read_price: null, image_output_price: null,
    per_request_price: null, intervals: [] }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

// Match ChannelsView: update replaces the parent-owned entry, and the actual
// card receives the new prop before a deferred default-price response arrives.
function render(initial = entry()) {
  const current = ref(initial)
  const platform = ref('anthropic')
  const updates = vi.fn((value: PricingFormEntry) => { current.value = value })
  const wrapper = mount(defineComponent({ setup: () => () => h(PricingEntryCard, {
    entry: current.value, platform: platform.value, onUpdate: updates
  }) }))
  const card = wrapper.getComponent(PricingEntryCard)
  async function setModels(models: string[]) {
    card.getComponent(ModelTagInput).vm.$emit('update:models', models)
    await nextTick()
    await nextTick()
  }
  async function addModel(model = 'claude-opus-4-8') {
    const input = card.getComponent(ModelTagInput).get('input')
    await input.setValue(model)
    await input.trigger('keydown', { key: 'Enter' })
    await nextTick()
  }
  return { wrapper, card, current, platform, updates, setModels, addModel }
}

beforeEach(() => {
  getPricing.mockReset()
  getPricing.mockResolvedValue(price)
})

describe('PricingEntryCard per-model defaults and pending reads', () => {
  it('populates the first single model through real tag input and preserves price units', async () => {
    const view = render()
    await view.addModel()
    await flushPromises()
    expect(getPricing).toHaveBeenCalledTimes(1)
    expect(getPricing).toHaveBeenCalledWith('claude-opus-4-8')
    expect(view.current.value).toEqual({ ...entry(['claude-opus-4-8']), input_price: 3,
      output_price: 15, cache_write_price: 4, cache_read_price: 0, image_output_price: 7 })
  })

  it('preserves NULL prices when appending a model to an existing default-priced entry', async () => {
    const view = render(entry(['claude-opus-4-7']))
    await view.addModel()
    await flushPromises()
    expect(getPricing).not.toHaveBeenCalled()
    expect(view.current.value).toEqual(entry(['claude-opus-4-7', 'claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('preserves individual model defaults when an empty entry receives multiple models', async () => {
    const view = render()
    await view.setModels(['claude-opus-4-7', 'claude-opus-4-8'])
    await flushPromises()
    expect(getPricing).not.toHaveBeenCalled()
    expect(view.current.value).toEqual(entry(['claude-opus-4-7', 'claude-opus-4-8']))
  })

  it('does not request a default when all models are cleared', async () => {
    const view = render(entry(['old']))
    await view.setModels([])
    expect(getPricing).not.toHaveBeenCalled()
    expect(view.current.value).toEqual(entry())
  })

  it.each(priceFields.flatMap(field => [0, '1.25'].map(value => ({ field, value }))))(
    'preserves configured $field=$value when adding the first model', async ({ field, value }) => {
      const configured = { ...entry(), [field]: value }
      const view = render(configured)
      await view.addModel()
      await flushPromises()
      expect(getPricing).not.toHaveBeenCalled()
      expect(view.current.value).toEqual({ ...configured, models: ['claude-opus-4-8'] })
    })

  it('preserves an existing tier configuration', async () => {
    const configured = { ...entry(), intervals: [{ min_tokens: 0, max_tokens: null,
      tier_label: 'HD', input_price: null, output_price: null, cache_write_price: null,
      cache_read_price: null, per_request_price: 0, sort_order: 0 }] }
    const view = render(configured)
    await view.addModel()
    await flushPromises()
    expect(getPricing).not.toHaveBeenCalled()
    expect(view.current.value).toEqual({ ...configured, models: ['claude-opus-4-8'] })
  })

  it.each(['not found', 'network error'])('keeps the model edit on %s', async outcome => {
    if (outcome === 'not found') getPricing.mockResolvedValue({ found: false })
    else getPricing.mockRejectedValue(new Error('offline'))
    const view = render()
    await view.addModel()
    await flushPromises()
    expect(getPricing).toHaveBeenCalledTimes(1)
    expect(view.current.value).toEqual(entry(['claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it.each(priceFields)('does not overwrite a pending manual $field edit, including explicit free', async field => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.current.value[field] = 0
    await nextTick()
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual({ ...entry(['claude-opus-4-8']), [field]: 0 })
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('does not revive a read after price edit and clear return to the same values', async () => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.current.value.input_price = 9
    await nextTick()
    view.current.value.input_price = null
    await nextTick()
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual(entry(['claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it.each(['remove', 'append', 'replace'])('does not restore old models after a pending %s edit', async change => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    const models = change === 'remove' ? [] : change === 'append' ? ['claude-opus-4-8', 'new'] : ['replacement']
    await view.setModels(models)
    const calls = view.updates.mock.calls.length
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual(entry(models))
    expect(view.updates).toHaveBeenCalledTimes(calls)
  })

  it.each(['per_request', 'image'] as const)('keeps a changed %s billing mode and its pending edits', async mode => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.current.value.billing_mode = mode
    await nextTick()
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual({ ...entry(['claude-opus-4-8']), billing_mode: mode })
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('keeps an interval edited while the default read is pending', async () => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.current.value.intervals.push({ min_tokens: 0, max_tokens: null, tier_label: '',
      input_price: 0, output_price: 1, cache_write_price: null, cache_read_price: null,
      per_request_price: null, sort_order: 0 })
    await nextTick()
    const saved = JSON.parse(JSON.stringify(view.current.value))
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual(saved)
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('does not apply a previous platform default to a reused card', async () => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.platform.value = 'openai'
    await nextTick()
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual(entry(['claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('does not price an identically shaped replacement entry', async () => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.current.value = entry(['claude-opus-4-8'])
    await nextTick()
    pending.resolve(price)
    await flushPromises()
    expect(view.current.value).toEqual(entry(['claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it.each(['success', 'error'])('ignores a pending %s after unmount', async outcome => {
    const pending = deferred<typeof price>()
    getPricing.mockReturnValue(pending.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    view.wrapper.unmount()
    if (outcome === 'success') pending.resolve(price)
    else pending.reject(new Error('late'))
    await flushPromises()
    expect(view.current.value).toEqual(entry(['claude-opus-4-8']))
    expect(view.updates).toHaveBeenCalledTimes(1)
  })

  it('keeps the newer read when cleared and the same model is added again', async () => {
    const old = deferred<typeof price>()
    const current = deferred<typeof price>()
    getPricing.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const view = render()
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(1)
    await view.setModels([])
    await view.addModel()
    expect(getPricing).toHaveBeenCalledTimes(2)
    current.resolve({ ...price, input_price: 8e-6 })
    await flushPromises()
    const saved = JSON.parse(JSON.stringify(view.current.value))
    const calls = view.updates.mock.calls.length
    old.resolve(price)
    await flushPromises()
    expect(view.current.value.input_price).toBe(8)
    expect(view.current.value).toEqual(saved)
    expect(view.updates).toHaveBeenCalledTimes(calls)
  })
})
