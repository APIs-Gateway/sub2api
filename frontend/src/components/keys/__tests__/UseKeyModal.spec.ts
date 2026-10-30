import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { execFileSync } from 'node:child_process'
import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhHK from '@/i18n/locales/zh-HK'

const { copyToClipboardMock } = vi.hoisted(() => ({ copyToClipboardMock: vi.fn().mockResolvedValue(true) }))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: copyToClipboardMock
  })
}))

import UseKeyModal from '../UseKeyModal.vue'

describe('UseKeyModal', () => {
  it('renders Grok Build and OpenCode setup for Grok groups', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-grok-test',
        baseUrl: 'https://example.com/v1',
        platform: 'grok'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const grokTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.grokCli')
    )
    expect(grokTab).toBeDefined()

    const grokConfig = wrapper.findAll('pre code')
      .map((code) => code.text())
      .find((content) => content.includes('[model."grok"]'))
    expect(grokConfig).toBeDefined()
    expect(grokConfig).toContain('model = "grok-4.3"')
    expect(grokConfig).toContain('base_url = "https://example.com/v1"')
    expect(grokConfig).toContain('api_key = "sk-grok-test"')
    expect(grokConfig).toContain('api_backend = "responses"')

    const windowsTab = wrapper.findAll('button').find(
      (button) => button.text().trim() === 'Windows'
    )
    expect(windowsTab).toBeDefined()
    await windowsTab!.trigger('click')
    await nextTick()
    expect(wrapper.text()).toContain('%userprofile%\\.grok/config.toml')

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )
    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const parsed = JSON.parse(wrapper.find('pre code').text())
    expect(parsed.provider.grok.npm).toBe('@ai-sdk/openai')
    expect(parsed.provider.grok.options).toEqual({
      baseURL: 'https://example.com/v1',
      apiKey: 'sk-grok-test'
    })
    expect(parsed.provider.grok.models['grok-4.3']).toBeDefined()
    expect(parsed.provider.grok.models['grok-build-0.1']).toBeDefined()
    expect(parsed.provider.grok.models['grok-4.20-0309-reasoning']).toBeDefined()
    expect(parsed.provider.grok.models['grok-4.20-0309-non-reasoning']).toBeDefined()
    expect(parsed.provider.grok.models['grok-4.20-multi-agent-0309']).toBeDefined()
    expect(parsed.provider.grok.models['grok-4.5']).toBeUndefined()
    expect(parsed.provider.grok.models['gpt-5.6']).toBeUndefined()
  })

  it('renders copyable Claude Code setup through the Grok Messages gateway', async () => {
    copyToClipboardMock.mockClear()
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-grok-claude-test',
        baseUrl: 'https://example.com/v1',
        platform: 'grok',
        allowMessagesDispatch: true
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const claudeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.claudeCode')
    )
    expect(claudeTab).toBeDefined()
    await claudeTab!.trigger('click')
    await nextTick()

    let codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    expect(codeBlocks.join('\n')).toContain('ANTHROPIC_BASE_URL="https://example.com"')
    expect(codeBlocks.join('\n')).toContain('ANTHROPIC_AUTH_TOKEN="sk-grok-claude-test"')
    const unixConfig = codeBlocks.find((content) => content.startsWith('export ANTHROPIC_BASE_URL'))
    expect(unixConfig).toBeDefined()
    for (const name of [
      'ANTHROPIC_MODEL',
      'ANTHROPIC_DEFAULT_OPUS_MODEL',
      'ANTHROPIC_DEFAULT_SONNET_MODEL',
      'ANTHROPIC_DEFAULT_HAIKU_MODEL',
      'ANTHROPIC_DEFAULT_FABLE_MODEL',
      'CLAUDE_CODE_SUBAGENT_MODEL'
    ]) {
      expect(unixConfig).toContain(`export ${name}="grok-4.3"`)
    }
    const settingsConfig = codeBlocks.find((content) => content.includes('"$schema"'))
    expect(settingsConfig).toBeDefined()
    const parsedSettings = JSON.parse(settingsConfig!)
    expect(parsedSettings.$schema).toBe('https://json.schemastore.org/claude-code-settings.json')
    expect(parsedSettings.env.ANTHROPIC_MODEL).toBe('grok-4.3')
    expect(wrapper.text()).toContain('keys.useKeyModal.claudeSettingsHint')
    expect(wrapper.text()).toContain('keys.useKeyModal.grok.claudeNote')
    expect(wrapper.find('nav[aria-label="Client"]').classes()).toContain('min-w-max')
    expect(wrapper.find('nav[aria-label="Client"]').element.parentElement?.classList.contains('overflow-x-auto')).toBe(true)

    const cmdTab = wrapper.findAll('button').find(
      (button) => button.text().trim() === 'Windows CMD'
    )
    expect(cmdTab).toBeDefined()
    await cmdTab!.trigger('click')
    await nextTick()

    codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    expect(codeBlocks.join('\n')).toContain('set "ANTHROPIC_MODEL=grok-4.3"')
    expect(codeBlocks.join('\n')).toContain('set "ANTHROPIC_DEFAULT_FABLE_MODEL=grok-4.3"')
    expect(codeBlocks.join('\n')).toContain('set "CLAUDE_CODE_SUBAGENT_MODEL=grok-4.3"')

    const powershellTab = wrapper.findAll('button').find(
      (button) => button.text().trim() === 'PowerShell'
    )
    expect(powershellTab).toBeDefined()
    await powershellTab!.trigger('click')
    await nextTick()

    codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    expect(codeBlocks.join('\n')).toContain("$env:ANTHROPIC_BASE_URL='https://example.com'")
    expect(codeBlocks.join('\n')).toContain("$env:ANTHROPIC_MODEL='grok-4.3'")
    expect(codeBlocks.join('\n')).toContain("$env:ANTHROPIC_DEFAULT_FABLE_MODEL='grok-4.3'")
    expect(codeBlocks.join('\n')).toContain("$env:CLAUDE_CODE_SUBAGENT_MODEL='grok-4.3'")
    expect(wrapper.text()).toContain('%USERPROFILE%\\.claude\\settings.json')

    const copyButton = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.copy')
    )
    expect(copyButton).toBeDefined()
    await copyButton!.trigger('click')
    expect(copyToClipboardMock).toHaveBeenCalledWith(
      expect.stringContaining("ANTHROPIC_AUTH_TOKEN='sk-grok-claude-test'"),
      'keys.copied'
    )
  })

  it('hides the Grok Claude Code setup when Messages dispatch is disabled', () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-grok-no-dispatch',
        baseUrl: 'https://example.com/v1',
        platform: 'grok',
        allowMessagesDispatch: false
      },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: { template: '<span />' }
        }
      }
    })

    expect(wrapper.findAll('button').some((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.claudeCode')
    )).toBe(false)
  })

  it('renders Codex custom provider setup through the Grok Responses gateway', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-grok-codex-test',
        baseUrl: 'https://example.com/v1',
        platform: 'grok'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const codexTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.codexCli')
    )
    expect(codexTab).toBeDefined()
    await codexTab!.trigger('click')
    await nextTick()

    let codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('[model_providers.sub2api_grok]'))
    expect(configToml).toBeDefined()
    expect(configToml).toContain('model_provider = "sub2api_grok"')
    expect(configToml).toContain('model = "grok-4.3"')
    expect(configToml).toContain('model_context_window = 1000000')
    expect(configToml).toContain('base_url = "https://example.com/v1"')
    expect(configToml).toContain('env_key = "SUB2API_API_KEY"')
    expect(configToml).toContain('wire_api = "responses"')
    expect(configToml).toContain('supports_websockets = true')
    expect(configToml).not.toContain('requires_openai_auth')
    expect(configToml).not.toContain('disable_response_storage')
    expect(configToml).not.toContain('network_access')
    expect(configToml).not.toContain('windows_wsl_setup_acknowledged')
    expect(configToml).toContain('[features]\nresponses_websockets_v2 = true')
    expect(configToml).not.toContain('goals = true')
    expect(codeBlocks).toContain('export SUB2API_API_KEY="sk-grok-codex-test"')
    expect(wrapper.text()).not.toContain('auth.json')

    const windowsTab = wrapper.findAll('button').find(
      (button) => button.text().trim() === 'Windows'
    )
    expect(windowsTab).toBeDefined()
    await windowsTab!.trigger('click')
    await nextTick()

    codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    expect(wrapper.text()).toContain('%USERPROFILE%\\.codex\\config.toml')
    expect(codeBlocks).toContain('$env:SUB2API_API_KEY="sk-grok-codex-test"')
  })

  it('omits the attribution override from every standard Claude Code setup form', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-anthropic-test',
        baseUrl: 'https://example.com/v1',
        platform: 'anthropic'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    for (const [shell, trafficSetting] of [
      ['macOS / Linux', 'export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1'],
      ['Windows CMD', 'set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1'],
      ['PowerShell', '$env:CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1']
    ]) {
      if (shell !== 'macOS / Linux') {
        const shellTab = wrapper.findAll('button').find(
          (button) => button.text().trim() === shell
        )
        expect(shellTab).toBeDefined()
        await shellTab!.trigger('click')
        await nextTick()
      }

      const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
      const allCode = codeBlocks.join('\n')
      // fork 的 VSCode settings.json 模板没有 "$schema"，按 "env" 块定位
      const settingsBlock = codeBlocks.find((content) => content.trimStart().startsWith('{') && content.includes('"env"'))
      expect(settingsBlock).toBeDefined()
      const settings = JSON.parse(settingsBlock!)

      expect(allCode).not.toContain('CLAUDE_CODE_ATTRIBUTION_HEADER')
      expect(allCode).toContain(trafficSetting)
      expect(settings.env.CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC).toBe('1')
      expect(settings.env).not.toHaveProperty('CLAUDE_CODE_ATTRIBUTION_HEADER')
    }
  })

  it('renders GPT-5.5 and goals feature in OpenAI Codex config', () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('model_provider = "OpenAI"'))

    expect(configToml).toBeDefined()
    expect(configToml).toContain('model = "gpt-5.5"')
    expect(configToml).toContain('review_model = "gpt-5.5"')
    expect(configToml).not.toContain('model = "gpt-5.4"')
    expect(configToml).not.toContain('model_context_window')
    expect(configToml).not.toContain('model_auto_compact_token_limit')
    expect(configToml).toContain('requires_openai_auth = true')
    expect(configToml).not.toContain('experimental_bearer_token')
    expect(configToml).not.toContain('x-openai-actor-authorization')
    expect(configToml).toContain('[features]\ngoals = true')
    // legacy 模式仍生成 auth.json
    expect(codeBlocks).toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).toContain('auth.json')
  })

  it('renders GPT-5.5 and goals feature in OpenAI Codex WebSocket config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const wsTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.codexCliWs')
    )

    expect(wsTab).toBeDefined()
    await wsTab!.trigger('click')
    await nextTick()

    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('supports_websockets = true'))

    expect(configToml).toBeDefined()
    expect(configToml).toContain('model = "gpt-5.5"')
    expect(configToml).toContain('review_model = "gpt-5.5"')
    expect(configToml).not.toContain('model = "gpt-5.4"')
    expect(configToml).not.toContain('model_context_window')
    expect(configToml).not.toContain('model_auto_compact_token_limit')
    expect(configToml).toContain('requires_openai_auth = true')
    expect(configToml).not.toContain('experimental_bearer_token')
    expect(configToml).not.toContain('x-openai-actor-authorization')
    expect(configToml).toContain('[features]\nresponses_websockets_v2 = true\ngoals = true')
    expect(codeBlocks).toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).toContain('auth.json')
  })

  it('renders API Key Mode authorization in OpenAI Codex config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const apiKeyMode = wrapper.get('[data-testid="codex-auth-mode-api-key"]')
    await apiKeyMode.trigger('click')
    await nextTick()

    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('model_provider = "OpenAI"'))

    expect(apiKeyMode.attributes('aria-checked')).toBe('true')
    expect(configToml).toBeDefined()
    expect(configToml).toContain('requires_openai_auth = false')
    expect(configToml).toContain('experimental_bearer_token = "sk-test"')
    expect(configToml).toContain('http_headers = { "x-openai-actor-authorization" = "local-image-extension" }')
    // API Key 模式凭证内联在 provider 块，不再生成 auth.json
    expect(codeBlocks).not.toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).not.toContain('auth.json')
  })

  it('preserves API Key Mode when switching to OpenAI Codex WebSocket config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    await wrapper.get('[data-testid="codex-auth-mode-api-key"]').trigger('click')

    const wsTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.codexCliWs')
    )
    expect(wsTab).toBeDefined()
    await wsTab!.trigger('click')
    await nextTick()

    const codeBlocks = wrapper.findAll('pre code').map((code) => code.text())
    const configToml = codeBlocks.find((content) => content.includes('supports_websockets = true'))

    expect(wrapper.get('[data-testid="codex-auth-mode-api-key"]').attributes('aria-checked')).toBe('true')
    expect(configToml).toBeDefined()
    expect(configToml).toContain('requires_openai_auth = false')
    expect(configToml).toContain('experimental_bearer_token = "sk-test"')
    expect(configToml).toContain('http_headers = { "x-openai-actor-authorization" = "local-image-extension" }')
    // API Key 模式凭证内联在 provider 块，不再生成 auth.json
    expect(codeBlocks).not.toContain('{\n  "OPENAI_API_KEY": "sk-test"\n}')
    expect(wrapper.text()).not.toContain('auth.json')
  })

  it('resets Codex authentication mode when the modal reopens or platform changes', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    await wrapper.get('[data-testid="codex-auth-mode-api-key"]').trigger('click')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(wrapper.get('[data-testid="codex-auth-mode-legacy"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.findAll('pre code').map((code) => code.text()).join('\n')).toContain('requires_openai_auth = true')

    await wrapper.get('[data-testid="codex-auth-mode-api-key"]').trigger('click')
    await wrapper.setProps({ platform: 'gemini' })
    await wrapper.setProps({ platform: 'openai' })
    await nextTick()

    expect(wrapper.get('[data-testid="codex-auth-mode-legacy"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.findAll('pre code').map((code) => code.text()).join('\n')).not.toContain('x-openai-actor-authorization')
  })

  it('renders GPT-5.4 mini entry in OpenCode config', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )

    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const codeBlock = wrapper.find('pre code')
    expect(codeBlock.exists()).toBe(true)
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Sol"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Terra"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.6 Luna"')
    expect(codeBlock.text()).toContain('"name": "GPT-5.4 Mini"')
    expect(codeBlock.text()).not.toContain('"name": "GPT-5.4 Nano"')
    expect(codeBlock.text()).toContain('"name": "GPT-6 (Astra)"')
    expect(codeBlock.text()).toContain('"name": "GPT-6 Astra"')

    const parsed = JSON.parse(codeBlock.text())
    expect(parsed.provider.openai.models['gpt-5.6'].name).toBe('GPT-5.6 (Sol)')
    expect(parsed.provider.openai.models['gpt-5.6'].variants.max).toEqual({})
    expect(parsed.provider.openai.models['gpt-5.6-sol'].limit).toEqual({ context: 1050000, output: 128000 })
    expect(parsed.provider.openai.models['gpt-5.6-terra'].limit).toEqual({ context: 1050000, output: 128000 })
    expect(parsed.provider.openai.models['gpt-5.6-luna'].limit).toEqual({ context: 1050000, output: 128000 })
    expect(parsed.provider.openai.models['gpt-5.6-sol'].variants.max).toEqual({})
    expect(parsed.provider.openai.models['gpt-5.6-terra'].variants.max).toEqual({})
    expect(parsed.provider.openai.models['gpt-5.6-luna'].variants.max).toEqual({})
    expect(parsed.provider.openai.models['gpt-6'].name).toBe('GPT-6 (Astra)')
    expect(parsed.provider.openai.models['gpt-6-astra'].name).toBe('GPT-6 Astra')
    expect(parsed.provider.openai.models['gpt-6'].limit).toEqual({ context: 1050000, output: 128000 })
    expect(parsed.provider.openai.models['gpt-6-astra'].limit).toEqual({ context: 1050000, output: 128000 })
    expect(parsed.provider.openai.models['gpt-6'].variants.max).toEqual({})
    expect(parsed.provider.openai.models['gpt-6-astra'].variants.max).toEqual({})
    for (const model of ['gpt-6-sol', 'gpt-6-luna']) {
      expect(parsed.provider.openai.models[model].limit).toEqual({ context: 1050000, output: 128000 })
      expect(parsed.provider.openai.models[model].variants).toHaveProperty('none')
      expect(parsed.provider.openai.models[model].variants).toHaveProperty('max')
    }
    expect(parsed.provider.openai.models['gpt-6-sol'].name).toBe('GPT-6 Sol')
    expect(parsed.provider.openai.models['gpt-6-luna'].name).toBe('GPT-6 Luna')
  })

  it('renders Claude Fable 5 OpenCode config with adaptive thinking', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'antigravity'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )

    expect(opencodeTab).toBeDefined()
    await opencodeTab!.trigger('click')
    await nextTick()

    const claudeConfig = wrapper.findAll('pre code')
      .map((code) => code.text())
      .find((content) => content.includes('"antigravity-claude"'))

    expect(claudeConfig).toBeDefined()
    const parsed = JSON.parse(claudeConfig!)
    const fable = parsed.provider['antigravity-claude'].models['claude-fable-5']

    expect(fable.name).toBe('Claude Fable 5')
    expect(fable.limit).toEqual({ context: 1048576, output: 128000 })
    expect(fable.options.thinking).toEqual({ type: 'adaptive' })
    expect(fable.options.thinking).not.toHaveProperty('budgetTokens')
  })

  it('exports Opus 5.5 only on the Anthropic provider with adaptive defaults', async () => {
    const wrapper = mount(UseKeyModal, {
      props: { show: true, apiKey: 'sk-test', baseUrl: 'https://example.com/v1', platform: 'anthropic' },
      global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Icon: { template: '<span />' } } }
    })
    const tab = wrapper.findAll('button').find(button => button.text().includes('keys.useKeyModal.cliTabs.opencode'))
    expect(tab).toBeDefined()
    await tab!.trigger('click')
    await nextTick()
    const model = JSON.parse(wrapper.find('pre code').text()).provider.anthropic.models['claude-opus-5-5']
    expect(model.limit).toEqual({ context: 1000000, output: 128000 })
    expect(model.options).toEqual({ thinking: { type: 'adaptive' }, effort: 'medium' })
    expect(model.variants.xhigh.effort).toBe('xhigh')
    expect(model.variants).not.toHaveProperty('none')
  })

  it('escapes the inline Codex bearer token as a TOML basic string', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-"quote\\slash',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    await wrapper.get('[data-testid="codex-auth-mode-api-key"]').trigger('click')
    await nextTick()

    const configToml = wrapper
      .findAll('pre code')
      .map((code) => code.text())
      .find((content) => content.includes('model_provider = "OpenAI"'))
    expect(configToml).toContain('experimental_bearer_token = "sk-\\"quote\\\\slash"')
  })
})

describe('Grok Claude shell literal values', () => {
  const mountGrok = (baseUrl: string) => mount(UseKeyModal, {
    props: { show: true, apiKey: 'sk-grok-literal_1', baseUrl, platform: 'grok', allowMessagesDispatch: true },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, Icon: true } },
  })
  const choose = async (wrapper: ReturnType<typeof mountGrok>, text: string) => {
    const button = wrapper.findAll('button').find((node) => node.text().trim() === text)
    expect(button).toBeDefined()
    await button!.trigger('click')
    await nextTick()
  }
  const codes = (wrapper: ReturnType<typeof mountGrok>) => wrapper.findAll('pre code').map((node) => node.text())
  const jsonEnvironment = (wrapper: ReturnType<typeof mountGrok>) => JSON.parse(codes(wrapper).find((code) => code.startsWith('{'))!).env

  it('preserves dollar, backtick, backslash and quote URL characters in real Unix assignments', async () => {
    const url = 'https://gateway.example/$tenant/`printf EXPANDED`/a\\b/"quoted"'
    const wrapper = mountGrok(url)
    await choose(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    const script = codes(wrapper).find((code) => code.startsWith('export '))!
    expect(script).toContain('export ANTHROPIC_BASE_URL="https://gateway.example/\\$tenant/\\`printf EXPANDED\\`/a\\\\b/\\"quoted\\""')
    // This test runs only through remote CI. Evaluate the actual generated
    // assignments, rather than reproducing the generator's escaping logic.
    const actual = execFileSync('bash', ['--noprofile', '--norc', '-c', `${script}\nprintf '%s' "$ANTHROPIC_BASE_URL"`], {
      encoding: 'utf8', env: { ...process.env, tenant: 'SHOULD_NOT_EXPAND' },
    })
    expect(actual).toBe(url)
    expect(jsonEnvironment(wrapper).ANTHROPIC_BASE_URL).toBe(url)
    wrapper.unmount()
  })

  it('uses PowerShell literal quoting for variables and ASCII or smart apostrophes', async () => {
    const url = "https://gateway.example/$tenant/O'Reilly/‘’‚‛"
    const wrapper = mountGrok(url)
    await choose(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await choose(wrapper, 'PowerShell')
    expect(codes(wrapper).join('\n')).toContain("$env:ANTHROPIC_BASE_URL='https://gateway.example/$tenant/O''Reilly/‘‘’’‚‚‛‛'")
    expect(jsonEnvironment(wrapper).ANTHROPIC_BASE_URL).toBe(url)
    wrapper.unmount()
  })

  it('quotes a CMD assignment containing an ampersand as one value', async () => {
    const url = 'https://gateway.example/a&b'
    const wrapper = mountGrok(url)
    await choose(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await choose(wrapper, 'Windows CMD')
    expect(codes(wrapper).find((code) => code.startsWith('set '))).toContain(`set "ANTHROPIC_BASE_URL=${url}"`)
    expect(jsonEnvironment(wrapper).ANTHROPIC_BASE_URL).toBe(url)
    wrapper.unmount()
  })

  it.each(['https://gateway.example/a%2Fb', 'https://gateway.example/!tenant!', 'https://gateway.example/"tenant"'])('provides only faithful settings JSON when CMD cannot guarantee the literal URL %s', async (url) => {
    const wrapper = mountGrok(url)
    await choose(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await choose(wrapper, 'Windows CMD')
    expect(codes(wrapper)).toHaveLength(1)
    expect(jsonEnvironment(wrapper)).toMatchObject({ ANTHROPIC_BASE_URL: url, ANTHROPIC_AUTH_TOKEN: 'sk-grok-literal_1', ANTHROPIC_MODEL: 'grok-4.3' })
    expect(wrapper.text()).toContain('keys.useKeyModal.grok.cmdSettingsOnly')
    wrapper.unmount()
  })

  it('resets the selected client when the separate Grok entry closes and reopens without Messages capability', async () => {
    const wrapper = mountGrok('https://gateway.example')
    await choose(wrapper, 'keys.useKeyModal.cliTabs.claudeCode')
    await wrapper.setProps({ show: false, platform: null, apiKey: '' })
    await wrapper.setProps({ show: true, platform: 'grok', apiKey: 'sk-grok-next', allowMessagesDispatch: false })
    expect(wrapper.findAll('button').some((node) => node.text().includes('keys.useKeyModal.cliTabs.claudeCode'))).toBe(false)
    expect(codes(wrapper).join('\n')).toContain('[model."grok"]')
    expect(codes(wrapper).join('\n')).not.toContain('ANTHROPIC_AUTH_TOKEN')
    wrapper.unmount()
  })
})


describe('key setup labels from actual locale data', () => {
  it.each([
    { locale: 'en', messages: en },
    { locale: 'zh-CN', messages: zhCN },
    { locale: 'zh-HK', messages: zhHK },
  ])('defines readable persistent settings and Grok labels in $locale', ({ messages }) => {
    // Read the actual dictionaries: the component fixture intentionally echoes
    // translation keys and cannot establish that a user-facing label exists.
    expect(messages.keys.useKeyModal.claudeSettingsHint).toContain('settings.json')
    expect(messages.keys.useKeyModal.claudeSettingsHint).not.toContain('keys.useKeyModal.')
    expect(messages.keys.useKeyModal.grok.cmdSettingsOnly).toContain('settings.json')
    expect(messages.keys.useKeyModal.cliTabs.grokCli).toBe('Grok Build')
    expect(messages.keys.grokSetupTemplates.trim().length).toBeGreaterThan(0)
  })
})
