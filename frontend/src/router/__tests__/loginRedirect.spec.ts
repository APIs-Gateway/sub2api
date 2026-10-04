import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { isSafeRedirectPath } from '@/utils/redirect'

// 用真实的 router/index.ts 守卫验证 redirect 的去向：
// createRouter 在本文件里被 mock，守卫是从 beforeEach 里截下来的那一份。

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
  isAuthenticated: false,
  isAdmin: false,
  isSimpleMode: false,
  hasPendingAuthSession: false,
}))

const appStore = vi.hoisted(() => ({
  siteName: 'Sub2API',
  backendModeEnabled: false,
  publicSettingsLoaded: true,
  cachedPublicSettings: null as null | { payment_enabled?: boolean; custom_menu_items?: [] },
  fetchPublicSettings: vi.fn(),
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

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))
vi.mock('@/stores/adminCompliance', () => ({
  useAdminComplianceStore: () => ({
    initialized: true,
    fetchStatus: vi.fn(),
    requireAcknowledgement: vi.fn(),
  }),
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

async function navigate(
  path: string,
  options: { query?: Record<string, unknown>; fullPath?: string; meta?: Record<string, unknown> } = {}
) {
  if (!routerHarness.guard) {
    throw new Error('router guard was not registered')
  }
  const next = vi.fn()
  await routerHarness.guard(
    {
      path,
      fullPath: options.fullPath ?? path,
      query: options.query ?? {},
      name: 'Test',
      params: {},
      meta: options.meta ?? {},
    },
    {},
    next
  )
  expect(next).toHaveBeenCalledOnce()
  return next.mock.calls[0]
}

const AUTH_PAGE_META = { requiresAuth: false }

describe('登录 / 注册页的 redirect（真实路由守卫）', () => {
  beforeAll(async () => {
    await import('@/router')
  })

  beforeEach(() => {
    authStore.isAuthenticated = false
    authStore.isAdmin = false
    authStore.isSimpleMode = false
    authStore.hasPendingAuthSession = false
    appStore.backendModeEnabled = false
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = null
  })

  describe('已登录用户访问 /login、/register', () => {
    beforeEach(() => {
      authStore.isAuthenticated = true
    })

    it.each(['/login', '/register'])('%s?redirect=/keys 直接去 /keys', async (path) => {
      const [target] = await navigate(path, { query: { redirect: '/keys' }, meta: AUTH_PAGE_META })
      expect(target).toBe('/keys')
    })

    it('redirect 带查询串时原样保留', async () => {
      const [target] = await navigate('/login', {
        query: { redirect: '/usage?model=gpt-5&range=7d' },
        meta: AUTH_PAGE_META,
      })
      expect(target).toBe('/usage?model=gpt-5&range=7d')
    })

    it('管理员同样尊重 redirect', async () => {
      authStore.isAdmin = true
      const [keys] = await navigate('/login', { query: { redirect: '/keys' }, meta: AUTH_PAGE_META })
      expect(keys).toBe('/keys')
      const [admin] = await navigate('/login', {
        query: { redirect: '/admin/users' },
        meta: AUTH_PAGE_META,
      })
      expect(admin).toBe('/admin/users')
    })

    it('没有 redirect 时保持原行为：普通用户回 /dashboard，管理员回 /admin/dashboard', async () => {
      const [user] = await navigate('/login', { meta: AUTH_PAGE_META })
      expect(user).toBe('/dashboard')
      authStore.isAdmin = true
      const [admin] = await navigate('/register', { meta: AUTH_PAGE_META })
      expect(admin).toBe('/admin/dashboard')
    })

    it.each([
      ['协议相对地址', '//evil.com'],
      ['反斜杠', '/\\evil.com'],
      ['绝对地址', 'https://evil.com'],
      ['脚本协议', 'javascript:alert(1)'],
      ['编码的双斜杠', '/%2F%2Fevil.com'],
      ['编码的反斜杠', '/%5Cevil.com'],
      ['多重编码', '/%252F%252Fevil.com'],
      ['换行', '/\n/evil.com'],
      ['点段', '/.//evil.com'],
      ['不是站内路径', 'keys'],
    ])('不合法的 redirect（%s）一律回默认首页', async (_name, redirect) => {
      const [user] = await navigate('/login', { query: { redirect }, meta: AUTH_PAGE_META })
      expect(user).toBe('/dashboard')
      authStore.isAdmin = true
      const [admin] = await navigate('/login', { query: { redirect }, meta: AUTH_PAGE_META })
      expect(admin).toBe('/admin/dashboard')
    })

    it('redirect 重复出现（数组）时按不合法处理', async () => {
      const [target] = await navigate('/login', {
        query: { redirect: ['/keys', '/usage'] },
        meta: AUTH_PAGE_META,
      })
      expect(target).toBe('/dashboard')
    })

    it('redirect 指回 /login 或 /register 时不再绕圈，回默认首页', async () => {
      for (const redirect of ['/login', '/register', '/login?redirect=/keys', '/LOGIN/']) {
        const [target] = await navigate('/login', { query: { redirect }, meta: AUTH_PAGE_META })
        expect(target, redirect).toBe('/dashboard')
      }
    })

    it('后端模式下的普通用户停留在登录页，不跟随 redirect（否则会和"受保护页 -> /login"互相弹）', async () => {
      appStore.backendModeEnabled = true
      const [target] = await navigate('/login', { query: { redirect: '/keys' }, meta: AUTH_PAGE_META })
      expect(target).toBeUndefined()
    })

    it('后端模式下的管理员跟随 redirect', async () => {
      appStore.backendModeEnabled = true
      authStore.isAdmin = true
      const [target] = await navigate('/login', {
        query: { redirect: '/admin/users' },
        meta: AUTH_PAGE_META,
      })
      expect(target).toBe('/admin/users')
    })

    it('redirect 的目标自己不会再把已登录用户弹回 /login（无环）', async () => {
      // 从 /login?redirect=/keys 出发：/keys 是受保护页，已登录用户直接放行
      const [first] = await navigate('/login', { query: { redirect: '/keys' }, meta: AUTH_PAGE_META })
      expect(first).toBe('/keys')
      const [second] = await navigate('/keys', { meta: { requiresAuth: true } })
      expect(second).toBeUndefined()
    })

    it('普通用户 redirect 到管理页：被守卫拦到 /dashboard，而不是回到 /login', async () => {
      const [first] = await navigate('/login', {
        query: { redirect: '/admin/users' },
        meta: AUTH_PAGE_META,
      })
      expect(first).toBe('/admin/users')
      const [second] = await navigate('/admin/users', {
        meta: { requiresAuth: true, requiresAdmin: true },
      })
      expect(second).toBe('/dashboard')
    })
  })

  describe('未登录用户', () => {
    it('访问受保护页时被送到 /login?redirect=原路径（原行为不变）', async () => {
      const [target] = await navigate('/keys', { meta: { requiresAuth: true } })
      expect(target).toEqual({ path: '/login', query: { redirect: '/keys' } })
    })

    it('带查询串的原路径整体保存', async () => {
      const [target] = await navigate('/usage', {
        fullPath: '/usage?model=gpt-5&range=7d',
        meta: { requiresAuth: true },
      })
      expect(target).toEqual({ path: '/login', query: { redirect: '/usage?model=gpt-5&range=7d' } })
    })

    it.each([
      '/keys',
      '/keys?new=1',
      '/usage?model=gpt-5%2Fmini&range=7d',
      '/usage?q=%E4%B8%AD%E6%96%87',
      '/purchase?tab=subscription',
      '/admin/accounts?platform=openai&page=2',
      '/orders#latest',
      '/custom/abc-123',
    ])('守卫写进去的 %s 能被校验函数接受（登录后回得去）', async (fullPath) => {
      const path = fullPath.split(/[?#]/, 1)[0]
      const [target] = await navigate(path, { fullPath, meta: { requiresAuth: true } })
      const written = (target as { query: { redirect: string } }).query.redirect
      expect(written).toBe(fullPath)
      expect(isSafeRedirectPath(written)).toBe(true)
    })

    it('访问带 redirect 的 /login 正常显示登录页，不被改写', async () => {
      const [target] = await navigate('/login', { query: { redirect: '/keys' }, meta: AUTH_PAGE_META })
      expect(target).toBeUndefined()
    })

    it('访问带 redirect 的 /register 正常显示注册页', async () => {
      const [target] = await navigate('/register', {
        query: { redirect: '/keys' },
        meta: AUTH_PAGE_META,
      })
      expect(target).toBeUndefined()
    })
  })
})
