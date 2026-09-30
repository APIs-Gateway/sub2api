import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import BaseDialog from '../BaseDialog.vue'

enableAutoUnmount(afterEach)

const pressEscape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))

function openDialog(title: string, options: { closeOnEscape?: boolean; zIndex?: number } = {}) {
  return mount(BaseDialog, {
    props: { show: true, title, ...options },
    global: { stubs: { Icon: true } }
  })
}

describe('stacked BaseDialog Escape handling', () => {
  it('closes only the newest dialog, then restores Escape handling to its parent', async () => {
    const parent = openDialog('Parent')
    const child = openDialog('Child')

    pressEscape()
    expect(child.emitted('close')).toHaveLength(1)
    expect(parent.emitted('close')).toBeUndefined()

    await child.setProps({ show: false })
    pressEscape()
    expect(parent.emitted('close')).toHaveLength(1)
  })

  it('keeps a parent open when the newest dialog disallows Escape', () => {
    const parent = openDialog('Parent')
    const child = openDialog('Protected child', { closeOnEscape: false })

    pressEscape()
    expect(child.emitted('close')).toBeUndefined()
    expect(parent.emitted('close')).toBeUndefined()

    child.unmount()
    pressEscape()
    expect(parent.emitted('close')).toHaveLength(1)
  })

  it('does not dismiss a newer dialog behind an older protected high-z-index dialog', () => {
    const high = openDialog('Compliance', { zIndex: 80, closeOnEscape: false })
    const low = openDialog('Later confirmation', { zIndex: 50 })

    pressEscape()
    expect(high.emitted('close')).toBeUndefined()
    expect(low.emitted('close')).toBeUndefined()

    high.unmount()
    pressEscape()
    expect(low.emitted('close')).toHaveLength(1)
  })

  it('updates the Escape target when a dialog z-index changes', async () => {
    const first = openDialog('First', { zIndex: 80 })
    const second = openDialog('Second', { zIndex: 50 })

    pressEscape()
    expect(first.emitted('close')).toHaveLength(1)
    expect(second.emitted('close')).toBeUndefined()

    await first.setProps({ zIndex: 40 })
    pressEscape()
    expect(first.emitted('close')).toHaveLength(1)
    expect(second.emitted('close')).toHaveLength(1)
  })
})
