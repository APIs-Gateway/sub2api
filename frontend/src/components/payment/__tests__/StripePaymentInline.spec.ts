import { describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

const { loadStripe, stripeElements, paymentElement } = vi.hoisted(() => ({
  loadStripe: vi.fn(),
  stripeElements: { create: vi.fn() },
  paymentElement: { mount: vi.fn(), on: vi.fn() },
}))

vi.mock('@stripe/stripe-js/pure', () => ({ loadStripe }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ resolve: vi.fn() }),
}))
vi.mock('@/api/payment', () => ({
  paymentAPI: { cancelOrder: vi.fn() },
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
}))

import StripePaymentInline from '../StripePaymentInline.vue'

describe('StripePaymentInline', () => {
  it('loads Stripe through the side-effect-free entry and mounts the payment element', async () => {
    const stripe = {
      elements: vi.fn().mockReturnValue(stripeElements),
      confirmPayment: vi.fn(),
    }
    loadStripe.mockReset().mockResolvedValue(stripe)
    stripeElements.create.mockReset().mockReturnValue(paymentElement)
    paymentElement.mount.mockReset()
    paymentElement.on.mockReset()

    const wrapper = shallowMount(StripePaymentInline, {
      props: {
        orderId: 42,
        amount: 100,
        clientSecret: 'pi_secret_42',
        publishableKey: 'pk_test',
        payAmount: 103,
      },
      global: {
        stubs: { Icon: true },
      },
    })

    await flushPromises()
    await flushPromises()

    expect(loadStripe).toHaveBeenCalledWith('pk_test')
    expect(stripe.elements).toHaveBeenCalledWith(expect.objectContaining({
      clientSecret: 'pi_secret_42',
    }))
    expect(paymentElement.mount).toHaveBeenCalled()
    wrapper.unmount()
  })
})
