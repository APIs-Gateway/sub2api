import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'
import CustomPageView from '../CustomPageView.vue'

const route = reactive({ params: { id: 'old' } })
const fetchPage = vi.fn()
const fetchSettings = vi.fn()
const publicMenus = () => [
  { id: 'old', url: 'md:old' },
  { id: 'new', url: 'md:new' },
  { id: 'external', url: 'https://example.com/docs' },
  { id: 'encoded', url: '', page_slug: 'doc beta/x' }
]
const app = reactive({
  publicSettingsLoaded: true,
  cachedPublicSettings: { custom_menu_items: publicMenus() },
  fetchPublicSettings: fetchSettings
})

vi.mock('vue-router', () => ({ useRoute: () => route }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } })
}))
vi.mock('@/stores', () => ({ useAppStore: () => app }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAdmin: false, user: { id: 1 }, token: 'fixture-token' })
}))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
enableAutoUnmount(afterEach)

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

const response = (markdown: string) => ({ ok: true, text: async () => markdown }) as Response
const mountPage = () => mount(CustomPageView, { global: { stubs: { Icon: true } } })

function finishOld(request: ReturnType<typeof deferred<Response>>, outcome: string) {
  if (outcome === 'network error') request.reject(new Error('offline'))
  else request.resolve(outcome === 'http error' ? { ok: false } as Response : response('# Old page'))
}

beforeEach(() => {
  route.params.id = 'old'
  app.publicSettingsLoaded = true
  app.cachedPublicSettings = { custom_menu_items: publicMenus() }
  fetchPage.mockReset()
  fetchSettings.mockReset().mockResolvedValue(undefined)
  vi.stubGlobal('fetch', fetchPage)
})
afterEach(() => vi.unstubAllGlobals())

describe('custom Markdown page request ordering', () => {
  it.each(['success', 'http error', 'network error'])('ignores an old %s after displaying the new page', async (outcome) => {
    const old = deferred<Response>()
    fetchPage.mockReturnValueOnce(old.promise).mockResolvedValueOnce(response('# New page\n\n```js\nnewCode()\n```'))
    const wrapper = mountPage()
    route.params.id = 'new'
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('New page')
    expect(wrapper.get('.toc-item').attributes('href')).toBe('#new-page-0')
    expect(wrapper.findAll('.copy-btn')).toHaveLength(1)
    finishOld(old, outcome)
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('New page')
    expect(wrapper.get('.toc-item').text()).toBe('New page')
    expect(wrapper.get('.markdown-page-content code').text()).toContain('newCode()')
    expect(wrapper.findAll('.copy-btn')).toHaveLength(1)
  })

  it.each(['success', 'body error'])('ignores an old %s body after displaying the new page', async (outcome) => {
    const oldBody = deferred<string>()
    fetchPage.mockResolvedValueOnce({ ok: true, text: () => oldBody.promise })
      .mockResolvedValueOnce(response('# New page'))
    const wrapper = mountPage()
    await flushPromises()
    route.params.id = 'new'
    await flushPromises()
    if (outcome === 'body error') oldBody.reject(new Error('body interrupted'))
    else oldBody.resolve('# Old page')
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('New page')
    expect(wrapper.get('.toc-item').text()).toBe('New page')
  })

  it.each(['success', 'http error', 'network error'])('keeps the new request loading after the old %s completes', async (outcome) => {
    const old = deferred<Response>()
    const current = deferred<Response>()
    fetchPage.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountPage()
    route.params.id = 'new'
    await nextTick()
    finishOld(old, outcome)
    await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    expect(wrapper.find('.markdown-page-content').exists()).toBe(false)
    current.resolve(response('# New page'))
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('New page')
    expect(wrapper.find('.animate-spin').exists()).toBe(false)
  })

  it.each(['external', 'missing'])('leaves pending Markdown immediately for the %s page', async (page) => {
    const old = deferred<Response>()
    fetchPage.mockReturnValueOnce(old.promise)
    const wrapper = mountPage()
    route.params.id = page
    await nextTick()
    expect(wrapper.find('.animate-spin').exists()).toBe(false)
    if (page === 'external') expect(wrapper.get('iframe').attributes('src')).toContain('https://example.com/docs')
    else expect(wrapper.text()).toContain('customPage.notFoundTitle')
    const readOldBody = vi.fn().mockResolvedValue('# Old page')
    old.resolve({ ok: true, text: readOldBody } as unknown as Response)
    await flushPromises()
    expect(readOldBody).not.toHaveBeenCalled()
    expect(wrapper.find('.markdown-page-content').exists()).toBe(false)
  })

  it('invalidates an earlier request when returning to the same Markdown slug', async () => {
    const old = deferred<Response>()
    const current = deferred<Response>()
    fetchPage.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountPage()
    route.params.id = 'external'
    await nextTick()
    route.params.id = 'old'
    await nextTick()
    old.resolve(response('# Obsolete first visit'))
    await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    current.resolve(response('# Fresh second visit'))
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('Fresh second visit')
    expect(wrapper.get('.toc-item').text()).toBe('Fresh second visit')
  })

  it('does not read a response body after the component is unmounted', async () => {
    const old = deferred<Response>()
    fetchPage.mockReturnValueOnce(old.promise)
    const wrapper = mountPage()
    wrapper.unmount()
    const readOldBody = vi.fn().mockResolvedValue('# Obsolete document')
    old.resolve({ ok: true, text: readOldBody } as unknown as Response)
    await flushPromises()
    expect(readOldBody).not.toHaveBeenCalled()
    expect(wrapper.exists()).toBe(false)
  })

  it('keeps a cached Markdown request loading when public settings finish first', async () => {
    app.publicSettingsLoaded = false
    const settings = deferred<void>()
    const document = deferred<Response>()
    fetchSettings.mockReturnValueOnce(settings.promise)
    fetchPage.mockReturnValueOnce(document.promise)
    const wrapper = mountPage()
    settings.resolve()
    await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    document.resolve(response('# Still awaited document'))
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('Still awaited document')
  })

  it('keeps newly discovered Markdown loading after settings reveal the menu', async () => {
    app.publicSettingsLoaded = false
    app.cachedPublicSettings = { custom_menu_items: [] }
    const settings = deferred<void>()
    const document = deferred<Response>()
    fetchSettings.mockReturnValueOnce(settings.promise)
    fetchPage.mockReturnValueOnce(document.promise)
    const wrapper = mountPage()
    app.cachedPublicSettings = { custom_menu_items: publicMenus() }
    await nextTick()
    settings.resolve()
    await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    document.resolve(response('# Discovered page'))
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('Discovered page')
  })

  it('waits for public settings when Markdown finishes first', async () => {
    app.publicSettingsLoaded = false
    const settings = deferred<void>()
    fetchSettings.mockReturnValueOnce(settings.promise)
    fetchPage.mockResolvedValueOnce(response('# Ready document\n\n```js\nreadyCode()\n```'))
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    settings.resolve()
    await flushPromises()
    expect(wrapper.get('.markdown-page-content h1').text()).toBe('Ready document')
    expect(wrapper.get('.toc-item').text()).toBe('Ready document')
    expect(wrapper.findAll('.copy-btn')).toHaveLength(1)
  })

  it.each(['http error', 'network error'])('still displays the current %s and clears loading', async (outcome) => {
    if (outcome === 'network error') fetchPage.mockRejectedValueOnce(new Error('offline'))
    else fetchPage.mockResolvedValueOnce({ ok: false })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.get('.markdown-page-content').text()).toContain(outcome === 'http error' ? 'errors.pageNotFound' : 'Failed to load page')
    expect(wrapper.find('.animate-spin').exists()).toBe(false)
    expect(wrapper.findAll('.toc-item')).toHaveLength(0)
  })

  it('preserves the fork route, bearer authorization, safe image rewriting and HTML sanitization', async () => {
    route.params.id = 'encoded'
    fetchPage.mockResolvedValueOnce(response('# Safe document\n\n![image](folder/pic.png?x=1#preview)\n\n![external](https://example.com/image.png)\n\n![escape](../escape.png)\n\n<script>window.evil = true</script><img src="javascript:alert(1)">'))
    const wrapper = mountPage()
    await flushPromises()
    expect(fetchPage).toHaveBeenCalledWith('/api/v1/pages/doc%20beta%2Fx', { headers: { Authorization: 'Bearer fixture-token' } })
    const images = wrapper.findAll('.markdown-page-content img')
    expect(images.map(image => image.attributes('src'))).toContain('/api/v1/pages/doc%20beta%2Fx/images/folder/pic.png?x=1#preview')
    expect(images.map(image => image.attributes('src'))).toContain('https://example.com/image.png')
    expect(images.map(image => image.attributes('src'))).toContain('../escape.png')
    expect(wrapper.find('.markdown-page-content script').exists()).toBe(false)
    expect(images.some(image => image.attributes('src')?.startsWith('javascript:'))).toBe(false)
    expect(wrapper.get('.toc-item').text()).toBe('Safe document')
  })
})
