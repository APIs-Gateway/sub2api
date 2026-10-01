# 环境变量

很多工具支持用环境变量配置，设一次，所有读取它的工具都生效。

## macOS 和 Linux {#env-vars-unix}

把下面的内容加到 `~/.zshrc` 或 `~/.bashrc`，保存后运行 `source ~/.zshrc`（或重新打开终端）：

```bash title="~/.zshrc 或 ~/.bashrc"
# 读取这两个变量的 OpenAI 兼容工具：Aider、LangChain 等
export OPENAI_BASE_URL="{{v1}}"
export OPENAI_API_KEY="sk-你的密钥"

# Anthropic 兼容的工具：Claude Code 等
export ANTHROPIC_BASE_URL="{{base}}"
export ANTHROPIC_AUTH_TOKEN="sk-你的密钥"
```

两组变量互不影响，只用其中一种工具的话，只写那一组。注意 Anthropic 的地址不带 `/v1`。

Codex 不读取 `OPENAI_BASE_URL`，设了也不会生效。Codex 请按「Codex CLI 与 Codex Desktop」一节写 `config.toml`。

## Windows PowerShell {#env-vars-windows}

下面的命令把变量永久写入当前用户的环境：

```powershell title="Windows PowerShell"
# 读取这两个变量的 OpenAI 兼容工具：Aider、LangChain 等
[System.Environment]::SetEnvironmentVariable("OPENAI_BASE_URL", "{{v1}}", "User")
[System.Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-你的密钥", "User")

# Anthropic 兼容的工具：Claude Code 等
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_BASE_URL", "{{base}}", "User")
[System.Environment]::SetEnvironmentVariable("ANTHROPIC_AUTH_TOKEN", "sk-你的密钥", "User")
```

Codex 不读取 `OPENAI_BASE_URL`，请按「Codex CLI 与 Codex Desktop」一节写 `config.toml`。

设置完要关闭并重新打开终端才生效。只想在当前窗口临时用，可以这样写：

```powershell
$env:OPENAI_BASE_URL = "{{v1}}"
$env:OPENAI_API_KEY = "sk-你的密钥"
```

## 检查是否生效 {#env-vars-check}

```bash title="macOS / Linux"
echo $OPENAI_BASE_URL
```

```powershell title="Windows PowerShell"
echo $env:OPENAI_BASE_URL
```

输出的是你填的地址，就说明生效了。如果工具还是连到别处，检查终端里有没有残留的旧变量，比如以前设置的 `ANTHROPIC_API_KEY`。
