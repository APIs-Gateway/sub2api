import { describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { clientsForPlatform, codexProviderId, type OnboardingClient } from '../keyOnboarding'
import {
  KEY_PLACEHOLDER,
  MANUAL_CODE_TABS,
  availableManualCodeTabs,
  buildConfigSnippets,
  buildManualCode,
  defaultManualCodeTab,
  isManualCodeTabAvailable,
  pickExampleModel,
  type ManualCodeInput,
  type ManualCodeTab
} from '../manualSamples'

const hasBash = spawnSync('bash', ['--version']).status === 0
const hasPython = spawnSync('python3', ['--version']).status === 0
const hasPwsh = spawnSync('pwsh', ['-NoProfile', '-Command', '1']).status === 0

// 带特殊字符的地址和密钥：引号、空格、$、反引号、反斜杠
const NASTY_KEY = `sk-a'b"c $HOME \`x\` \\ d`
const NASTY_BASE = `https://example.com/it's`
const KEY = 'sk-test-1234567890'

const input = (over: Partial<ManualCodeInput> = {}): ManualCodeInput => ({
  base: 'https://api.example.com',
  platform: 'openai',
  apiKey: KEY,
  models: [],
  siteName: 'Hiyo',
  ...over
})
const codeOf = (tab: ManualCodeTab, id: string, over: Partial<ManualCodeInput> = {}) =>
  buildManualCode(tab, input(over)).find((f) => f.id === id)!.code

describe('哪些代码页签可用：跟随 clientsForPlatform', () => {
  const tabsFor = (platform: string | null, allowMessagesDispatch?: boolean) =>
    availableManualCodeTabs(clientsForPlatform(platform, { allowMessagesDispatch }))

  it.each([
    ['openai', false, ['openai', 'curl', 'codex']],
    ['openai', true, ['openai', 'curl', 'codex', 'claude']],
    ['anthropic', false, ['openai', 'curl', 'claude']],
    ['grok', false, ['openai', 'curl', 'claude']],
    ['gemini', false, ['openai', 'curl', 'gemini']],
    ['antigravity', false, ['claude', 'gemini']],
    [null, false, []],
    ['unknown', false, []]
  ] as const)('%s（调度 %s）', (platform, dispatch, expected) => {
    expect(tabsFor(platform, dispatch)).toEqual(expected)
  })

  it('每个页签的判断只看客户端清单：OpenAI SDK 和 curl 要有 Codex 或 OpenCode，其余要有同名客户端', () => {
    const only = (...c: OnboardingClient[]) => MANUAL_CODE_TABS.filter((t) => isManualCodeTabAvailable(t, c))
    expect(only('opencode')).toEqual(['openai', 'curl'])
    expect(only('codex')).toEqual(['openai', 'curl', 'codex'])
    expect(only('claude')).toEqual(['claude'])
    expect(only('gemini')).toEqual(['gemini'])
    expect(only()).toEqual([])
  })
})

describe('defaultManualCodeTab：打开时默认选中的页签', () => {
  it.each([
    ['openai', false, 'openai'],
    ['openai', true, 'openai'],
    ['anthropic', false, 'claude'],
    ['grok', false, 'claude'],
    ['gemini', false, 'gemini'],
    ['antigravity', false, 'claude'],
    [null, false, 'openai'],
    ['unknown', false, 'openai']
  ] as const)('%s（调度 %s）：%s', (platform, dispatch, expected) => {
    expect(defaultManualCodeTab(clientsForPlatform(platform, { allowMessagesDispatch: dispatch }))).toBe(expected)
  })

  it('openai 分组是 OpenAI SDK，其余分组是原生客户端页签；有页签可用时默认一定是可用的那个', () => {
    for (const platform of ['openai', 'anthropic', 'grok', 'gemini', 'antigravity']) {
      for (const dispatch of [false, true]) {
        const clients = clientsForPlatform(platform, { allowMessagesDispatch: dispatch })
        expect(isManualCodeTabAvailable(defaultManualCodeTab(clients), clients), `${platform} ${dispatch}`).toBe(true)
      }
    }
  })

  it('只看客户端清单：有 Codex 就是 OpenAI SDK，否则是第一个原生客户端（Claude Code 在 Gemini 前），都没有时是第一个可用页签', () => {
    expect(defaultManualCodeTab(['codex', 'claude'])).toBe('openai')
    expect(defaultManualCodeTab(['claude', 'gemini'])).toBe('claude')
    expect(defaultManualCodeTab(['gemini', 'claude'])).toBe('claude')
    expect(defaultManualCodeTab(['opencode'])).toBe('openai')
    expect(defaultManualCodeTab([])).toBe('openai')
  })
})

describe('pickExampleModel：示例模型取自分组', () => {
  it('还不知道分组有什么模型时用平台默认值', () => {
    expect(pickExampleModel('openai', [])).toBe('gpt-5.6-sol')
    expect(pickExampleModel('anthropic', [])).toBe('claude-sonnet-5')
    expect(pickExampleModel('gemini', [])).toBe('gemini-3.1-pro-preview')
    expect(pickExampleModel('antigravity', [])).toBe('gemini-3.1-pro-high')
    expect(pickExampleModel('grok', [])).toBe('grok-4.3')
    expect(pickExampleModel(null, [])).toBe('gpt-5.6-sol')
  })

  it('openai：分组里有 gpt-5.6-sol 就用它；没有就取第一个 gpt-*', () => {
    expect(pickExampleModel('openai', ['gpt-5.5', 'gpt-5.6-sol'])).toBe('gpt-5.6-sol')
    expect(pickExampleModel('openai', ['o3', 'gpt-5.5', 'gpt-5.6-luna'])).toBe('gpt-5.5')
  })

  it('anthropic：取 sonnet 档；没有 sonnet 取第一个 claude-*', () => {
    expect(pickExampleModel('anthropic', ['claude-opus-5', 'claude-sonnet-4-5', 'claude-haiku-5'])).toBe('claude-sonnet-4-5')
    expect(pickExampleModel('anthropic', ['claude-opus-5', 'claude-sonnet-5'])).toBe('claude-sonnet-5')
    expect(pickExampleModel('anthropic', ['claude-opus-5', 'claude-haiku-5'])).toBe('claude-opus-5')
  })

  it('跳过图片、向量这类不能对话的模型；一个对话模型都没有就用默认值', () => {
    expect(pickExampleModel('openai', ['gpt-image-2', 'text-embedding-3', 'gpt-5.5'])).toBe('gpt-5.5')
    expect(pickExampleModel('openai', ['gpt-image-2', 'text-embedding-3'])).toBe('gpt-5.6-sol')
    expect(pickExampleModel('openai', ['my-custom-model'])).toBe('my-custom-model')
  })
})

describe('buildManualCode：文本', () => {
  it('OpenAI SDK：Python，直接填好地址、密钥和分组里的模型', () => {
    const [py] = buildManualCode('openai', input({ models: ['gpt-5.5'] }))
    expect(py.label).toBe('Python')
    expect(py.code).toBe(
      [
        '# pip install openai',
        'from openai import OpenAI',
        '',
        'client = OpenAI(',
        '    base_url="https://api.example.com/v1",',
        `    api_key="${KEY}",`,
        ')',
        '',
        'resp = client.chat.completions.create(',
        '    model="gpt-5.5",',
        '    messages=[{"role": "user", "content": "Hello"}],',
        ')',
        'print(resp.choices[0].message.content)'
      ].join('\n')
    )
  })

  it('curl：bash 一份、Windows curl.exe 一份，都打到 /v1/chat/completions', () => {
    const files = buildManualCode('curl', input())
    expect(files.map((f) => f.label)).toEqual(['macOS / Linux', 'Windows PowerShell (curl.exe)'])
    expect(files[0].code).toBe(
      [
        'curl https://api.example.com/v1/chat/completions \\',
        `  -H 'Authorization: Bearer ${KEY}' \\`,
        "  -H 'Content-Type: application/json' \\",
        `  -d '{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Hello"}]}'`
      ].join('\n')
    )
    expect(files[1].code).toBe(
      [
        "@'",
        '{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Hello"}]}',
        `'@ | curl.exe https://api.example.com/v1/chat/completions -H 'Authorization: Bearer ${KEY}' -H 'Content-Type: application/json' -d '@-'`
      ].join('\n')
    )
  })

  it('Codex：密钥放环境变量，地址等用 -c 覆盖成自定义 provider（OPENAI_BASE_URL 对新版 Codex 已经不起作用），最后是验证命令', () => {
    expect(codeOf('codex', 'bash', { models: ['gpt-5.5'] })).toBe(
      [
        `export OPENAI_API_KEY='${KEY}'`,
        'codex exec --skip-git-repo-check -m gpt-5.5 \\',
        '  -c model_provider=hiyo \\',
        '  -c model_providers.hiyo.name=Hiyo \\',
        '  -c model_providers.hiyo.base_url=https://api.example.com/v1 \\',
        '  -c model_providers.hiyo.env_key=OPENAI_API_KEY \\',
        '  -c model_providers.hiyo.wire_api=responses \\',
        "  'Hello'"
      ].join('\n')
    )
    expect(codeOf('codex', 'windows')).toBe(
      [
        `$env:OPENAI_API_KEY = '${KEY}'`,
        "codex exec --skip-git-repo-check -m gpt-5.6-sol -c model_provider=hiyo -c model_providers.hiyo.name=Hiyo -c model_providers.hiyo.base_url=https://api.example.com/v1 -c model_providers.hiyo.env_key=OPENAI_API_KEY -c model_providers.hiyo.wire_api=responses 'Hello'"
      ].join('\n')
    )
  })

  it('Claude Code：用根地址（antigravity 带 /antigravity），变量是 ANTHROPIC_AUTH_TOKEN', () => {
    expect(codeOf('claude', 'bash', { platform: 'anthropic' })).toBe(
      ["export ANTHROPIC_BASE_URL='https://api.example.com'", `export ANTHROPIC_AUTH_TOKEN='${KEY}'`, "claude -p 'Hello'"].join('\n')
    )
    expect(codeOf('claude', 'windows', { platform: 'antigravity' })).toContain("$env:ANTHROPIC_BASE_URL = 'https://api.example.com/antigravity'")
  })

  it('Gemini：根地址，模型取 gemini-*', () => {
    expect(codeOf('gemini', 'bash', { platform: 'gemini', models: ['gemini-3-pro'] })).toBe(
      ["export GOOGLE_GEMINI_BASE_URL='https://api.example.com'", `export GEMINI_API_KEY='${KEY}'`, "gemini -m gemini-3-pro -p 'Hello'"].join('\n')
    )
  })

  it('配置的地址以 /v1 结尾或带斜杠时不会变成 /v1/v1', () => {
    expect(codeOf('curl', 'bash', { base: 'https://api.example.com/v1/' })).toContain('curl https://api.example.com/v1/chat/completions')
    expect(codeOf('openai', 'python', { base: 'https://api.example.com/v1/' })).toContain('base_url="https://api.example.com/v1"')
  })

  it('拿不到完整密钥：所有代码都用占位符，不出现空字符串', () => {
    for (const tab of MANUAL_CODE_TABS) {
      for (const f of buildManualCode(tab, input({ apiKey: '' }))) {
        expect(f.code, `${tab}/${f.id}`).toContain(KEY_PLACEHOLDER)
        expect(f.code).not.toMatch(/''|""/)
      }
    }
    for (const s of buildConfigSnippets(['claude', 'codex', 'gemini', 'opencode'], snippetInput({ apiKey: '' }))) {
      for (const f of s.files) expect(f.code, `${s.id}/${f.label}`).toContain(KEY_PLACEHOLDER)
    }
  })

  it('模型名不会写死：分组没有 gpt-5.6-sol 时 Codex、curl、Python 都换成分组里的', () => {
    const models = ['gpt-5.4']
    for (const [tab, id] of [['openai', 'python'], ['curl', 'bash'], ['curl', 'windows'], ['codex', 'bash'], ['codex', 'windows']] as const) {
      const code = codeOf(tab, id, { models })
      expect(code, `${tab}/${id}`).toContain('gpt-5.4')
      expect(code).not.toContain('gpt-5.6-sol')
    }
  })
})

// ---------------------------------------------------------------- 真的跑一遍

/** 在临时目录里放假的外部命令，把收到的参数、环境变量和标准输入记到文件里 */
function sandbox() {
  const dir = mkdtempSync(join(tmpdir(), 'manual-samples-'))
  const out = join(dir, 'out.bin')
  const fake = (name: string) => {
    writeFileSync(
      join(dir, name),
      // 第一段是 4 个环境变量，后面是命令行参数，再后面是标准输入；段与段用 \0 分隔
      `#!/bin/sh\n{ printf '%s\\0' "$OPENAI_BASE_URL" "$OPENAI_API_KEY" "$ANTHROPIC_BASE_URL" "$ANTHROPIC_AUTH_TOKEN" "$GOOGLE_GEMINI_BASE_URL" "$GEMINI_API_KEY" "$@"; } > "${out}"\n`
    )
    chmodSync(join(dir, name), 0o755)
  }
  for (const name of ['curl', 'codex', 'claude', 'gemini']) fake(name)
  // curl.exe 会收到管道里的 JSON：参数后面再接标准输入
  writeFileSync(join(dir, 'curl.exe'), `#!/bin/sh\n{ printf '%s\\0' "$@"; printf 'STDIN\\0'; cat; } > "${out}"\n`)
  chmodSync(join(dir, 'curl.exe'), 0o755)
  const read = () => readFileSync(out, 'utf-8').split('\0')
  return { dir, read, cleanup: () => rmSync(dir, { recursive: true, force: true }) }
}

const SHELL_ENV = (dir: string) => ({ PATH: `${dir}:/usr/bin:/bin`, HOME: dir })

/** Codex 示例应该传给 codex 的命令行参数 */
const codexArgs = (base: string, siteName: string, id = 'hiyo') => [
  'exec',
  '--skip-git-repo-check',
  '-m',
  'gpt-5.6-sol',
  '-c',
  `model_provider=${id}`,
  '-c',
  `model_providers.${id}.name=${siteName}`,
  '-c',
  `model_providers.${id}.base_url=${base}/v1`,
  '-c',
  `model_providers.${id}.env_key=OPENAI_API_KEY`,
  '-c',
  `model_providers.${id}.wire_api=responses`,
  'Hello'
]

describe.skipIf(!hasBash)('buildManualCode：bash 示例原样运行，取值带引号、空格、$、反引号也原样传过去', () => {
  const run = (code: string) => {
    const sb = sandbox()
    const res = spawnSync('bash', ['-c', code], { env: SHELL_ENV(sb.dir), encoding: 'utf-8', cwd: sb.dir })
    const parts = sb.read()
    sb.cleanup()
    return { status: res.status, stderr: res.stderr, parts }
  }

  it('curl：URL、两个请求头、JSON 请求体一字不差', () => {
    const r = run(codeOf('curl', 'bash', { base: NASTY_BASE, apiKey: NASTY_KEY, models: ['gpt-5.5'] }))
    expect(r.status, r.stderr).toBe(0)
    // 前 6 段是环境变量（这里都是空），之后是 curl 的参数
    expect(r.parts.slice(6, -1)).toEqual([
      `${NASTY_BASE}/v1/chat/completions`,
      '-H',
      `Authorization: Bearer ${NASTY_KEY}`,
      '-H',
      'Content-Type: application/json',
      '-d',
      '{"model":"gpt-5.5","messages":[{"role":"user","content":"Hello"}]}'
    ])
  })

  it('Codex：密钥在环境变量里，命令行的 -c 覆盖原样传到', () => {
    const r = run(codeOf('codex', 'bash', { base: NASTY_BASE, apiKey: NASTY_KEY, siteName: 'My "Site"' }))
    expect(r.status, r.stderr).toBe(0)
    expect(r.parts.slice(0, 2)).toEqual(['', NASTY_KEY])
    expect(r.parts.slice(6, -1)).toEqual(codexArgs(NASTY_BASE, 'My "Site"', 'mysite'))
  })

  it('Codex：站点名是纯数字或 true 时，provider id 加了 site_ 前缀，显示名用 id，命令行里没有引号', () => {
    for (const siteName of ['2024', 'true']) {
      const r = run(codeOf('codex', 'bash', { base: NASTY_BASE, apiKey: NASTY_KEY, siteName }))
      expect(r.status, r.stderr).toBe(0)
      expect(r.parts.slice(6, -1)).toEqual(codexArgs(NASTY_BASE, `site_${siteName}`, `site_${siteName}`))
    }
  })

  it('Claude Code', () => {
    const r = run(codeOf('claude', 'bash', { platform: 'anthropic', base: NASTY_BASE, apiKey: NASTY_KEY }))
    expect(r.status, r.stderr).toBe(0)
    expect(r.parts.slice(2, 4)).toEqual([NASTY_BASE, NASTY_KEY])
    expect(r.parts.slice(6, -1)).toEqual(['-p', 'Hello'])
  })

  it('Gemini', () => {
    const r = run(codeOf('gemini', 'bash', { platform: 'gemini', base: NASTY_BASE, apiKey: NASTY_KEY }))
    expect(r.status, r.stderr).toBe(0)
    expect(r.parts.slice(4, 6)).toEqual([NASTY_BASE, NASTY_KEY])
    expect(r.parts.slice(6, -1)).toEqual(['-m', 'gemini-3.1-pro-preview', '-p', 'Hello'])
  })
})

// Codex 把 -c 的值先当 TOML 解析，解析不了才当普通字符串。这里用 Python 的 tomllib 复刻这条规则，
// 确认不管站点名是什么，provider 的 name、id 到了 Codex 那边都还是字符串。
const hasTomllib = hasPython && spawnSync('python3', ['-c', 'import tomllib']).status === 0
describe.skipIf(!hasTomllib)('buildManualCode：Codex 的 -c 值按 Codex 的规则解析后仍是字符串', () => {
  const parseLikeCodex = (value: string): unknown => {
    const script = [
      'import json, sys, tomllib',
      'v = sys.stdin.read()',
      'try:',
      '    out = tomllib.loads("v = " + v)["v"]',
      'except Exception:',
      '    out = v',
      'print(json.dumps(out))'
    ].join('\n')
    return JSON.parse(spawnSync('python3', ['-c', script], { input: value, encoding: 'utf-8' }).stdout)
  }

  /** 取出每个 -c 的 key=value（值里没有空格的直接取，有空格的在 PowerShell 里被单引号包着） */
  const overridesOf = (siteName: string) => {
    const code = codeOf('codex', 'windows', { siteName })
    const overrides = [...code.matchAll(/-c (?:'([^']*(?:''[^']*)*)'|(\S+))/g)].map((m) => (m[1] ?? m[2]).replace(/''/g, "'"))
    const byKey = Object.fromEntries(overrides.map((o) => [o.slice(0, o.indexOf('=')), o.slice(o.indexOf('=') + 1)]))
    const id = Object.keys(byKey).find((k) => k.endsWith('.name'))!.split('.')[1]
    return { byKey, id }
  }

  // 这些站点名按 TOML 会被读成数字、布尔值、日期，或者写得像数组、表（[x] 本身不是合法数组，但一并回避）：provider id 加 site_ 前缀，显示名改用 id
  const NON_STRING_NAMES = ['2024', 'true', 'false', 'inf', 'nan', '-5', '+5', '3.14', '1e5', '0x1f', '1_000', '[1]', '[x]', '{a=1}', '1979-05-27', '07:32:00']
  const expectedName = (siteName: string, id: string) => (NON_STRING_NAMES.includes(siteName) ? id : siteName)

  it.each(['Hiyo', 'My Site', '土星 AI', '7eleven', '"quoted"', "it's", ...NON_STRING_NAMES])('站点名 %j', (siteName) => {
    const { byKey, id } = overridesOf(siteName)
    expect(parseLikeCodex(byKey['model_provider'])).toBe(id)
    expect(parseLikeCodex(byKey[`model_providers.${id}.name`])).toBe(expectedName(siteName, id))
    expect(parseLikeCodex(byKey[`model_providers.${id}.base_url`])).toBe('https://api.example.com/v1')
    expect(parseLikeCodex(byKey[`model_providers.${id}.env_key`])).toBe('OPENAI_API_KEY')
  })

  // Windows PowerShell 5.1 往外部程序传参数时会吃掉值里的双引号：这里模拟吃掉之后 Codex 收到的值，类型不能变
  it.each(['Hiyo', 'My Site', '7eleven', ...NON_STRING_NAMES])('PowerShell 5.1 吃掉双引号以后，站点名 %j 的 id 和显示名仍是字符串', (siteName) => {
    const { byKey, id } = overridesOf(siteName)
    const eaten = (v: string) => v.replace(/"/g, '')
    expect(parseLikeCodex(eaten(byKey['model_provider']))).toBe(id)
    expect(parseLikeCodex(eaten(byKey[`model_providers.${id}.name`]))).toBe(expectedName(siteName, id))
  })

  it('provider id 不需要引号：命令行里 model_provider 和 model_providers.<id> 都是裸值', () => {
    for (const siteName of ['2024', 'true', '1e5', '0x1f']) {
      const { byKey, id } = overridesOf(siteName)
      expect(id).toBe(codexProviderId(siteName))
      expect(byKey['model_provider']).toBe(id)
      expect(id).toMatch(/^site_/)
    }
  })
})

describe.skipIf(!hasPython)('buildManualCode：Python 示例语法正确，字符串取值原样', () => {
  it('地址、密钥、模型里有引号和反斜杠也能解析回原值', () => {
    const code = codeOf('openai', 'python', { base: NASTY_BASE, apiKey: NASTY_KEY, models: ['m"odel\\x'] })
    const script = [
      'import ast, json, sys',
      'tree = ast.parse(sys.stdin.read())',
      'vals = [n.value for n in ast.walk(tree) if isinstance(n, ast.Constant) and isinstance(n.value, str)]',
      'print(json.dumps(vals))'
    ].join('\n')
    const res = spawnSync('python3', ['-c', script], { input: code, encoding: 'utf-8' })
    expect(res.status, res.stderr).toBe(0)
    const vals = JSON.parse(res.stdout) as string[]
    expect(vals).toEqual(expect.arrayContaining([`${NASTY_BASE}/v1`, NASTY_KEY, 'm"odel\\x', 'Hello']))
  })
})

// 用 pwsh（7.x）加假的 curl.exe / codex 等跑一遍 Windows 版。
// 只能验证写法和引号；Windows PowerShell 5.1 自身的细节差异（外部程序的参数转义、管道编码）在这里验证不到。
describe.skipIf(!hasPwsh || process.platform === 'win32')('buildManualCode：PowerShell 示例', { timeout: 30_000 }, () => {
  const pwsh = spawnSync('sh', ['-c', 'command -v pwsh'], { encoding: 'utf-8' }).stdout.trim()
  const run = (code: string) => {
    const sb = sandbox()
    const file = join(sb.dir, 'script.ps1')
    writeFileSync(file, code)
    const res = spawnSync(pwsh, ['-NoProfile', '-File', file], {
      env: { PATH: `${sb.dir}:/usr/bin:/bin`, HOME: sb.dir, DOTNET_SYSTEM_GLOBALIZATION_INVARIANT: '1' },
      encoding: 'utf-8'
    })
    const parts = sb.read()
    sb.cleanup()
    return { out: res.stdout + res.stderr, status: res.status, parts }
  }

  it('curl.exe：请求体走管道（-d @-），地址和请求头一字不差', () => {
    const r = run(codeOf('curl', 'windows', { base: NASTY_BASE, apiKey: NASTY_KEY, models: ['gpt-5.5'] }))
    expect(r.status, r.out).toBe(0)
    const at = r.parts.indexOf('STDIN')
    expect(r.parts.slice(0, at)).toEqual([
      `${NASTY_BASE}/v1/chat/completions`,
      '-H',
      `Authorization: Bearer ${NASTY_KEY}`,
      '-H',
      'Content-Type: application/json',
      '-d',
      '@-'
    ])
    expect(JSON.parse(r.parts[at + 1])).toEqual({ model: 'gpt-5.5', messages: [{ role: 'user', content: 'Hello' }] })
  })

  it('Codex / Claude Code / Gemini：设置环境变量后运行命令', () => {
    const codex = run(codeOf('codex', 'windows', { base: NASTY_BASE, apiKey: NASTY_KEY }))
    expect(codex.status, codex.out).toBe(0)
    expect(codex.parts.slice(0, 2)).toEqual(['', NASTY_KEY])
    expect(codex.parts.slice(6, -1)).toEqual(codexArgs(NASTY_BASE, 'Hiyo'))
    const claude = run(codeOf('claude', 'windows', { platform: 'anthropic', base: NASTY_BASE, apiKey: NASTY_KEY }))
    expect(claude.parts.slice(2, 4)).toEqual([NASTY_BASE, NASTY_KEY])
    expect(claude.parts.slice(6, -1)).toEqual(['-p', 'Hello'])
    const gemini = run(codeOf('gemini', 'windows', { platform: 'gemini', base: NASTY_BASE, apiKey: NASTY_KEY }))
    expect(gemini.parts.slice(4, 6)).toEqual([NASTY_BASE, NASTY_KEY])
    expect(gemini.parts.slice(6, -1)).toEqual(['-m', 'gemini-3.1-pro-preview', '-p', 'Hello'])
  })

  it('请求体里的非 ASCII 字符写成 \\uXXXX，Windows PowerShell 5.1 的管道按 ASCII 传也不会变问号', () => {
    const code = codeOf('curl', 'windows', { models: ['模型-é'] })
    // eslint-disable-next-line no-control-regex
    expect(code).toMatch(/^[\u0000-\u007f]*$/)
    expect(code).toContain('\\u6a21\\u578b-\\u00e9')
  })
})

// ---------------------------------------------------------------- 配置文件片段

function snippetInput(over: Partial<ManualCodeInput> = {}) {
  return {
    ...input(over),
    envVarsLabel: 'Environment variables',
    clientLabels: { claude: 'Claude Code', codex: 'Codex CLI', gemini: 'Gemini CLI', opencode: 'OpenCode' } as Record<OnboardingClient, string>
  }
}

describe('buildConfigSnippets：按客户端的配置文件片段', () => {
  const find = (clients: OnboardingClient[], over: Partial<ManualCodeInput> = {}) => buildConfigSnippets(clients, snippetInput(over))

  it('每个客户端一段，顺序跟随客户端清单；OpenCode 也有', () => {
    expect(find(['codex', 'claude', 'opencode']).map((s) => s.title)).toEqual(['Codex CLI', 'Claude Code', 'OpenCode'])
    expect(find([])).toEqual([])
  })

  it('Claude：环境变量和 settings.json', () => {
    const [claude] = find(['claude'], { platform: 'anthropic' })
    expect(claude.files.map((f) => f.label)).toEqual(['Environment variables', '~/.claude/settings.json'])
    expect(JSON.parse(claude.files[1].code)).toEqual({ env: { ANTHROPIC_BASE_URL: 'https://api.example.com', ANTHROPIC_AUTH_TOKEN: KEY } })
  })

  it('Codex：config.toml 的模型取自分组', () => {
    const [codex] = find(['codex'], { models: ['gpt-5.5'] })
    expect(codex.files[0].label).toBe('~/.codex/config.toml')
    expect(codex.files[0].code).toContain('model = "gpt-5.5"')
    expect(codex.files[0].code).toContain('base_url = "https://api.example.com/v1"')
    expect(codex.files[0].code).toContain(`experimental_bearer_token = "${KEY}"`)
  })

  it('Gemini：环境变量，外加和一键安装写的一样的 ~/.gemini/.env', () => {
    const [gemini] = find(['gemini'], { platform: 'gemini' })
    expect(gemini.files.map((f) => f.label)).toEqual(['Environment variables', '~/.gemini/.env'])
    expect(gemini.files[1].code).toBe(`GOOGLE_GEMINI_BASE_URL='https://api.example.com'\nGEMINI_API_KEY='${KEY}'`)
  })

  it('Gemini 的 .env：值里有单引号改用双引号；写不成一行合法 .env 的值就不给这份文件', () => {
    const quoted = find(['gemini'], { platform: 'gemini', apiKey: `sk-it's` })[0]
    expect(quoted.files[1].code).toContain(`GEMINI_API_KEY="sk-it's"`)
    const broken = find(['gemini'], { platform: 'gemini', base: 'https://a.example/\nz' })[0]
    expect(broken.files.map((f) => f.label)).toEqual(['Environment variables'])
  })

  it('OpenCode：和一键安装同样写 provider.<名字>.options，名字按平台', () => {
    const json = (platform: string) => JSON.parse(find(['opencode'], { platform })[0].files[0].code)
    expect(find(['opencode'])[0].files[0].label).toBe('~/.config/opencode/opencode.json')
    expect(json('openai')).toEqual({
      $schema: 'https://opencode.ai/config.json',
      provider: { openai: { options: { baseURL: 'https://api.example.com/v1', apiKey: KEY } } }
    })
    expect(Object.keys(json('anthropic').provider)).toEqual(['anthropic'])
    expect(json('gemini').provider.google.options.baseURL).toBe('https://api.example.com/v1beta')
  })
})
