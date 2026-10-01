# 接口列表

所有接口共用同一个地址和同一个密钥。请求路径接在 `{{base}}` 后面。

## 鉴权 {#endpoints-auth}

在请求头里带上密钥，下面三种写法任选一种：

| 请求头 | 适用 |
|---|---|
| `Authorization: Bearer sk-你的密钥` | 所有接口，最常用 |
| `x-api-key: sk-你的密钥` | Anthropic 格式的客户端和 SDK |
| `x-goog-api-key: sk-你的密钥` | Gemini 格式的客户端和 SDK |

> 不要把密钥放进网址参数（`?key=`），在 `/v1` 接口上这样的请求会返回 400。

## 常用接口 {#endpoints-core}

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/responses` | OpenAI Responses 格式，Codex 用的就是它 |
| POST | `/v1/chat/completions` | OpenAI Chat Completions 格式，大多数客户端用它 |
| POST | `/v1/messages` | Anthropic Messages 格式，Claude Code 用它 |
| GET | `/v1/models` | 查看当前密钥能用的模型 |

Responses：

```bash
curl {{v1}}/responses \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "{{model}}", "input": "你好"}'
```

Chat Completions：

```bash
curl {{v1}}/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "{{model}}", "messages": [{"role": "user", "content": "你好"}]}'
```

Messages：

```bash
curl {{base}}/v1/messages \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "价格页上的模型名", "max_tokens": 1024, "messages": [{"role": "user", "content": "你好"}]}'
```

> `/v1/messages` 是否对某个分组开放由分组决定，不开放时会返回 403，说明里写着该分组不支持。回复较长的请求建议加 `"stream": true`，否则等待太久连接可能被断开。

## 按分组开放的接口 {#endpoints-group}

下面这些接口是否可用，取决于密钥所在的分组。不可用时会返回 404 或 403，说明里会提示该分组不支持。

| 方法 | 路径 | 说明 | 开放范围 |
|---|---|---|---|
| POST | `/v1/embeddings` | 文本向量 | OpenAI 分组 |
| POST | `/v1/images/generations` | 生成图片 | OpenAI 分组 |
| POST | `/v1/images/edits` | 编辑图片（表单上传） | OpenAI 分组 |
| POST | `/v1/messages/count_tokens` | 估算 Anthropic 格式请求的 token 数 | 非 OpenAI 分组 |
| POST | `/v1beta/models/{模型}:generateContent` | Gemini 原生格式 | 仅 Gemini 分组，非 Gemini 分组会返回 400 |

不确定自己的密钥能不能用，先调 `GET /v1/models` 看模型列表，再直接试一次。

文本向量：

```bash
curl {{v1}}/embeddings \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "模型名", "input": "你好"}'
```

生成图片：

```bash
curl {{v1}}/images/generations \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "模型名", "prompt": "一只橘猫", "size": "1024x1024"}'
```

编辑图片：

```bash
curl {{v1}}/images/edits \
  -H "Authorization: Bearer sk-你的密钥" \
  -F "model=模型名" \
  -F "image=@photo.png" \
  -F "prompt=把背景换成沙滩"
```

估算 token 数：

```bash
curl {{v1}}/messages/count_tokens \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "模型名", "messages": [{"role": "user", "content": "你好"}]}'
```

Gemini 原生格式：

```bash
curl "{{base}}/v1beta/models/模型名:generateContent" \
  -H "x-goog-api-key: sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "你好"}]}]}'
```

> 上面的「模型名」换成「价格与计费」页上该分组能用的模型。

## 模型列表 {#endpoints-models}

`GET /v1/models` 返回当前密钥所在分组能用的模型，适合在程序里动态读取：

```bash
curl {{v1}}/models \
  -H "Authorization: Bearer sk-你的密钥"
```

## 查询当前密钥 {#endpoints-key}

用密钥本身查询它的用量：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/usage` | 这个密钥的额度、今日和累计用量，可用 `days`（1 到 90）指定每日明细天数 |

```bash
curl {{v1}}/usage \
  -H "Authorization: Bearer sk-你的密钥"
```

没登录时也可以打开 [API Key 用量查询](/key-usage) 页，粘贴密钥查看用量。

## 不带 /v1 的路径 {#endpoints-nov1}

`/responses`、`/chat/completions`、`/models`、`/embeddings`、`/images/generations`、`/images/edits` 这些不带 `/v1` 的路径也能访问，和带 `/v1` 的效果一样。配置客户端时建议统一用带 `/v1` 的地址。Anthropic 格式的 `/v1/messages` 必须带 `/v1`，不能省略。
