import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import BaseDialog from '../BaseDialog.vue'

enableAutoUnmount(afterEach)

const pressEscape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))

function openDialog(title: string, closeOnEscape = true) {
  return mount(BaseDialog, {
    props: { show: true, title, closeOnEscape },
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
    const child = openDialog('Protected child', false)

    pressEscape()
    expect(child.emitted('close')).toBeUndefined()
    expect(parent.emitted('close')).toBeUndefined()

    child.unmount()
    pressEscape()
    expect(parent.emitted('close')).toHaveLength(1)
  })
})
