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

> 当前版本的 openai 包要求 Node.js 22 或更高。

curl、流式输出、Anthropic 格式和多轮对话的完整示例见「API 调用示例」一节。

## 用环境变量配置 {#openai-sdk-env}

官方 SDK 会自动读取 `OPENAI_API_KEY` 和 `OPENAI_BASE_URL`。设置好之后，代码里直接 `OpenAI()`（Node 是 `new OpenAI()`）就行，不用再把密钥写进代码：

```bash title="macOS / Linux"
export OPENAI_API_KEY="sk-你的密钥"
export OPENAI_BASE_URL="{{v1}}"
```

```powershell title="Windows PowerShell"
$env:OPENAI_API_KEY = "sk-你的密钥"
$env:OPENAI_BASE_URL = "{{v1}}"
```

很多基于 OpenAI SDK 的第三方框架和脚本也认这两个变量。

## 查看可用模型 {#openai-sdk-models}

能用哪些模型由密钥所在的分组决定。想在代码里动态读取：

```python
for m in client.models.list():
    print(m.id)
```

## 常见问题 {#openai-sdk-faq}

**报 404。** 多半是 `base_url` 少了 `/v1`。OpenAI SDK 用 `{{v1}}`，Anthropic SDK 用 `{{base}}`（不带 `/v1`）。

**报 401 或 403。** 密钥没复制完整、密钥所在分组不能用这个接口或这个模型，见「错误排查」。

**模型不存在。** 模型名以「价格与计费」页为准，并且要是密钥所在分组能用的模型。
