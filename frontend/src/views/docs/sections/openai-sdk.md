# 在代码里调用

用官方 SDK 调用，只需要改两处：`base_url` 和 `api_key`。其余写法和调用 OpenAI 官方接口一样。

## Python {#openai-sdk-python}

```bash
pip install openai
```

```python title="hello.py"
from openai import OpenAI

client = OpenAI(
    base_url="{{v1}}",
    api_key="sk-你的密钥",
)

resp = client.responses.create(
    model="{{model}}",
    input="用一句话介绍你自己",
)
print(resp.output_text)
```

用 Chat Completions 接口也可以：

```python
resp = client.chat.completions.create(
    model="{{model}}",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

## Node.js {#openai-sdk-node}

```bash
npm install openai
```

```javascript title="hello.mjs"
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "{{v1}}",
  apiKey: "sk-你的密钥",
});

const resp = await client.responses.create({
  model: "{{model}}",
  input: "用一句话介绍你自己",
});
console.log(resp.output_text);
```

## 流式输出 {#openai-sdk-stream}

请求里加上 `stream: true`，内容会边生成边返回。

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

回复比较长的请求，建议一律用流式。非流式请求要等全部生成完才返回，等待太久会被中途断开，见「错误排查」里的 524。

## curl {#openai-sdk-curl}

```bash title="macOS / Linux"
curl {{v1}}/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "{{model}}",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

## Anthropic SDK {#openai-sdk-anthropic}

如果密钥所在的分组开放了 Anthropic 格式，也可以用 Anthropic 官方 SDK。地址不带 `/v1`：

```python title="hello_anthropic.py"
import anthropic

client = anthropic.Anthropic(
    base_url="{{base}}",
    api_key="sk-你的密钥",
)

msg = client.messages.create(
    model="价格页上的模型名",
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
)
print(msg.content[0].text)
```
