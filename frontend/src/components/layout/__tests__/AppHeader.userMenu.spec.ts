import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppHeader from '../AppHeader.vue'

const stores = vi.hoisted(() => ({
  app: {
    cachedPublicSettings: null as Record<string, unknown> | null,
    contactInfo: '',
    docUrl: '',
    toggleMobileSidebar: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn(),
  },
  auth: {
    isAdmin: false,
    isSimpleMode: false,
    logout: vi.fn(),
    user: { id: 10086, email: 'user@example.com', role: 'user', username: 'User' } as Record<string, unknown> | null,
  },
  adminSettings: { customMenuItems: [] },
  onboarding: { replay: vi.fn() },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => stores.app,
  useAuthStore: () => stores.auth,
  useOnboardingStore: () => stores.onboarding,
}))

// The clipboard composable reads the app store (toasts) through its own module path.
vi.mock('@/stores/app', () => ({
  useAppStore: () => stores.app,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => stores.adminSettings,
}))

vi.mock('@/composables/useCurrencyDisplay', () => ({
  useCurrencyDisplay: () => ({ canSwitch: { value: false } }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ meta: {}, name: 'Dashboard', params: {}, path: '/dashboard' }),
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
  }
})

const defaultUser = { id: 10086, email: 'user@example.com', role: 'user', username: 'User' }

async function openMenu(): Promise<VueWrapper> {
  const wrapper = mount(AppHeader, {
    attachTo: document.body,
    global: {
      // the administrator menu also renders a $t() label (restart tour)
      mocks: { $t: (key: string) => key },
      stubs: {
        AnnouncementBell: true,
        CurrencyModeSwitch: true,
        Icon: true,
        LocaleSwitcher: true,
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        SubscriptionProgressMini: true,
      },
    },
  })
  await wrapper.find('button[aria-label="common.userMenu"]').trigger('click')
  return wrapper
}

describe('account menu: user ID', () => {
  let writeText: ReturnType<typeof vi.fn>

  beforeEach(() => {
    stores.auth.user = { ...defaultUser }
    stores.auth.isSimpleMode = false
    stores.app.cachedPublicSettings = null
    writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(window, 'isSecureContext', { value: true, writable: true, configurable: true })
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, writable: true, configurable: true })
  })

  afterEach(() => {
    vi.clearAllMocks()
    document.body.innerHTML = ''
    if ('execCommand' in document) {
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('shows the user ID under the email', async () => {
    const wrapper = await openMenu()

    const row = wrapper.find('[data-testid="copy-user-id"]')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('nav.userId')
    expect(row.text()).toContain('10086')

    wrapper.unmount()
  })

  it('copies the ID on click, says so, and flips the icon to a check', async () => {
    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="copy-user-id"] icon-stub').attributes('name')).toBe('copy')
    await wrapper.find('[data-testid="copy-user-id"]').trigger('click')
    await flushPromises()

    expect(writeText).toHaveBeenCalledWith('10086')
    expect(stores.app.showSuccess).toHaveBeenCalledWith('nav.userIdCopied')
    expect(stores.app.showError).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="copy-user-id"] icon-stub').attributes('name')).toBe('check')
    // the menu stays open so the confirmation is visible
    expect(wrapper.find('[data-testid="copy-user-id"]').exists()).toBe(true)

    wrapper.unmount()
  })

  it('reports a failed copy instead of pretending it worked', async () => {
    writeText.mockRejectedValue(new Error('denied'))
    const wrapper = await openMenu()

    await wrapper.find('[data-testid="copy-user-id"]').trigger('click')
    await flushPromises()

    expect(stores.app.showError).toHaveBeenCalledTimes(1)
    expect(stores.app.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="copy-user-id"] icon-stub').attributes('name')).toBe('copy')

    wrapper.unmount()
  })

  it('reports a failed copy when the browser has no clipboard access at all', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, writable: true, configurable: true })
    const wrapper = await openMenu()

    await wrapper.find('[data-testid="copy-user-id"]').trigger('click')
    await flushPromises()

    expect(stores.app.showError).toHaveBeenCalledTimes(1)
    expect(stores.app.showSuccess).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('has no ID row when the account has no ID yet', async () => {
    stores.auth.user = { email: 'user@example.com', role: 'user', username: 'User' }

    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="copy-user-id"]').exists()).toBe(false)

    wrapper.unmount()
  })
})

describe('account menu: top up', () => {
  beforeEach(() => {
    stores.auth.user = { ...defaultUser }
    stores.auth.isSimpleMode = false
    stores.app.cachedPublicSettings = null
  })

  afterEach(() => {
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('links to the purchase page', async () => {
    stores.app.cachedPublicSettings = { payment_enabled: true }

    const wrapper = await openMenu()

    const entry = wrapper.find('[data-testid="account-menu-topup"]')
    expect(entry.exists()).toBe(true)
    expect(entry.attributes('href')).toBe('/purchase')
    expect(entry.text()).toContain('nav.topUp')

    wrapper.unmount()
  })

  it('is shown while public settings have not loaded (payment is on by default)', async () => {
    stores.app.cachedPublicSettings = null

    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="account-menu-topup"]').exists()).toBe(true)

    wrapper.unmount()
  })

  it('is hidden when payment is switched off', async () => {
    stores.app.cachedPublicSettings = { payment_enabled: false }

    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="account-menu-topup"]').exists()).toBe(false)
    // the rest of the menu is still there
    expect(wrapper.findAll('.dropdown-item').length).toBeGreaterThan(0)

    wrapper.unmount()
  })

  it('is hidden in simple mode, like the sidebar recharge entry', async () => {
    stores.app.cachedPublicSettings = { payment_enabled: true }
    stores.auth.isSimpleMode = true

    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="account-menu-topup"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('keeps profile and API keys in the menu next to it', async () => {
    const wrapper = await openMenu()

    const hrefs = wrapper.findAll('.dropdown a').map((a) => a.attributes('href'))
    expect(hrefs).toEqual(['/profile', '/purchase', '/keys'])

    wrapper.unmount()
  })
})

describe('account menu for administrators', () => {
  beforeEach(() => {
    stores.auth.user = { ...defaultUser, role: 'admin', username: 'Admin' }
    stores.auth.isAdmin = true
    stores.auth.isSimpleMode = false
    stores.app.cachedPublicSettings = { payment_enabled: true }
  })

  afterEach(() => {
    stores.auth.isAdmin = false
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('shows the user ID and the top-up entry too, next to profile', async () => {
    const wrapper = await openMenu()

    expect(wrapper.find('[data-testid="copy-user-id"]').text()).toContain('10086')
    expect(wrapper.find('[data-testid="account-menu-topup"]').attributes('href')).toBe('/purchase')
    const hrefs = wrapper.findAll('.dropdown a').map((a) => a.attributes('href'))
    expect(hrefs).toContain('/profile')
    expect(hrefs).toContain('/purchase')

    wrapper.unmount()
  })
})
