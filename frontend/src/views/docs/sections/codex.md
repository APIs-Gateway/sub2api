# Codex CLI 与 Codex Desktop

Codex Desktop 和 Codex CLI 共用同一份配置文件，配一次两个都能用。

## 安装 {#codex-install}

Codex CLI 需要 Node.js 22 或更高版本。终端里运行 `node --version` 确认。没有的话到 [nodejs.org](https://nodejs.org) 下载 LTS 版本。

```bash
npm install -g @openai/codex
```

Codex Desktop 从 OpenAI 官网下载安装即可。

## 写配置文件 {#codex-config}

配置目录在 macOS 和 Linux 是 `~/.codex/`，在 Windows 是 `%USERPROFILE%\.codex\`。目录不存在就先建一个。

需要两个文件。先写 `config.toml`：

```toml title="~/.codex/config.toml"
model_provider = "OpenAI"
model = "{{model}}"

[model_providers.OpenAI]
name = "OpenAI"
base_url = "{{v1}}"
wire_api = "responses"
requires_openai_auth = true
```

再写 `auth.json`，把 `sk-你的密钥` 换成你自己的密钥：

```json title="~/.codex/auth.json"
{
  "OPENAI_API_KEY": "sk-你的密钥"
}
```

`model` 填价格页上列出的模型名。想换模型，只改这一行。

> 这两个文件里有明文密钥。截图或把配置发给别人之前，先把密钥遮住。

## 一直提示 401 怎么办 {#codex-401}

较新版本的 Codex 不再读取 `auth.json`。如果配好后一直返回 401，把 `config.toml` 里的供应商部分改成直接写入密钥：

```toml title="~/.codex/config.toml"
model_provider = "OpenAI"
model = "{{model}}"

[model_providers.OpenAI]
name = "OpenAI"
base_url = "{{v1}}"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "sk-你的密钥"
```

## 验证 {#codex-verify}

完全退出 Codex Desktop（macOS 按 Cmd+Q，只关窗口不算），或结束正在运行的 CLI，再重新打开并新建会话。旧会话不会读取新配置。

在会话里发一句「你好」，能正常回复就接入成功了。回到「使用记录」页，能看到刚才这次请求。

## 切回官方账号 {#codex-revert}

删掉指向 {{site}} 的配置，再用官方账号登录：

```bash title="macOS / Linux"
rm -f ~/.codex/auth.json ~/.codex/config.toml
codex login
```

```powershell title="Windows PowerShell"
Remove-Item "$env:USERPROFILE\.codex\auth.json", "$env:USERPROFILE\.codex\config.toml" -ErrorAction SilentlyContinue
codex login
```

如果你原来的配置文件里还有别的设置，不要整个删除。只删掉上面写的 `model_provider`、`model` 和 `[model_providers.OpenAI]` 部分。
