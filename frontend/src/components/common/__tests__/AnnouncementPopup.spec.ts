import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import AnnouncementPopup from '../AnnouncementPopup.vue'
import { useAnnouncementStore } from '@/stores/announcements'

const announcementMarkdownStyles = readFileSync(
  resolve(process.cwd(), 'src/styles/announcement-markdown.css'),
  'utf8',
)

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('AnnouncementPopup', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.innerHTML = ''
    document.body.style.overflow = ''
  })

  it('renders mixed Markdown and HTML inside the shared styled container', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = {
      id: 1,
      title: 'Mixed content announcement',
      content: [
        '## Markdown heading',
        '',
        '<div><h3>HTML heading</h3><ul><li>HTML list item</li></ul></div>',
        '',
        '<table><thead><tr><th>Status</th></tr></thead><tbody><tr><td>OK</td></tr></tbody></table>',
        '<script>window.__announcementXss = true</script>',
      ].join('\n'),
      notify_mode: 'popup',
      created_at: '2026-07-24T07:30:00Z',
      updated_at: '2026-07-24T07:30:00Z',
    }

    const wrapper = mount(AnnouncementPopup)
    await wrapper.vm.$nextTick()

    const content = document.body.querySelector('.markdown-body')
    expect(content?.querySelector('h2')?.textContent).toBe('Markdown heading')
    expect(content?.querySelector('h3')?.textContent).toBe('HTML heading')
    expect(content?.querySelector('li')?.textContent).toBe('HTML list item')
    expect(content?.querySelector('table td')?.textContent).toBe('OK')
    expect(content?.querySelector('script')).toBeNull()

    wrapper.unmount()
  })

  it('removes executable announcement attributes and URLs while preserving safe content', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = {
      id: 2,
      title: 'Sanitized announcement',
      content: [
        '<p>Trusted <strong>formatting</strong></p>',
        '<a id="safe-link" href="https://example.test/docs">Safe link</a>',
        '<img id="notice-image" src="/notice.png" onerror="window.__announcementXss=true">',
        '<a id="blocked-link" href="javascript:window.__announcementXss=true" onclick="window.__announcementXss=true">Blocked link</a>',
      ].join('\n'),
      notify_mode: 'popup',
      created_at: '2026-10-23T00:00:00Z',
      updated_at: '2026-10-23T00:00:00Z',
    }

    const wrapper = mount(AnnouncementPopup)
    try {
      await wrapper.vm.$nextTick()
      const content = document.body.querySelector('.markdown-body')
      expect(content).not.toBeNull()
      expect(content?.querySelector('strong')?.textContent).toBe('formatting')
      const safeLink = content?.querySelector('#safe-link')
      expect(safeLink?.textContent).toBe('Safe link')
      expect(safeLink?.getAttribute('href')).toBe('https://example.test/docs')
      const image = content?.querySelector('#notice-image')
      expect(image?.getAttribute('src')).toBe('/notice.png')
      expect(image?.hasAttribute('onerror')).toBe(false)
      const blockedLink = content?.querySelector('#blocked-link')
      expect(blockedLink?.textContent).toBe('Blocked link')
      expect(blockedLink?.hasAttribute('href')).toBe(false)
      expect(blockedLink?.hasAttribute('onclick')).toBe(false)
      expect(content?.querySelector('script, [onerror], [onclick]')).toBeNull()
    } finally {
      wrapper.unmount()
    }
  })

  it.each(['h2', 'h3', 'ul', 'li', 'blockquote', 'table', 'th', 'td', 'code'])(
    'loads a shared style rule for mixed-content <%s> elements',
    (element) => {
      expect(announcementMarkdownStyles).toContain(`.markdown-body ${element}`)
    },
  )
})
