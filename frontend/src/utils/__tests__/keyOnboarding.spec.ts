import { describe, expect, it } from 'vitest'
import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import en from '@/i18n/locales/en'
import {
  AI_CLIENTS,
  CODEX_MIN_NODE_MAJOR,
  aiClientsForPlatform,
  buildAiPrompt,
  buildInstallScript,
  chatgptUrl,
  claudeUrl,
  clientsForPlatform,
  codexProviderId,
  looksLikeNonStringToml,
  docsSectionFor,
  endpointFor,
  psQuote,
  shQuote,
  tutorialHref,
  type AiClient,
  type CodexInstallMode,
  type OnboardingClient,
  type ScriptMessages
} from '../keyOnboarding'

const hasPython = spawnSync('python3', ['--version']).status === 0
const hasBash = spawnSync('bash', ['--version']).status === 0
const hasPwsh = spawnSync('pwsh', ['-NoProfile', '-Command', '1']).status === 0

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

  it('provider id 不能写出来像数字、true/false 这类 TOML 值：加 site_ 前缀，命令行里就不用加引号', () => {
    for (const [name, id] of [
      ['2024', 'site_2024'],
      ['1_000', 'site_1_000'],
      ['1e5', 'site_1e5'],
      ['0x1f', 'site_0x1f'],
      ['true', 'site_true'],
      ['False', 'site_false'],
      ['inf', 'site_inf'],
      ['NaN', 'site_nan'],
      ['2024-05', 'site_202405']
    ]) {
      expect(codexProviderId(name), name).toBe(id)
    }
    // 只是数字开头、或名字里带数字的不受影响
    expect(codexProviderId('7eleven')).toBe('7eleven')
    expect(codexProviderId('Hiyo 2')).toBe('hiyo2')
    expect(codexProviderId('site_2024')).toBe('site_2024')
    expect(codexProviderId('truelove')).toBe('truelove')
    expect(codexProviderId('sub2api')).toBe('sub2api')
  })

  it('looksLikeNonStringToml：被 TOML 读成字符串以外类型的文字', () => {
    for (const v of ['2024', '-5', '+5', '3.14', '1_000', '1e5', '1E-5', '0x1f', '0o7', '0b101', 'true', 'false', 'inf', '-inf', 'nan', '1979-05-27', '07:32:00', '[1]', '{a=1}', ' 2024 ']) {
      expect(looksLikeNonStringToml(v), v).toBe(true)
    }
    for (const v of ['Hiyo', '7eleven', 'My Site', 'truelove', 'site_2024', '', 'sub2api', '1.2.3', 'v2024']) {
      expect(looksLikeNonStringToml(v), v).toBe(false)
    }
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

describe('教程链接', () => {
  it('每个客户端对应站内文档的小节；没有对应小节的返回 null', () => {
    expect(docsSectionFor('claude')).toBe('claude-code')
    expect(docsSectionFor('codex')).toBe('codex')
    expect(docsSectionFor('opencode')).toBe('other-clients')
    expect(docsSectionFor('gemini')).toBeNull()
  })

  it('链接是站内 /docs 加小节锚点，跟随站点部署前缀', () => {
    expect(tutorialHref('codex')).toBe('/docs#codex')
    expect(tutorialHref('claude', '/')).toBe('/docs#claude-code')
    expect(tutorialHref('opencode', '/app/')).toBe('/app/docs#other-clients')
    expect(tutorialHref('opencode', '/app')).toBe('/app/docs#other-clients')
    expect(tutorialHref('gemini')).toBeNull()
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

describe('codex：完整安装与只刷新配置', () => {
  const input = { baseUrl: 'https://x.io', apiKey: 'sk-plain', platform: 'openai', siteName: 'Hiyo' }
  const make = (os: 'unix' | 'windows', mode?: CodexInstallMode) => buildInstallScript('codex', os, { ...input, mode })

  it('不指定模式就是只刷新配置：没有 npm，也不检查 Node.js', () => {
    for (const os of ['unix', 'windows'] as const) {
      const script = make(os)
      expect(script).not.toMatch(/npm/i)
      expect(script).not.toMatch(/node/i)
      expect(make(os, 'refresh')).toBe(script)
    }
  })

  it('只刷新配置：只写 config.toml，写之前先备份', () => {
    const unix = make('unix', 'refresh')
    expect(unix).toContain('$HOME/.codex/config.toml')
    expect(unix.indexOf('cp -p "$TARGET" "$B"')).toBeGreaterThan(-1)
    expect(unix.indexOf('cp -p "$TARGET" "$B"')).toBeLessThan(unix.indexOf('cat "$OUT" > "$TARGET"'))
    const win = make('windows', 'refresh')
    expect(win.indexOf('Backup-File $target')).toBeGreaterThan(-1)
    expect(win.indexOf('Backup-File $target')).toBeLessThan(win.indexOf('Save-Text $target $out'))
  })

  it('完整安装（unix）：先检查 Node.js 和 npm，再 npm install -g，最后写配置', () => {
    const script = make('unix', 'full')
    expect(script).toContain('npm install -g @openai/codex@latest')
    const at = (needle: string) => {
      const i = script.indexOf(needle)
      expect(i, needle).toBeGreaterThan(-1)
      return i
    }
    // 先找 python（写配置要用），再查 Node，再查 npm，再装，最后才动配置文件
    expect(at('command -v python3')).toBeLessThan(at('command -v node'))
    expect(at('command -v node')).toBeLessThan(at('node --version'))
    expect(at('node --version')).toBeLessThan(at('command -v npm'))
    expect(at('command -v npm')).toBeLessThan(at('npm install -g'))
    expect(at('npm install -g')).toBeLessThan(at('TS="$(date'))
    expect(at('npm install -g')).toBeLessThan(at('mkdir -p'))
    expect(at('npm install -g')).toBeLessThan(at('cp -p "$TARGET" "$B"'))
    expect(script).toContain(`-lt ${CODEX_MIN_NODE_MAJOR}`)
  })

  it('完整安装（Windows）：先检查 Node.js 和 npm，再 npm.cmd install -g，最后写配置', () => {
    const script = make('windows', 'full')
    expect(script).toContain("& npm.cmd install -g '@openai/codex@latest'")
    const at = (needle: string) => {
      const i = script.indexOf(needle)
      expect(i, needle).toBeGreaterThan(-1)
      return i
    }
    expect(at('Get-Command node')).toBeLessThan(at('node --version'))
    expect(at('node --version')).toBeLessThan(at('Get-Command npm.cmd'))
    expect(at('Get-Command npm.cmd')).toBeLessThan(at('npm.cmd install -g'))
    expect(at('npm.cmd install -g')).toBeLessThan(at('$dir = '))
    expect(at('npm.cmd install -g')).toBeLessThan(at('Backup-File $target'))
    expect(script).toContain(`$nodeMajor -lt ${CODEX_MIN_NODE_MAJOR}`)
    // 原生命令往 stderr 写字（npm 的警告）时，5.1 在 Stop 模式下会当成错误；调用期间要放宽
    expect(at("$ErrorActionPreference = 'Continue'")).toBeLessThan(at('npm.cmd install -g'))
    expect(at('npm.cmd install -g')).toBeLessThan(at('$ErrorActionPreference = $prevEap'))
  })

  it('完整安装的写配置部分与只刷新配置完全相同', () => {
    const unixFull = make('unix', 'full')
    const unixRefresh = make('unix', 'refresh')
    expect(unixFull.slice(unixFull.indexOf('TS="$(date'))).toBe(unixRefresh.slice(unixRefresh.indexOf('TS="$(date')))
    const winFull = make('windows', 'full')
    const winRefresh = make('windows', 'refresh')
    expect(winFull.slice(winFull.indexOf('$dir = '))).toBe(winRefresh.slice(winRefresh.indexOf('$dir = ')))
    // 前半段（变量、检查）除了多出来的安装步骤，其余也一样
    expect(unixFull.slice(0, unixFull.indexOf('NODE_VER=""'))).toBe(unixRefresh.slice(0, unixRefresh.indexOf('TS="$(date')))
  })

  it('完整安装不替用户装 Node.js，也不用 sudo、不下载任何东西，脚本里没有反引号', () => {
    for (const os of ['unix', 'windows'] as const) {
      const script = make(os, 'full')
      expect(script).not.toMatch(/\bsudo\b/i)
      expect(script).not.toMatch(/\b(curl|wget|Invoke-WebRequest|Invoke-RestMethod|iwr|irm|brew|winget|choco|nvm install)\b/i)
      expect(script).not.toContain('`')
      // 唯一的安装命令是 Codex 自己
      expect(script.match(/npm(\.cmd)? install/g)).toHaveLength(1)
    }
  })

  it('完整安装的 PowerShell 兼容 5.1：没有 5.1 不支持的语法，也没有同名自动变量赋值', () => {
    const script = make('windows', 'full')
    expect(script).not.toMatch(/-AsHashtable|\?\.|\?\?| \? .* : /)
    expect(script).not.toMatch(/\$(pid|home|host|input|args|error|matches)\s*=/i)
    // 5.1 默认执行策略会拒绝 npm.ps1，所以调用 npm.cmd
    expect(script).not.toMatch(/&\s+npm\s/)
  })

  it('其他客户端忽略 mode', () => {
    for (const client of ['claude', 'gemini', 'opencode'] as const) {
      for (const os of ['unix', 'windows'] as const) {
        const plain = buildInstallScript(client, os, input)
        expect(buildInstallScript(client, os, { ...input, mode: 'full' })).toBe(plain)
        expect(plain).not.toMatch(/npm/i)
      }
    }
  })

  it('提示可按界面语言替换；{version} 取运行时的 Node.js 版本，文字都走引号字面量', () => {
    const messages: Partial<ScriptMessages> = {
      nodeMissing: "需要 Node.js。it's \"x\" $HOME",
      nodeTooOld: "Node.js {version} 太旧（it's）",
      npmMissing: '没有 npm',
      npmInstalling: '安装中',
      npmPermission: '没有权限',
      npmFailed: '安装失败'
    }
    const unix = buildInstallScript('codex', 'unix', { ...input, mode: 'full', messages })
    expect(unix).toContain(`echo ${shQuote(messages.nodeMissing!)}`)
    expect(unix).toContain(`printf '%s%s%s\\n' 'Node.js ' "$NODE_VER" ${shQuote(' 太旧（it\'s）')}`)
    expect(unix).toContain(`echo ${shQuote('没有 npm')}`)
    expect(unix).toContain(`echo ${shQuote('安装中')}`)
    expect(unix).toContain(`echo ${shQuote('没有权限')}`)
    expect(unix).toContain(`echo ${shQuote('安装失败')}`)
    expect(unix).not.toContain('Installing Codex CLI')
    const win = buildInstallScript('codex', 'windows', { ...input, mode: 'full', messages })
    expect(win).toContain(`Write-Host ${psQuote(messages.nodeMissing!)} -ForegroundColor Red`)
    expect(win).toContain("('Node.js ' + $nodeVer + ' 太旧（it''s）')")
    expect(win).toContain(`Write-Host ${psQuote('没有权限')} -ForegroundColor Red`)
    expect(win).not.toContain('Installing Codex CLI')
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

describe.skipIf(!hasPython || !hasBash)('install scripts: codex full install on unix', () => {
  // 受限的 PATH：只放脚本用到的系统命令，再按需放假的 node / npm，这样「没装 Node」这种情况才能稳定复现
  const SYSTEM_TOOLS = ['bash', 'sh', 'env', 'dirname', 'date', 'mkdir', 'cat', 'rm', 'cp', 'mv', 'mktemp', 'tee', 'grep', 'chmod', 'python3']
  const resolveTool = (name: string) => spawnSync('sh', ['-c', `command -v ${name}`], { encoding: 'utf-8' }).stdout.trim()
  const tools = Object.fromEntries(SYSTEM_TOOLS.map((n) => [n, resolveTool(n)]))
  const hasTools = Object.values(tools).every(Boolean)
  const messages: Partial<ScriptMessages> = {
    nodeMissing: 'NO-NODE: install Node first',
    nodeTooOld: 'OLD-NODE {version}: upgrade',
    npmMissing: 'NO-NPM: reinstall Node',
    npmInstalling: 'INSTALLING',
    npmPermission: 'NPM-PERMISSION: use nvm or fix the prefix',
    npmFailed: 'NPM-FAILED: see above'
  }

  function runFull(opts: { node?: string; npm?: string; mode?: CodexInstallMode; files?: Record<string, string> }) {
    const home = mkdtempSync(join(tmpdir(), 'keyonb-home-'))
    const bin = mkdtempSync(join(tmpdir(), 'keyonb-bin-'))
    const log = join(bin, 'npm-args.log')
    for (const [rel, content] of Object.entries(opts.files ?? {})) {
      const full = join(home, rel)
      mkdirSync(join(full, '..'), { recursive: true })
      writeFileSync(full, content)
    }
    for (const [name, real] of Object.entries(tools)) symlinkSync(real, join(bin, name))
    const fake = (name: string, body: string) => {
      writeFileSync(join(bin, name), `#!/bin/sh\n${body}\n`)
      chmodSync(join(bin, name), 0o755)
    }
    // 固定成 Linux：macOS 上的命令行开发工具检查另有测试
    fake('uname', 'echo Linux')
    if (opts.node !== undefined) fake('node', opts.node)
    if (opts.npm !== undefined) fake('npm', opts.npm.replace('$LOG', log))
    const script = buildInstallScript('codex', 'unix', {
      baseUrl: 'https://x.io',
      apiKey: 'sk-plain',
      platform: 'openai',
      siteName: 'Hiyo',
      mode: opts.mode ?? 'full',
      messages
    })
    const res = spawnSync(tools.bash, ['-c', script], { env: { PATH: bin, HOME: home }, encoding: 'utf-8' })
    const npmArgs = existsSync(log) ? readFileSync(log, 'utf-8').trim() : null
    const cleanup = () => {
      rmSync(home, { recursive: true, force: true })
      rmSync(bin, { recursive: true, force: true })
    }
    return { home, res, npmArgs, cleanup }
  }
  const NPM_OK = 'echo "$@" > "$LOG"; echo "added 1 package"'
  const original = 'model = "old"\n\n[projects."/tmp/x"]\ntrust_level = "trusted"\n'

  it.skipIf(!hasTools)('没装 Node.js：提示并退出，不装、不碰配置', () => {
    const { home, res, npmArgs, cleanup } = runFull({ npm: NPM_OK, files: { '.codex/config.toml': original } })
    try {
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('NO-NODE: install Node first')
      expect(npmArgs).toBeNull()
      expect(readFileSync(join(home, '.codex/config.toml'), 'utf-8')).toBe(original)
      expect(readdirSync(join(home, '.codex'))).toEqual(['config.toml'])
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('Node.js 版本太低：提示当前版本并退出', () => {
    const { home, res, npmArgs, cleanup } = runFull({ node: 'echo v14.21.3', npm: NPM_OK })
    try {
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('OLD-NODE v14.21.3: upgrade')
      expect(npmArgs).toBeNull()
      expect(existsSync(join(home, '.codex'))).toBe(false)
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('版本号读不出来时按没装处理', () => {
    const { res, npmArgs, cleanup } = runFull({ node: 'exit 1', npm: NPM_OK })
    try {
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('NO-NODE')
      expect(npmArgs).toBeNull()
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('刚好满足最低版本：继续安装', () => {
    const { res, npmArgs, cleanup } = runFull({ node: `echo v${CODEX_MIN_NODE_MAJOR}.0.0`, npm: NPM_OK })
    try {
      expect(res.status, res.stdout + res.stderr).toBe(0)
      expect(npmArgs).toBe('install -g @openai/codex@latest')
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('有 Node.js 没有 npm：提示并退出，不碰配置', () => {
    const { home, res, cleanup } = runFull({ node: 'echo v20.11.0' })
    try {
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('NO-NPM: reinstall Node')
      expect(existsSync(join(home, '.codex'))).toBe(false)
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('npm 装成功：先装，再备份并写入配置，输出里有 npm 的进度', () => {
    const { home, res, npmArgs, cleanup } = runFull({ node: 'echo v20.11.0', npm: NPM_OK, files: { '.codex/config.toml': original } })
    try {
      expect(res.status, res.stdout + res.stderr).toBe(0)
      expect(npmArgs).toBe('install -g @openai/codex@latest')
      const dir = join(home, '.codex')
      const toml = readFileSync(join(dir, 'config.toml'), 'utf-8')
      expect(toml).toContain('model_provider = "hiyo"')
      expect(toml).toContain('experimental_bearer_token = "sk-plain"')
      expect(toml).toContain('[projects."/tmp/x"]')
      expect(toml).not.toContain('model = "old"')
      const bak = readdirSync(dir).filter((f) => f.startsWith('config.toml.bak-'))
      expect(bak).toHaveLength(1)
      expect(readFileSync(join(dir, bak[0]), 'utf-8')).toBe(original)
      const out = res.stdout
      expect(out.indexOf('INSTALLING')).toBeGreaterThan(-1)
      expect(out.indexOf('INSTALLING')).toBeLessThan(out.indexOf('added 1 package'))
      expect(out.indexOf('added 1 package')).toBeLessThan(out.indexOf('Backup:'))
      expect(out.indexOf('Backup:')).toBeLessThan(out.indexOf('Updated:'))
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('npm 没有权限：给出处理办法并退出，不自动 sudo，不改配置', () => {
    const { home, res, cleanup } = runFull({
      node: 'echo v20.11.0',
      npm: 'echo "npm error code EACCES" >&2; echo "npm error path /usr/local/lib/node_modules" >&2; exit 243',
      files: { '.codex/config.toml': original }
    })
    try {
      expect(res.status).toBe(1)
      // npm 自己的报错照常显示在终端里
      expect(res.stdout).toContain('npm error code EACCES')
      expect(res.stdout).toContain('NPM-PERMISSION: use nvm or fix the prefix')
      expect(res.stdout).not.toContain('NPM-FAILED')
      expect(res.stdout).not.toContain('Updated:')
      expect(readFileSync(join(home, '.codex/config.toml'), 'utf-8')).toBe(original)
      expect(readdirSync(join(home, '.codex'))).toEqual(['config.toml'])
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('npm 因其他原因失败：提示看上面的报错，不改配置', () => {
    const { home, res, cleanup } = runFull({ node: 'echo v20.11.0', npm: 'echo "npm error code E404" >&2; exit 1' })
    try {
      expect(res.status).toBe(1)
      expect(res.stdout).toContain('npm error code E404')
      expect(res.stdout).toContain('NPM-FAILED: see above')
      expect(res.stdout).not.toContain('NPM-PERMISSION')
      expect(existsSync(join(home, '.codex'))).toBe(false)
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('npm 不读脚本的标准输入：不会吞掉后面的脚本内容', () => {
    // 如果 npm 读走了脚本本身，后面的写配置就不会执行
    const { home, res, cleanup } = runFull({ node: 'echo v20.11.0', npm: 'cat >/dev/null; echo done' })
    try {
      expect(res.status, res.stdout + res.stderr).toBe(0)
      expect(existsSync(join(home, '.codex/config.toml'))).toBe(true)
    } finally {
      cleanup()
    }
  })

  it.skipIf(!hasTools)('只刷新配置：没有 Node.js 和 npm 也能运行，和以前一样', () => {
    const { home, res, npmArgs, cleanup } = runFull({ mode: 'refresh', files: { '.codex/config.toml': original } })
    try {
      expect(res.status, res.stdout + res.stderr).toBe(0)
      expect(npmArgs).toBeNull()
      expect(res.stdout).not.toContain('INSTALLING')
      expect(readFileSync(join(home, '.codex/config.toml'), 'utf-8')).toContain('model_provider = "hiyo"')
      expect(readdirSync(join(home, '.codex')).filter((f) => f.startsWith('config.toml.bak-'))).toHaveLength(1)
    } finally {
      cleanup()
    }
  })
})

describe.skipIf(!hasPwsh)('install scripts: PowerShell 语法', { timeout: 30_000 }, () => {
  it.each(['claude', 'codex', 'gemini', 'opencode'] as const)('%s 的 Windows 脚本能被 PowerShell 解析', (client) => {
    for (const mode of client === 'codex' ? (['refresh', 'full'] as const) : ([undefined] as const)) {
      const script = buildInstallScript(client, 'windows', {
        baseUrl: NASTY_BASE,
        apiKey: NASTY_KEY,
        platform: 'openai',
        siteName: "O'Neil Site",
        mode
      })
      const dir = mkdtempSync(join(tmpdir(), 'keyonb-ps-'))
      try {
        const file = join(dir, 'script.ps1')
        writeFileSync(file, script)
        const res = spawnSync(
          'pwsh',
          [
            '-NoProfile',
            '-Command',
            '$e = $null; $t = $null; [void][System.Management.Automation.Language.Parser]::ParseFile($env:KEYONB_PS_FILE, [ref]$t, [ref]$e); if ($e.Count -gt 0) { $e | ForEach-Object { $_.Message }; exit 1 }'
          ],
          { encoding: 'utf-8', env: { ...process.env, KEYONB_PS_FILE: file } }
        )
        expect(res.status, res.stdout + res.stderr).toBe(0)
      } finally {
        rmSync(dir, { recursive: true, force: true })
      }
    }
  })
})

// 用 pwsh（7.x）加假的 node / npm.cmd 跑一遍 Windows 版完整安装的流程。
// 只能验证流程和判断；Windows PowerShell 5.1 自身的细节差异在这里验证不到。
describe.skipIf(!hasPwsh || process.platform === 'win32')('install scripts: codex full install in PowerShell', { timeout: 30_000 }, () => {
  const pwsh = spawnSync('sh', ['-c', 'command -v pwsh'], { encoding: 'utf-8' }).stdout.trim()
  const messages: Partial<ScriptMessages> = {
    nodeMissing: 'NO-NODE: install Node first',
    nodeTooOld: 'OLD-NODE {version}: upgrade',
    npmMissing: 'NO-NPM: reinstall Node',
    npmInstalling: 'INSTALLING',
    npmPermission: 'NPM-PERMISSION: use nvm or fix the prefix',
    npmFailed: 'NPM-FAILED: see above'
  }
  const original = 'model = "old"\n\n[projects."/tmp/x"]\ntrust_level = "trusted"\n'

  function runPs(opts: { node?: string; npm?: string; mode?: CodexInstallMode }) {
    const home = mkdtempSync(join(tmpdir(), 'keyonb-pshome-'))
    const bin = mkdtempSync(join(tmpdir(), 'keyonb-psbin-'))
    const log = join(bin, 'npm-args.log')
    mkdirSync(join(home, '.codex'), { recursive: true })
    writeFileSync(join(home, '.codex', 'config.toml'), original)
    const fake = (name: string, body: string) => {
      writeFileSync(join(bin, name), `#!/bin/sh\n${body}\n`)
      chmodSync(join(bin, name), 0o755)
    }
    if (opts.node !== undefined) fake('node', opts.node)
    if (opts.npm !== undefined) fake('npm.cmd', `echo "$@" > "${log}"\n${opts.npm}`)
    const script = buildInstallScript('codex', 'windows', {
      baseUrl: 'https://x.io',
      apiKey: 'sk-plain',
      platform: 'openai',
      siteName: 'Hiyo',
      mode: opts.mode ?? 'full',
      messages
    })
    const file = join(bin, 'script.ps1')
    writeFileSync(file, script)
    const res = spawnSync(pwsh, ['-NoProfile', '-File', file], {
      // PATH 只留假命令，真实机器上装了 Node 也不影响「没装」这种情况
      env: { PATH: bin, USERPROFILE: home, HOME: home, DOTNET_SYSTEM_GLOBALIZATION_INVARIANT: '1' },
      encoding: 'utf-8'
    })
    const dir = join(home, '.codex')
    return {
      out: res.stdout + res.stderr,
      npmArgs: existsSync(log) ? readFileSync(log, 'utf-8').trim() : null,
      toml: readFileSync(join(dir, 'config.toml'), 'utf-8'),
      backups: readdirSync(dir).filter((f) => f.startsWith('config.toml.bak-')),
      cleanup: () => {
        rmSync(home, { recursive: true, force: true })
        rmSync(bin, { recursive: true, force: true })
      }
    }
  }

  it('没装 Node.js：提示并停止，不装、不碰配置', () => {
    const r = runPs({ npm: 'echo ok' })
    try {
      expect(r.out).toContain('NO-NODE: install Node first')
      expect(r.npmArgs).toBeNull()
      expect(r.toml).toBe(original)
      expect(r.backups).toHaveLength(0)
    } finally {
      r.cleanup()
    }
  })

  it('Node.js 版本太低、或没有 npm：提示并停止', () => {
    const old = runPs({ node: 'echo v14.21.3', npm: 'echo ok' })
    const noNpm = runPs({ node: 'echo v20.11.0' })
    try {
      expect(old.out).toContain('OLD-NODE v14.21.3: upgrade')
      expect(old.npmArgs).toBeNull()
      expect(old.toml).toBe(original)
      expect(noNpm.out).toContain('NO-NPM: reinstall Node')
      expect(noNpm.toml).toBe(original)
    } finally {
      old.cleanup()
      noNpm.cleanup()
    }
  })

  it('装成功：先 npm.cmd install -g，再备份并写入配置', () => {
    const r = runPs({ node: 'echo v20.11.0', npm: 'echo "added 1 package"' })
    try {
      expect(r.npmArgs).toBe('install -g @openai/codex@latest')
      expect(r.toml).toContain('model_provider = "hiyo"')
      expect(r.toml).toContain('experimental_bearer_token = "sk-plain"')
      expect(r.toml).toContain('[projects."/tmp/x"]')
      expect(r.backups).toHaveLength(1)
      expect(r.out.indexOf('INSTALLING')).toBeLessThan(r.out.indexOf('added 1 package'))
      expect(r.out.indexOf('added 1 package')).toBeLessThan(r.out.indexOf('Backup:'))
    } finally {
      r.cleanup()
    }
  })

  it('npm 没有权限：给出处理办法并停止；其他失败：提示看报错；两种都不改配置', () => {
    const denied = runPs({ node: 'echo v20.11.0', npm: 'echo "npm error code EACCES" >&2; exit 243' })
    const other = runPs({ node: 'echo v20.11.0', npm: 'echo "npm error code E404" >&2; exit 1' })
    try {
      expect(denied.out).toContain('npm error code EACCES')
      expect(denied.out).toContain('NPM-PERMISSION: use nvm or fix the prefix')
      expect(denied.out).not.toContain('NPM-FAILED')
      expect(denied.toml).toBe(original)
      expect(other.out).toContain('npm error code E404')
      expect(other.out).toContain('NPM-FAILED: see above')
      expect(other.out).not.toContain('NPM-PERMISSION')
      expect(other.toml).toBe(original)
      expect(denied.backups.length + other.backups.length).toBe(0)
    } finally {
      denied.cleanup()
      other.cleanup()
    }
  })

  it('只刷新配置：不检查也不调用 npm', () => {
    const r = runPs({ mode: 'refresh' })
    try {
      expect(r.npmArgs).toBeNull()
      expect(r.toml).toContain('model_provider = "hiyo"')
      expect(r.backups).toHaveLength(1)
    } finally {
      r.cleanup()
    }
  })
})

describe('交给 AI 的工具按分组过滤', () => {
  const PLATFORMS = ['openai', 'anthropic', 'grok', 'gemini', 'antigravity'] as const
  const ALWAYS: AiClient[] = ['chat', 'code', 'other']

  it.each([
    ['openai', false, ['codex', 'cursor', ...ALWAYS]],
    ['openai', true, ['codex', 'claude', 'cursor', ...ALWAYS]],
    ['anthropic', false, ['claude', 'cursor', ...ALWAYS]],
    ['grok', false, ['claude', 'cursor', ...ALWAYS]],
    ['gemini', false, ALWAYS],
    ['antigravity', false, ['claude', ...ALWAYS]]
  ] as [string, boolean, AiClient[]][])('%s 分组（开了调度：%s）：%j', (platform, allowMessagesDispatch, expected) => {
    expect(aiClientsForPlatform(platform, { allowMessagesDispatch })).toEqual(expected)
  })

  it('调度开关只对 openai 分组有影响', () => {
    for (const p of PLATFORMS.filter((x) => x !== 'openai')) {
      expect(aiClientsForPlatform(p, { allowMessagesDispatch: true })).toEqual(aiClientsForPlatform(p, { allowMessagesDispatch: false }))
    }
    expect(aiClientsForPlatform('openai')).toEqual(aiClientsForPlatform('openai', { allowMessagesDispatch: false }))
  })

  it('没有分组时一个都没有；不认识的平台只有任何分组都能用的三个', () => {
    expect(aiClientsForPlatform(null)).toEqual([])
    expect(aiClientsForPlatform(undefined)).toEqual([])
    expect(aiClientsForPlatform('')).toEqual([])
    expect(aiClientsForPlatform('unknown')).toEqual(ALWAYS)
  })

  it('第一个就是默认选中的：openai 排 Codex、Claude Code 其次，其他平台顺序同 AI_CLIENTS；每个分组至少有一个可选', () => {
    const openaiOrder: AiClient[] = ['codex', 'claude', 'cursor', ...ALWAYS]
    for (const p of PLATFORMS) for (const allowMessagesDispatch of [false, true]) {
      const list = aiClientsForPlatform(p, { allowMessagesDispatch })
      expect(list.length).toBeGreaterThan(0)
      const order = p === 'openai' ? openaiOrder : AI_CLIENTS
      expect(list).toEqual(order.filter((c) => list.includes(c)))
    }
    // openai 分组开不开调度，默认都是 Codex
    expect(aiClientsForPlatform('openai')[0]).toBe('codex')
    expect(aiClientsForPlatform('openai', { allowMessagesDispatch: true })[0]).toBe('codex')
    expect(aiClientsForPlatform('anthropic')[0]).toBe('claude')
    expect(aiClientsForPlatform('gemini')[0]).toBe('chat')
  })

  it('Claude Code 和 Codex 是否可选，与一键安装里的客户端一致', () => {
    for (const p of [...PLATFORMS, 'unknown', null]) for (const allowMessagesDispatch of [false, true]) {
      const install = clientsForPlatform(p, { allowMessagesDispatch })
      const ai = aiClientsForPlatform(p, { allowMessagesDispatch })
      expect(ai.includes('claude'), `${p} claude`).toBe(install.includes('claude'))
      expect(ai.includes('codex'), `${p} codex`).toBe(install.includes('codex'))
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
      // Claude Code 用 API 根地址，其他工具在 openai 分组下用 /v1 地址
      expect(text).toContain(client === 'claude' ? 'https://codex.hiyo.top' : 'https://codex.hiyo.top/v1')
      expect(text).toContain('Hiyo')
      expect(text).not.toMatch(/\{\w+\}/)
    }
  })

  describe('地址、接口格式和要设置的变量互相对得上', () => {
    const urlLine = (text: string) => /endpoint is (\S+) and/.exec(text)?.[1]
    const formatOf = (text: string) => /API format is (\w+)\./.exec(text)?.[1]

    it('openai 分组选 Claude Code（分组开了调度）：根地址 + Anthropic，不能是 /v1 + OpenAI', () => {
      const short = buildAiPrompt({ ...base, client: 'claude', clientLabel: 'Claude Code' })
      expect(urlLine(short)).toBe('https://codex.hiyo.top')
      expect(formatOf(short)).toBe('Anthropic')
      expect(short).toContain('ANTHROPIC_BASE_URL')
      expect(short).not.toContain('/v1')
      expect(short).not.toContain('OpenAI')

      const detailed = buildAiPrompt({ ...base, client: 'claude', clientLabel: 'Claude Code', detailed: true })
      expect(detailed).toContain('- Endpoint: https://codex.hiyo.top\n')
      expect(detailed).toContain('- API format: Anthropic')
      expect(detailed).not.toContain('/v1')
    })

    it('openai 分组选 Codex：/v1 地址 + OpenAI', () => {
      const short = buildAiPrompt({ ...base, client: 'codex', clientLabel: 'Codex' })
      expect(urlLine(short)).toBe('https://codex.hiyo.top/v1')
      expect(formatOf(short)).toBe('OpenAI')
      expect(short).toContain('wire_api')
    })

    it.each([
      ['anthropic', 'https://codex.hiyo.top'],
      ['grok', 'https://codex.hiyo.top'],
      ['antigravity', 'https://codex.hiyo.top/antigravity']
    ])('%s 分组选 Claude Code：%s + Anthropic', (platform, url) => {
      const short = buildAiPrompt({ ...base, platform, client: 'claude', clientLabel: 'Claude Code' })
      expect(urlLine(short)).toBe(url)
      expect(formatOf(short)).toBe('Anthropic')
    })

    it('Claude Code 的地址跟着选中的线路走：根地址不带结尾的 / 和 /v1', () => {
      for (const baseUrl of ['https://cdn.example.com/', 'https://cdn.example.com/v1', ' https://cdn.example.com// ']) {
        const short = buildAiPrompt({ ...base, baseUrl, client: 'claude', clientLabel: 'Claude Code' })
        expect(urlLine(short)).toBe('https://cdn.example.com')
      }
    })

    it('其他工具不受影响：按分组平台的原生接口', () => {
      for (const client of ['cursor', 'chat', 'code', 'other'] as const) {
        const openai = buildAiPrompt({ ...base, client, clientLabel: client })
        expect(urlLine(openai)).toBe('https://codex.hiyo.top/v1')
        expect(formatOf(openai)).toBe('OpenAI')
        const gemini = buildAiPrompt({ ...base, platform: 'gemini', client, clientLabel: client })
        expect(urlLine(gemini)).toBe('https://codex.hiyo.top')
        expect(formatOf(gemini)).toBe('Gemini')
      }
    })
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
