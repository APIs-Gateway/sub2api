# Codex CLI 与 Codex Desktop

Codex Desktop 和 Codex CLI 共用同一份配置文件，配一次两个都能用。

> 不想手动改文件，可以在「API 密钥」页点密钥的「接入」，选「一键安装」，脚本会写好下面这份配置。

## 安装 {#codex-install}

Codex CLI 需要较新版本的 Node.js（16 或更高）。终端里运行 `node --version` 确认。没有的话到 [nodejs.org](https://nodejs.org) 下载 LTS 版本。

```bash
npm install -g @openai/codex
```

用 Homebrew 的 macOS 用户也可以运行 `brew install --cask codex`。Codex Desktop 从 OpenAI 官网下载安装即可。

## 写配置文件 {#codex-config}

配置目录在 macOS 和 Linux 是 `~/.codex/`，在 Windows 是 `%USERPROFILE%\.codex\`。目录不存在就先建一个。

只需要写 `config.toml` 一个文件，密钥直接写在里面。

如果 `config.toml` 已经存在，先复制一份备份（比如 `config.toml.bak`），再把下面的内容合并进去，不要整个覆盖。`model_provider` 和 `model` 两行必须放在文件顶部，在所有 `[表]` 之前，放在某个表的下面就不会生效。

```toml title="~/.codex/config.toml"
model_provider = "{{provider}}"
model = "{{model}}"

[model_providers.{{provider}}]
name = "{{providerName}}"
base_url = "{{v1}}"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "sk-你的密钥"
```

把 `sk-你的密钥` 换成你自己的密钥。`model` 填价格页上列出的模型名，想换模型，只改这一行。

`model_provider` 的值要和 `[model_providers.…]` 里的名字一致。这个名字可以自己改，只能用小写字母、数字和下划线，不要用 `openai`、`ollama`、`lmstudio`。

> 这个文件里有明文密钥。截图或把配置发给别人之前，先把密钥遮住。

## 验证 {#codex-verify}

完全退出 Codex Desktop（macOS 按 Cmd+Q，只关窗口不算），或结束正在运行的 CLI，再重新打开并新建会话。旧会话不会读取新配置。

在会话里发一句「你好」，能正常回复就接入成功了。回到「使用记录」页，能看到刚才这次请求。

## 常见问题 {#codex-faq}

- **返回 401**：多半是密钥填错。到「API 密钥」页用复制按钮复制，不要手打，容易把 `l` 和 `1`、`O` 和 `0` 看错。粘贴后确认以 `sk-` 开头，前后没有空格，也没有换行。
- **改了配置没生效**：确认 `model_provider` 和 `model` 在文件最顶部，并且完全退出后重新打开、新建会话。
- **提示模型不可用**：能用哪些模型由密钥所在的分组决定，`model` 要填价格与计费页上这个分组列出的名字。
- **提示要登录官方账号，或要配置 `OPENAI_API_KEY`**：多半是没有完全退出，或者还在用旧会话。Windows 版的 Codex Desktop 要从任务栏托盘里退出，只关窗口不算；退出后重新打开并新建会话。
- **想临时换模型**：在会话里输入 `/model` 选择；要长期换，改 `config.toml` 里的 `model`。

## 切回官方账号 {#codex-revert}

先把 `config.toml` 复制一份备份。然后打开它，只删掉下面两处，文件里的其他设置不要动：

- 顶部的 `model_provider` 和 `model` 两行。
- `[model_providers.{{provider}}]` 这一段，包括它下面的 `name`、`base_url`、`wire_api`、`requires_openai_auth`、`experimental_bearer_token`。

保存后，用官方账号重新登录：

```bash
codex login
```
