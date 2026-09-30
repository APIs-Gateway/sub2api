import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import EmailTemplateEditor from '../EmailTemplateEditor.vue'

const { getEmailTemplates, getEmailTemplate, previewEmailTemplate, showError } = vi.hoisted(() => ({
  getEmailTemplates: vi.fn(),
  getEmailTemplate: vi.fn(),
  previewEmailTemplate: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api', () => ({ adminAPI: { settings: { getEmailTemplates, getEmailTemplate, previewEmailTemplate } } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.clearAllMocks()
  getEmailTemplates.mockResolvedValue({ events: ['auth.verify_code', 'auth.password_reset'], locales: ['en'] })
  getEmailTemplate.mockImplementation((event: string) => Promise.resolve({ subject: event, html: `<p>${event}</p>` }))
  previewEmailTemplate.mockResolvedValue({ subject: 'Initial', html: '<p>Initial</p>' })
})

async function mountEditor() {
  const wrapper = mount(EmailTemplateEditor)
  await flushPromises()
  return wrapper
}

function previewButton(wrapper: Awaited<ReturnType<typeof mountEditor>>) {
  const button = wrapper.findAll('button').find(item =>
    item.text() === 'admin.settings.emailTemplates.preview' ||
    item.text() === 'admin.settings.emailTemplates.previewing'
  )
  if (!button) throw new Error('Preview button not found')
  return button
}

describe('email template previews', () => {
  it('keeps the newer request loading when the older preview completes', async () => {
    const wrapper = await mountEditor()
    let finishOld!: (value: object) => void
    let finishCurrent!: (value: object) => void
    previewEmailTemplate.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    await previewButton(wrapper).trigger('click')
    previewEmailTemplate.mockImplementationOnce(() => new Promise(resolve => { finishCurrent = resolve }))
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    await flushPromises()

    finishOld({ subject: 'Old', html: '<p>Old</p>' })
    await flushPromises()
    expect(previewButton(wrapper).text()).toBe('admin.settings.emailTemplates.previewing')
    expect(wrapper.get('iframe').attributes('srcdoc')).not.toBe('<p>Old</p>')

    finishCurrent({ subject: 'Current', html: '<p>Current</p>' })
    await flushPromises()
    expect(previewButton(wrapper).text()).toBe('admin.settings.emailTemplates.preview')
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>Current</p>')
  })

  it.each(['success', 'failure'] as const)('ignores an obsolete preview %s after selection changes', async outcome => {
    const wrapper = await mountEditor()
    let resolveOld!: (value: object) => void
    let rejectOld!: (error: Error) => void
    previewEmailTemplate.mockImplementationOnce(() => new Promise((resolve, reject) => {
      resolveOld = resolve
      rejectOld = reject
    }))
    await previewButton(wrapper).trigger('click')
    previewEmailTemplate.mockResolvedValueOnce({ subject: 'Newest', html: '<p>Newest</p>' })
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>Newest</p>')

    if (outcome === 'success') resolveOld({ subject: 'Obsolete', html: '<p>Obsolete</p>' })
    else rejectOld(new Error('obsolete preview failed'))
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>Newest</p>')
    expect(showError).not.toHaveBeenCalled()
  })

  it('clears invalidated preview loading when the newly selected template fails to load', async () => {
    const wrapper = await mountEditor()
    let finishOld!: (value: object) => void
    previewEmailTemplate.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    await previewButton(wrapper).trigger('click')
    getEmailTemplate.mockRejectedValueOnce(new Error('template load failed'))
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('template load failed')
    expect(previewButton(wrapper).text()).toBe('admin.settings.emailTemplates.preview')
    expect(previewButton(wrapper).attributes('disabled')).toBeUndefined()

    finishOld({ subject: 'Obsolete', html: '<p>Obsolete</p>' })
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).not.toBe('<p>Obsolete</p>')
    expect(previewButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('ignores a failed preview after the editor unmounts', async () => {
    const wrapper = await mountEditor()
    let rejectOld!: (error: Error) => void
    previewEmailTemplate.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectOld = reject }))
    await previewButton(wrapper).trigger('click')
    wrapper.unmount()
    rejectOld(new Error('obsolete preview failed'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
  })
})
