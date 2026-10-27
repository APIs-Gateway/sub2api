import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { createPinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AvailableChannelsTable from '../AvailableChannelsTable.vue'
import type { UserAvailableChannel } from '@/api/channels'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

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

const rows: UserAvailableChannel[] = [
  {
    name: 'Primary channel',
    description: 'Fast and reliable access',
    platforms: [
      {
        platform: 'anthropic',
        groups: [
          {
            id: 1,
            name: 'Exclusive Pro',
            platform: 'anthropic',
            subscription_type: 'standard',
            rate_multiplier: 1.2,
            is_exclusive: true,
          },
          {
            id: 2,
            name: 'Public',
            platform: 'anthropic',
            subscription_type: 'standard',
            rate_multiplier: 1,
            is_exclusive: false,
          },
        ],
        supported_models: [{ name: 'claude-test', platform: 'anthropic', pricing: null }],
      },
    ],
  },
]

const baseProps = {
  columns: {
    name: 'Channel',
    description: 'Description',
    platform: 'Platform',
    groups: 'Groups and rates',
    supportedModels: 'Models and pricing',
  },
  rows,
  loading: false,
  pricingKeyPrefix: 'availableChannels.pricing',
  noPricingLabel: 'No pricing',
  noModelsLabel: 'No models',
  emptyLabel: 'No channels',
  userGroupRates: { 1: 0.8 },
}

function mountTable(props = {}) {
  return mount(AvailableChannelsTable, {
    props: { ...baseProps, ...props },
    global: {
      plugins: [createPinia()],
      stubs: {
        Icon: { props: ['name'], template: '<i :data-icon="name" />' },
        PlatformIcon: { template: '<i data-platform-icon />' },
        GroupBadge: {
          props: ['name', 'rateMultiplier', 'userRateMultiplier'],
          template:
            '<span data-group-badge>{{ name }}:{{ rateMultiplier }}:{{ userRateMultiplier }}</span>',
        },
        SupportedModelChip: {
          props: ['model', 'noPricingLabel'],
          template: '<span data-model-chip>{{ model.name }}:{{ noPricingLabel }}</span>',
        },
      },
    },
  })
}

describe('AvailableChannelsTable responsive surfaces', () => {
  it('keeps the five-column table as the desktop-only surface', () => {
    const wrapper = mountTable()
    const desktop = wrapper.get('[data-testid="desktop-channels"]')

    // TablePageLayout has a more-specific `display: table` rule for descendants,
    // so these display utilities must remain important at both breakpoints.
    expect(desktop.classes()).toContain('!hidden')
    expect(desktop.classes()).toContain('lg:!table')
    expect(desktop.findAll('thead th')).toHaveLength(5)
    expect(desktop.text()).toContain('Primary channel')
    expect(desktop.text()).toContain('Fast and reliable access')
    expect(desktop.findAll('[data-group-badge]')).toHaveLength(2)
    expect(desktop.get('[data-model-chip]').text()).toContain('claude-test:No pricing')
  })

  it('renders a mobile-only readable surface with groups, rates, and model pricing chips', () => {
    const wrapper = mountTable()
    const mobile = wrapper.get('[data-testid="mobile-channels"]')

    expect(mobile.classes()).toContain('lg:hidden')
    expect(mobile.classes()).toContain('overflow-x-hidden')
    expect(mobile.text()).toContain('Primary channel')
    expect(mobile.text()).toContain('Fast and reliable access')
    expect(mobile.text()).toContain('Groups and rates')
    expect(mobile.text()).toContain('Models and pricing')
    expect(mobile.text()).toContain('availableChannels.exclusive')
    expect(mobile.text()).toContain('availableChannels.public')
    expect(mobile.get('[data-group-badge]').text()).toBe('Exclusive Pro:1.2:0.8')
    expect(mobile.findAll('[data-group-badge]')).toHaveLength(2)
    expect(mobile.get('[data-model-chip]').text()).toBe('claude-test:No pricing')
    expect(mobile.findAll('.max-w-full')).not.toHaveLength(0)
  })

  it('keeps the mobile placeholders when a platform has no groups or models', () => {
    const wrapper = mountTable({
      rows: [
        {
          name: 'Fallback channel',
          description: '',
          platforms: [{ platform: 'openai', groups: [], supported_models: [] }],
        },
      ],
    })
    const mobile = wrapper.get('[data-testid="mobile-channels"]')

    expect(mobile.text()).toContain('Fallback channel')
    expect(mobile.text()).toContain('openai')
    expect(mobile.text()).toContain('No models')
    expect(mobile.findAll('dd')[0].text()).toBe('-')
  })

  it('provides loading and empty states on both responsive surfaces', async () => {
    const wrapper = mountTable({ loading: true, rows: [] })

    expect(wrapper.get('[data-testid="desktop-channels"] [data-icon="refresh"]')).toBeTruthy()
    expect(wrapper.get('[data-testid="mobile-loading"] [data-icon="refresh"]')).toBeTruthy()

    await wrapper.setProps({ loading: false })

    expect(wrapper.get('[data-testid="desktop-channels"]').text()).toContain('No channels')
    expect(wrapper.get('[data-testid="mobile-empty"]').text()).toContain('No channels')
  })
})
