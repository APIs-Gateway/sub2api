import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import BackupView from '../BackupView.vue'

const { getS3Config, updateS3Config, getSchedule, listBackups, createBackup, getDownloadURL, restoreBackup, showError, showSuccess, showWarning } = vi.hoisted(() => ({
  getS3Config: vi.fn(),
  updateS3Config: vi.fn(),
  getSchedule: vi.fn(),
  listBackups: vi.fn(),
  createBackup: vi.fn(),
  getDownloadURL: vi.fn(),
  restoreBackup: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api', () => ({
  adminAPI: {
    backup: {
      getS3Config,
      updateS3Config,
      getSchedule,
      listBackups,
      getBackup: vi.fn(),
      testS3Connection: vi.fn(),
      updateSchedule: vi.fn(),
      createBackup,
      getDownloadURL,
      restoreBackup,
      deleteBackup: vi.fn()
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showSuccess, showWarning })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const StepUpDialogStub = {
  props: {
    controller: { type: Object, required: true }
  },
  template: `
    <div v-if="controller.visible.value" data-test="step-up-dialog">
      <button data-test="step-up-verify" @click="controller.onVerified()">verify</button>
      <button data-test="step-up-cancel" @click="controller.onCancel()">cancel</button>
    </div>
  `
}

function mountView() {
  return mount(BackupView, {
    global: {
      stubs: {
        TotpStepUpDialog: StepUpDialogStub
      }
    }
  })
}

describe('admin BackupView S3 step-up gate', () => {
  beforeEach(() => {
    getS3Config.mockReset()
    updateS3Config.mockReset()
    getSchedule.mockReset()
    listBackups.mockReset()
    createBackup.mockReset()
    getDownloadURL.mockReset()
    restoreBackup.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()

    getS3Config.mockResolvedValue({
      endpoint: '',
      region: 'auto',
      bucket: '',
      access_key_id: '',
      prefix: 'backups/',
      force_path_style: false
    })
    getSchedule.mockResolvedValue({ enabled: false, cron_expr: '0 2 * * *', retain_days: 14, retain_count: 10 })
    listBackups.mockResolvedValue({ items: [] })
  })

  it('opens step-up and retries saving S3 config after verification', async () => {
    updateS3Config
      .mockRejectedValueOnce({ status: 403, code: 'STEP_UP_REQUIRED' })
      .mockResolvedValueOnce({})

    const wrapper = mountView()
    await flushPromises()

    const saveButton = wrapper.findAll('button').find((button) => button.text() === 'common.save')
    expect(saveButton).toBeDefined()
    await saveButton!.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('[data-test="step-up-dialog"]').exists()).toBe(true))

    await wrapper.get('[data-test="step-up-verify"]').trigger('click')
    await flushPromises()

    expect(updateS3Config).toHaveBeenCalledTimes(2)
    expect(showSuccess).toHaveBeenCalledWith('admin.backup.s3.saved')
    wrapper.unmount()
  })

  it('does not report an error when the user cancels step-up', async () => {
    updateS3Config.mockRejectedValueOnce({ status: 403, code: 'STEP_UP_REQUIRED' })

    const wrapper = mountView()
    await flushPromises()

    const saveButton = wrapper.findAll('button').find((button) => button.text() === 'common.save')
    await saveButton!.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('[data-test="step-up-dialog"]').exists()).toBe(true))

    await wrapper.get('[data-test="step-up-cancel"]').trigger('click')
    await flushPromises()

    expect(updateS3Config).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('uses the same step-up retry wrapper for create and download actions', async () => {
    createBackup
      .mockRejectedValueOnce({ status: 403, code: 'STEP_UP_REQUIRED' })
      .mockResolvedValueOnce({ id: 'new-backup' })
    getDownloadURL
      .mockRejectedValueOnce({ status: 403, code: 'STEP_UP_REQUIRED' })
      .mockResolvedValueOnce({ url: 'https://example.test/backup' })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const wrapper = mountView()
    await flushPromises()

    const vm = wrapper.vm as unknown as {
      createBackup: () => Promise<void>
      downloadBackup: (id: string) => Promise<void>
    }
    const create = vm.createBackup()
    await vi.waitFor(() => expect(wrapper.find('[data-test="step-up-dialog"]').exists()).toBe(true))
    await wrapper.get('[data-test="step-up-verify"]').trigger('click')
    await create

    const download = vm.downloadBackup('new-backup')
    await vi.waitFor(() => expect(wrapper.find('[data-test="step-up-dialog"]').exists()).toBe(true))
    await wrapper.get('[data-test="step-up-verify"]').trigger('click')
    await download

    expect(createBackup).toHaveBeenCalledTimes(2)
    expect(getDownloadURL).toHaveBeenCalledTimes(2)
    expect(open).toHaveBeenCalledWith('https://example.test/backup', '_blank')
    open.mockRestore()
    wrapper.unmount()
  })
})
