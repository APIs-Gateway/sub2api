import { describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import en from '@/i18n/locales/en'
import {
  AI_CLIENTS,
  buildAiPrompt,
  buildInstallScript,
  chatgptUrl,
  claudeUrl,
  clientsForPlatform,
  codexProviderId,
  endpointFor,
  psQuote,
  shQuote,
  type OnboardingClient,
  type ScriptMessages
} from '../keyOnboarding'

const hasPython = spawnSync('python3', ['--version']).status === 0
const hasBash = spawnSync('bash', ['--version']).status === 0

// 带特殊字符的地址和密钥：引号、空格、$、反引号、反斜杠
const NASTY_KEY = `sk-a'b"c $HOME \`x\` \\ d`
const NASTY_BASE = `https://example.com/it's`

function lookup(obj: unknown, path: string): string | undefined {
  let cur = obj as Record<string, unknown> | string | undefined
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = cur[seg] as Record<string, unknown> | string | undefined
  }
  return typeof cur === 'string' ? cur : undefined
}
const enT = (key: string, params: Record<string, unknown> = {}) =>
  (lookup(en, key) ?? key).replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? ''))

describe('quoting', () => {
  it('shQuote 处理单引号', () => {
    expect(shQuote(`a'b`)).toBe(`'a'\\''b'`)
    expect(shQuote('$HOME `x`')).toBe("'$HOME `x`'")
  })

  it('psQuote 加倍直引号和弯引号', () => {
    expect(psQuote(`a'b`)).toBe(`'a''b'`)
    expect(psQuote('a‘b’c')).toBe("'a‘‘b’’c'")
    expect(psQuote('$env:X "y"')).toBe(`'$env:X "y"'`)
  })

  it('provider id 只含安全字符且不与内置重名', () => {
    expect(codexProviderId('My Site!')).toBe('mysite')
    expect(codexProviderId('')).toBe('sub2api')
    expect(codexProviderId('OpenAI')).toBe('openai_site')
  })
})

describe('clients and endpoints', () => {
  it('按平台给出可用客户端', () => {
    expect(clientsForPlatform('anthropic')).toEqual(['claude', 'opencode'])
    expect(clientsForPlatform('openai')).toEqual(['codex', 'opencode'])
    expect(clientsForPlatform('openai', { allowMessagesDispatch: true })).toContain('claude')
    expect(clientsForPlatform('gemini')).toEqual(['gemini', 'opencode'])
    expect(clientsForPlatform('antigravity')).toEqual(['claude', 'gemini'])
    expect(clientsForPlatform(null)).toEqual([])
  })

  it('各客户端的接口地址', () => {
    expect(endpointFor('claude', 'anthropic', 'https://x.io/')).toBe('https://x.io')
    expect(endpointFor('claude', 'antigravity', 'https://x.io')).toBe('https://x.io/antigravity')
    expect(endpointFor('codex', 'openai', 'https://x.io/v1')).toBe('https://x.io/v1')
    expect(endpointFor('opencode', 'gemini', 'https://x.io')).toBe('https://x.io/v1beta')
  })
})

describe('install scripts: text', () => {
  const clients: OnboardingClient[] = ['claude', 'codex', 'gemini', 'opencode']
  const input = { baseUrl: NASTY_BASE, apiKey: NASTY_KEY, platform: 'anthropic', siteName: "O'Neil Site" }

  it.each(clients)('%s 脚本：不联网、先备份、结构里没有反引号', (client) => {
    for (const os of ['unix', 'windows'] as const) {
      // 反引号是 PowerShell 的转义符；取值本身可以含反引号（在引号里），这里只检查脚本骨架
      const script = buildInstallScript(client, os, { ...input, apiKey: 'sk-plain', baseUrl: 'https://x.io' })
      expect(script).not.toMatch(/\b(curl|wget|Invoke-WebRequest|Invoke-RestMethod|iwr|irm)\b/i)
      expect(script).not.toContain('`')
      expect(script).toMatch(/bak-/)
      expect(script).toMatch(os === 'unix' ? /\$TS/ : /\$ts/)
    }
  })

  it('unix 脚本把值放进 shell 单引号字面量', () => {
    const script = buildInstallScript('claude', 'unix', input)
    expect(script).toContain(`export SUB_KEY=${shQuote(NASTY_KEY)}`)
    expect(script).toContain(`export SUB_ENDPOINT=${shQuote(NASTY_BASE)}`)
    expect(script.startsWith("bash <<'SUB2API_INSTALL_EOF'")).toBe(true)
  })

  it('windows 脚本把值放进 PowerShell 单引号字面量，且兼容 5.1', () => {
    const script = buildInstallScript('claude', 'windows', {
      ...input,
      apiKey: `k'’"$x`
    })
    expect(script).toContain(`$SubKey = ${psQuote(`k'’"$x`)}`)
    // 5.1 没有的写法：-AsHashtable、?. 、?? 、三元
    expect(script).not.toMatch(/-AsHashtable|\?\.|\?\?| \? .* : /)
    // 不能用带 BOM 的写入
    expect(script).not.toMatch(/Set-Content|Out-File/)
    expect(script).toContain('UTF8Encoding($false)')
    expect(script).toContain('Copy-Item')
    expect(script.startsWith('& {')).toBe(true)
  })

  it('windows 脚本没有与 PowerShell 自动变量同名的赋值', () => {
    for (const c of clients) {
      const script = buildInstallScript(c, 'windows', input)
      expect(script).not.toMatch(/\$(pid|home|host|input|args|error|matches)\s*=/i)
    }
  })

  it('unix 脚本：macOS 上先检查命令行开发工具，没有就提示并退出，检查在找 python 之前', () => {
    const script = buildInstallScript('claude', 'unix', input)
    const check = script.indexOf('xcode-select -p >/dev/null 2>&1')
    expect(check).toBeGreaterThan(-1)
    expect(script).toContain('"$(uname -s)" = "Darwin"')
    expect(check).toBeLessThan(script.indexOf('command -v python3'))
    // Windows 脚本没有这一步
    expect(buildInstallScript('claude', 'windows', input)).not.toContain('xcode-select')
  })

  it('claude 脚本写入前清掉残留的 ANTHROPIC_* 变量（unix 与 PowerShell 都有）', () => {
    const stale = [
      'ANTHROPIC_API_KEY',
      'ANTHROPIC_MODEL',
      'ANTHROPIC_DEFAULT_HAIKU_MODEL',
      'ANTHROPIC_DEFAULT_SONNET_MODEL',
      'ANTHROPIC_DEFAULT_OPUS_MODEL',
      'ANTHROPIC_SMALL_FAST_MODEL'
    ]
    const unix = buildInstallScript('claude', 'unix', input)
    const win = buildInstallScript('claude', 'windows', input)
    for (const name of stale) {
      expect(unix).toContain(`'${name}'`)
      expect(win).toContain(`'${name}'`)
    }
    expect(unix).toContain('env.pop(k, None)')
    expect(win).toContain('$envObj.PSObject.Properties.Remove($k)')
    // 先删旧变量，再写新地址
    expect(unix.indexOf('env.pop(k, None)')).toBeLessThan(unix.indexOf("env['ANTHROPIC_BASE_URL']"))
    expect(win.indexOf('Properties.Remove($k)')).toBeLessThan(win.indexOf("Set-Prop $envObj 'ANTHROPIC_BASE_URL'"))
    // 其他客户端不碰这些变量
    expect(buildInstallScript('codex', 'unix', input)).not.toContain('ANTHROPIC_API_KEY')
  })

  it('备份文件名同一秒内重跑会递增序号，不覆盖已有备份（unix 与 PowerShell）', () => {
    const unix = buildInstallScript('claude', 'unix', input)
    expect(unix).toContain('while [ -e "$B" ]; do N=$((N+1)); B="$TARGET.bak-$TS-$N"; done')
    expect(unix).toContain('cp -p "$TARGET" "$B"')
    const win = buildInstallScript('claude', 'windows', input)
    expect(win).toContain('while (Test-Path -LiteralPath $b) { $n++; $b = $p + \'.bak-\' + $ts + \'-\' + $n }')
  })

  it('终端提示默认英文，可按界面语言替换；取值走引号字面量', () => {
    const unix = buildInstallScript('claude', 'unix', input)
    expect(unix).toContain("printf '%s%s%s\\n' 'Backup: ' \"$B\" ''")
    expect(unix).toContain("printf '%s%s%s\\n' 'Updated: ' \"$TARGET\" ''")
    expect(unix).toContain("echo 'python3 is required to update the config file.'")
    const win = buildInstallScript('claude', 'windows', input)
    expect(win).toContain("Write-Host ('Backup: ' + $b + '')")
    expect(win).toContain("Write-Host ('Updated: ' + $target + '')")
    expect(win).toContain("Write-Host ('Failed: ' + $_.Exception.Message + '')")

    const messages: ScriptMessages = {
      pythonMissing: "需要 Python 3。it's \"quoted\" $HOME",
      xcodeMissing: '请改用「CC Switch」或「手动配置」页签。',
      backup: "已备份：{path}（it's）",
      updated: '已更新：{path}',
      failed: '失败：{error}'
    }
    const zhUnix = buildInstallScript('claude', 'unix', { ...input, messages })
    expect(zhUnix).toContain(`echo ${shQuote(messages.pythonMissing)}`)
    expect(zhUnix).toContain(`echo ${shQuote(messages.xcodeMissing)}`)
    expect(zhUnix).toContain(`printf '%s%s%s\\n' ${shQuote('已备份：')} "$B" ${shQuote('（it\'s）')}`)
    expect(zhUnix).not.toContain('Backup:')
    const zhWin = buildInstallScript('claude', 'windows', { ...input, messages })
    expect(zhWin).toContain("Write-Host ('已备份：' + $b + '（it''s）')")
    expect(zhWin).toContain("Write-Host ('失败：' + $_.Exception.Message + '') -ForegroundColor Red")
    expect(zhWin).not.toContain('Failed:')

    // 空白的翻译退回默认；没有占位符时值接在整句后面
    const partial = buildInstallScript('claude', 'unix', { ...input, messages: { backup: 'Saved', updated: '   ' } })
    expect(partial).toContain("printf '%s%s%s\\n' 'Saved ' \"$B\" ''")
    expect(partial).toContain("printf '%s%s%s\\n' 'Updated: ' \"$TARGET\" ''")
  })

  it('codex 的 wire_api 用 responses', () => {
    expect(buildInstallScript('codex', 'unix', { ...input, platform: 'openai' })).toContain('wire_api = "responses"')
    expect(buildInstallScript('codex', 'windows', { ...input, platform: 'openai' })).toContain('wire_api = "responses"')
  })
})

describe.skipIf(!hasPython || !hasBash)('install scripts: run on unix', () => {
  /**
   * opts.home：沿用已有的 HOME（模拟重复运行）；opts.bin：放在 PATH 最前面的假命令（名字 -> sh 脚本体），
   * 用来模拟 macOS（uname）、没装命令行开发工具（xcode-select）、同一秒内重跑（date）。
   */
  function run(
    client: OnboardingClient,
    files: Record<string, string>,
    extra: Partial<Parameters<typeof buildInstallScript>[2]> = {},
    opts: { home?: string; bin?: Record<string, string> } = {}
  ) {
    const home = opts.home ?? mkdtempSync(join(tmpdir(), 'keyonb-'))
    for (const [rel, content] of Object.entries(files)) {
      const full = join(home, rel)
      mkdirSync(join(full, '..'), { recursive: true })
      writeFileSync(full, content)
    }
    let pathEnv = process.env.PATH ?? ''
    let binDir = ''
    if (opts.bin) {
      binDir = mkdtempSync(join(tmpdir(), 'keyonb-bin-'))
      for (const [name, body] of Object.entries(opts.bin)) {
        writeFileSync(join(binDir, name), `#!/bin/sh\n${body}\n`)
        chmodSync(join(binDir, name), 0o755)
      }
      pathEnv = `${binDir}:${pathEnv}`
    }
    const script = buildInstallScript(client, 'unix', {
      baseUrl: NASTY_BASE,
      apiKey: NASTY_KEY,
      platform: client === 'codex' ? 'openai' : client === 'gemini' ? 'gemini' : 'anthropic',
      siteName: "O'Neil Site",
      ...extra
    })
    const res = spawnSync('bash', ['-c', script], {
      env: { PATH: pathEnv, HOME: home, XDG_CONFIG_HOME: '' },
      encoding: 'utf-8'
    })
    if (binDir) rmSync(binDir, { recursive: true, force: true })
    return { home, res }
  }
  const backups = (dir: string, name: string) => readdirSync(dir).filter((f) => f.startsWith(`${name}.bak-`))

  it('claude：保留原有设置、写入地址和密钥、先备份', () => {
    const original = JSON.stringify({ theme: 'dark', env: { FOO: '1' } })
    const { home, res } = run('claude', { '.claude/settings.json': original })
    try {
      expect(res.status, res.stderr).toBe(0)
      const dir = join(home, '.claude')
      const saved = JSON.parse(readFileSync(join(dir, 'settings.json'), 'utf-8'))
      expect(saved.theme).toBe('dark')
      expect(saved.env.FOO).toBe('1')
      expect(saved.env.ANTHROPIC_BASE_URL).toBe(NASTY_BASE)
      expect(saved.env.ANTHROPIC_AUTH_TOKEN).toBe(NASTY_KEY)
      const bak = backups(dir, 'settings.json')
      expect(bak).toHaveLength(1)
      expect(bak[0]).toMatch(/^settings\.json\.bak-\d{8}-\d{6}$/)
      expect(readFileSync(join(dir, bak[0]), 'utf-8')).toBe(original)
      expect(res.stdout).toContain(`Backup: ${join(dir, bak[0])}`)
      expect(res.stdout).toContain(`Updated: ${join(dir, 'settings.json')}`)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('claude：写入前清掉残留的 ANTHROPIC_* 变量，其他设置保留', () => {
    const original = JSON.stringify({
      env: {
        FOO: '1',
        ANTHROPIC_API_KEY: 'old-key',
        ANTHROPIC_AUTH_TOKEN: 'old-token',
        ANTHROPIC_MODEL: 'old-model',
        ANTHROPIC_DEFAULT_HAIKU_MODEL: 'h',
        ANTHROPIC_DEFAULT_SONNET_MODEL: 's',
        ANTHROPIC_DEFAULT_OPUS_MODEL: 'o',
        ANTHROPIC_SMALL_FAST_MODEL: 'f'
      }
    })
    const { home, res } = run('claude', { '.claude/settings.json': original })
    try {
      expect(res.status, res.stderr).toBe(0)
      const dir = join(home, '.claude')
      const saved = JSON.parse(readFileSync(join(dir, 'settings.json'), 'utf-8'))
      expect(Object.keys(saved.env).sort()).toEqual(
        ['ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC', 'FOO'].sort()
      )
      expect(saved.env.ANTHROPIC_AUTH_TOKEN).toBe(NASTY_KEY)
      // 原文件已备份，旧变量留在备份里
      const bak = backups(dir, 'settings.json')
      expect(JSON.parse(readFileSync(join(dir, bak[0]), 'utf-8')).env.ANTHROPIC_API_KEY).toBe('old-key')
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('同一秒内连续运行两次：第一次的原始备份不会被覆盖，后面的备份递增序号', () => {
    const original = JSON.stringify({ theme: 'dark' })
    const bin = { date: 'echo 20260101-000000' }
    const first = run('claude', { '.claude/settings.json': original }, {}, { bin })
    try {
      expect(first.res.status, first.res.stderr).toBe(0)
      const dir = join(first.home, '.claude')
      const second = run('claude', {}, {}, { home: first.home, bin })
      expect(second.res.status, second.res.stderr).toBe(0)
      const third = run('claude', {}, {}, { home: first.home, bin })
      expect(third.res.status, third.res.stderr).toBe(0)

      expect(backups(dir, 'settings.json').sort()).toEqual([
        'settings.json.bak-20260101-000000',
        'settings.json.bak-20260101-000000-2',
        'settings.json.bak-20260101-000000-3'
      ])
      // 最早那份仍是用户的原始文件
      expect(readFileSync(join(dir, 'settings.json.bak-20260101-000000'), 'utf-8')).toBe(original)
      expect(JSON.parse(readFileSync(join(dir, 'settings.json.bak-20260101-000000-2'), 'utf-8')).env.ANTHROPIC_AUTH_TOKEN).toBe(NASTY_KEY)
      expect(second.res.stdout).toContain(`Backup: ${join(dir, 'settings.json.bak-20260101-000000-2')}`)
    } finally {
      rmSync(first.home, { recursive: true, force: true })
    }
  })

  describe('macOS 没装命令行开发工具', () => {
    const messages: Partial<ScriptMessages> = {
      xcodeMissing: '请改用「CC Switch」或「手动配置」页签。 "q" $HOME',
      backup: '已备份：{path}',
      updated: '已更新：{path}'
    }
    const original = JSON.stringify({ theme: 'dark' })

    it('xcode-select -p 失败：打印提示（跟随界面语言）并退出，不改任何文件', () => {
      const { home, res } = run(
        'claude',
        { '.claude/settings.json': original },
        { messages },
        { bin: { uname: 'echo Darwin', 'xcode-select': 'exit 1' } }
      )
      try {
        expect(res.status).toBe(1)
        expect(res.stdout).toContain(messages.xcodeMissing)
        expect(res.stdout).not.toContain('Updated')
        const dir = join(home, '.claude')
        expect(readFileSync(join(dir, 'settings.json'), 'utf-8')).toBe(original)
        expect(readdirSync(dir)).toEqual(['settings.json'])
      } finally {
        rmSync(home, { recursive: true, force: true })
      }
    })

    it('没有 .claude 目录时也不会创建任何文件', () => {
      const { home, res } = run('claude', {}, {}, { bin: { uname: 'echo Darwin', 'xcode-select': 'exit 1' } })
      try {
        expect(res.status).toBe(1)
        expect(res.stdout).toContain('command line developer tools')
        expect(existsSync(join(home, '.claude'))).toBe(false)
      } finally {
        rmSync(home, { recursive: true, force: true })
      }
    })

    it('xcode-select -p 成功：照常写入，提示也用界面语言', () => {
      const { home, res } = run(
        'claude',
        { '.claude/settings.json': original },
        { messages },
        { bin: { uname: 'echo Darwin', 'xcode-select': 'echo /Library/Developer/CommandLineTools' } }
      )
      try {
        expect(res.status, res.stderr).toBe(0)
        const dir = join(home, '.claude')
        expect(JSON.parse(readFileSync(join(dir, 'settings.json'), 'utf-8')).env.ANTHROPIC_AUTH_TOKEN).toBe(NASTY_KEY)
        expect(res.stdout).toContain(`已更新：${join(dir, 'settings.json')}`)
        expect(res.stdout).toMatch(/已备份：.*settings\.json\.bak-\d{8}-\d{6}/)
        expect(res.stdout).not.toContain('Updated:')
      } finally {
        rmSync(home, { recursive: true, force: true })
      }
    })

    it('不是 macOS 时不检查 xcode-select', () => {
      const { home, res } = run(
        'claude',
        {},
        {},
        { bin: { uname: 'echo Linux', 'xcode-select': 'exit 1' } }
      )
      try {
        expect(res.status, res.stderr).toBe(0)
        expect(existsSync(join(home, '.claude/settings.json'))).toBe(true)
      } finally {
        rmSync(home, { recursive: true, force: true })
      }
    })
  })

  it('python 缺失：用界面语言提示并退出，不创建任何文件', () => {
    // PATH 里只有 bash 和一个假的 uname，找不到 python
    const tools = mkdtempSync(join(tmpdir(), 'keyonb-tools-'))
    const home = mkdtempSync(join(tmpdir(), 'keyonb-'))
    try {
      const bash = spawnSync('sh', ['-c', 'command -v bash'], { encoding: 'utf-8' }).stdout.trim()
      writeFileSync(join(tools, 'bash'), `#!/bin/sh\nexec ${bash} "$@"\n`)
      writeFileSync(join(tools, 'uname'), '#!/bin/sh\necho Linux\n')
      chmodSync(join(tools, 'bash'), 0o755)
      chmodSync(join(tools, 'uname'), 0o755)
      const script = buildInstallScript('claude', 'unix', {
        baseUrl: 'https://x.io',
        apiKey: 'sk-plain',
        platform: 'anthropic',
        messages: { pythonMissing: '更新配置文件需要 Python 3。' }
      })
      const res = spawnSync(bash, ['-c', script], { env: { PATH: tools, HOME: home }, encoding: 'utf-8' })
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('更新配置文件需要 Python 3。')
      expect(existsSync(join(home, '.claude'))).toBe(false)
    } finally {
      rmSync(tools, { recursive: true, force: true })
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('claude：原配置不是合法 JSON 时报错且不改动', () => {
    const { home, res } = run('claude', { '.claude/settings.json': '{not json' })
    try {
      expect(res.status).not.toBe(0)
      expect(readFileSync(join(home, '.claude/settings.json'), 'utf-8')).toBe('{not json')
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('claude：没有原配置时直接创建，不产生备份', () => {
    const { home, res } = run('claude', {})
    try {
      expect(res.status, res.stderr).toBe(0)
      expect(backups(join(home, '.claude'), 'settings.json')).toHaveLength(0)
      expect(existsSync(join(home, '.claude/settings.json'))).toBe(true)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('codex：保留其他设置，替换旧的同名提供方，并备份', () => {
    const original = [
      'model = "old-model"',
      'model_reasoning_effort = "high"',
      '',
      '[projects."/tmp/x"]',
      'trust_level = "trusted"',
      '',
      '[model_providers.oneilsite]',
      'name = "stale"',
      'base_url = "https://stale"',
      ''
    ].join('\n')
    const { home, res } = run('codex', { '.codex/config.toml': original })
    try {
      expect(res.status, res.stderr).toBe(0)
      const dir = join(home, '.codex')
      const toml = readFileSync(join(dir, 'config.toml'), 'utf-8')
      expect(toml).toContain('model_reasoning_effort = "high"')
      expect(toml).toContain('[projects."/tmp/x"]')
      expect(toml).not.toContain('old-model')
      expect(toml).not.toContain('stale')
      expect(toml.match(/^model_provider = /gm)).toHaveLength(1)
      expect(toml).toContain('model_provider = "oneilsite"')
      expect(toml).toContain('wire_api = "responses"')
      // 密钥里的反斜杠和双引号按 TOML 转义
      expect(toml).toContain('experimental_bearer_token = "sk-a\'b\\"c $HOME `x` \\\\ d"')
      expect(readFileSync(join(dir, backups(dir, 'config.toml')[0]), 'utf-8')).toBe(original)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('gemini：替换旧的变量，保留其他行', () => {
    const { home, res } = run('gemini', { '.gemini/.env': 'FOO=1\nexport GEMINI_API_KEY=old\n' })
    try {
      expect(res.status, res.stderr).toBe(0)
      const env = readFileSync(join(home, '.gemini/.env'), 'utf-8')
      expect(env).toContain('FOO=1')
      expect(env).not.toContain('=old')
      expect(env).toContain(`GEMINI_API_KEY="${NASTY_KEY}"`)
      expect(env).toContain(`GOOGLE_GEMINI_BASE_URL="${NASTY_BASE}"`)
      expect(backups(join(home, '.gemini'), '.env')).toHaveLength(1)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('opencode：只合并 provider 的地址和密钥', () => {
    const original = JSON.stringify({ theme: 'x', provider: { anthropic: { options: { timeout: 5 } } } })
    const { home, res } = run('opencode', { '.config/opencode/opencode.json': original })
    try {
      expect(res.status, res.stderr).toBe(0)
      const dir = join(home, '.config/opencode')
      const saved = JSON.parse(readFileSync(join(dir, 'opencode.json'), 'utf-8'))
      expect(saved.theme).toBe('x')
      expect(saved.provider.anthropic.options).toEqual({
        timeout: 5,
        baseURL: `${NASTY_BASE}/v1`,
        apiKey: NASTY_KEY
      })
      expect(backups(dir, 'opencode.json')).toHaveLength(1)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })
})

describe('交给 AI 的文本', () => {
  const SECRET = 'sk-SECRET-1234567890abcdef'
  const base = {
    t: enT,
    baseUrl: 'https://codex.hiyo.top/',
    platform: 'openai',
    siteName: 'Hiyo',
    models: ['gpt-5.6-sol', 'gpt-5.6-luna'],
    docUrl: 'https://docs.example.com'
  }

  it.each(AI_CLIENTS)('%s：简短版和详细版都不含密钥，且带地址', (client) => {
    for (const detailed of [false, true]) {
      const text = buildAiPrompt({ ...base, client, clientLabel: client, detailed })
      expect(text).not.toContain(SECRET)
      expect(text).not.toContain('sk-')
      expect(text).toContain('https://codex.hiyo.top/v1')
      expect(text).toContain('Hiyo')
      expect(text).not.toMatch(/\{\w+\}/)
    }
  })

  it('详细版带模型列表、文档地址和配置要点', () => {
    const text = buildAiPrompt({ ...base, client: 'codex', clientLabel: 'Codex', detailed: true })
    expect(text).toContain('gpt-5.6-sol, gpt-5.6-luna')
    expect(text).toContain('https://docs.example.com')
    expect(text).toContain('config.toml')
    expect(text.split('\n').length).toBeGreaterThan(4)
  })

  it('简短版是一段话；没有文档地址时不带文档行', () => {
    const text = buildAiPrompt({ ...base, docUrl: '', client: 'claude', clientLabel: 'Claude Code' })
    expect(text).not.toContain('\n')
    expect(text).not.toContain('Docs:')
  })

  it('打开链接把文本编码进 q 参数', () => {
    const text = 'a b&c=d?'
    expect(chatgptUrl(text)).toBe('https://chatgpt.com/?q=a%20b%26c%3Dd%3F')
    expect(claudeUrl(text)).toBe('https://claude.ai/new?q=a%20b%26c%3Dd%3F')
  })
})
