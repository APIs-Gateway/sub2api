import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { invalidateAdminComplianceSession } from '@/utils/adminComplianceSession'

type NavigationGuard = (
  to: Record<string, unknown>,
  from: Record<string, unknown>,
  next: ReturnType<typeof vi.fn>
) => Promise<void>

const routerHarness = vi.hoisted(() => ({
  guard: null as NavigationGuard | null,
}))

const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  isAuthenticated: true,
  isAdmin: false,
  isSimpleMode: false,
  hasPendingAuthSession: false,
}))

const appStore = vi.hoisted(() => ({
  siteName: 'Sub2API',
  backendModeEnabled: false,
  publicSettingsLoaded: false,
  cachedPublicSettings: null as null | {
    payment_enabled?: boolean
    risk_control_enabled?: boolean
    custom_menu_items?: []
  },
  fetchPublicSettings: vi.fn(),
}))

const complianceStore = vi.hoisted(() => ({
  initialized: true,
  fetchStatus: vi.fn(),
  requireAcknowledgement: vi.fn(),
}))

vi.mock('vue-router', () => ({
  createWebHistory: vi.fn(() => ({})),
  createRouter: vi.fn(() => ({
    beforeEach: vi.fn((guard: NavigationGuard) => {
      routerHarness.guard = guard
    }),
    afterEach: vi.fn(),
    onError: vi.fn(),
  })),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))

vi.mock('@/stores/adminCompliance', () => ({
  useAdminComplianceStore: () => complianceStore,
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  }),
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  }),
}))

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function runGuard(meta: Record<string, unknown>, path: string) {
  if (!routerHarness.guard) {
    throw new Error('router guard was not registered')
  }

  const next = vi.fn()
  const navigation = routerHarness.guard(
    {
      path,
      fullPath: path,
      name: 'FeatureRoute',
      params: {},
      meta: { requiresAuth: true, ...meta },
    },
    {},
    next
  )
  return { navigation, next }
}

describe('feature route guard', () => {
  beforeAll(async () => {
    await import('@/router')
  })

  beforeEach(() => {
    authStore.isAuthenticated = true
    authStore.isAdmin = false
    authStore.isSimpleMode = false
    appStore.publicSettingsLoaded = false
    appStore.cachedPublicSettings = null
    appStore.fetchPublicSettings.mockReset()
    complianceStore.initialized = true
    complianceStore.fetchStatus.mockReset()
    complianceStore.requireAcknowledgement.mockReset()
  })

  it.each([
    ['payment', { requiresPayment: true }, { payment_enabled: true }],
    ['risk control', { requiresRiskControl: true }, { risk_control_enabled: true }],
  ])('allows %s routes when loaded settings explicitly enable them', async (_name, meta, settings) => {
    authStore.isAdmin = meta.requiresRiskControl === true
    appStore.cachedPublicSettings = settings
    appStore.publicSettingsLoaded = true

    const { navigation, next } = runGuard(meta, '/feature')
    await navigation

    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it.each([
    ['payment', { requiresPayment: true }, { payment_enabled: false }, '/dashboard'],
    [
      'risk control',
      { requiresRiskControl: true },
      { risk_control_enabled: false },
      '/admin/settings',
    ],
  ])('redirects when loaded settings explicitly disable %s', async (_name, meta, settings, target) => {
    authStore.isAdmin = meta.requiresRiskControl === true
    appStore.cachedPublicSettings = settings
    appStore.publicSettingsLoaded = true

    const { navigation, next } = runGuard(meta, '/feature')
    await navigation

    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith(target)
  })

  it.each([
    ['payment', { requiresPayment: true }, '/purchase'],
    ['risk control', { requiresRiskControl: true }, '/admin/risk-control'],
  ])('allows %s routes when the initial settings load fails', async (_name, meta, path) => {
    authStore.isAdmin = meta.requiresRiskControl === true
    appStore.fetchPublicSettings.mockResolvedValue(null)

    const { navigation, next } = runGuard(meta, path)
    await navigation

    expect(appStore.fetchPublicSettings).toHaveBeenCalledOnce()
    expect(appStore.publicSettingsLoaded).toBe(false)
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it('allows a feature route when the settings action rejects', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    appStore.fetchPublicSettings.mockRejectedValue(new Error('settings unavailable'))

    const { navigation, next } = runGuard({ requiresPayment: true }, '/purchase')
    await navigation

    expect(appStore.publicSettingsLoaded).toBe(false)
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it('waits for an in-flight settings load before deciding access', async () => {
    const deferred = createDeferred<{ payment_enabled: boolean }>()
    appStore.fetchPublicSettings.mockImplementation(async () => {
      const settings = await deferred.promise
      appStore.cachedPublicSettings = settings
      appStore.publicSettingsLoaded = true
      return settings
    })

    const { navigation, next } = runGuard({ requiresPayment: true }, '/purchase')

    await vi.waitFor(() => expect(appStore.fetchPublicSettings).toHaveBeenCalledOnce())
    expect(next).not.toHaveBeenCalled()

    deferred.resolve({ payment_enabled: false })
    await navigation
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith('/dashboard')
  })

  it.each([
    ['success', false],
    ['success', true],
    ['423', false],
    ['423', true],
  ] as const)('cancels an old admin navigation after compliance reset (%s, new admin: %s)', async (outcome, newAdmin) => {
    authStore.isAdmin = true
    complianceStore.initialized = false
    let resolveRequest!: (value: unknown) => void
    let rejectRequest!: (error: unknown) => void
    complianceStore.fetchStatus.mockImplementation(() => new Promise((resolve, reject) => {
      resolveRequest = resolve
      rejectRequest = reject
    }))

    const { navigation, next } = runGuard({ requiresAdmin: true }, '/admin/dashboard')
    await vi.waitFor(() => expect(complianceStore.fetchStatus).toHaveBeenCalledOnce())
    invalidateAdminComplianceSession()
    authStore.isAuthenticated = newAdmin
    authStore.isAdmin = newAdmin
    if (outcome === 'success') {
      resolveRequest({ required: false })
    } else {
      rejectRequest({
        status: 423,
        code: 'ADMIN_COMPLIANCE_ACK_REQUIRED',
        metadata: { version: 'old-admin-version' },
      })
    }

    await navigation
    expect(complianceStore.requireAcknowledgement).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith(false)
  })

  it('still accepts a 423 from the current admin session', async () => {
    authStore.isAdmin = true
    complianceStore.initialized = false
    complianceStore.fetchStatus.mockRejectedValue({
      status: 423,
      code: 'ADMIN_COMPLIANCE_ACK_REQUIRED',
      metadata: { version: 'current-admin-version' },
    })

    const { navigation, next } = runGuard({ requiresAdmin: true }, '/admin/dashboard')
    await navigation
    expect(complianceStore.requireAcknowledgement).toHaveBeenCalledWith({ version: 'current-admin-version' })
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it.each([false, true])('cancels navigation if the session changes during public settings load (new admin: %s)', async (newAdmin) => {
    authStore.isAdmin = true
    const deferred = createDeferred<{ risk_control_enabled: boolean }>()
    appStore.fetchPublicSettings.mockReturnValue(deferred.promise)
    const { navigation, next } = runGuard({ requiresAdmin: true, requiresRiskControl: true }, '/admin/risk-control')
    await vi.waitFor(() => expect(appStore.fetchPublicSettings).toHaveBeenCalledOnce())

    invalidateAdminComplianceSession()
    authStore.isAuthenticated = newAdmin
    authStore.isAdmin = newAdmin
    deferred.resolve({ risk_control_enabled: true })
    await navigation

    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith(false)
  })
})
