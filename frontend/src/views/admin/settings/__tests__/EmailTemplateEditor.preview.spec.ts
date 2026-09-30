import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import EmailTemplateEditor from '../EmailTemplateEditor.vue'

const {
  getEmailTemplates, getEmailTemplate, updateEmailTemplate, restoreOfficialEmailTemplate,
  previewEmailTemplate, showError, showSuccess,
} = vi.hoisted(() => ({
  getEmailTemplates: vi.fn(),
  getEmailTemplate: vi.fn(),
  updateEmailTemplate: vi.fn(),
  restoreOfficialEmailTemplate: vi.fn(),
  previewEmailTemplate: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api', () => ({ adminAPI: { settings: {
  getEmailTemplates, getEmailTemplate, updateEmailTemplate, restoreOfficialEmailTemplate, previewEmailTemplate,
} } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.resetAllMocks()
  getEmailTemplates.mockResolvedValue({ events: ['auth.verify_code', 'auth.password_reset', 'subscription.purchase_success'], locales: ['en'] })
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

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

describe('email template previews', () => {
  it('keeps the newest template fields and saves under that selection when an older load resolves last', async () => {
    const wrapper = await mountEditor()
    const old = deferred<{ subject: string; html: string }>()
    const current = deferred<{ subject: string; html: string }>()
    getEmailTemplate.mockImplementation((event: string) =>
      event === 'auth.password_reset' ? old.promise : current.promise
    )
    previewEmailTemplate.mockImplementation(({ event }: { event: string }) =>
      Promise.resolve({ subject: event, html: `<p>${event}</p>` })
    )

    const eventSelect = wrapper.findAll('select')[0]
    await eventSelect.setValue('auth.password_reset')
    expect(eventSelect.attributes('disabled')).toBeUndefined()
    expect(getEmailTemplate).toHaveBeenLastCalledWith('auth.password_reset', 'en')
    await eventSelect.setValue('subscription.purchase_success')
    expect(eventSelect.element).toHaveProperty('value', 'subscription.purchase_success')
    expect(getEmailTemplate).toHaveBeenCalledTimes(3)
    expect(getEmailTemplate).toHaveBeenLastCalledWith('subscription.purchase_success', 'en')
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    expect(previewButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.get('#email-template-subject').attributes('disabled')).toBeDefined()
    current.resolve({ subject: 'Current subscription', html: '<p>Current subscription</p>' })
    await flushPromises()
    expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('Current subscription')
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>subscription.purchase_success</p>')

    old.resolve({ subject: 'Old password reset', html: '<p>Old password reset</p>' })
    await flushPromises()
    expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('Current subscription')
    expect((wrapper.get('#email-template-html').element as HTMLTextAreaElement).value).toBe('<p>Current subscription</p>')
    expect(previewEmailTemplate).toHaveBeenCalledTimes(2)
    expect(previewEmailTemplate).toHaveBeenLastCalledWith(expect.objectContaining({
      event: 'subscription.purchase_success',
      subject: 'Current subscription',
    }))

    updateEmailTemplate.mockResolvedValue({ subject: 'Current subscription', html: '<p>Current subscription</p>' })
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateEmailTemplate).toHaveBeenCalledWith(
      'subscription.purchase_success',
      'en',
      { subject: 'Current subscription', html: '<p>Current subscription</p>' },
    )
  })

  it('ignores an older template load failure without clearing the current load state', async () => {
    const wrapper = await mountEditor()
    const old = deferred<{ subject: string; html: string }>()
    const current = deferred<{ subject: string; html: string }>()
    getEmailTemplate.mockImplementation((event: string) =>
      event === 'auth.password_reset' ? old.promise : current.promise
    )

    const eventSelect = wrapper.findAll('select')[0]
    await eventSelect.setValue('auth.password_reset')
    expect(eventSelect.attributes('disabled')).toBeUndefined()
    expect(getEmailTemplate).toHaveBeenLastCalledWith('auth.password_reset', 'en')
    await eventSelect.setValue('subscription.purchase_success')
    expect(eventSelect.element).toHaveProperty('value', 'subscription.purchase_success')
    expect(getEmailTemplate).toHaveBeenCalledTimes(3)
    expect(getEmailTemplate).toHaveBeenLastCalledWith('subscription.purchase_success', 'en')
    old.reject(new Error('obsolete template failure'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    expect(previewButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.get('#email-template-subject').attributes('disabled')).toBeDefined()

    current.resolve({ subject: 'Current subscription', html: '<p>Current subscription</p>' })
    await flushPromises()
    expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('Current subscription')
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
    expect(showError).not.toHaveBeenCalled()
  })

  it('does not create another preview after a pending template load resolves on unmount', async () => {
    const wrapper = await mountEditor()
    const pending = deferred<{ subject: string; html: string }>()
    getEmailTemplate.mockReturnValueOnce(pending.promise)
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    wrapper.unmount()

    pending.resolve({ subject: 'Old password reset', html: '<p>Old password reset</p>' })
    await flushPromises()
    expect(previewEmailTemplate).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
  })

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
    expect(previewButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('#email-template-html').element as HTMLTextAreaElement).value).toBe('')

    finishOld({ subject: 'Obsolete', html: '<p>Obsolete</p>' })
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).not.toBe('<p>Obsolete</p>')
    expect(previewButton(wrapper).attributes('disabled')).toBeDefined()
    expect(updateEmailTemplate).not.toHaveBeenCalled()
  })

  it('can restore the selected official template after its load fails', async () => {
    const wrapper = await mountEditor()
    getEmailTemplate.mockRejectedValueOnce(new Error('template load failed'))
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    await flushPromises()
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()

    restoreOfficialEmailTemplate.mockResolvedValueOnce({
      subject: 'Restored password reset', html: '<p>Restored password reset</p>',
    })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    try {
      const restore = wrapper.findAll('button').find(button =>
        button.text() === 'admin.settings.emailTemplates.restoreOfficial'
      )
      if (!restore) throw new Error('Restore button not found')
      await restore.trigger('click')
      await flushPromises()
    } finally {
      confirm.mockRestore()
    }

    expect(restoreOfficialEmailTemplate).toHaveBeenCalledWith('auth.password_reset', 'en')
    expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('Restored password reset')
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
    expect(showSuccess).toHaveBeenCalledWith('admin.settings.emailTemplates.restoreSuccess')
  })

  it.each([
    ['save', 'success'], ['save', 'failure'],
    ['restore', 'success'], ['restore', 'failure'],
  ] as const)('ignores an old %s %s after selecting another template', async (action, outcome) => {
    const wrapper = await mountEditor()
    const pending = deferred<{ subject: string; html: string }>()
    previewEmailTemplate.mockImplementation(({ event }: { event: string }) =>
      Promise.resolve({ subject: event, html: `<p>${event}</p>` })
    )
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    try {
      if (action === 'save') {
        updateEmailTemplate.mockReturnValueOnce(pending.promise)
        await wrapper.get('button.btn-primary').trigger('click')
        expect(updateEmailTemplate).toHaveBeenCalledWith(
          'auth.verify_code', 'en',
          { subject: 'auth.verify_code', html: '<p>auth.verify_code</p>' },
        )
      } else {
        restoreOfficialEmailTemplate.mockReturnValueOnce(pending.promise)
        const restore = wrapper.findAll('button').find(button =>
          button.text() === 'admin.settings.emailTemplates.restoreOfficial'
        )
        if (!restore) throw new Error('Restore button not found')
        await restore.trigger('click')
        expect(restoreOfficialEmailTemplate).toHaveBeenCalledWith('auth.verify_code', 'en')
      }

      await wrapper.findAll('select')[0].setValue('auth.password_reset')
      await flushPromises()
      expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>auth.password_reset</p>')
      expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()

      if (outcome === 'success') pending.resolve({ subject: 'Old verification', html: '<p>Old verification</p>' })
      else pending.reject(new Error('old action failed'))
      await flushPromises()
      expect((wrapper.get('#email-template-subject').element as HTMLInputElement).value).toBe('auth.password_reset')
      expect((wrapper.get('#email-template-html').element as HTMLTextAreaElement).value).toBe('<p>auth.password_reset</p>')
      expect(wrapper.get('iframe').attributes('srcdoc')).toBe('<p>auth.password_reset</p>')
      expect(previewEmailTemplate).toHaveBeenCalledTimes(2)
      expect(showSuccess).not.toHaveBeenCalled()
      expect(showError).not.toHaveBeenCalled()
    } finally {
      confirm.mockRestore()
    }
  })

  it.each(['save', 'restore'] as const)('reports a current %s failure and releases its busy state', async action => {
    const wrapper = await mountEditor()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    try {
      if (action === 'save') {
        updateEmailTemplate.mockRejectedValueOnce(new Error('current action failed'))
        await wrapper.get('button.btn-primary').trigger('click')
      } else {
        restoreOfficialEmailTemplate.mockRejectedValueOnce(new Error('current action failed'))
        const restore = wrapper.findAll('button').find(button =>
          button.text() === 'admin.settings.emailTemplates.restoreOfficial'
        )
        if (!restore) throw new Error('Restore button not found')
        await restore.trigger('click')
      }
      await flushPromises()
      expect(showError).toHaveBeenCalledWith('current action failed')
      expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
    } finally {
      confirm.mockRestore()
    }
  })

  it('suppresses a template-list failure after the editor unmounts', async () => {
    const pending = deferred<{ events: string[]; locales: string[] }>()
    getEmailTemplates.mockReturnValueOnce(pending.promise)
    const wrapper = mount(EmailTemplateEditor)
    wrapper.unmount()
    pending.reject(new Error('obsolete list failure'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    expect(getEmailTemplate).not.toHaveBeenCalled()
  })

  it('clears an old preview when the newly selected template has no previewable HTML', async () => {
    const wrapper = await mountEditor()
    let finishOld!: (value: object) => void
    previewEmailTemplate.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    await previewButton(wrapper).trigger('click')
    getEmailTemplate.mockResolvedValueOnce({ subject: 'No HTML', html: '' })
    await wrapper.findAll('select')[0].setValue('auth.password_reset')
    await flushPromises()

    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('')
    expect(previewButton(wrapper).text()).toBe('admin.settings.emailTemplates.preview')
    finishOld({ subject: 'Obsolete', html: '<p>Obsolete</p>' })
    await flushPromises()
    expect(wrapper.get('iframe').attributes('srcdoc')).toBe('')
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
