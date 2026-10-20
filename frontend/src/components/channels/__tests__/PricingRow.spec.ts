import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PricingRow from '../PricingRow.vue'

describe('PricingRow', () => {
  it('shows the actual magnitude of a small per-request price', () => {
    const wrapper = mount(PricingRow, {
      props: { label: 'Per request', value: 1e-10, scale: 1, unit: '/ request' },
    })

    expect(wrapper.text()).toContain('$1e-10 / request')
  })
})
