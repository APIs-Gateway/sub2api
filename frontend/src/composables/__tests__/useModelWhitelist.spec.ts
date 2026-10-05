import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

import { buildModelMappingObject, findModelMappingConflict, getModelsByPlatform, getPresetMappingsByPlatform, removeModelMappingEntry, splitModelMappingObject } from '../useModelWhitelist'

describe('useModelWhitelist', () => {
  it('keeps the admitted source when a strict saved rewrite is deleted and reopened', () => {
    const { allowedModels, modelMappings } = splitModelMappingObject({ alias: 'gpt-6', stable: 'stable', other: 'gpt-5.6' })
    removeModelMappingEntry(modelMappings, 0, allowedModels)
    const saved = buildModelMappingObject('combined', allowedModels, modelMappings)
    expect(saved).toEqual({ stable: 'stable', alias: 'alias', other: 'gpt-5.6' })
    expect(splitModelMappingObject(saved!)).toEqual({ allowedModels: ['stable', 'alias'], modelMappings: [{ from: 'other', to: 'gpt-5.6' }] })
  })

  it('keeps the final strict source instead of changing an empty mapping to allow all models', () => {
    const mappings = [{ from: ' alias ', to: ' gpt-6 ' }], allowed: string[] = []
    removeModelMappingEntry(mappings, 0, allowed)
    expect(buildModelMappingObject('combined', allowed, mappings)).toEqual({ alias: 'alias' })
    expect(buildModelMappingObject('mapping', allowed, mappings)).toBeNull()
  })

  it('does not tighten permissive rename-only admission with an identity', () => {
    const mappings = [{ from: 'alias', to: 'gpt-6' }], allowed: string[] = []
    removeModelMappingEntry(mappings, 0, allowed, false)
    expect(mappings).toEqual([])
    expect(allowed).toEqual([])
  })

  it('deduplicates a trimmed whitelist source', () => {
    const mappings = [{ from: ' alias ', to: 'gpt-6' }], allowed = [' alias ']
    removeModelMappingEntry(mappings, 0, allowed)
    expect(allowed).toEqual([' alias '])
  })

  it('keeps another effective rewrite without adding a conflicting identity', () => {
    const mappings = [{ from: 'alias', to: 'gpt-6' }, { from: 'alias', to: 'gpt-5.6' }], allowed: string[] = []
    removeModelMappingEntry(mappings, 0, allowed)
    expect(allowed).toEqual([])
    expect(buildModelMappingObject('combined', allowed, mappings)).toEqual({ alias: 'gpt-5.6' })
  })

  it('restores admission when remaining duplicate rows have no effective rewrite', () => {
    const mappings = [{ from: 'alias', to: 'gpt-6' }, { from: 'alias', to: 'alias' }, { from: 'alias', to: 'invalid-*' }], allowed: string[] = []
    removeModelMappingEntry(mappings, 0, allowed)
    expect(allowed).toEqual(['alias'])
    expect(findModelMappingConflict('alias', mappings)).toBeUndefined()
  })

  it.each([
    { from: '', to: 'gpt-6' }, { from: ' ', to: 'gpt-6' }, { from: 'alias', to: '' },
    { from: 'alias', to: ' ' }, { from: 'alias-*', to: 'gpt-6' }, { from: 'alias', to: 'gpt-*' }
  ])('does not admit an invalid or wildcard source when deleting $from -> $to', mapping => {
    const mappings = [mapping], allowed: string[] = []
    removeModelMappingEntry(mappings, 0, allowed)
    expect(mappings).toEqual([])
    expect(allowed).toEqual([])
  })

  it.each([-1, 1, 0.5, NaN])('leaves both arrays unchanged for invalid index %s', index => {
    const mappings = [{ from: 'alias', to: 'gpt-6' }], allowed = ['stable']
    removeModelMappingEntry(mappings, index, allowed)
    expect(mappings).toEqual([{ from: 'alias', to: 'gpt-6' }])
    expect(allowed).toEqual(['stable'])
  })

  it('keeps mapping precedence over a duplicate whitelist identity on save and reopen', () => {
    const saved = buildModelMappingObject('combined', ['gpt-latest', 'gpt-6'], [
      { from: 'gpt-latest', to: 'deepseek-chat' }
    ])
    expect(saved).toEqual({ 'gpt-latest': 'deepseek-chat', 'gpt-6': 'gpt-6' })
    expect(splitModelMappingObject(saved)).toEqual({
      allowedModels: ['gpt-6'],
      modelMappings: [{ from: 'gpt-latest', to: 'deepseek-chat' }]
    })
  })

  it('keeps Create and Bulk Edit whitelist mode independent of mapping entries', () => {
    expect(buildModelMappingObject('whitelist', ['gpt-6'], [
      { from: 'gpt-6', to: 'different-target' }
    ])).toEqual({ 'gpt-6': 'gpt-6' })
  })

  it('detects only the effective valid mapping when duplicate source rows exist', () => {
    expect(findModelMappingConflict('gpt-6', [
      { from: 'gpt-6', to: 'different-target' },
      { from: 'gpt-6', to: 'gpt-6' }
    ])).toBeUndefined()
    expect(findModelMappingConflict('gpt-6', [
      { from: 'gpt-6', to: 'gpt-6' },
      { from: 'gpt-6', to: 'different-target' },
      { from: 'gpt-6', to: 'invalid-*' }
    ])).toEqual({ from: 'gpt-6', to: 'different-target' })
  })

  it('openai 模型列表包含 GPT-5.4 官方快照', () => {
    const models = getModelsByPlatform('openai')

	    expect(models).toContain('gpt-5.6')
    expect(models).toContain('gpt-5.6-sol')
    expect(models).toContain('gpt-5.6-terra')
    expect(models).toContain('gpt-5.6-luna')
    expect(models).toContain('gpt-5.4')
    expect(models).toContain('gpt-5.4-mini')
    expect(models).toContain('gpt-5.4-2026-03-05')
    expect(models).toContain('codex-auto-review')
    expect(models).toContain('gpt-6')
    expect(models).toContain('gpt-6-astra')
    expect(models).toContain('gpt-6-sol')
    expect(models).toContain('gpt-6-luna')
    expect(models.indexOf('gpt-5.6-sol')).toBeLessThan(models.indexOf('gpt-5.5'))
  })

  it('openai 预设映射包含 GPT-6 别名和 Astra', () => {
    expect(getPresetMappingsByPlatform('openai')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'GPT-6', from: 'gpt-6', to: 'gpt-6' }),
      expect.objectContaining({ label: 'GPT-6 Astra', from: 'gpt-6-astra', to: 'gpt-6-astra' }),
      expect.objectContaining({ label: 'GPT-6 Sol', from: 'gpt-6-sol', to: 'gpt-6-sol' }),
      expect.objectContaining({ label: 'GPT-6 Luna', from: 'gpt-6-luna', to: 'gpt-6-luna' })
    ]))
  })

  it('openai 模型列表不再暴露已下线的 ChatGPT 登录 Codex 模型', () => {
    const models = getModelsByPlatform('openai')

    expect(models).not.toContain('gpt-5')
    expect(models).not.toContain('gpt-5.1')
    expect(models).not.toContain('gpt-5.1-codex')
    expect(models).not.toContain('gpt-5.1-codex-max')
    expect(models).not.toContain('gpt-5.1-codex-mini')
    expect(models).not.toContain('gpt-5.2-codex')
  })

  it('antigravity 模型列表包含图片模型兼容项', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models).toContain('gemini-3-pro-image')
  })

  it('Claude 模型列表包含新发布的 Claude 模型', () => {
    expect(getModelsByPlatform('claude')).toContain('claude-fable-5')
    expect(getModelsByPlatform('claude')).toContain('claude-sonnet-5')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5')
    expect(getModelsByPlatform('claude')).toContain('claude-opus-4-8')
    expect(getModelsByPlatform('antigravity')).toContain('claude-opus-4-8')
    expect(getModelsByPlatform('claude')).toContain('claude-opus-5-5')
    expect(getModelsByPlatform('antigravity')).not.toContain('claude-opus-5-5')
    expect(getPresetMappingsByPlatform('anthropic')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Opus 5.5', from: 'claude-opus-5-5', to: 'claude-opus-5-5' })
    ]))
  })

  it('provides Sonnet 5 mappings for Anthropic and Bedrock', () => {
    expect(getPresetMappingsByPlatform('anthropic')).toContainEqual(expect.objectContaining({
      from: 'claude-sonnet-5',
      to: 'claude-sonnet-5'
    }))
    expect(getPresetMappingsByPlatform('bedrock')).toContainEqual(expect.objectContaining({
      from: 'claude-sonnet-5',
      to: 'us.anthropic.claude-sonnet-5-v1'
    }))
  })

  it('gemini 模型列表包含原生生图模型', () => {
    const models = getModelsByPlatform('gemini')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.0-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
  })

  it('antigravity 模型列表会把新的 Gemini 图片模型排在前面', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash-lite'))
  })

  it('whitelist 模式会忽略通配符条目', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image'
    })
  })

  it('whitelist 模式会保留 GPT-5.4 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-2026-03-05'], [])

    expect(mapping).toEqual({
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05'
    })
  })

  it('whitelist keeps GPT-5.4 mini exact mappings', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-mini'], [])

    expect(mapping).toEqual({
      'gpt-5.4-mini': 'gpt-5.4-mini'
    })
  })

  it('combined 模式会同时保留白名单身份映射和模型映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['gpt-5.4', 'claude-*'],
      [
        { from: 'gpt-latest', to: 'gpt-5.4' },
        { from: 'gpt-5.4', to: 'gpt-5.4-mini' }
      ]
    )

    expect(mapping).toEqual({
      'gpt-5.4': 'gpt-5.4-mini',
      'gpt-latest': 'gpt-5.4'
    })
  })

  it('splitModelMappingObject 会把身份映射还原成白名单，其余保留为映射', () => {
    const parsed = splitModelMappingObject({
      'gpt-5.4': 'gpt-5.4',
      'gpt-latest': 'gpt-5.4',
      ' ': 'gpt-empty',
      broken: 123
    })

    expect(parsed).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }]
    })
  })
})
