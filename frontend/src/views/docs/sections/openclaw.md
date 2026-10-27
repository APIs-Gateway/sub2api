# OpenClaw

[OpenClaw](https://docs.openclaw.ai/) 是开源、可自己部署的 AI 助手，能接入 Telegram、Slack、WhatsApp 等聊天渠道。把它的模型提供商指向 {{site}}，就能用本站的密钥驱动它。

## 安装 {#openclaw-install}

安装方式和系统版本要求以 [官方安装文档](https://docs.openclaw.ai/install) 为准。macOS、Linux 和 WSL2 的安装命令：

```bash
curl -fsSL https://openclaw.ai/install.sh | bash
```

装完运行 `openclaw --version`，能看到版本号就行。

## 配置步骤 {#openclaw-config}

OpenClaw 用「自定义提供商」接入第三方服务。在配置文件里加一个提供商，再把默认模型指过去。

1. 打开 OpenClaw 的配置文件，路径是 `~/.openclaw/openclaw.json`。
2. 加入下面的内容，已有其他配置时只合并 `models` 和 `agents` 两部分：

```json title="~/.openclaw/openclaw.json"
{
  "agents": {
    "defaults": {
      "model": { "primary": "{{provider}}/{{model}}" }
    }
  },
  "models": {
    "mode": "merge",
    "providers": {
      "{{provider}}": {
        "baseUrl": "{{v1}}",
        "apiKey": "sk-你的密钥",
        "api": "openai-completions",
        "models": [
          { "id": "{{model}}", "name": "{{model}}" }
        ]
      }
    }
  }
}
```

要点：

- `baseUrl` 要带 `/v1`。
- 默认模型写成「提供商名/模型名」，中间有斜杠，这里是 `{{provider}}/{{model}}`。只写模型名会找不到。
- `models` 里列出你想用的模型。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

也可以先运行 `openclaw onboard`，在向导里选自定义提供商，兼容类型选 OpenAI，再填上面的地址、密钥和模型名。

## 验证 {#openclaw-verify}

```bash
openclaw doctor
openclaw gateway status
```

`doctor` 没有报配置错误，网关在运行，就可以在 OpenClaw 的对话界面或已接入的聊天渠道里发一句话。能回复就接入成功。改过配置后如果没生效，重启网关再试。

## 切回原来的服务 {#openclaw-revert}

删掉 `models.providers` 里的 `{{provider}}`，并把 `agents.defaults.model.primary` 改回原来的模型。
