import { normalizeApiBase } from '@/utils/apiEndpoints'
import {
  codexProviderId,
  endpointFor,
  looksLikeNonStringToml,
  opencodeProviderFor,
  psQuote,
  shQuote,
  type OnboardingClient
} from '@/utils/keyOnboarding'

/**
 * 「手动配置」页签里的代码示例和配置片段的纯函数生成器。
 *
 * 约定：
 * - 示例都能原样复制、直接运行；拿到完整密钥时直接填进去，拿不到时填占位符 KEY_PLACEHOLDER；
 * - bash 里的值一律走单引号字面量（shQuote），PowerShell 走 psQuote，所以地址、密钥、模型名里有引号或 $ 也不会破坏命令；
 * - 示例模型取自这把密钥所在分组可用的模型，见 pickExampleModel。
 */

/** 拿不到完整密钥时代码里的占位符。只含字母和下划线，粘进 bash 和 PowerShell 都不会被当成别的语法。 */
export const KEY_PLACEHOLDER = 'YOUR_API_KEY'

// ---------------------------------------------------------------- 哪些代码页签可用

export type ManualCodeTab = 'openai' | 'curl' | 'codex' | 'claude' | 'gemini'

/** 页签的显示顺序 */
export const MANUAL_CODE_TABS: readonly ManualCodeTab[] = ['openai', 'curl', 'codex', 'claude', 'gemini']

/** 页签上的字：都是产品和工具的名字，不随界面语言变化 */
export const MANUAL_TAB_LABELS: Record<ManualCodeTab, string> = {
  openai: 'OpenAI SDK',
  curl: 'curl',
  codex: 'Codex',
  claude: 'Claude Code',
  gemini: 'Gemini'
}

/**
 * 代码页签对当前分组是否可用。**「可用与否」只在这里判断。**
 *
 * 和一键安装用同一份依据：clientsForPlatform 给出的客户端清单（openai 分组只有开了 Messages 调度才有 claude）。
 * - OpenAI SDK 和 curl 请求的是 /v1/chat/completions：清单里有 Codex 或 OpenCode 的分组（openai、anthropic、grok、gemini）
 *   都有这个接口，antigravity 没有。这里看清单只是借它判断分组有没有 OpenAI 兼容接口，不代表 OpenCode 读的就是这个地址：
 *   OpenCode 在 gemini 分组读的是 /v1beta（见 buildConfigSnippets），其余读 /v1；
 * - 其余页签一一对应同名客户端。
 *
 * 产品上还没定不可用的页签是「隐藏」还是「置灰并提示换分组」。现在是隐藏（见 availableManualCodeTabs）；
 * 改成置灰只需要在 ManualTab 里改用 MANUAL_CODE_TABS 逐个渲染、再用这个函数决定是否禁用，判断本身不用动。
 */
export function isManualCodeTabAvailable(tab: ManualCodeTab, clients: readonly OnboardingClient[]): boolean {
  switch (tab) {
    case 'openai':
    case 'curl':
      return clients.includes('codex') || clients.includes('opencode')
    case 'codex':
      return clients.includes('codex')
    case 'claude':
      return clients.includes('claude')
    case 'gemini':
      return clients.includes('gemini')
  }
}

/** 当前分组下要显示的代码页签（按固定顺序）。 */
export function availableManualCodeTabs(clients: readonly OnboardingClient[]): ManualCodeTab[] {
  return MANUAL_CODE_TABS.filter((tab) => isManualCodeTabAvailable(tab, clients))
}

/**
 * 打开手动配置时默认选中的页签：
 * - openai 分组（清单里有 Codex）是 OpenAI SDK，也就是显示出来的第一个；
 * - anthropic、grok、gemini、antigravity 分组是该分组的原生客户端页签（Claude Code 或 Gemini），不是 OpenAI SDK。
 * 没有分组、什么都不可用时返回 openai（页签区不显示，值不会被用到）。
 */
export function defaultManualCodeTab(clients: readonly OnboardingClient[]): ManualCodeTab {
  const tabs = availableManualCodeTabs(clients)
  if (clients.includes('codex')) return tabs[0] ?? 'openai'
  return tabs.find((tab) => tab === 'claude' || tab === 'gemini') ?? tabs[0] ?? 'openai'
}

// ---------------------------------------------------------------- 示例模型

/** 各平台的示例模型：默认值，以及分组里没有默认值时按什么名字找。 */
const EXAMPLE_MODELS: Record<string, { fallback: string; pattern: RegExp; second?: RegExp }> = {
  openai: { fallback: 'gpt-5.6-sol', pattern: /^gpt-/i },
  // anthropic 取 sonnet 档；分组里一个 sonnet 都没有时取第一个 claude-*
  anthropic: { fallback: 'claude-sonnet-5', pattern: /sonnet/i, second: /^claude/i },
  gemini: { fallback: 'gemini-3.1-pro-preview', pattern: /^gemini/i },
  antigravity: { fallback: 'gemini-3.1-pro-high', pattern: /^gemini/i },
  grok: { fallback: 'grok-4.3', pattern: /^grok/i }
}

/** 图片、向量、语音这类不能放进「你好」对话示例里的模型 */
const NON_CHAT_MODEL = /image|embed|whisper|tts|audio|moderation|dall-?e|rerank|realtime|transcribe/i

/**
 * 示例用哪个模型：
 * 1. 还不知道分组有哪些模型（没加载完或加载失败）：用平台的默认值；
 * 2. 分组里有默认值：用默认值；
 * 3. 分组里没有：openai 取第一个 gpt-*，anthropic 取第一个 sonnet（没有就取第一个 claude-*），其他平台取同名前缀的第一个；
 * 4. 还是没有：取第一个对话模型；一个都没有就用默认值。
 */
export function pickExampleModel(platform: string | null | undefined, models: readonly string[]): string {
  const rule = EXAMPLE_MODELS[platform || ''] ?? EXAMPLE_MODELS.openai
  const chat = models.filter((m) => !NON_CHAT_MODEL.test(m))
  if (chat.length === 0 || chat.includes(rule.fallback)) return rule.fallback
  return (
    chat.find((m) => rule.pattern.test(m)) ?? (rule.second ? chat.find((m) => rule.second!.test(m)) : undefined) ?? chat[0]
  )
}

// ---------------------------------------------------------------- 引号

/** 只含安全字符的词不加引号（地址、模型名通常如此），其余走单引号。 */
function shWord(value: string): string {
  return /^[A-Za-z0-9_%+=:./-]+$/.test(value) ? value : shQuote(value)
}
function psWord(value: string): string {
  return /^[A-Za-z0-9_%+=:./-]+$/.test(value) ? value : psQuote(value)
}

/** 非 ASCII 字符写成 \uXXXX：Windows PowerShell 5.1 往外部程序管道传文本默认按 ASCII 编码，原样传中文会变成问号。 */
function jsonAscii(value: unknown): string {
  return JSON.stringify(value).replace(/[\u007f-￿]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`)
}

const PROMPT = 'Hello'

// ---------------------------------------------------------------- 代码示例

export interface ManualCodeFile {
  /** 同一个页签里的稳定标识，复制按钮的 id 用它 */
  id: string
  /** 代码块标题：操作系统或语言名，是不用翻译的专有名词 */
  label: string
  code: string
}

export interface ManualCodeInput {
  /** 当前线路的 API 根地址（不带结尾的 / 和 /v1） */
  base: string
  platform: string | null
  /** 完整密钥；空表示拿不到，代码里用占位符 */
  apiKey: string
  /** 分组可用的模型，可能为空 */
  models: readonly string[]
  siteName: string
}

const BASH = 'macOS / Linux'
const POWERSHELL = 'Windows PowerShell'

function chatBody(model: string): string {
  return JSON.stringify({ model, messages: [{ role: 'user', content: PROMPT }] })
}

/** 每个代码页签要显示的代码块。 */
export function buildManualCode(tab: ManualCodeTab, input: ManualCodeInput): ManualCodeFile[] {
  const key = input.apiKey || KEY_PLACEHOLDER
  const v1 = `${normalizeApiBase(input.base)}/v1`
  switch (tab) {
    case 'openai': {
      const model = pickExampleModel(input.platform, input.models)
      return [
        {
          id: 'python',
          label: 'Python',
          code: [
            '# pip install openai',
            'from openai import OpenAI',
            '',
            'client = OpenAI(',
            `    base_url=${JSON.stringify(v1)},`,
            `    api_key=${JSON.stringify(key)},`,
            ')',
            '',
            'resp = client.chat.completions.create(',
            `    model=${JSON.stringify(model)},`,
            `    messages=[{"role": "user", "content": ${JSON.stringify(PROMPT)}}],`,
            ')',
            'print(resp.choices[0].message.content)'
          ].join('\n')
        }
      ]
    }
    case 'curl': {
      const model = pickExampleModel(input.platform, input.models)
      return [
        {
          id: 'bash',
          label: BASH,
          code: [
            `curl ${shWord(`${v1}/chat/completions`)} \\`,
            `  -H ${shQuote(`Authorization: Bearer ${key}`)} \\`,
            `  -H 'Content-Type: application/json' \\`,
            `  -d ${shQuote(chatBody(model))}`
          ].join('\n')
        },
        {
          // Windows 自带的 PowerShell 里 curl 是 Invoke-WebRequest 的别名，要写 curl.exe；
          // JSON 里的双引号传给外部程序时会被吃掉，所以用单引号 here-string 通过管道交给 curl（-d @-）
          id: 'windows',
          label: `${POWERSHELL} (curl.exe)`,
          code: [
            "@'",
            jsonAscii({ model, messages: [{ role: 'user', content: PROMPT }] }),
            "'@ | curl.exe " +
              [
                psWord(`${v1}/chat/completions`),
                '-H',
                psQuote(`Authorization: Bearer ${key}`),
                '-H',
                "'Content-Type: application/json'",
                '-d',
                "'@-'"
              ].join(' ')
          ].join('\n')
        }
      ]
    }
    case 'codex': {
      // 实测（Codex CLI 0.145）：OPENAI_BASE_URL 环境变量已经不起作用，请求仍然发往 OpenAI 官方地址，所以不能只导出两个环境变量。
      // 做法是把本站当成自定义 provider：密钥放在环境变量里（env_key），地址等用 -c 临时覆盖，不改任何配置文件。
      // codex exec 默认只在 git 仓库里运行；示例是在任意目录粘贴运行，所以加 --skip-git-repo-check。
      const model = pickExampleModel('openai', input.models)
      const id = codexProviderId(input.siteName)
      const overrides = [
        ['model_provider', id],
        [`model_providers.${id}.name`, codexDisplayName(input.siteName, id)],
        [`model_providers.${id}.base_url`, endpointFor('codex', input.platform, input.base)],
        [`model_providers.${id}.env_key`, 'OPENAI_API_KEY'],
        [`model_providers.${id}.wire_api`, 'responses']
      ].map(([k, v]) => `${k}=${tomlOverrideValue(v)}`)
      return [
        {
          id: 'bash',
          label: BASH,
          code: [
            `export OPENAI_API_KEY=${shQuote(key)}`,
            `codex exec --skip-git-repo-check -m ${shWord(model)} \\`,
            ...overrides.map((o) => `  -c ${shWord(o)} \\`),
            `  ${shQuote(PROMPT)}`
          ].join('\n')
        },
        {
          id: 'windows',
          label: POWERSHELL,
          code: [
            `$env:OPENAI_API_KEY = ${psQuote(key)}`,
            `codex exec --skip-git-repo-check -m ${psWord(model)} ${overrides.map((o) => `-c ${psWord(o)}`).join(' ')} ${psQuote(PROMPT)}`
          ].join('\n')
        }
      ]
    }
    case 'claude': {
      const url = endpointFor('claude', input.platform, input.base)
      return [
        {
          id: 'bash',
          label: BASH,
          code: [
            `export ANTHROPIC_BASE_URL=${shQuote(url)}`,
            `export ANTHROPIC_AUTH_TOKEN=${shQuote(key)}`,
            `claude -p ${shQuote(PROMPT)}`
          ].join('\n')
        },
        {
          id: 'windows',
          label: POWERSHELL,
          code: [
            `$env:ANTHROPIC_BASE_URL = ${psQuote(url)}`,
            `$env:ANTHROPIC_AUTH_TOKEN = ${psQuote(key)}`,
            `claude -p ${psQuote(PROMPT)}`
          ].join('\n')
        }
      ]
    }
    case 'gemini': {
      const model = pickExampleModel(input.platform === 'antigravity' ? 'antigravity' : 'gemini', input.models)
      const url = endpointFor('gemini', input.platform, input.base)
      return [
        {
          id: 'bash',
          label: BASH,
          code: [
            `export GOOGLE_GEMINI_BASE_URL=${shQuote(url)}`,
            `export GEMINI_API_KEY=${shQuote(key)}`,
            `gemini -m ${shWord(model)} -p ${shQuote(PROMPT)}`
          ].join('\n')
        },
        {
          id: 'windows',
          label: POWERSHELL,
          code: [
            `$env:GOOGLE_GEMINI_BASE_URL = ${psQuote(url)}`,
            `$env:GEMINI_API_KEY = ${psQuote(key)}`,
            `gemini -m ${psWord(model)} -p ${psQuote(PROMPT)}`
          ].join('\n')
        }
      ]
    }
  }
}

// ---------------------------------------------------------------- 按客户端的配置文件片段

export interface ConfigSnippetFile {
  /** 路径（等宽显示）或普通文字标题 */
  label: string
  /** label 是文件路径 */
  path?: boolean
  code: string
}

export interface ConfigSnippet {
  id: OnboardingClient
  title: string
  files: ConfigSnippetFile[]
}

export interface ConfigSnippetInput extends ManualCodeInput {
  /** 「环境变量」这个标题的当前语言文案 */
  envVarsLabel: string
  clientLabels: Record<OnboardingClient, string>
}

function tomlQuote(v: string): string {
  return `"${v
    .replace(/\\/g, '\\\\')
    .replace(/"/g, '\\"')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u001f\u007f]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`)}"`
}

/**
 * -c 里的 provider 显示名：站点名（没有就 sub2api）。
 * 站点名叫 2024、true 这类会被 TOML 读成数字、布尔值的名字时改用 provider id（已经避开了这些写法），
 * 否则 Windows PowerShell 5.1 吃掉引号后 Codex 收到的类型就错了。config.toml 片段里名字是带引号的字符串，不受影响。
 */
function codexDisplayName(siteName: string, id: string): string {
  const name = siteName || 'sub2api'
  return looksLikeNonStringToml(name) ? id : name
}

/**
 * codex 的 -c key=value：Codex 先把 value 当 TOML 解析，解析不了才当成普通字符串。
 * 地址、名字这类普通文字解析不了，直接写，不带引号——Windows PowerShell 5.1 往外部程序传参数时会吃掉值里的双引号，带引号反而坏事；
 * 只有会被解析成数字、日期、布尔值、数组、字符串的值（站点名叫 2024 或 true 之类）才加 TOML 引号，不然类型就错了。
 */
function tomlOverrideValue(v: string): string {
  return /^[0-9+\-.[{"'`]|^(?:true|false|inf|nan)$/i.test(v) ? tomlQuote(v) : v
}

/**
 * .env 里的值：默认单引号，值里有单引号时改用双引号，再不行用反引号（dotenv 三种引号都认，但引号里不能再出现同一种引号，也没有转义）。
 * 含换行等控制字符、或三种引号都出现的值写不成一行合法的 .env，返回 null，调用方就不给这份文件，免得给出一份坏的。
 */
function dotenvValue(v: string): string | null {
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(v)) return null
  if (!v.includes("'")) return `'${v}'`
  if (!v.includes('"')) return `"${v}"`
  if (!v.includes('`')) return `\`${v}\``
  return null
}

/** 每个客户端一段配置文件片段（折叠区里用）。 */
export function buildConfigSnippets(clients: readonly OnboardingClient[], input: ConfigSnippetInput): ConfigSnippet[] {
  const key = input.apiKey || KEY_PLACEHOLDER
  const list: ConfigSnippet[] = []
  for (const c of clients) {
    const url = endpointFor(c, input.platform, input.base)
    const title = input.clientLabels[c]
    if (c === 'claude') {
      list.push({
        id: c,
        title,
        files: [
          { label: input.envVarsLabel, code: `export ANTHROPIC_BASE_URL=${shQuote(url)}\nexport ANTHROPIC_AUTH_TOKEN=${shQuote(key)}` },
          {
            label: '~/.claude/settings.json',
            path: true,
            code: JSON.stringify({ env: { ANTHROPIC_BASE_URL: url, ANTHROPIC_AUTH_TOKEN: key } }, null, 2)
          }
        ]
      })
    } else if (c === 'codex') {
      const id = codexProviderId(input.siteName)
      list.push({
        id: c,
        title,
        files: [
          {
            label: '~/.codex/config.toml',
            path: true,
            code: [
              `model_provider = ${tomlQuote(id)}`,
              `model = ${tomlQuote(pickExampleModel('openai', input.models))}`,
              '',
              `[model_providers.${id}]`,
              `name = ${tomlQuote(input.siteName || 'sub2api')}`,
              `base_url = ${tomlQuote(url)}`,
              'wire_api = "responses"',
              'requires_openai_auth = false',
              `experimental_bearer_token = ${tomlQuote(key)}`
            ].join('\n')
          }
        ]
      })
    } else if (c === 'gemini') {
      const files: ConfigSnippetFile[] = [
        { label: input.envVarsLabel, code: `export GOOGLE_GEMINI_BASE_URL=${shQuote(url)}\nexport GEMINI_API_KEY=${shQuote(key)}` }
      ]
      const dotenvUrl = dotenvValue(url)
      const dotenvKey = dotenvValue(key)
      if (dotenvUrl && dotenvKey) {
        files.push({ label: '~/.gemini/.env', path: true, code: `GOOGLE_GEMINI_BASE_URL=${dotenvUrl}\nGEMINI_API_KEY=${dotenvKey}` })
      }
      list.push({ id: c, title, files })
    } else if (c === 'opencode') {
      list.push({
        id: c,
        title,
        files: [
          {
            label: '~/.config/opencode/opencode.json',
            path: true,
            code: JSON.stringify(
              {
                $schema: 'https://opencode.ai/config.json',
                provider: { [opencodeProviderFor(input.platform)]: { options: { baseURL: url, apiKey: key } } }
              },
              null,
              2
            )
          }
        ]
      })
    }
  }
  return list
}
