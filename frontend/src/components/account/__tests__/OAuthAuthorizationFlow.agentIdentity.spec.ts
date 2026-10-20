import { defineComponent, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copied: ref(false),
    copyToClipboard: vi.fn()
  })
}))

import OAuthAuthorizationFlow from '../OAuthAuthorizationFlow.vue'

const IconStub = defineComponent({
  name: 'Icon',
  template: '<span />'
})

function mountFlow(props: Record<string, unknown>) {
  return mount(OAuthAuthorizationFlow, {
    props: {
      addMethod: 'oauth',
      platform: 'openai',
      showCookieOption: false,
      ...props
    },
    global: {
      stubs: {
        Icon: IconStub
      }
    }
  })
}

describe('OAuthAuthorizationFlow Agent Identity input', () => {
  it('selects the standalone Agent Identity input and emits its JSON content', async () => {
    const wrapper = mountFlow({
      showManualOption: false,
      showAgentIdentityOption: true,
      initialInputMethod: 'agent_identity'
    })

    expect(wrapper.find('input[value="agent_identity"]').exists()).toBe(true)
    expect(wrapper.find('input[value="manual"]').exists()).toBe(false)
    expect(wrapper.find('input[value="codex_session"]').exists()).toBe(false)

    const payload = '{"auth_mode":"agentIdentity","agent_identity":{}}'
    await wrapper.get('textarea').setValue(payload)
    await wrapper.get('button[type="button"]').trigger('click')

    expect(wrapper.emitted('import-codex-session')).toEqual([[payload]])
  })

  it('keeps the regular Codex session input separate', () => {
    const wrapper = mountFlow({
      showCodexSessionImportOption: true,
      initialInputMethod: 'codex_session'
    })

    expect(wrapper.find('input[value="codex_session"]').exists()).toBe(true)
    expect(wrapper.find('input[value="agent_identity"]').exists()).toBe(false)
  })
})
