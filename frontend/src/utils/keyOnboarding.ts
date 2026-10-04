import type { GroupPlatform } from '@/types'
import { normalizeApiBase } from '@/utils/apiEndpoints'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'

/**
 * 「一键安装」脚本与「交给 AI」文本的纯函数生成器。
 *
 * 约定：
 * - 脚本完全内联，不联网下载；所有取值都先放进单引号字面量（再经环境变量/变量传给
 *   解释器），不拼进 JSON / TOML 源码，所以地址和密钥里有引号、空格、$ 等字符也不会破坏脚本。
 * - 修改任何已有配置文件之前，先复制一份带时间戳的备份（<文件>.bak-<时间戳>）；同一秒内重复运行时
 *   在文件名后加序号（-2、-3……），不会覆盖更早的备份。
 * - 脚本里不使用反引号（PowerShell 的转义符，粘贴时也容易被聊天软件改写）。
 * - Codex 有两种脚本：「刷新配置」只写 config.toml；「完整安装」先检查 Node.js / npm 并用 npm 装好
 *   Codex CLI，再执行同一段写配置的逻辑。脚本从不替用户安装 Node.js，也不使用 sudo。
 * - 「交给 AI」文本永远不包含密钥。
 */

export type OnboardingClient = 'claude' | 'codex' | 'gemini' | 'opencode'
export type ScriptOs = 'unix' | 'windows'
/** Codex 的脚本类型：full 先装 CLI 再写配置，refresh 只写配置（默认）。其他客户端忽略。 */
export type CodexInstallMode = 'full' | 'refresh'

export const CLIENT_LABELS: Record<OnboardingClient, string> = {
  claude: 'Claude Code',
  codex: 'Codex CLI',
  gemini: 'Gemini CLI',
  opencode: 'OpenCode'
}

/** 完整安装要求的 Node.js 主版本号，与使用文档 codex 小节一致（16 或更高）。 */
export const CODEX_MIN_NODE_MAJOR = 16
export const CODEX_NPM_PACKAGE = '@openai/codex@latest'

/**
 * 站内使用文档（/docs）里各客户端对应的小节 id，以文档页 sections.ts 为准。
 * 没有对应小节的客户端（目前是 Gemini CLI）不显示教程链接；OpenCode 归在「其他兼容 OpenAI 的客户端」。
 */
const DOCS_SECTIONS: Partial<Record<OnboardingClient, string>> = {
  claude: 'claude-code',
  codex: 'codex',
  opencode: 'other-clients'
}

export function docsSectionFor(client: OnboardingClient): string | null {
  return DOCS_SECTIONS[client] ?? null
}

/** 教程链接：站内文档页的对应小节；没有对应小节时返回 null。basePath 是站点的部署前缀（默认根路径）。 */
export function tutorialHref(client: OnboardingClient, basePath = '/'): string | null {
  const section = docsSectionFor(client)
  if (!section) return null
  const root = (basePath || '/').replace(/\/?$/, '/')
  return `${root}docs#${section}`
}

/** 各平台的分组能用的客户端；没有分组（platform 为空）时没有可用客户端。 */
export function clientsForPlatform(
  platform: GroupPlatform | string | null | undefined,
  opts: { allowMessagesDispatch?: boolean } = {}
): OnboardingClient[] {
  switch (platform) {
    case 'openai':
      return opts.allowMessagesDispatch ? ['codex', 'claude', 'opencode'] : ['codex', 'opencode']
    case 'gemini':
      return ['gemini', 'opencode']
    case 'antigravity':
      return ['claude', 'gemini']
    case 'anthropic':
    case 'grok':
      return ['claude', 'opencode']
    default:
      return []
  }
}

// ---------------------------------------------------------------- 地址

// 地址归一化与文档页、接入弹窗的线路选择共用一套（utils/apiEndpoints.ts）
const rootUrl = normalizeApiBase

function withV1(root: string): string {
  return `${root}/v1`
}

/** 各客户端实际要填的接口地址。 */
export function endpointFor(
  client: OnboardingClient,
  platform: GroupPlatform | string | null | undefined,
  baseUrl: string
): string {
  const root = rootUrl(baseUrl)
  switch (client) {
    case 'claude':
      return platform === 'antigravity' ? `${root}/antigravity` : root
    case 'gemini':
      return platform === 'antigravity' ? `${root}/antigravity` : root
    case 'codex':
      return withV1(root)
    case 'opencode':
      return platform === 'gemini' ? `${root}/v1beta` : withV1(root)
  }
}

/** 平台原生接口的地址：交给 AI 的文本里使用。 */
export function nativeEndpoint(platform: GroupPlatform | string | null | undefined, baseUrl: string): string {
  const root = rootUrl(baseUrl)
  switch (platform) {
    case 'openai':
      return withV1(root)
    case 'antigravity':
      return `${root}/antigravity`
    default:
      return root
  }
}

/** OpenCode 里这个平台对应的内置 provider 名；一键安装脚本和手动配置片段都按它写 provider.<名字>.options。 */
export function opencodeProviderFor(platform: GroupPlatform | string | null | undefined): string {
  return platform === 'gemini' ? 'google' : platform === 'openai' ? 'openai' : 'anthropic'
}

export function protocolFor(platform: GroupPlatform | string | null | undefined): string {
  switch (platform) {
    case 'openai':
      return 'OpenAI'
    case 'gemini':
      return 'Gemini'
    default:
      return 'Anthropic'
  }
}

// ---------------------------------------------------------------- 引号

/** bash 单引号字面量。 */
export function shQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

/** PowerShell 单引号字面量；PowerShell 还把弯引号当成引号，一并加倍。 */
export function psQuote(value: string): string {
  return `'${value.replace(/['‘’‚‛]/g, (m) => m + m)}'`
}

/** 单行文本：换行和控制字符换成空格，避免破坏脚本结构。 */
function oneLine(value: string): string {
  // eslint-disable-next-line no-control-regex
  return (value || '').replace(/[\u0000-\u001f\u007f]+/g, ' ').trim()
}

// 数字（2024、1_000、-5、3.14、1e5、0x1f）、true/false、inf/nan、日期时间（1979-05-27、07:32:00）、以 [ 或 { 开头的数组和表
const NON_STRING_TOML =
  /^(?:[+-]?(?:[0-9][0-9_]*(?:\.[0-9][0-9_]*)?(?:e[+-]?[0-9_]+)?|0[xob][0-9a-f_]+|inf|nan)|true|false|\d{4}-\d{2}-\d{2}.*|\d{2}:\d{2}:\d{2}.*|[[{].*)$/

/**
 * 这个文字被 TOML 当成值解析时是不是字符串以外的类型。
 * Codex 的 -c key=value 先把 value 当 TOML 解析；站点名派生出来的 provider id 和显示名撞上这些写法时，
 * 在 Windows PowerShell 5.1 里（往外部程序传参会吃掉值里的双引号）就读成了数字、布尔值、日期，类型不对。
 */
export function looksLikeNonStringToml(value: string): boolean {
  return NON_STRING_TOML.test(value.trim().toLowerCase())
}

/**
 * Codex 的 provider id：只含小写字母、数字、下划线，且不与内置 provider 重名；
 * 也不能写出来像数字、true/false 这类 TOML 值（站点名叫 2024、true 时加 site_ 前缀），这样命令行里不用加引号。
 */
export function codexProviderId(siteName?: string): string {
  const raw = (siteName || '').toLowerCase().replace(/[^a-z0-9_]/g, '')
  const id = raw || 'sub2api'
  if (['openai', 'ollama', 'lmstudio'].includes(id)) return `${id}_site`
  return looksLikeNonStringToml(id) ? `site_${id}` : id
}

// ---------------------------------------------------------------- 脚本

/** 脚本里 {path} / {error} / {version} 会在运行时替换成实际的文件路径 / 错误信息 / Node.js 版本。 */
export const SCRIPT_PATH_TOKEN = '{path}'
export const SCRIPT_ERROR_TOKEN = '{error}'
export const SCRIPT_VERSION_TOKEN = '{version}'

/** 脚本在终端里打印给用户看的话。 */
export interface ScriptMessages {
  /** 找不到 python3 */
  pythonMissing: string
  /** macOS 没装命令行开发工具（此时 /usr/bin/python3 只是个会弹安装窗口的占位程序） */
  xcodeMissing: string
  backup: string
  updated: string
  failed: string
  // 以下只用于 Codex 的「完整安装」
  /** 没装 Node.js */
  nodeMissing?: string
  /** Node.js 版本低于要求；{version} 是当前版本 */
  nodeTooOld?: string
  /** 有 Node.js 但找不到 npm */
  npmMissing?: string
  /** 开始用 npm 安装 */
  npmInstalling?: string
  /** npm 没有全局安装的权限 */
  npmPermission?: string
  /** npm 安装因其他原因失败 */
  npmFailed?: string
}

export const DEFAULT_SCRIPT_MESSAGES: Required<ScriptMessages> = {
  pythonMissing: 'python3 is required to update the config file.',
  xcodeMissing: 'The macOS command line developer tools are not installed. Use the CC Switch or Manual option instead.',
  backup: `Backup: ${SCRIPT_PATH_TOKEN}`,
  updated: `Updated: ${SCRIPT_PATH_TOKEN}`,
  failed: `Failed: ${SCRIPT_ERROR_TOKEN}`,
  nodeMissing: `Node.js ${CODEX_MIN_NODE_MAJOR} or newer is required. Install the LTS version from nodejs.org, then run this again.`,
  nodeTooOld: `Node.js ${SCRIPT_VERSION_TOKEN} is too old. Install Node.js ${CODEX_MIN_NODE_MAJOR} or newer (the LTS version from nodejs.org), then run this again.`,
  npmMissing: 'npm was not found. It comes with Node.js, so reinstall Node.js from nodejs.org, then run this again.',
  npmInstalling: 'Installing Codex CLI with npm...',
  npmPermission:
    'npm does not have permission to install global packages. Install Node.js with a version manager such as nvm, or set the npm prefix to a folder you own (npm config set prefix), then run this again.',
  npmFailed: 'Codex CLI could not be installed. Check the error above, then run this again.'
}

export interface InstallScriptInput {
  baseUrl: string
  apiKey: string
  platform?: GroupPlatform | string | null
  siteName?: string
  /** Codex 的脚本类型，默认 refresh（只写配置）；其他客户端忽略。 */
  mode?: CodexInstallMode
  /** 脚本运行结束时打印的一行话（默认英文）。 */
  doneMessage?: string
  /** 终端里的其余提示（默认英文），由调用方按界面语言传入。 */
  messages?: Partial<ScriptMessages>
}

/** 脚本会写入的配置文件（界面上展示用）。 */
export function scriptTargetPath(client: OnboardingClient, os: ScriptOs): string {
  const win = os === 'windows'
  switch (client) {
    case 'claude':
      return win ? '%USERPROFILE%\\.claude\\settings.json' : '~/.claude/settings.json'
    case 'codex':
      return win ? '%USERPROFILE%\\.codex\\config.toml' : '~/.codex/config.toml'
    case 'gemini':
      return win ? '%USERPROFILE%\\.gemini\\.env' : '~/.gemini/.env'
    case 'opencode':
      return win ? '%USERPROFILE%\\.config\\opencode\\opencode.json' : '~/.config/opencode/opencode.json'
  }
}

interface ScriptParams {
  /** 客户端要用的接口地址 */
  endpoint: string
  apiKey: string
  providerId: string
  providerName: string
  model: string
  opencodeProvider: string
  /** 只有 Codex 的完整安装会先装 CLI */
  installCli: boolean
  done: string
  msg: Required<ScriptMessages>
}

function resolveMessages(custom: Partial<ScriptMessages> | undefined): Required<ScriptMessages> {
  const out = { ...DEFAULT_SCRIPT_MESSAGES }
  for (const key of Object.keys(out) as (keyof ScriptMessages)[]) {
    const text = oneLine(custom?.[key] || '')
    if (text) out[key] = text
  }
  return out
}

/** 把「前缀{占位符}后缀」拆成前后两段；没有占位符时整句当前缀，值接在后面。 */
function splitTemplate(template: string, token: string): [string, string] {
  const at = template.indexOf(token)
  return at < 0 ? [`${template} `, ''] : [template.slice(0, at), template.slice(at + token.length)]
}

/** bash：打印「前缀 + 变量值 + 后缀」，文字都走单引号字面量，翻译里有引号或 $ 也不会破坏脚本。 */
function shPrint(template: string, token: string, valueExpr: string): string {
  const [pre, post] = splitTemplate(template, token)
  return `printf '%s%s%s\\n' ${shQuote(pre)} ${valueExpr} ${shQuote(post)}`
}

/** PowerShell：同上，返回一个字符串拼接表达式。 */
function psJoin(template: string, token: string, valueExpr: string): string {
  const [pre, post] = splitTemplate(template, token)
  return `(${psQuote(pre)} + ${valueExpr} + ${psQuote(post)})`
}

/** Claude Code 的 env 里可能残留、会盖掉或干扰新写入地址和密钥的旧变量；写入前先删掉。 */
const CLAUDE_STALE_ENV = [
  'ANTHROPIC_API_KEY',
  'ANTHROPIC_MODEL',
  'ANTHROPIC_DEFAULT_HAIKU_MODEL',
  'ANTHROPIC_DEFAULT_SONNET_MODEL',
  'ANTHROPIC_DEFAULT_OPUS_MODEL',
  'ANTHROPIC_SMALL_FAST_MODEL'
]

function scriptParams(client: OnboardingClient, input: InstallScriptInput): ScriptParams {
  const platform = input.platform || 'anthropic'
  return {
    endpoint: endpointFor(client, platform, input.baseUrl),
    apiKey: input.apiKey,
    providerId: codexProviderId(input.siteName),
    providerName: oneLine(input.siteName || '') || 'sub2api',
    model: OPENAI_CC_SWITCH_CODEX_MODEL,
    opencodeProvider: opencodeProviderFor(platform),
    installCli: client === 'codex' && input.mode === 'full',
    done: oneLine(input.doneMessage || '') || 'Done. Restart the client to apply.',
    msg: resolveMessages(input.messages)
  }
}

const UNIX_EOF = 'SUB2API_INSTALL_EOF'

function unixPython(client: OnboardingClient): { target: string; code: string } {
  switch (client) {
    case 'claude':
      return {
        target: '"$HOME/.claude/settings.json"',
        code: `import json, os, sys
p = os.environ['TARGET']
data = {}
if os.path.exists(p) and os.path.getsize(p) > 0:
    with open(p, encoding='utf-8') as f:
        data = json.load(f)
if not isinstance(data, dict):
    sys.exit('settings.json is not a JSON object')
env = data.get('env')
if not isinstance(env, dict):
    env = {}
    data['env'] = env
for k in (${CLAUDE_STALE_ENV.map((k) => `'${k}'`).join(', ')}):
    env.pop(k, None)
env['ANTHROPIC_BASE_URL'] = os.environ['SUB_ENDPOINT']
env['ANTHROPIC_AUTH_TOKEN'] = os.environ['SUB_KEY']
env['CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC'] = '1'
out = json.dumps(data, ensure_ascii=False, indent=2) + '\\n'`
      }
    case 'opencode':
      return {
        target: '"$CONFIG_HOME/opencode/opencode.json"',
        code: `import json, os, sys
p = os.environ['TARGET']
data = {}
if os.path.exists(p) and os.path.getsize(p) > 0:
    with open(p, encoding='utf-8') as f:
        data = json.load(f)
if not isinstance(data, dict):
    sys.exit('opencode.json is not a JSON object')
data.setdefault('$schema', 'https://opencode.ai/config.json')
provider = data.get('provider')
if not isinstance(provider, dict):
    provider = {}
    data['provider'] = provider
entry = provider.get(os.environ['SUB_PROVIDER'])
if not isinstance(entry, dict):
    entry = {}
    provider[os.environ['SUB_PROVIDER']] = entry
options = entry.get('options')
if not isinstance(options, dict):
    options = {}
    entry['options'] = options
options['baseURL'] = os.environ['SUB_ENDPOINT']
options['apiKey'] = os.environ['SUB_KEY']
out = json.dumps(data, ensure_ascii=False, indent=2) + '\\n'`
      }
    case 'codex':
      return {
        target: '"$HOME/.codex/config.toml"',
        code: `import os, re
p = os.environ['TARGET']
pid = os.environ['SUB_PROVIDER_ID']
text = ''
if os.path.exists(p):
    with open(p, encoding='utf-8') as f:
        text = f.read()
out_lines = []
top = True
skip = False
for line in text.splitlines():
    s = line.strip()
    if s.startswith('['):
        top = False
        skip = s == '[model_providers.' + pid + ']' or s.startswith('[model_providers.' + pid + '.')
        if skip:
            continue
    elif skip:
        continue
    elif top and re.match(r'(model_provider|model)\\s*=', s):
        continue
    out_lines.append(line)
while out_lines and not out_lines[0].strip():
    out_lines.pop(0)
while out_lines and not out_lines[-1].strip():
    out_lines.pop()
def q(v):
    return '"' + v.replace('\\\\', '\\\\\\\\').replace('"', '\\\\"') + '"'
head = ['model_provider = ' + q(pid), 'model = ' + q(os.environ['SUB_MODEL']), '']
tail = [
    '',
    '[model_providers.' + pid + ']',
    'name = ' + q(os.environ['SUB_PROVIDER_NAME']),
    'base_url = ' + q(os.environ['SUB_ENDPOINT']),
    'wire_api = "responses"',
    'requires_openai_auth = false',
    'experimental_bearer_token = ' + q(os.environ['SUB_KEY']),
]
out = '\\n'.join(head + out_lines + tail) + '\\n'`
      }
    case 'gemini':
      return {
        target: '"$HOME/.gemini/.env"',
        code: `import os, re
p = os.environ['TARGET']
text = ''
if os.path.exists(p):
    with open(p, encoding='utf-8') as f:
        text = f.read()
names = ('GOOGLE_GEMINI_BASE_URL', 'GEMINI_API_KEY')
out_lines = [l for l in text.splitlines() if not re.match(r'\\s*(export\\s+)?(' + '|'.join(names) + r')\\s*=', l)]
def q(v):
    return '"' + v + '"' if "'" in v else "'" + v + "'"
out_lines.append('GOOGLE_GEMINI_BASE_URL=' + q(os.environ['SUB_ENDPOINT']))
out_lines.append('GEMINI_API_KEY=' + q(os.environ['SUB_KEY']))
out = '\\n'.join(out_lines) + '\\n'`
      }
  }
}

/**
 * Codex 完整安装（bash）：检查 Node.js 和 npm，没有或版本太低就提示并退出（不替用户装 Node.js），
 * 然后 npm install -g。权限不足时告诉用户怎么处理，不自动 sudo。放在「找 python」之后、动任何文件之前，
 * 所以前置条件不满足时不会留下半成品。
 */
function unixCodexInstall(p: ScriptParams): string[] {
  return [
    'NODE_VER=""',
    'if command -v node >/dev/null 2>&1; then NODE_VER="$(node --version 2>/dev/null || true)"; fi',
    `if [ -z "$NODE_VER" ]; then echo ${shQuote(p.msg.nodeMissing)}; exit 1; fi`,
    // node --version 形如 v18.19.0
    'NODE_MAJOR="${NODE_VER#v}"; NODE_MAJOR="${NODE_MAJOR%%.*}"',
    'case "$NODE_MAJOR" in *[!0-9]*|"") NODE_MAJOR=0 ;; esac',
    `if [ "$NODE_MAJOR" -lt ${CODEX_MIN_NODE_MAJOR} ]; then ${shPrint(p.msg.nodeTooOld, SCRIPT_VERSION_TOKEN, '"$NODE_VER"')}; exit 1; fi`,
    `if ! command -v npm >/dev/null 2>&1; then echo ${shQuote(p.msg.npmMissing)}; exit 1; fi`,
    `echo ${shQuote(p.msg.npmInstalling)}`,
    'NPM_LOG="$(mktemp)"',
    // 一边显示 npm 的输出，一边留一份用来判断是不是权限问题；</dev/null 避免 npm 读走脚本本身的标准输入
    'set -o pipefail',
    `if ! npm install -g ${CODEX_NPM_PACKAGE} </dev/null 2>&1 | tee "$NPM_LOG"; then`,
    `  if grep -qE 'EACCES|EPERM' "$NPM_LOG"; then echo ${shQuote(p.msg.npmPermission)}; else echo ${shQuote(p.msg.npmFailed)}; fi`,
    '  rm -f "$NPM_LOG"',
    '  exit 1',
    'fi',
    'rm -f "$NPM_LOG"'
  ]
}

function buildUnixScript(client: OnboardingClient, input: InstallScriptInput): string {
  const p = scriptParams(client, input)
  const { target, code } = unixPython(client)
  const lines = [
    `bash <<'${UNIX_EOF}'`,
    'set -e',
    `export SUB_ENDPOINT=${shQuote(p.endpoint)}`,
    `export SUB_KEY=${shQuote(p.apiKey)}`
  ]
  if (client === 'codex') {
    lines.push(
      `export SUB_PROVIDER_ID=${shQuote(p.providerId)}`,
      `export SUB_PROVIDER_NAME=${shQuote(p.providerName)}`,
      `export SUB_MODEL=${shQuote(p.model)}`
    )
  }
  if (client === 'opencode') {
    lines.push(`export SUB_PROVIDER=${shQuote(p.opencodeProvider)}`)
  }
  lines.push(
    // macOS 没装命令行开发工具时，/usr/bin/python3 只是个占位程序：command -v 找得到，一调用就弹安装窗口
    `if [ "$(uname -s)" = "Darwin" ] && ! xcode-select -p >/dev/null 2>&1; then echo ${shQuote(p.msg.xcodeMissing)}; exit 1; fi`,
    'PY="$(command -v python3 || command -v python || true)"',
    `if [ -z "$PY" ]; then echo ${shQuote(p.msg.pythonMissing)}; exit 1; fi`
  )
  if (p.installCli) lines.push(...unixCodexInstall(p))
  lines.push('TS="$(date +%Y%m%d-%H%M%S)"')
  if (client === 'opencode') {
    lines.push('if [ -n "$XDG_CONFIG_HOME" ]; then CONFIG_HOME="$XDG_CONFIG_HOME"; else CONFIG_HOME="$HOME/.config"; fi')
  }
  lines.push(
    `TARGET=${target}`,
    'export TARGET',
    'mkdir -p "$(dirname "$TARGET")"',
    'OUT="$TARGET.new-$TS"',
    'export OUT',
    `"$PY" - <<'PY'`,
    code,
    `with open(os.environ['OUT'], 'w', encoding='utf-8') as f:`,
    '    f.write(out)',
    `os.chmod(os.environ['OUT'], 0o600)`,
    'PY',
    'if [ -e "$TARGET" ]; then',
    '  B="$TARGET.bak-$TS"; N=1',
    '  while [ -e "$B" ]; do N=$((N+1)); B="$TARGET.bak-$TS-$N"; done',
    '  cp -p "$TARGET" "$B"',
    `  ${shPrint(p.msg.backup, SCRIPT_PATH_TOKEN, '"$B"')}`,
    '  cat "$OUT" > "$TARGET"',
    '  rm -f "$OUT"',
    'else',
    '  mv "$OUT" "$TARGET"',
    'fi',
    shPrint(p.msg.updated, SCRIPT_PATH_TOKEN, '"$TARGET"'),
    `echo ${shQuote(p.done)}`,
    UNIX_EOF
  )
  return lines.join('\n')
}

function psCommon(backupTemplate: string): string {
  return `$ErrorActionPreference = 'Stop'
$ts = Get-Date -Format 'yyyyMMdd-HHmmss'
$utf8 = New-Object System.Text.UTF8Encoding($false)
function Read-Text($p) { if (Test-Path -LiteralPath $p) { [System.IO.File]::ReadAllText($p) } else { '' } }
function Backup-File($p) { if (Test-Path -LiteralPath $p) { $b = $p + '.bak-' + $ts; $n = 1; while (Test-Path -LiteralPath $b) { $n++; $b = $p + '.bak-' + $ts + '-' + $n }; Copy-Item -LiteralPath $p -Destination $b; Write-Host ${psJoin(backupTemplate, SCRIPT_PATH_TOKEN, '$b')} } }
function Save-Text($p, $text) { [System.IO.File]::WriteAllText($p, $text, $utf8) }
function Set-Prop($o, $n, $v) { if ($o.PSObject.Properties[$n]) { $o.$n = $v } else { $o | Add-Member -NotePropertyName $n -NotePropertyValue $v } }
function Get-Child($o, $n) { $c = $o.PSObject.Properties[$n]; if ($c -and $c.Value -is [System.Management.Automation.PSCustomObject]) { return $c.Value }; $new = New-Object PSObject; Set-Prop $o $n $new; return $new }
function Load-Json($p) { $raw = Read-Text $p; if ($raw.Trim().Length -eq 0) { return (New-Object PSObject) }; $o = $raw | ConvertFrom-Json; if ($o -isnot [System.Management.Automation.PSCustomObject]) { throw ($p + ' is not a JSON object') }; return $o }
function Quote-Toml($v) { return '"' + $v.Replace('\\', '\\\\').Replace('"', '\\"') + '"' }`
}

function psBody(client: OnboardingClient): { dir: string; file: string; code: string } {
  switch (client) {
    case 'claude':
      return {
        dir: `Join-Path $env:USERPROFILE '.claude'`,
        file: 'settings.json',
        code: `$cfg = Load-Json $target
$envObj = Get-Child $cfg 'env'
foreach ($k in @(${CLAUDE_STALE_ENV.map((k) => `'${k}'`).join(', ')})) { $envObj.PSObject.Properties.Remove($k) }
Set-Prop $envObj 'ANTHROPIC_BASE_URL' $SubEndpoint
Set-Prop $envObj 'ANTHROPIC_AUTH_TOKEN' $SubKey
Set-Prop $envObj 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC' '1'
$out = (ConvertTo-Json -InputObject $cfg -Depth 20) + [Environment]::NewLine`
      }
    case 'opencode':
      return {
        dir: `Join-Path $env:USERPROFILE '.config\\opencode'`,
        file: 'opencode.json',
        code: `$cfg = Load-Json $target
if (-not $cfg.PSObject.Properties['$schema']) { Set-Prop $cfg '$schema' 'https://opencode.ai/config.json' }
$prov = Get-Child $cfg 'provider'
$entry = Get-Child $prov $SubProvider
$opts = Get-Child $entry 'options'
Set-Prop $opts 'baseURL' $SubEndpoint
Set-Prop $opts 'apiKey' $SubKey
$out = (ConvertTo-Json -InputObject $cfg -Depth 20) + [Environment]::NewLine`
      }
    case 'codex':
      return {
        dir: `Join-Path $env:USERPROFILE '.codex'`,
        file: 'config.toml',
        code: `$lines = (Read-Text $target) -split '\\r?\\n'
$kept = New-Object System.Collections.Generic.List[string]
$top = $true
$skip = $false
foreach ($line in $lines) {
  $s = $line.Trim()
  if ($s.StartsWith('[')) {
    $top = $false
    $skip = ($s -eq ('[model_providers.' + $SubProviderId + ']')) -or $s.StartsWith('[model_providers.' + $SubProviderId + '.')
    if ($skip) { continue }
  } elseif ($skip) { continue }
  elseif ($top -and ($s -match '^(model_provider|model)\\s*=')) { continue }
  $kept.Add($line)
}
while ($kept.Count -gt 0 -and $kept[0].Trim().Length -eq 0) { $kept.RemoveAt(0) }
while ($kept.Count -gt 0 -and $kept[$kept.Count - 1].Trim().Length -eq 0) { $kept.RemoveAt($kept.Count - 1) }
$all = New-Object System.Collections.Generic.List[string]
$all.Add('model_provider = ' + (Quote-Toml $SubProviderId))
$all.Add('model = ' + (Quote-Toml $SubModel))
$all.Add('')
$all.AddRange($kept)
$all.Add('')
$all.Add('[model_providers.' + $SubProviderId + ']')
$all.Add('name = ' + (Quote-Toml $SubProviderName))
$all.Add('base_url = ' + (Quote-Toml $SubEndpoint))
$all.Add('wire_api = "responses"')
$all.Add('requires_openai_auth = false')
$all.Add('experimental_bearer_token = ' + (Quote-Toml $SubKey))
$out = ($all -join [Environment]::NewLine) + [Environment]::NewLine`
      }
    case 'gemini':
      return {
        dir: `Join-Path $env:USERPROFILE '.gemini'`,
        file: '.env',
        code: `$lines = (Read-Text $target) -split '\\r?\\n'
$kept = New-Object System.Collections.Generic.List[string]
foreach ($line in $lines) {
  if ($line -match '^\\s*(export\\s+)?(GOOGLE_GEMINI_BASE_URL|GEMINI_API_KEY)\\s*=') { continue }
  $kept.Add($line)
}
while ($kept.Count -gt 0 -and $kept[$kept.Count - 1].Trim().Length -eq 0) { $kept.RemoveAt($kept.Count - 1) }
function Quote-Env($v) { if ($v.Contains("'")) { return '"' + $v + '"' } else { return "'" + $v + "'" } }
$kept.Add('GOOGLE_GEMINI_BASE_URL=' + (Quote-Env $SubEndpoint))
$kept.Add('GEMINI_API_KEY=' + (Quote-Env $SubKey))
$out = ($kept -join [Environment]::NewLine) + [Environment]::NewLine`
      }
  }
}

/**
 * Codex 完整安装（PowerShell 5.1）：与 unixCodexInstall 同样的流程。
 * - 出错用 return 退出外层脚本块，不动任何文件。
 * - 调用 npm.cmd 而不是 npm：默认执行策略下 PowerShell 会拒绝加载 npm.ps1。
 * - 5.1 里 $ErrorActionPreference = 'Stop' 会把原生命令写到 stderr 的内容（npm 的 WARN）当成终止错误，
 *   所以调用 npm 期间临时改回 Continue，用退出码判断成败。
 */
function windowsCodexInstall(p: ScriptParams): string[] {
  return [
    "$nodeVer = ''",
    'if (Get-Command node -ErrorAction SilentlyContinue) { $nodeVer = ((& node --version) | Out-String).Trim() }',
    `if ($nodeVer.Length -eq 0) { Write-Host ${psQuote(p.msg.nodeMissing)} -ForegroundColor Red; return }`,
    '$nodeMajor = 0',
    "$nodeMatch = [regex]::Match($nodeVer, '^v?(\\d+)')",
    'if ($nodeMatch.Success) { $nodeMajor = [int]$nodeMatch.Groups[1].Value }',
    `if ($nodeMajor -lt ${CODEX_MIN_NODE_MAJOR}) { Write-Host ${psJoin(p.msg.nodeTooOld, SCRIPT_VERSION_TOKEN, '$nodeVer')} -ForegroundColor Red; return }`,
    `if (-not (Get-Command npm.cmd -ErrorAction SilentlyContinue)) { Write-Host ${psQuote(p.msg.npmMissing)} -ForegroundColor Red; return }`,
    `Write-Host ${psQuote(p.msg.npmInstalling)}`,
    '$prevEap = $ErrorActionPreference',
    "$ErrorActionPreference = 'Continue'",
    '$npmOut = $null',
    `& npm.cmd install -g ${psQuote(CODEX_NPM_PACKAGE)} 2>&1 | ForEach-Object { $_.ToString() } | Tee-Object -Variable npmOut | Out-Host`,
    '$npmCode = $LASTEXITCODE',
    '$ErrorActionPreference = $prevEap',
    'if ($npmCode -ne 0) {',
    `  if (($npmOut | Out-String) -match 'EACCES|EPERM') { Write-Host ${psQuote(p.msg.npmPermission)} -ForegroundColor Red } else { Write-Host ${psQuote(p.msg.npmFailed)} -ForegroundColor Red }`,
    '  return',
    '}'
  ]
}

function buildWindowsScript(client: OnboardingClient, input: InstallScriptInput): string {
  const p = scriptParams(client, input)
  const body = psBody(client)
  const vars = [
    `$SubEndpoint = ${psQuote(p.endpoint)}`,
    `$SubKey = ${psQuote(p.apiKey)}`
  ]
  if (client === 'codex') {
    vars.push(
      `$SubProviderId = ${psQuote(p.providerId)}`,
      `$SubProviderName = ${psQuote(p.providerName)}`,
      `$SubModel = ${psQuote(p.model)}`
    )
  }
  if (client === 'opencode') vars.push(`$SubProvider = ${psQuote(p.opencodeProvider)}`)

  return [
    '& {',
    psCommon(p.msg.backup),
    ...vars,
    'try {',
    ...(p.installCli ? windowsCodexInstall(p) : []),
    `$dir = ${body.dir}`,
    'New-Item -ItemType Directory -Force -Path $dir | Out-Null',
    `$target = Join-Path $dir '${body.file}'`,
    body.code,
    'Backup-File $target',
    'Save-Text $target $out',
    `Write-Host ${psJoin(p.msg.updated, SCRIPT_PATH_TOKEN, '$target')}`,
    `Write-Host ${psQuote(p.done)}`,
    '} catch {',
    `Write-Host ${psJoin(p.msg.failed, SCRIPT_ERROR_TOKEN, '$_.Exception.Message')} -ForegroundColor Red`,
    '}',
    '}'
  ].join('\n')
}

export function buildInstallScript(client: OnboardingClient, os: ScriptOs, input: InstallScriptInput): string {
  return os === 'windows' ? buildWindowsScript(client, input) : buildUnixScript(client, input)
}

// ---------------------------------------------------------------- 交给 AI

export type AiClient = 'claude' | 'codex' | 'cursor' | 'chat' | 'code' | 'other'
/** 「交给 AI」里的全部工具，顺序就是界面上的顺序；某个分组实际能选哪些见 aiClientsForPlatform。 */
export const AI_CLIENTS: AiClient[] = ['claude', 'codex', 'cursor', 'chat', 'code', 'other']

/**
 * 各平台的分组在「交给 AI」里能选哪些工具，第一个就是这个分组默认选中的。
 * 顺序同 AI_CLIENTS；openai 分组把 Codex 排在 Claude Code 前面（分组开了调度时两个都有，主力工具是 Codex）。
 *
 * - Claude Code：anthropic、grok、antigravity；openai 分组开了 /v1/messages 调度之后也行
 *   （和一键安装的 clientsForPlatform 一致）
 * - Codex：只有 openai
 * - Cursor：openai、anthropic、grok；gemini 和 antigravity 分组不提供
 * - 聊天客户端、写代码调用、其他：任何有分组的密钥都能用
 *
 * 没有分组（platform 为空）时一个都没有。
 */
export function aiClientsForPlatform(
  platform: GroupPlatform | string | null | undefined,
  opts: { allowMessagesDispatch?: boolean } = {}
): AiClient[] {
  if (!platform) return []
  const isOpenai = platform === 'openai'
  const isAnthropicLike = platform === 'anthropic' || platform === 'grok'
  const usable: Record<AiClient, boolean> = {
    claude: isAnthropicLike || platform === 'antigravity' || (isOpenai && !!opts.allowMessagesDispatch),
    codex: isOpenai,
    cursor: isOpenai || isAnthropicLike,
    chat: true,
    code: true,
    other: true
  }
  const order: AiClient[] = isOpenai ? ['codex', 'claude', 'cursor', 'chat', 'code', 'other'] : AI_CLIENTS
  return order.filter((c) => usable[c])
}

export type TranslateFn = (key: string, params?: Record<string, unknown>) => string

export interface AiPromptInput {
  t: TranslateFn
  client: AiClient
  clientLabel: string
  baseUrl: string
  platform?: GroupPlatform | string | null
  siteName?: string
  models?: string[]
  docUrl?: string
  detailed?: boolean
}

/**
 * 生成可以发给 AI 助手的话。刻意不接收密钥：调用方没有任何办法把密钥带进来。
 */
export function buildAiPrompt(input: AiPromptInput): string {
  const { t, client } = input
  const platform = input.platform || 'anthropic'
  // Claude Code 只说 Anthropic 接口：地址是 API 根地址（antigravity 带 /antigravity），格式是 Anthropic，
  // 和提示里要设置的 ANTHROPIC_BASE_URL 对得上。其他工具按分组平台的原生接口。
  // 否则 openai 分组开了调度、选 Claude Code 时，会得到「地址 /v1、格式 OpenAI」却要设置 ANTHROPIC_BASE_URL 的矛盾说法。
  const url = client === 'claude' ? endpointFor('claude', platform, input.baseUrl) : nativeEndpoint(platform, input.baseUrl)
  const protocol = client === 'claude' ? 'Anthropic' : protocolFor(platform)
  const site = oneLine(input.siteName || '') || 'sub2api'
  const params = { client: input.clientLabel, site, url, protocol }
  const hint = t(`keyOnboarding.ai.hint.${client}`, params)
  const docLine = input.docUrl ? t('keyOnboarding.ai.docLine', { url: input.docUrl }) : ''

  if (!input.detailed) {
    return [t('keyOnboarding.ai.short', { ...params, hint }), docLine].filter(Boolean).join(' ')
  }

  const models = (input.models || []).filter(Boolean)
  const lines = [
    t('keyOnboarding.ai.detailIntro', params),
    '',
    t('keyOnboarding.ai.detailUrl', params),
    t('keyOnboarding.ai.detailProtocol', params),
    t('keyOnboarding.ai.detailKey'),
    models.length ? t('keyOnboarding.ai.detailModels', { models: models.join(', ') }) : '',
    '',
    hint,
    '',
    t('keyOnboarding.ai.detailSteps'),
    docLine
  ]
  return lines.filter((l, i, arr) => l !== '' || (i > 0 && arr[i - 1] !== '')).join('\n').trim()
}

export function chatgptUrl(prompt: string): string {
  return `https://chatgpt.com/?q=${encodeURIComponent(prompt)}`
}

export function claudeUrl(prompt: string): string {
  return `https://claude.ai/new?q=${encodeURIComponent(prompt)}`
}
