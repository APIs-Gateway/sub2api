import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PaymentMethodSelector from '../PaymentMethodSelector.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('PaymentMethodSelector', () => {
  it('wraps a full method roster without letting labels widen the selector', () => {
    const methods = [
      'easypay',
      'crypto',
      'alipay',
      'wxpay',
      'stripe',
      'airwallex',
      'card',
      'link',
      'alipay_direct',
      'wxpay_direct',
    ].map((type) => ({ type, fee_rate: 0, available: true }))

    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'alipay',
        methods,
      },
    })

    const grid = wrapper.get('[data-testid="payment-method-grid"]')
    expect(grid.classes()).toEqual(expect.arrayContaining(['grid', 'sm:grid-cols-3', 'lg:grid-cols-4']))
    expect(grid.classes()).not.toContain('sm:flex')

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(methods.length)
    expect(buttons.every((button) => button.classes().includes('min-w-0'))).toBe(true)
    expect(
      buttons.every((button, index) => button.attributes('title') === `payment.methods.${methods[index].type}`),
    ).toBe(true)
    expect(
      wrapper.findAll('[data-testid="payment-method-label"]').every((label) => label.classes().includes('truncate')),
    ).toBe(true)
  })

  it('emits select when an available method button is clicked', async () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'alipay',
        methods: [
          { type: 'alipay', fee_rate: 0, available: true },
          { type: 'wxpay', fee_rate: 0, available: false },
        ],
      },
    })

    const buttons = wrapper.findAll('button')
    await buttons[1].trigger('click')
    expect(wrapper.emitted('select')).toBeUndefined()

    await buttons[0].trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual(['alipay'])
  })
})
