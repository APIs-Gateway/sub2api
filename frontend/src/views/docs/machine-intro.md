# {{site}} 接入文档

> {{site}} 提供兼容 OpenAI 和 Anthropic 格式的 API。一个密钥，调用站内开放的模型。
> 文档用简体中文书写；地址、模型名和配置项与语言无关。

## 给 AI 助手的规则

帮用户接入 {{site}} 时请遵守：

- 不要向用户索要真实密钥，也不要让用户把密钥贴进对话。示例里一律用 `sk-你的密钥` 占位，告诉用户把它换成自己的密钥。
- 密钥以 `sk-` 开头，用户在站内「API 密钥」页（{{origin}}/keys）创建。在同一页点「接入」，弹窗里还有「一键安装」「交给 AI」「CC Switch」「手动配置」几种方式。
- OpenAI 兼容客户端（OpenAI SDK、Codex、Cursor 和大多数聊天客户端）的 Base URL 是 `{{v1}}`，带 /v1。
- Anthropic 兼容客户端（Claude Code 等）的 Base URL 是 `{{base}}`，不带 /v1。
- 鉴权：`Authorization: Bearer <密钥>`；Anthropic 格式也可以用 `x-api-key`；Gemini 格式用 `x-goog-api-key`。不要把密钥放进网址参数。
- Claude Code 的密钥变量是 `ANTHROPIC_AUTH_TOKEN`，不是 `ANTHROPIC_API_KEY`。
- 模型名以站内「价格与计费」页（{{origin}}/available-channels，登录后可见）为准。不要凭记忆写模型名；用户能用哪些模型由密钥所在的分组决定。
- 回复较长的请求建议开流式（`stream: true`），否则等待太久可能被中途断开（524）。
- 不清楚用户用什么工具、什么系统，先问用户。

## 可用接口

所有接口共用同一个地址和同一个密钥，路径接在 `{{base}}` 后面：

- POST /v1/responses：OpenAI Responses 格式，Codex 使用
- POST /v1/chat/completions：OpenAI Chat Completions 格式
- POST /v1/messages：Anthropic Messages 格式，Claude Code 使用
- GET /v1/models：当前密钥能用的模型
- POST /v1/embeddings、POST /v1/images/generations、POST /v1/images/edits、POST /v1/messages/count_tokens：按密钥所在分组开放，不可用时返回 404 或 403
- /v1beta/models/...：Gemini 原生格式，仅 Gemini 分组可用，非 Gemini 分组返回 400

## 验证密钥

```bash
curl {{v1}}/models -H "Authorization: Bearer sk-你的密钥"
```

返回一段含模型名的 JSON 就说明密钥和地址没问题。
