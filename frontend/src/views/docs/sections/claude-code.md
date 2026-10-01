# Claude Code

Claude Code 是 Anthropic 官方的命令行编程助手。它用的是 Anthropic 格式的接口，地址不带 `/v1`。

> 只有开放了 Claude Code 的分组才能用。密钥所在的分组不支持时，请求会返回 403，提示该分组不允许这种调用。这时在「API 密钥」页给密钥换一个分组。

## 安装 {#claude-code-install}

用 npm 安装需要 Node.js 22 或更高版本。

```bash
npm install -g @anthropic-ai/claude-code
```

装完运行 `claude --version`，能看到版本号就行。

不要用 `sudo` 运行 npm 安装。提示权限不足时，改用下面的官方安装脚本，或者把 npm 的全局目录改到自己有权限的位置。

不想装 Node.js 的话，可以用官方安装脚本，不依赖 Node.js：

```bash title="macOS / Linux / WSL"
curl -fsSL https://claude.ai/install.sh | bash
```

```powershell title="Windows PowerShell"
irm https://claude.ai/install.ps1 | iex
```

装完重新打开终端，再运行 `claude --version`。

## 写配置文件（推荐） {#claude-code-config}

编辑 `~/.claude/settings.json`，没有就新建。Windows 的路径是 `%USERPROFILE%\.claude\settings.json`。

```json title="~/.claude/settings.json"
{
  "env": {
    "ANTHROPIC_BASE_URL": "{{base}}",
    "ANTHROPIC_AUTH_TOKEN": "sk-你的密钥"
  }
}
```

变量名是 `ANTHROPIC_AUTH_TOKEN`，不是 `ANTHROPIC_API_KEY`。用错了会认证失败。

如果你以前用官方账号登录过，先启动 Claude Code，输入 `/logout` 退出，否则两边会冲突。

> 密钥只写在自己电脑上的这个文件里，不要写进项目目录里的 `.claude/settings.json`，那个文件通常会提交到代码仓库。

## 只想临时用一下 {#claude-code-env}

不改配置文件，在当前终端设置环境变量：

```bash title="macOS / Linux"
export ANTHROPIC_BASE_URL="{{base}}"
export ANTHROPIC_AUTH_TOKEN="sk-你的密钥"
claude
```

```powershell title="Windows PowerShell"
$env:ANTHROPIC_BASE_URL = "{{base}}"
$env:ANTHROPIC_AUTH_TOKEN = "sk-你的密钥"
claude
```

这样设置只在当前终端窗口有效，关掉就没了。

## 验证 {#claude-code-verify}

启动 Claude Code，输入 `/status`。看到 Anthropic base URL 显示的是 `{{base}}`，说明配置生效了。再发一句话，能回复就接入成功。

`/status` 里没有 Anthropic base URL 这一行，说明地址没有传进来，检查配置文件的位置和格式，改完要完全退出再重新启动。

## 切换模型 {#claude-code-model}

输入 `/model` 打开模型选择器，或者直接输入完整的模型名。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

也可以在启动时指定：

```bash
claude --model 价格页上的模型名
```

想让以后每次启动都用同一个模型，在配置文件的最外层加一行 `model`，不要放进 `env` 里：

```json title="~/.claude/settings.json"
{
  "env": {
    "ANTHROPIC_BASE_URL": "{{base}}",
    "ANTHROPIC_AUTH_TOKEN": "sk-你的密钥"
  },
  "model": "价格页上的模型名"
}
```

改完要重启 Claude Code，再用 `/status` 确认。

如果提示找不到模型，说明这个名字不在密钥所在分组里，回到「价格与计费」页核对，或者换一个分组。

> 如果用 `/model` 切换后下次启动又变回去了，检查有没有设置 `ANTHROPIC_MODEL` 环境变量，它的优先级比配置文件里的 `model` 高。`ANTHROPIC_DEFAULT_OPUS_MODEL`、`ANTHROPIC_DEFAULT_SONNET_MODEL`、`ANTHROPIC_DEFAULT_HAIKU_MODEL` 和 `CLAUDE_CODE_SUBAGENT_MODEL` 这几个变量会改变对应档位和子任务用的模型，设置时同样要填价格页上有的名字。

`/effort` 可以调整推理强度，可选的档位因模型而异。

## 在 Windows 上使用 {#claude-code-windows}

可以直接在 Windows 的 PowerShell 或 CMD 里运行，也可以在 WSL 里按 Linux 的方式装和用，两种方式二选一。

- 配置文件在 `%USERPROFILE%\.claude\settings.json`。在 WSL 里用，配置文件在 WSL 自己的 `~/.claude/settings.json`，和 Windows 那份是分开的。
- 原生 Windows 下，装了 Git for Windows 的话，Claude Code 会用 Git Bash 执行命令，没装就用 PowerShell，两种都能用。找不到 Git Bash 时，在配置文件的 `env` 里加 `CLAUDE_CODE_GIT_BASH_PATH`，值写 `bash.exe` 的路径，比如 `C:\\Program Files\\Git\\bin\\bash.exe`（JSON 里反斜杠要写两个）。
- PowerShell 里用 `$env:` 设置的变量只对当前窗口有效，想长期生效就写配置文件。

## 在 VS Code 里使用 {#claude-code-vscode}

VS Code 里安装官方的 Claude Code 扩展：打开扩展面板，搜索「Claude Code」，安装发布者是 Anthropic 的那个。

扩展和命令行共用 `~/.claude/settings.json`，上面写好的配置它也会读到。但扩展首次打开时会先检查登录状态，可能还是弹出登录页。这时把地址和密钥再填到 VS Code 自己的用户设置里（命令面板运行 `Preferences: Open User Settings (JSON)`）：

```json title="VS Code 用户设置 settings.json"
{
  "claudeCode.environmentVariables": [
    { "name": "ANTHROPIC_BASE_URL", "value": "{{base}}" },
    { "name": "ANTHROPIC_AUTH_TOKEN", "value": "sk-你的密钥" }
  ]
}
```

改完运行命令面板里的 `Developer: Reload Window`，再打开 Claude Code 面板。如果仍然提示登录，在 VS Code 设置里搜索「Claude Code login」，勾选 Disable Login Prompt。

## 常见问题 {#claude-code-faq}

**一直提示登录，或者提示认证冲突。** 以前用官方账号登录过的话，启动 Claude Code，输入 `/logout` 退出。再检查电脑上有没有残留的 `ANTHROPIC_API_KEY` 环境变量，有就删掉。

**配好了还是要求登录。** 地址和密钥要写在用户级的 `~/.claude/settings.json`，或者写成 shell 环境变量。只写在某个项目目录里的 `.claude/settings.json`，首次启动时可能还没生效。

**返回 400，提示 `Extra inputs are not permitted` 之类的字段不被接受。** 在 `env` 里加一行 `"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1"`，重启后再试。

**其他报错。** 401、403、404、429、524 的原因和处理办法见「错误排查」一节。

## 切回官方账号 {#claude-code-revert}

打开 `~/.claude/settings.json`，删掉 `env` 里的 `ANTHROPIC_BASE_URL` 和 `ANTHROPIC_AUTH_TOKEN` 两行，如果加过 `model` 也一并删掉。用过 VS Code 设置的话，同样删掉 `claudeCode.environmentVariables` 里这两项。然后重新启动 Claude Code，输入 `/login` 登录。
