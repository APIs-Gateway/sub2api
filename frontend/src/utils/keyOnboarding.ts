import type { GroupPlatform } from '@/types'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'

/**
 * 「一键安装」脚本与「交给 AI」文本的纯函数生成器。
 *
 * 约定：
 * - 脚本完全内联，不联网下载；所有取值都先放进单引号字面量（再经环境变量/变量传给
 *   解释器），不拼进 JSON / TOML 源码，所以地址和密钥里有引号、空格、$ 等字符也不会破坏脚本。
 * - 修改任何已有配置文件之前，先复制一份带时间戳的备份（<文件>.bak-<时间戳>）。
 * - 脚本里不使用反引号（PowerShell 的转义符，粘贴时也容易被聊天软件改写）。
 * - 「交给 AI」文本永远不包含密钥。
 */

export type OnboardingClient = 'claude' | 'codex' | 'gemini' | 'opencode'
export type ScriptOs = 'unix' | 'windows'

export const CLIENT_LABELS: Record<OnboardingClient, string> = {
  claude: 'Claude Code',
  codex: 'Codex CLI',
  gemini: 'Gemini CLI',
  opencode: 'OpenCode'
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

function rootUrl(baseUrl: string): string {
  return (baseUrl || '').trim().replace(/\/+$/, '').replace(/\/v1$/, '')
}

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

/** Codex 的 provider id：只含小写字母、数字、下划线，且不与内置 provider 重名。 */
export function codexProviderId(siteName?: string): string {
  const raw = (siteName || '').toLowerCase().replace(/[^a-z0-9_]/g, '')
  const id = raw || 'sub2api'
  return ['openai', 'ollama', 'lmstudio'].includes(id) ? `${id}_site` : id
}

// ---------------------------------------------------------------- 脚本

export interface InstallScriptInput {
  baseUrl: string
  apiKey: string
  platform?: GroupPlatform | string | null
  siteName?: string
  /** 脚本运行结束时打印的一行话（默认英文）。 */
  doneMessage?: string
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
  done: string
}

function scriptParams(client: OnboardingClient, input: InstallScriptInput): ScriptParams {
  const platform = input.platform || 'anthropic'
  return {
    endpoint: endpointFor(client, platform, input.baseUrl),
    apiKey: input.apiKey,
    providerId: codexProviderId(input.siteName),
    providerName: oneLine(input.siteName || '') || 'sub2api',
    model: OPENAI_CC_SWITCH_CODEX_MODEL,
    opencodeProvider: platform === 'gemini' ? 'google' : platform === 'openai' ? 'openai' : 'anthropic',
    done: oneLine(input.doneMessage || '') || 'Done. Restart the client to apply.'
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
    'PY="$(command -v python3 || command -v python || true)"',
    'if [ -z "$PY" ]; then echo "python3 is required to update the config file."; exit 1; fi',
    'TS="$(date +%Y%m%d-%H%M%S)"'
  )
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
    '  cp -p "$TARGET" "$TARGET.bak-$TS"',
    '  echo "Backup: $TARGET.bak-$TS"',
    '  cat "$OUT" > "$TARGET"',
    '  rm -f "$OUT"',
    'else',
    '  mv "$OUT" "$TARGET"',
    'fi',
    'echo "Updated: $TARGET"',
    `echo ${shQuote(p.done)}`,
    UNIX_EOF
  )
  return lines.join('\n')
}

const PS_COMMON = `$ErrorActionPreference = 'Stop'
$ts = Get-Date -Format 'yyyyMMdd-HHmmss'
$utf8 = New-Object System.Text.UTF8Encoding($false)
function Read-Text($p) { if (Test-Path -LiteralPath $p) { [System.IO.File]::ReadAllText($p) } else { '' } }
function Backup-File($p) { if (Test-Path -LiteralPath $p) { $b = $p + '.bak-' + $ts; Copy-Item -LiteralPath $p -Destination $b; Write-Host ('Backup: ' + $b) } }
function Save-Text($p, $text) { [System.IO.File]::WriteAllText($p, $text, $utf8) }
function Set-Prop($o, $n, $v) { if ($o.PSObject.Properties[$n]) { $o.$n = $v } else { $o | Add-Member -NotePropertyName $n -NotePropertyValue $v } }
function Get-Child($o, $n) { $c = $o.PSObject.Properties[$n]; if ($c -and $c.Value -is [System.Management.Automation.PSCustomObject]) { return $c.Value }; $new = New-Object PSObject; Set-Prop $o $n $new; return $new }
function Load-Json($p) { $raw = Read-Text $p; if ($raw.Trim().Length -eq 0) { return (New-Object PSObject) }; $o = $raw | ConvertFrom-Json; if ($o -isnot [System.Management.Automation.PSCustomObject]) { throw ($p + ' is not a JSON object') }; return $o }
function Quote-Toml($v) { return '"' + $v.Replace('\\', '\\\\').Replace('"', '\\"') + '"' }`

function psBody(client: OnboardingClient): { dir: string; file: string; code: string } {
  switch (client) {
    case 'claude':
      return {
        dir: `Join-Path $env:USERPROFILE '.claude'`,
        file: 'settings.json',
        code: `$cfg = Load-Json $target
$envObj = Get-Child $cfg 'env'
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
    PS_COMMON,
    ...vars,
    'try {',
    `$dir = ${body.dir}`,
    'New-Item -ItemType Directory -Force -Path $dir | Out-Null',
    `$target = Join-Path $dir '${body.file}'`,
    body.code,
    'Backup-File $target',
    'Save-Text $target $out',
    `Write-Host ('Updated: ' + $target)`,
    `Write-Host ${psQuote(p.done)}`,
    '} catch {',
    `Write-Host ('Failed: ' + $_.Exception.Message) -ForegroundColor Red`,
    '}',
    '}'
  ].join('\n')
}

export function buildInstallScript(client: OnboardingClient, os: ScriptOs, input: InstallScriptInput): string {
  return os === 'windows' ? buildWindowsScript(client, input) : buildUnixScript(client, input)
}

// ---------------------------------------------------------------- 交给 AI

export type AiClient = 'claude' | 'codex' | 'cursor' | 'chat' | 'code' | 'other'
export const AI_CLIENTS: AiClient[] = ['claude', 'codex', 'cursor', 'chat', 'code', 'other']

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
  const url = nativeEndpoint(platform, input.baseUrl)
  const protocol = protocolFor(platform)
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
