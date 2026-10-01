# 接口列表

所有接口共用同一个地址和同一个密钥。请求路径接在 `{{base}}` 后面。

## 常用接口 {#endpoints-core}

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/responses` | OpenAI Responses 格式，Codex 用的就是它 |
| POST | `/v1/chat/completions` | OpenAI Chat Completions 格式，大多数客户端用它 |
| POST | `/v1/messages` | Anthropic Messages 格式，Claude Code 用它 |
| GET | `/v1/models` | 查看当前密钥能用的模型 |

## 按分组开放的接口 {#endpoints-group}

下面这些接口是否可用，取决于密钥所在的分组。不可用时会返回 404 或 403，说明里会提示该分组不支持。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/embeddings` | 文本向量 |
| POST | `/v1/images/generations` | 生成图片 |
| POST | `/v1/images/edits` | 编辑图片 |
| POST | `/v1/messages/count_tokens` | 估算 Anthropic 格式请求的 token 数 |
| POST | `/v1beta/models/{模型}:generateContent` | Gemini 原生格式，仅 Gemini 分组可用，非 Gemini 分组会返回 400 |

> 不带 `/v1` 的路径（比如 `/responses`、`/chat/completions`）也能访问，和带 `/v1` 的效果一样。配置客户端时建议统一用带 `/v1` 的地址。Anthropic 格式的 `/v1/messages` 必须带 `/v1`，不能省略。

## 模型列表 {#endpoints-models}

`GET /v1/models` 返回当前密钥所在分组能用的模型，适合在程序里动态读取：

```bash
curl {{v1}}/models \
  -H "Authorization: Bearer sk-你的密钥"
```
