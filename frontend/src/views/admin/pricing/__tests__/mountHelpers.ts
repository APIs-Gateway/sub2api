import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { Component } from 'vue'
import { resetFiatDataMissingForTest } from '@/composables/useCurrencyDisplay'
import { useAppStore } from '@/stores/app'

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = { template: '<div><slot name="filters" /><slot name="table" /></div>' }

/**
 * @param recharge 1 元能买到多少美元额度；13 表示人民币口径（默认），1 表示站点没有充值倍率
 */
export async function mountPricingView(component: Component, options: { recharge?: number; officialRate?: number } = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)
  resetFiatDataMissingForTest()
  window.localStorage.removeItem('currency-display-mode')
  const app = useAppStore()
  app.cachedPublicSettings = {
    balance_recharge_multiplier: options.recharge ?? 13,
    official_price_cny_rate: options.officialRate ?? 7
  } as never

  const wrapper = mount(component, {
    global: {
      plugins: [pinia],
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        teleport: true,
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }
      }
    }
  })
  await flushPromises()
  return wrapper
}
