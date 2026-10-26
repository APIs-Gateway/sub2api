# API 调用示例

这一页给开发者：同一个地址、同一个密钥，三种接口格式各给一份 curl、Python、Node.js 示例，最后是流式和多轮对话。SDK 的安装和基本用法见「在代码里调用」，接口清单见「接口列表」。

> 示例里的模型名是 `{{model}}`，能用哪些模型取决于密钥所在的分组，以「价格与计费」页为准。把 `sk-你的密钥` 换成你自己的密钥。

| 接口 | 地址 | 说明 |
|---|---|---|
| Chat Completions | `{{v1}}/chat/completions` | OpenAI 格式，兼容面最广 |
| Responses | `{{v1}}/responses` | OpenAI 新格式，Codex 用的就是它 |
| Messages | `{{base}}/v1/messages` | Anthropic 格式，仅开放了 Claude Code 的分组可用 |

## Chat Completions {#examples-chat}

```bash title="curl"
curl {{v1}}/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "{{model}}",
    "messages": [
      {"role": "system", "content": "你是一个简洁的助手。"},
      {"role": "user", "content": "用一句话解释什么是 API"}
    ]
  }'
```

```python title="chat.py"
from openai import OpenAI

client = OpenAI(base_url="{{v1}}", api_key="sk-你的密钥")

resp = client.chat.completions.create(
    model="{{model}}",
    messages=[
        {"role": "system", "content": "你是一个简洁的助手。"},
        {"role": "user", "content": "用一句话解释什么是 API"},
    ],
)
print(resp.choices[0].message.content)
```

```javascript title="chat.mjs"
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "{{v1}}", apiKey: "sk-你的密钥" });

const resp = await client.chat.completions.create({
  model: "{{model}}",
  messages: [
    { role: "system", content: "你是一个简洁的助手。" },
    { role: "user", content: "用一句话解释什么是 API" },
  ],
});
console.log(resp.choices[0].message.content);
```

## Responses {#examples-responses}

```bash title="curl"
curl {{v1}}/responses \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "{{model}}",
    "instructions": "你是一个简洁的助手。",
    "input": "用一句话解释什么是 API"
  }'
```

返回的 JSON 里，回复文字在 `output` 数组的 `message` 项下。用 SDK 的话直接读 `output_text` 更省事：

```python title="responses.py"
from openai import OpenAI

client = OpenAI(base_url="{{v1}}", api_key="sk-你的密钥")

resp = client.responses.create(
    model="{{model}}",
    instructions="你是一个简洁的助手。",
    input="用一句话解释什么是 API",
)
print(resp.output_text)
```

```javascript title="responses.mjs"
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "{{v1}}", apiKey: "sk-你的密钥" });

const resp = await client.responses.create({
  model: "{{model}}",
  instructions: "你是一个简洁的助手。",
  input: "用一句话解释什么是 API",
});
console.log(resp.output_text);
```

## Anthropic Messages {#examples-messages}

> 只有开放了 Claude Code 的分组才能用。密钥所在的分组不支持时会返回 403，这时在「API 密钥」页给密钥换一个分组。

这一格式的地址不带 `/v1`（SDK 会自己补），`max_tokens` 必填。curl 里密钥可以用 `x-api-key`，也可以用 `Authorization: Bearer`。

```bash title="curl"
curl {{base}}/v1/messages \
  -H "x-api-key: sk-你的密钥" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "价格页上的模型名",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "用一句话解释什么是 API"}]
  }'
```

```python title="messages.py"
import anthropic

client = anthropic.Anthropic(base_url="{{base}}", api_key="sk-你的密钥")

msg = client.messages.create(
    model="价格页上的模型名",
    max_tokens=1024,
    system="你是一个简洁的助手。",
    messages=[{"role": "user", "content": "用一句话解释什么是 API"}],
)
print(msg.content[0].text)
```

```javascript title="messages.mjs"
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "{{base}}", apiKey: "sk-你的密钥" });

const msg = await client.messages.create({
  model: "价格页上的模型名",
  max_tokens: 1024,
  system: "你是一个简洁的助手。",
  messages: [{ role: "user", content: "用一句话解释什么是 API" }],
});
console.log(msg.content[0].text);
```

Python 先 `pip install anthropic`，Node.js 先 `npm install @anthropic-ai/sdk`。

## 流式输出 {#examples-stream}

请求里加 `"stream": true`，服务器会用 SSE 一段一段推回内容。回复较长时一律建议用流式，非流式等太久可能被中途断开（524）。

### curl {#examples-stream-curl}

加 `-N` 关掉 curl 的输出缓冲，才能看到逐段返回：

```bash
curl -N {{v1}}/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "{{model}}",
    "messages": [{"role": "user", "content": "写一首四行的短诗"}],
    "stream": true
  }'
```

每一行是 `data: {...}`，最后以 `data: [DONE]` 结束。

### Chat Completions {#examples-stream-chat}

```python
stream = client.chat.completions.create(
    model="{{model}}",
    messages=[{"role": "user", "content": "写一首四行的短诗"}],
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

```javascript
const stream = await client.chat.completions.create({
  model: "{{model}}",
  messages: [{ role: "user", content: "写一首四行的短诗" }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
}
```

### Responses {#examples-stream-responses}

Responses 的流是一串带类型的事件，文字在 `response.output_text.delta` 事件里：

```python
stream = client.responses.create(
    model="{{model}}",
    input="写一首四行的短诗",
    stream=True,
)
for event in stream:
    if event.type == "response.output_text.delta":
        print(event.delta, end="", flush=True)
```

```javascript
const stream = await client.responses.create({
  model: "{{model}}",
  input: "写一首四行的短诗",
  stream: true,
});
for await (const event of stream) {
  if (event.type === "response.output_text.delta") {
    process.stdout.write(event.delta);
  }
}
```

### Anthropic Messages {#examples-stream-messages}

同样只有开放了 Claude Code 的分组可用。

```python
# 这里的 client 是上面「Anthropic Messages」小节里的 anthropic.Anthropic(...)
with client.messages.stream(
    model="价格页上的模型名",
    max_tokens=1024,
    messages=[{"role": "user", "content": "写一首四行的短诗"}],
) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)
```

```javascript
// client 同上面「Anthropic Messages」小节
const stream = client.messages.stream({
  model: "价格页上的模型名",
  max_tokens: 1024,
  messages: [{ role: "user", content: "写一首四行的短诗" }],
});
stream.on("text", (text) => process.stdout.write(text));
await stream.finalMessage();
```

## 多轮对话 {#examples-multi-turn}

接口本身不保存对话，每次请求要把之前的消息一起带上。把上一轮的回复追加到 `messages` 里，再加上新的提问：

```python
messages = [{"role": "user", "content": "我叫小李"}]
resp = client.chat.completions.create(model="{{model}}", messages=messages)
messages.append({"role": "assistant", "content": resp.choices[0].message.content})

messages.append({"role": "user", "content": "我叫什么？"})
resp = client.chat.completions.create(model="{{model}}", messages=messages)
print(resp.choices[0].message.content)
```

对话越长，每次请求带的内容越多，消耗也越多。

## 常见问题 {#examples-faq}

- 401：密钥没复制完整，或已被删除。Anthropic 格式也可以用 `x-api-key` 头，但不要把密钥放进网址参数。
- 403 提示分组不允许：当前分组没开放这种接口，在「API 密钥」页给密钥换分组。
- 404 `model_not_found`：模型名写错，或所在分组没有这个模型，用 `GET {{v1}}/models` 查看当前密钥能用的模型。
- 524 或连接中断：改用流式。

更多错误见「错误排查」。
