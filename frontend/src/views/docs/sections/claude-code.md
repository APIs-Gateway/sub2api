# Claude Code

Claude Code 是 Anthropic 官方的命令行编程助手。它用的是 Anthropic 格式的接口，地址不带 `/v1`。

> 只有开放了 Claude Code 的分组才能用。密钥所在的分组不支持时，请求会返回 403，提示该分组不允许这种调用。这时在「API 密钥」页给密钥换一个分组。

## 安装 {#claude-code-install}

需要 Node.js 18 或更高版本。

```bash
npm install -g @anthropic-ai/claude-code
```

装完运行 `claude --version`，能看到版本号就行。macOS 和 Linux 提示权限不足时，在命令前加 `sudo`。

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

如果你以前用官方账号登录过，先运行 `claude /logout` 退出，否则两边会冲突。

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

## 验证 {#claude-code-verify}

启动 Claude Code，输入 `/status`。看到 Anthropic base URL 显示的是 `{{base}}`，说明配置生效了。再发一句话，能回复就接入成功。

## 切换模型 {#claude-code-model}

输入 `/model` 打开模型选择器，或者直接输入完整的模型名。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

## 切回官方账号 {#claude-code-revert}

打开 `~/.claude/settings.json`，删掉 `env` 里的 `ANTHROPIC_BASE_URL` 和 `ANTHROPIC_AUTH_TOKEN` 两行，然后重新登录：

```bash
claude /login
```
