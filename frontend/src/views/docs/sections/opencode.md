# OpenCode

[OpenCode](https://opencode.ai) 是运行在终端里的 AI 编程工具。这里按 OpenAI 兼容方式接入，走 Chat Completions 接口，地址带 `/v1`。

## 配置步骤 {#opencode-steps}

编辑全局配置文件 `~/.config/opencode/opencode.json`，没有就新建。Windows 的路径是 `%USERPROFILE%\.config\opencode\opencode.json`。想只对某个项目生效，把同样的内容放到项目根目录的 `opencode.json`。

```json title="~/.config/opencode/opencode.json"
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "{{provider}}": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "{{providerName}}",
      "options": {
        "baseURL": "{{v1}}",
        "apiKey": "{env:SITE_API_KEY}"
      },
      "models": {
        "{{model}}": {
          "name": "{{model}}"
        }
      }
    }
  },
  "model": "{{provider}}/{{model}}"
}
```

然后在终端里设置密钥变量，再启动 OpenCode：

```bash title="macOS / Linux"
export SITE_API_KEY="sk-你的密钥"
opencode
```

```powershell title="Windows PowerShell"
$env:SITE_API_KEY = "sk-你的密钥"
opencode
```

> `models` 里要列出你想用的模型，没列出的模型不会出现在选择列表里。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

## 不想用环境变量 {#opencode-connect}

也可以在 OpenCode 里输入 `/connect`，滑到最底下选 **Other**，Provider ID 填 `{{provider}}`，再粘贴密钥。密钥会保存在本机 `~/.local/share/opencode/auth.json`。

用这种方式时，配置文件里的 `options` 只需要保留 `baseURL`，把 `apiKey` 那一行删掉。`provider` 里的 ID 要和这里填的一致。

## 验证 {#opencode-verify}

启动 OpenCode，输入 `/models`，能看到刚配置的模型，选中后发一句话，能回复就接入成功。

## 常见问题 {#opencode-faq}

- **`/models` 里没有模型**：检查 `models` 里有没有写模型名，以及 `provider` 的 ID 和 `model` 字段里斜杠前面的部分是否一致。
- **提示认证失败**：确认启动 OpenCode 的终端里已经设置了 `SITE_API_KEY`，密钥以 `sk-` 开头、没有多余空格。
- **返回 404**：确认 `baseURL` 是 `{{v1}}`，末尾带 `/v1`。
- **模型不可用或被拒绝**：去「API 密钥」页看这把密钥所在的分组是否开放了该模型。

## 切回原来的服务 {#opencode-revert}

删掉配置文件里的 `{{provider}}` 这一段和 `model` 字段；如果用过 `/connect`，再运行 `opencode auth logout` 选择对应条目，或删除 `auth.json` 里的对应项。
