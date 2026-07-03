# Issue #19 路由/账号调度与 failover 治理 spec

## §0 Main 分支复审结论

- `main` 已有账号优先级、粘性会话、失败账号排除、冷却字段排除和最多切换次数，不应把 #19 定义成“从零实现切号”。
- `main` 已有 `HandleUpstreamError`，并覆盖 401/402/403/429/529 及部分 400 失格样本，不应另起一套账号下线机制。
- `main` 已有 `ResolveUpstreamErrorResponse`、`IsRequestShapedUpstream4xx` 和 `client_via_upstream` 归因，客户端请求形 4xx 的透传与 SLA 排除已具备基础。
- #19 的真实缺口是：所有入口是否都调用同一错误治理、429/4xx/5xx 是否有统一可观测分桶、失格类错误是否确实写入冷却/禁用、健康降权是否建立在上述统一入口上。

## §1 问题定义

当上游返回错误时，系统必须先判断错误归属，再决定是否 failover 和是否改变账号调度状态。

- 本地配额/并发/API key 额度类 429 属于用户或本地策略，不进入账号 failover，不计入上游可用率。
- 请求形上游 4xx 属于客户端请求错误，默认透传真实状态码和上游报文，不 failover，不计入服务 SLA。
- 账号失格类 401/402/403/部分 400 必须触发现有账号错误治理，使账号进入 `SetError`、限流冷却或临时不可调度。
- 真上游 5xx/529/429 可以 failover，但需要记录是否 recover，避免只看到最终 502 而看不到换号链路。
- 高调用量下偶发上游 429 属于账号/上游容量的瞬时限流，不等于账号失格；应先统计滑窗比例，只有比例过高或上游明确给出长冷却信号时才冷却并切号。

## §2 阶段 1：观测与路径覆盖矩阵

### §2.1 必填观测字段

每次上游错误处理应能在 ops 日志或请求上下文中还原以下字段：

- `error_owner`: `client`、`client_via_upstream`、`provider`、`platform`。
- `error_source`: 本地准入、上游 HTTP、上游 SSE/WS、调度等待、账号运行时状态。
- `upstream_status_code` 与对外 `status_code`。
- 入口类型：Anthropic、OpenAI、Gemini、Antigravity；HTTP、SSE、WS；compat 或 native。
- 账号动作：无动作、`SetError`、`SetRateLimited`、`SetTempUnschedulable`、`SetOverloaded`。
- failover 动作：未进入、同账号重试、切换账号、切换耗尽、recover 成功。

### §2.2 路径覆盖矩阵

实现前必须维护一张覆盖矩阵，至少包含这些入口：

- Anthropic gateway: Messages、Responses、Chat Completions compat。
- OpenAI gateway: Responses、Chat Completions、Messages bridge、Images、Embeddings、WS v2。
- Gemini gateway: native v1beta、Messages compat、Chat Completions compat。
- Antigravity gateway: Claude 格式、Gemini 格式、WS/重试循环。

矩阵每一行必须标明：

- 是否调用 `HandleUpstreamError` 或等价的现有平台封装。
- 是否调用 `ResolveUpstreamErrorResponse` 或等价的共享响应策略。
- 是否设置 `OpsUpstreamStatusCodeKey`，以便 `client_via_upstream` 归因。
- 是否可能返回 `UpstreamFailoverError`，以及返回前是否已经执行账号动作。

## §3 阶段 2：决策修正

### §3.1 请求形 4xx

`400/404/408/409/413/415/416/422` 默认视为请求形上游 4xx。

- 不进入账号切换。
- 不写账号异常。
- 对外透传真实状态码，message 来自上游错误体，空报文时使用中性兜底文案。
- ops 归因为 `client_via_upstream`，SLA 口径排除。

### §3.2 账号失格类错误

以下错误必须进入现有账号治理：

- 401: token revoked、token invalidated、无 refresh_token 的 OAuth 账号。
- 402: workspace deactivated、余额或账务问题。
- 403: 非 OpenAI/Antigravity 的权限问题，以及 OpenAI 403 阈值治理命中。
- 400: organization disabled、credit balance、identity verification required。
- 429: 不归入失格类，按 §3.3 的比例阈值处理。
- 529: 写入已有 overload 冷却字段。

### §3.3 上游 429 比例阈值

高并发或大量调用时，健康账号可能偶发上游 429；这是正常容量信号，不应按单次错误失格处理，也不应每次都立即冷却切号。

- 单次或低比例上游 429：记录事件和分桶，不写 `SetError`，不改变 `status=active`，不立刻冷却账号，也不因单次 429 触发换号惩罚。
- 滑窗内 429 比例超过阈值时：当前请求允许 failover 到其它可调度账号，并对该账号写入限流冷却或临时降权。
- 如果上游明确提供 `Retry-After`、reset 时间或可解析的重试窗口，可以视为强冷却信号；冷却到该时间，但仍不写 `SetError`。
- 没有上游冷却时间时，冷却应使用短默认窗口并带 jitter，避免大量请求同时回到同一账号。
- 若 429 只在单个模型或单个窗口发生，优先记录模型/窗口维度；没有模型维度时才按账号级冷却处理。
- 如果所有候选账号都因 429/等待队列满不可用，对外返回 429/503 的既有语义，不把它包装成客户端请求错误。
- ops 中必须把它和本地配额 429 区分：`error_owner=provider`、`upstream_status_code=429`，并记录 `failover_recovered=true/false`。
- 阈值应同时考虑请求量和比例，例如最小样本数达到 N 后，最近 T 秒 / M 次请求中 429 占比超过 P% 才触发冷却切号，避免低流量账号因 1 次 429 被误惩罚。
- 健康治理只做轻量降权；只有连续窗口内 429 比例持续超过阈值时才进入更长冷却或更强降权，不能因零星 429 禁用账号。

### §3.4 400 failover 白名单

`gateway.failover_on_400` 保持默认关闭。

开启时只能允许明确的服务端配置类或临时类样本进入 failover，例如 Google `invalid project resource name`；不得把超上下文、参数非法、内容校验失败纳入白名单。

## §4 阶段 3：健康治理

健康治理只能建在阶段 1 的统一入口之上。

- 对连续 provider-owned 5xx/529 建立滑动窗口降权；429 必须按 §3.3 的比例阈值处理。
- 冷却到期后采用半开探测，探测成功再恢复权重。
- 高优先级账号恢复后允许 failback，但必须受负载感知和健康分保护。
- `gateway.scheduling.load_batch_enabled` 必须在运行时观测中暴露，避免生产配置偏离默认值时误判调度问题。

## §5 验收

- 请求形上游 4xx 不 failover，并在所有入口对外透传真实状态码。
- 账号失格类 401/402/403/部分 400 在所有入口都触发账号治理，冷却或禁用字段可查。
- 本地配额 429 与上游账号 429 可分桶查询，SLA 口径不混淆；上游 429 只有超过阈值或带强冷却信号时才触发冷却切号。
- failover 链路能查询到进入次数、切换次数、最终是否 recover。
- 路径覆盖矩阵覆盖 §2.2 列出的所有入口，新增入口必须同步补矩阵和测试。

## §6 测试要求

- 单元测试锁定各平台 `shouldFailover*` 策略：请求形 4xx 不切号，401/403/5xx/529 可切号；429 另测滑窗阈值策略，不按单个状态码无条件切号。
- 单元测试覆盖 `ResolveUpstreamErrorResponse`：请求形 4xx 透传，401/403/402/429/5xx 保留既有语义。
- handler 测试覆盖 failover exhausted 后仍走共享错误响应策略。
- 集成测试覆盖至少一个失格账号样本，证明错误返回后账号不再被 `IsSchedulable()` 选中。

## §7 实现覆盖矩阵

| 入口 | 上游错误入口 | 429 统计/阈值 | 账号动作 | ops/recover 观测 | 测试 |
| --- | --- | --- | --- | --- | --- |
| Anthropic Messages | `backend/internal/service/gateway_service.go` | 上游请求前 `recordUpstream429Attempt`；429 由 `RateLimitService` 判定阈值或强冷却信号 | `HandleUpstreamError` 统一处理 401/402/403/429/529 和 temp-unsched 策略 | `appendOpsUpstreamError` + `MarkOpsUpstreamFailoverRecovered` | `rate_limit_429_cooldown_test.go`、`ratelimit_service_anthropic_window_limit_test.go` |
| Anthropic Responses compat | `backend/internal/service/gateway_forward_as_responses.go` | 上游请求前记录 attempt；handler 在 429 低比例时阻止换号 | 复用 `HandleUpstreamError` | failover event 可标记 `failover_recovered` | `openai_failover_cached_body_test.go`、`ops_upstream_context_test.go` |
| Anthropic Chat Completions compat | `backend/internal/service/gateway_forward_as_chat_completions.go` | 上游请求前记录 attempt；429 低比例不冷却/不换号 | 复用 `HandleUpstreamError` | failover event 可标记 `failover_recovered` | `rate_limit_429_cooldown_test.go` |
| OpenAI Responses | `backend/internal/service/openai_gateway_service.go`、`backend/internal/handler/openai_gateway_handler.go` | HTTP/SSE 请求前记录 attempt；handler failover 循环调用 `ShouldSwitchAccountOn429` | `handleOpenAIAccountUpstreamError` + runtime block fastpath | failover event 可标记 recover | `openai_account_runtime_block_fastpath_test.go`、`openai_gateway_handler_test.go` |
| OpenAI Chat Completions | `backend/internal/service/openai_gateway_chat_completions.go`、`backend/internal/handler/openai_chat_completions.go` | 请求前记录 attempt；429 低比例不切换账号 | OpenAI 账号错误治理封装 | failover event 可标记 recover | `rate_limit_429_cooldown_test.go` |
| OpenAI Messages bridge/raw fallback | `backend/internal/service/openai_gateway_messages.go`、`openai_gateway_chat_completions_raw.go`、`openai_gateway_responses_chat_fallback.go` | 请求前记录 attempt；429 阈值统一读取 | OpenAI 账号错误治理封装 | failover event 可标记 recover | `openai_failover_cached_body_test.go` |
| OpenAI Images | `backend/internal/service/openai_images.go`、`openai_images_responses.go`、`backend/internal/handler/openai_images.go` | 请求前记录 attempt；图片 API 明确 rate-limit body 作为强信号 | 模型/能力冷却，不把零星 429 写 `SetError` | handler 低比例 429 不换号 | `ratelimit_service_openai_image_test.go` |
| OpenAI Embeddings | `backend/internal/service/openai_embeddings.go`、`backend/internal/handler/openai_embeddings.go` | 请求前记录 attempt；handler 低比例 429 不换号 | OpenAI 账号错误治理封装 | failover event 可标记 recover | handler/service unit tests |
| OpenAI WS v2 | `backend/internal/service/openai_ws_forwarder.go`、`openai_ws_http_bridge.go`、`openai_ws_v2_passthrough_adapter.go` | dial/write/prewarm 429 均记录 attempt；usage-limit/event 429 进入阈值 | OpenAI runtime block 仅在阈值/强信号后生效 | WS failover event 可标记 recover | `openai_ws_http_bridge_test.go`、`openai_ws_ratelimit_signal_test.go` |
| Gemini native v1beta | `backend/internal/service/gemini_messages_compat_service.go` | native generate 调用前记录 attempt；`handleGeminiUpstreamError` 判定阈值或 reset/retry-after | Google One/Code Assist/API Key 按账号类型冷却 | provider 429 与本地 429 分桶 | `gemini_error_policy_test.go` |
| Gemini Chat Completions compat | `backend/internal/service/gemini_chat_completions_compat_service.go` | 请求前记录 attempt；429 重试循环中调用 Gemini 错误治理 | reset/retry-after 为强信号，无 reset 需过阈值 | retry/failover event 进入 ops context | `gemini_error_policy_test.go` |
| Antigravity Claude/Gemini/WS | `backend/internal/service/antigravity_gateway_service.go`、`antigravity_credits_overages.go` | smart retry、retry-in-place、URL fallback、credit overage 路径记录 attempt；无 reset 429 需过阈值 | Retry-After/模型容量为强信号；弱 429 不立即账号冷却 | retry/failover event 进入 ops context | `antigravity_rate_limit_test.go`、`antigravity_smart_retry_test.go` |

## §8 验收核对

- §3.1 请求形 4xx：沿用 `ResolveUpstreamErrorResponse`/`IsRequestShapedUpstream4xx` 透传策略；本次未把请求形 4xx 纳入 429 failover 逻辑。
- §3.2 账号失格：`RateLimitService.HandleUpstreamError` 继续作为 401/402/403/部分 400/529 的统一入口；Anthropic 官方窗口耗尽类 429 作为强冷却信号但不写 `SetError`。
- §3.3 上游 429：新增 `upstream_429_window.go`，按最小样本数 + 滑窗比例触发 `ShouldSwitchAccountOn429`；`Retry-After`、reset 时间、官方窗口耗尽等强信号绕过比例等待并写冷却时间。
- §3.3 低比例 429：handler failover loop 和 OpenAI/Gemini/Antigravity 平台入口均在阈值未达时停止切号；`RateLimitService` 在阈值未达时跳过 `SetRateLimited`/`SetTempUnschedulable`。
- §3.3 observability：`OpsUpstreamErrorEvent` 持久化 `failover_recovered`，成功 recover 的请求由 `OpsErrorLoggerMiddleware` 标记。
- §5/§6 测试：新增/更新 429 滑窗、Retry-After 强信号、Anthropic window limit、pool-mode temp-unsched 阈值、OpenAI WS 429、Antigravity smart retry 和 ops recover 序列化测试；CI 级 unit/integration coverage 与真实 Docker/DB 集成测试已覆盖当前实现。
