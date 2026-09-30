import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import Select from '../Select.vue'
import ProxySelector from '../ProxySelector.vue'
import type { Proxy } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { testProxy: vi.fn() } } }))
enableAutoUnmount(afterEach)
afterEach(() => { document.body.innerHTML = '' })

describe('selectors disabled while open', () => {
  it('closes a teleported Select, clears its search, and reopens only when enabled', async () => {
    const wrapper = mount(Select, {
      attachTo: document.body,
      props: {
        modelValue: 1,
        searchable: true,
        options: [{ value: 1, label: 'One' }, { value: 2, label: 'Two' }]
      },
      global: { stubs: { Transition: true } }
    })

    await wrapper.get('.select-trigger').trigger('click')
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()
    const searchInput = document.body.querySelector<HTMLInputElement>('.select-search-input')!
    searchInput.value = 'Two'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(document.body.querySelectorAll('[role="option"]')).toHaveLength(1)
    await wrapper.setProps({ disabled: true })
    expect(wrapper.get('.select-trigger').attributes('aria-expanded')).toBe('false')
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await wrapper.get('.select-trigger').trigger('click')
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
    await wrapper.setProps({ disabled: false })
    await wrapper.get('.select-trigger').trigger('click')
    expect(document.body.querySelectorAll('[role="option"]')).toHaveLength(2)
  })

  it('closes a ProxySelector and clears its search until reenabled', async () => {
    const proxies = [1, 2].map(id => ({
      id, name: `Proxy ${id}`, host: 'localhost', port: 8080, protocol: 'http'
    } as Proxy))
    const wrapper = mount(ProxySelector, {
      props: { modelValue: null, proxies },
      global: { stubs: { Transition: true } }
    })

    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.get('.select-search-input').setValue('Proxy 2')
    expect(wrapper.find('.select-dropdown').exists()).toBe(true)
    expect(wrapper.findAll('.select-option')).toHaveLength(2)
    await wrapper.setProps({ disabled: true })
    expect(wrapper.find('.select-dropdown').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await wrapper.get('.select-trigger').trigger('click')
    expect(wrapper.find('.select-dropdown').exists()).toBe(false)
    await wrapper.setProps({ disabled: false })
    await wrapper.get('.select-trigger').trigger('click')
    expect(wrapper.get('.select-search-input').element).toHaveProperty('value', '')
    expect(wrapper.findAll('.select-option')).toHaveLength(3)
  })
})
