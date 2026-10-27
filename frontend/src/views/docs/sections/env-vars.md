# 环境变量

很多工具支持用环境变量配置，设一次，所有读取它的工具都生效。

## 各工具读哪个变量 {#env-vars-which}

不同工具读的变量名不一样，按你用的工具选：

| 工具 | 地址变量 | 密钥变量 | 地址怎么填 |
|---|---|---|---|
| Claude Code | `ANTHROPIC_BASE_URL` | `ANTHROPIC_AUTH_TOKEN` | `{{base}}`，不带 `/v1` |
| OpenAI 官方 SDK（Python、Node.js） | `OPENAI_BASE_URL` | `OPENAI_API_KEY` | `{{v1}}` |
| Aider、LangChain | `OPENAI_API_BASE` | `OPENAI_API_KEY` | `{{v1}}` |
| Gemini CLI | `GOOGLE_GEMINI_BASE_URL` | `GEMINI_API_KEY` | `{{base}}`，不带 `/v1` 和 `/v1beta`；仅 Gemini 分组可用 |
| Open WebUI（自部署） | `OPENAI_API_BASE_URL` | `OPENAI_API_KEY` | `{{v1}}`，只在首次启动时生效 |
| LobeChat（自部署） | `OPENAI_PROXY_URL` | `OPENAI_API_KEY` | `{{v1}}` |
| NextChat（自部署） | `BASE_URL` | `OPENAI_API_KEY` | `{{base}}`，不带 `/v1` |
| Codex | 不读取环境变量 | 不读取环境变量 | 写 `config.toml`，见「Codex CLI 与 Codex Desktop」一节 |

Aider 和 LangChain 读的是 `OPENAI_API_BASE`，不是 `OPENAI_BASE_URL`。Gemini CLI 和自部署的聊天客户端各有自己的变量，见各自的章节。拿不准的工具，两个地址变量都设上，互不冲突。

## macOS 和 Linux {#env-vars-unix}

把下面的内容加到你用的 shell 的配置文件里：macOS 默认是 zsh，用 `~/.zshrc`；多数 Linux 是 bash，用 `~/.bashrc`。

```bash title="~/.zshrc 或 ~/.bashrc"
# OpenAI 兼容的工具：OpenAI SDK、Aider、LangChain 等
export OPENAI_BASE_URL="{{v1}}"
export OPENAI_API_BASE="{{v1}}"
export OPENAI_API_KEY="sk-你的密钥"

# Anthropic 兼容的工具：Claude Code 等
export ANTHROPIC_BASE_URL="{{base}}"
export ANTHROPIC_AUTH_TOKEN="sk-你的密钥"
```

保存后运行 `source ~/.zshrc`（bash 运行 `source ~/.bashrc`），或者重新打开终端。

用 fish 的话，运行下面的命令，会永久保存：

```bash title="fish"
set -Ux OPENAI_BASE_URL "{{v1}}"
set -Ux OPENAI_API_BASE "{{v1}}"
set -Ux OPENAI_API_KEY "sk-你的密钥"
set -Ux ANTHROPIC_BASE_URL "{{base}}"
set -Ux ANTHROPIC_AUTH_TOKEN "sk-你的密钥"
```

两组变量互不影响，只用其中一种工具的话，只写那一组。注意 Anthropic 的地址不带 `/v1`。

Codex 不读取 `OPENAI_BASE_URL`，设了也不会生效。Codex 请按「Codex CLI 与 Codex Desktop」一节写 `config.toml`。

只想在当前终端临时用，直接在终端里运行上面的 `export` 命令就行，关掉终端就失效，不会写进文件。

## Windows PowerShell {#env-vars-windows}

下面的命令把变量永久写入当前用户的环境，不需要管理员权限：

```powershell title="Windows PowerShell"
# OpenAI 兼容的工具：OpenAI SDK、Aider、LangChain 等
[System.Environment]::SetEnvironmentVariable("OPENAI_BASE_URL", "{{v1}}", "User")
[System.Environment]::SetEnvironmentVariable("OPENAI_API_BASE", "{{v1}}", "User")
[System.Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-你的密钥", "User")

# Anthropic 兼容的工具：Claude Code 等
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_BASE_URL", "{{base}}", "User")
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_AUTH_TOKEN", "sk-你的密钥", "User")
```

Codex 不读取 `OPENAI_BASE_URL`，请按「Codex CLI 与 Codex Desktop」一节写 `config.toml`。

设置完要关闭并重新打开终端才生效，已经打开的窗口读不到新变量。只想在当前窗口临时用，可以这样写，关掉窗口就失效：

```powershell
$env:OPENAI_BASE_URL = "{{v1}}"
$env:OPENAI_API_BASE = "{{v1}}"
$env:OPENAI_API_KEY = "sk-你的密钥"
```

用 cmd 的话，用 `setx` 永久保存，同样要重新打开窗口才生效：

```bat title="Windows cmd"
setx OPENAI_BASE_URL "{{v1}}"
setx OPENAI_API_BASE "{{v1}}"
setx OPENAI_API_KEY "sk-你的密钥"
setx ANTHROPIC_BASE_URL "{{base}}"
setx ANTHROPIC_AUTH_TOKEN "sk-你的密钥"
```

也可以在图形界面里设：开始菜单搜索「编辑账户的环境变量」，在「用户变量」里新建。

## 检查是否生效 {#env-vars-check}

在**新打开的终端**里运行：

```bash title="macOS / Linux"
echo $OPENAI_BASE_URL
```

```powershell title="Windows PowerShell"
echo $env:OPENAI_BASE_URL
```

输出的是你填的地址，就说明生效了。检查密钥变量时只看它有没有值，不要把完整密钥贴到聊天或截图里。

## 不用了怎么删 {#env-vars-remove}

```bash title="macOS / Linux"
# 先从 ~/.zshrc 或 ~/.bashrc 里删掉对应的 export 行，再清掉当前终端里的
unset OPENAI_BASE_URL OPENAI_API_BASE OPENAI_API_KEY ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN
```

```powershell title="Windows PowerShell"
foreach ($n in "OPENAI_BASE_URL","OPENAI_API_BASE","OPENAI_API_KEY","ANTHROPIC_BASE_URL","ANTHROPIC_AUTH_TOKEN") {
  [System.Environment]::SetEnvironmentVariable($n, $null, "User")
}
```

## 常见问题 {#env-vars-faq}

- 工具还是连到别处：检查终端里有没有残留的旧变量，比如以前设置的 `ANTHROPIC_API_KEY`。Claude Code 同时存在 `ANTHROPIC_API_KEY` 和 `ANTHROPIC_AUTH_TOKEN` 时可能用错密钥，本站请只设 `ANTHROPIC_AUTH_TOKEN`。
- Claude Code 的 `~/.claude/settings.json` 里如果也写了 `env`，以配置文件里的为准，会覆盖终端里设的同名变量。
- 改了配置文件没反应：已经打开的终端不会自动重新读取，运行 `source` 或新开一个终端。
- 密钥不要写进会提交到 Git 的文件，也不要发给别人。
