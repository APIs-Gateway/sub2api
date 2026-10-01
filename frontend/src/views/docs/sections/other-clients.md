# 其他兼容 OpenAI 的客户端

大多数 AI 客户端都支持「OpenAI 兼容」的自定义服务商，包括 Cline、Roo Code、Continue、Aider、OpenCode、Open WebUI、LobeChat、NextChat 和沉浸式翻译。填法都一样。

## 通用填法 {#other-clients-generic}

在客户端的服务商设置里，选「OpenAI Compatible」或「自定义 OpenAI」，然后填：

| 配置项 | 填什么 |
|---|---|
| Base URL（也叫 API 地址、API Base） | `{{v1}}` |
| API Key | 你的密钥，`sk-` 开头 |
| 模型（Model ID） | 价格页上列出的模型名 |

有的客户端会自己在地址后面补 `/v1`，这时地址只填 `{{base}}`。拿不准的话，先带 `/v1` 试；报 404 就去掉再试。

## Aider {#other-clients-aider}

```bash
aider --openai-api-base {{v1}} --openai-api-key sk-你的密钥 --model openai/{{model}}
```

模型名前面要加 `openai/`，Aider 靠它判断走哪种接口。

## 客户端支持 Anthropic 格式时 {#other-clients-anthropic}

有些客户端同时支持 Anthropic 格式的服务商。这种情况下地址填 `{{base}}`，不带 `/v1`。能不能用取决于密钥所在的分组。
