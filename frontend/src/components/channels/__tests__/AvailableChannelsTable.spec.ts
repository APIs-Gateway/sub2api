import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AvailableChannelsTable from '../AvailableChannelsTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AvailableChannelsTable.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('AvailableChannelsTable scroll integration', () => {
  it('renders the table inside the runtime scroll wrapper', () => {
    const wrapper = mount(AvailableChannelsTable, {
      props: {
        columns: {
          name: 'Name',
          description: 'Description',
          platform: 'Platform',
          groups: 'Groups',
          supportedModels: 'Models',
        },
        rows: [],
        loading: false,
        pricingKeyPrefix: 'pricing',
        noPricingLabel: 'No pricing',
        noModelsLabel: 'No models',
        emptyLabel: 'No channels',
        userGroupRates: {},
      },
      global: {
        stubs: {
          Icon: true,
          PlatformIcon: true,
          GroupBadge: true,
          SupportedModelChip: true,
        },
      },
    })

    expect(wrapper.find('.table-wrapper').exists()).toBe(true)
    expect(wrapper.find('table').exists()).toBe(true)
  })

  it('mounts the table on the .table-wrapper scroll hook', () => {
    expect(componentSource).toMatch(/<div class="table-wrapper">\s*<table/)
  })

  it('does not clip content with its own overflow-hidden card wrapper', () => {
    expect(componentSource).not.toMatch(/<div class="card overflow-hidden">/)
  })
})
