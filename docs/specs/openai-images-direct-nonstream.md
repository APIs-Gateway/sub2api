# OpenAI 图片仅直连非流式策略

## §1 目标

生产可通过 `gateway.disable_openai_images_streaming` 和
`gateway.disable_openai_responses_image_generation` 两个开关，将 OpenAI 图片能力
收敛为仅 `/v1/images/generations` 与 `/v1/images/edits` 的同步直连 API Key 路径。

## §2 图片接口非流式

当 `disable_openai_images_streaming=true`：

1. 无论客户端是否携带 `stream: true`，两个 Images 接口均按非流式处理。
2. 下游不会收到 SSE、partial image 或轮询 keepalive；转发给上游的请求固定
   `stream: false`，并移除 `partial_images`。
3. JSON 和 multipart 编辑请求均适用；模型、图片文件及其他合法参数保持不变。

关闭开关时保留现有行为（省略 `stream` 默认补 SSE）。

## §3 禁用 Responses 生图

当 `disable_openai_responses_image_generation=true`：

1. HTTP `/v1/responses`（含别名）中显式 `image_generation` / `image_gen` 意图返回
   OpenAI 形状的 400 `invalid_request_error`，不触发上游调用或计费。
2. Responses WebSocket 以策略违规关闭；纯文本 Responses 不受影响。
3. Images 路由只选择 API Key 账户，且始终转发到上游 Images API，不得因账户
   `openai_responses_supported`、OAuth 或异步桥接改走 Responses。
4. 管理后台的 OpenAI 图片账号测试也只请求上游 Images API。

关闭开关时保留既有 Responses/OAuth 图片桥接行为。

## §4 验证

单测覆盖：配置关闭时的既有兼容、开启时 JSON/multipart stream 强制归一化、
API Key 不再走 Responses、OAuth 不再是 Images 候选，以及 HTTP/WS Responses
图片请求被拒绝。部署后以管理 Key 复测 Images `stream:true` 返回 JSON，及
Responses 图片工具返回 400。
