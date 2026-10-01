import { describe, expect, it } from 'vitest'

describe('/docs route', () => {
  it('is public and not admin-only', async () => {
    const { default: router } = await import('@/router')
    const record = router.getRoutes().find((r) => r.path === '/docs')

    expect(record).toBeDefined()
    expect(record?.meta.requiresAuth).toBe(false)
    expect(record?.meta.requiresAdmin).not.toBe(true)
  })
})
