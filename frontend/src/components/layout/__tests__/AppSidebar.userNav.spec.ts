import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppSidebar from '../AppSidebar.vue'

type Dict = Record<string, unknown>

const state = vi.hoisted(() => ({
  route: { path: '/dashboard' },
  app: {
    sidebarCollapsed: false,
    mobileOpen: false,
    backendModeEnabled: false,
    publicSettingsLoaded: true,
    siteName: 'Site',
    siteLogo: '',
    sidebarScrollTop: 0,
    cachedPublicSettings: null as Dict | null,
    toggleSidebar: vi.fn(),
    setMobileOpen: vi.fn(),
  },
  auth: { isAdmin: false, isSimpleMode: false },
  adminSettings: {
    opsMonitoringEnabled: true,
    paymentEnabled: true,
    customMenuItems: [] as Dict[],
    fetch: vi.fn(),
  },
  onboarding: {
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
    getDriverInstance: vi.fn((): unknown => null),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => state.app,
  useAuthStore: () => state.auth,
  useAdminSettingsStore: () => state.adminSettings,
  useOnboardingStore: () => state.onboarding,
}))

// Feature flags read the app store through its own module path.
vi.mock('@/stores/app', () => ({
  useAppStore: () => state.app,
}))

vi.mock('vue-router', () => ({
  useRoute: () => state.route,
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const allFlagsOn = {
  payment_enabled: true,
  available_channels_enabled: true,
  channel_monitor_enabled: true,
  risk_control_enabled: true,
}

const customItem = (id: string, label: string, visibility: 'user' | 'admin', sort_order: number) => ({
  id,
  label,
  visibility,
  sort_order,
  icon_svg: '',
  url: 'https://example.com',
})

function mountSidebar(slots: Record<string, string> = {}): VueWrapper {
  return mount(AppSidebar, {
    slots,
    global: {
      stubs: {
        BrandMark: true,
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      },
    },
  })
}

function navLinks(wrapper: VueWrapper): string[] {
  return wrapper.findAll('nav a').map((el) => el.attributes('href') as string)
}

/** Document-order outline of everything the nav renders: links, group buttons, section titles. */
function outline(wrapper: VueWrapper): string[] {
  const nav = wrapper.find('nav').element
  return Array.from(nav.querySelectorAll('a, button.sidebar-link, .sidebar-section-title-text')).map((el) => {
    if (el.tagName === 'A') return `link ${el.getAttribute('href')}`
    if (el.tagName === 'BUTTON') return `group-button ${el.textContent?.trim()}`
    return `title ${el.textContent?.trim()}`
  })
}

beforeEach(() => {
  // The sidebar reads the OS colour scheme once when it is set up.
  window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as unknown as typeof window.matchMedia
  state.route.path = '/dashboard'
  state.app.sidebarCollapsed = false
  state.app.backendModeEnabled = false
  state.app.cachedPublicSettings = { ...allFlagsOn, custom_menu_items: [] }
  state.auth.isAdmin = false
  state.auth.isSimpleMode = false
  state.adminSettings.opsMonitoringEnabled = true
  state.adminSettings.paymentEnabled = true
  state.adminSettings.customMenuItems = []
  state.onboarding.getDriverInstance.mockReturnValue(null)
})

afterEach(() => {
  vi.clearAllMocks()
})

// The user sidebar is the same flat list it was before the rework: no titles, no folding.
// These expectations also pass against the previous AppSidebar.vue.
describe('user sidebar menu', () => {
  const fullMenu = [
    '/dashboard',
    '/keys',
    '/usage',
    '/available-channels',
    '/monitor',
    '/subscriptions',
    '/purchase',
    '/orders',
    '/redeem',
    '/points',
    '/docs',
    '/profile',
  ]

  it('renders the flat menu in its original order, with no titles or fold buttons', () => {
    const wrapper = mountSidebar()

    expect(navLinks(wrapper)).toEqual(fullMenu)
    expect(outline(wrapper)).toEqual(fullMenu.map((path) => `link ${path}`))
    expect(wrapper.find('nav button').exists()).toBe(false)
    expect(wrapper.find('nav .sidebar-section-title').exists()).toBe(false)
    expect(wrapper.find('[data-nav-group]').exists()).toBe(false)
  })

  it('puts every link in one plain section directly under the nav', () => {
    const wrapper = mountSidebar()
    const nav = wrapper.find('nav').element

    const sections = Array.from(nav.children)
    expect(sections).toHaveLength(1)
    expect(sections[0].className).toBe('sidebar-section')
    for (const link of Array.from(nav.querySelectorAll('a'))) {
      expect(link.parentElement).toBe(sections[0])
    }
  })

  it('keeps the API keys link addressable for the first-run tour', () => {
    const wrapper = mountSidebar()

    expect(wrapper.findAll('[data-tour="sidebar-my-keys"]')).toHaveLength(1)
    expect(wrapper.find('[data-tour="sidebar-my-keys"]').attributes('href')).toBe('/keys')
  })

  it('hides payment entries when payment is switched off and keeps the rest', () => {
    state.app.cachedPublicSettings = { ...allFlagsOn, payment_enabled: false, custom_menu_items: [] }

    const wrapper = mountSidebar()

    expect(navLinks(wrapper)).toEqual(fullMenu.filter((path) => path !== '/purchase' && path !== '/orders'))
  })

  it('hides price and status entries when their switches are off', () => {
    state.app.cachedPublicSettings = {
      ...allFlagsOn,
      available_channels_enabled: false,
      channel_monitor_enabled: false,
      custom_menu_items: [],
    }

    const wrapper = mountSidebar()

    expect(navLinks(wrapper)).toEqual(fullMenu.filter((path) => path !== '/available-channels' && path !== '/monitor'))
  })

  it('applies the same tolerance as before while public settings have not loaded', () => {
    state.app.cachedPublicSettings = null

    const wrapper = mountSidebar()

    // opt-out flags stay visible, the opt-in price page stays hidden
    expect(navLinks(wrapper)).toEqual(fullMenu.filter((path) => path !== '/available-channels'))
  })

  it('hides the entries that simple mode removes', () => {
    state.auth.isSimpleMode = true

    const wrapper = mountSidebar()

    expect(navLinks(wrapper)).toEqual(['/dashboard', '/keys', '/monitor', '/docs', '/profile'])
  })

  it('does not render the user menu in backend mode', () => {
    state.app.backendModeEnabled = true

    const wrapper = mountSidebar()

    expect(wrapper.findAll('nav a')).toHaveLength(0)
  })
})

describe('custom menu items in the user sidebar', () => {
  beforeEach(() => {
    state.app.cachedPublicSettings = {
      ...allFlagsOn,
      custom_menu_items: [
        customItem('late', 'Late page', 'user', 20),
        customItem('early', 'Early page', 'user', 5),
        customItem('ops-only', 'Admin only page', 'admin', 1),
      ],
    }
  })

  it('trails the built-in entries, in sort order, in the same list', () => {
    const wrapper = mountSidebar()
    const links = navLinks(wrapper)

    expect(links.slice(-3)).toEqual(['/profile', '/custom/early', '/custom/late'])
    expect(links).not.toContain('/custom/ops-only')
    expect(wrapper.find('nav .sidebar-section').findAll('a')).toHaveLength(links.length)
  })

  it('keeps showing custom items in simple mode, still last', () => {
    state.auth.isSimpleMode = true

    const wrapper = mountSidebar()

    expect(navLinks(wrapper).slice(-2)).toEqual(['/custom/early', '/custom/late'])
  })
})

// B1 of the first review: a clipping wrapper around the links cut the keyboard focus ring off.
// jsdom does not lay out boxes or compute outline clipping, so what can be pinned here is the
// structure that keeps the ring unclipped: the links sit in a plain section directly under the
// nav, and that section does not clip overflow. The rendered ring is compared against main in a
// real browser (see the PR description).
describe('sidebar links keep the browser focus ring', () => {
  const globalCss = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css'), 'utf8')
  const rule = (css: string, selector: string): string => {
    const start = css.indexOf(`  ${selector} {`)
    expect(start, `${selector} rule exists`).toBeGreaterThan(-1)
    return css.slice(start, css.indexOf('}', start))
  }

  it('has no wrapper between the nav and the link list that could clip', () => {
    const wrapper = mountSidebar()
    const nav = wrapper.find('nav').element

    for (const link of Array.from(nav.querySelectorAll('a'))) {
      const chain: string[] = []
      for (let el = link.parentElement; el && el !== nav; el = el.parentElement) chain.push(el.className)
      expect(chain).toEqual(['sidebar-section'])
    }
  })

  it('keeps the nav padding that gives the ring room, and no clipping on the section', () => {
    expect(rule(globalCss, '.sidebar-nav')).toMatch(/px-3/)
    expect(rule(globalCss, '.sidebar-section')).not.toMatch(/overflow/)
  })
})

describe('balance card mount point', () => {
  it('renders nothing between the menu and the bottom block when no card is slotted in', () => {
    const wrapper = mountSidebar()

    const nav = wrapper.find('nav').element
    expect(nav.nextElementSibling?.className).toContain('mt-auto')
  })

  it('renders a slotted card for users, below the menu, with the sidebar state', () => {
    const wrapper = mount(AppSidebar, {
      slots: { 'balance-card': '<template #balance-card="{ collapsed }"><div data-testid="card">{{ collapsed }}</div></template>' },
      global: {
        stubs: {
          BrandMark: true,
          RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        },
      },
    })

    const card = wrapper.find('[data-testid="card"]')
    expect(card.exists()).toBe(true)
    expect(card.text()).toBe('false')
    expect(wrapper.find('nav').element.nextElementSibling).toBe(card.element)
  })

  it('does not render the slot for administrators', () => {
    state.auth.isAdmin = true

    const wrapper = mountSidebar({ 'balance-card': '<div data-testid="card" />' })

    expect(wrapper.find('[data-testid="card"]').exists()).toBe(false)
  })
})

describe('admin sidebar is unchanged', () => {
  const adminOutline = [
    'link /admin/dashboard',
    'link /admin/ops',
    'link /admin/users',
    'link /admin/groups',
    'group-button nav.pricingConfig',
    'group-button nav.channelManagement',
    'link /admin/subscriptions',
    'link /admin/accounts',
    'link /admin/announcements',
    'link /admin/proxies',
    'link /admin/risk-control',
    'link /admin/prompt-audit',
    'link /admin/redeem',
    'link /admin/promo-codes',
    'group-button nav.affiliateManagement',
    'group-button nav.orderManagement',
    'link /admin/usage',
    'link /admin/settings',
    'link /custom/admin-page',
    'title nav.myAccount',
    'link /keys',
    'link /usage',
    'link /available-channels',
    'link /monitor',
    'link /subscriptions',
    'link /purchase',
    'link /orders',
    'link /redeem',
    'link /points',
    'link /docs',
    'link /profile',
    'link /custom/user-page',
  ]

  beforeEach(() => {
    state.auth.isAdmin = true
    state.adminSettings.customMenuItems = [customItem('admin-page', 'Admin page', 'admin', 1)]
    state.app.cachedPublicSettings = {
      ...allFlagsOn,
      custom_menu_items: [customItem('user-page', 'User page', 'user', 1)],
    }
  })

  it('renders the same admin menu, then the flat "my account" list, in the same order', () => {
    const wrapper = mountSidebar()

    expect(outline(wrapper)).toEqual(adminOutline)
  })

  it('applies the same gates to the "my account" list as before', () => {
    state.app.cachedPublicSettings = {
      ...allFlagsOn,
      payment_enabled: false,
      available_channels_enabled: false,
      channel_monitor_enabled: false,
      custom_menu_items: [],
    }
    state.adminSettings.opsMonitoringEnabled = false
    state.adminSettings.paymentEnabled = false

    const wrapper = mountSidebar()
    const personal = outline(wrapper).slice(outline(wrapper).indexOf('title nav.myAccount') + 1)

    expect(personal).toEqual([
      'link /keys',
      'link /usage',
      'link /subscriptions',
      'link /redeem',
      'link /points',
      'link /docs',
      'link /profile',
    ])
    expect(outline(wrapper)).not.toContain('link /admin/ops')
    expect(outline(wrapper)).not.toContain('group-button nav.orderManagement')
  })

  it('hides the personal section in simple mode and shows the keys link in the admin list', () => {
    state.auth.isSimpleMode = true

    const wrapper = mountSidebar()
    const lines = outline(wrapper)

    expect(lines).not.toContain('title nav.myAccount')
    expect(lines).toContain('link /keys')
    expect(lines.at(-2)).toBe('link /admin/settings')
    expect(lines.at(-1)).toBe('link /custom/admin-page')
  })
})
