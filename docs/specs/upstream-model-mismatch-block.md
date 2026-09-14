# 上游模型不一致拦截

## 目标

第三方中转 / 账号池对端会偷换模型：网关把 `gpt-5.6-sol` 发给上游，上游响应体里的 `model` 却是 `gpt-6-sol`。此前网关不拦截、照常计费。本功能在 OpenAI 平台的全部转发路径（Responses 主路径与 passthrough、Chat Completions 入站、Anthropic Messages 入站、raw chat 上游、各 chat fallback、Grok、WS v2 三条路径）检测并拦截这类响应。

## 判定口径

- `A` = 实际发给上游的模型：账号级 `model_mapping` + 规范化之后写进上游请求体的模型（与 `usage_logs.upstream_model` 同源）。passthrough 路径请求体原样转发时 `A` = 客户端请求的模型。
- `B` = 上游响应体里的 `model`（Responses 事件取 `response.model`，Chat chunk / 整包取顶层 `model`）。
- `A != B` 即不一致。账号级刻意映射（如 `sol → luna`）的 `A` 已经是 `luna`，与 `B` 相等，天然放行。

豁免（`upstreamModelMatches`），全部忽略大小写与首尾空白：

- `B` 为空放行。
- 日期快照：`B == A + "-" + 日期`（`YYYY-MM-DD` / `YYYYMMDD`）放行；`A` 以 `-latest` 结尾且 `B` 以去掉 `-latest` 的前缀开头放行。
- provider 前缀：去掉 `provider/` 前缀（`lastOpenAIModelSegment`）后再做上面两条比对，`openai/gpt-5.6-sol` 与 `gpt-5.6-sol` 互相回显视为一致；前缀不掩盖真正的换模（`openai/gpt-5.6-sol` vs `gpt-6-sol`、`gpt-5.6-sol` vs `anthropic/gpt-5.6-sol-mini` 仍拦截）。
- codex 别名，只认升级方向：`A` 是精确别名表（`codexModelMap`）里的键且 `B` 正是它的目标才放行（`gpt-5.3 → gpt-5.3-codex`、`gpt-5.1 → gpt-5.4`、`gpt-5.4-high → gpt-5.4`）；反向不放行，`A = gpt-5.4` 收到 `gpt-5-mini` / `gpt-5` / `gpt-5.4` 收到别的降级仍拦截。
- 同族 reasoning / 日期后缀剥离：任一方去掉已知后缀（`codexVersionModelPrefixes` + `isKnownCodexModelSuffix`，即 `none/minimal/low/medium/high/xhigh` 与 `YYYY-MM-DD`）后等于另一方放行（`gpt-5.4 ↔ gpt-5.4-high`）。不用任何 `Contains` 启发式，`gpt-5.6-sol-mini`、`gpt-5.8 → gpt-5.4` 都不会被折叠放行。
- grok 只记录不拦截：`A` 以 `grok` 开头时不一致只打标（`Blocked=false`）、照常透传、照常计费（见「计费口径」）。xAI 用带日期的模型名（如 `grok-4.3-0709`），上述豁免覆盖不了，真实回显尚未验证，先观察。

## 拦截行为

- 只在客户端尚未收到任何业务字节时拦截：返回 `UpstreamFailoverError`（502，`error.code = upstream_model_mismatch`），走现有切号重试；全部账号耗尽后客户端收到 502（流式已开始则补 `response.failed`）。等待上游期间写出的 SSE 心跳（注释行 / Anthropic `ping`）不算业务字节，见「已知边界」。
- 客户端已收到输出（上游把 `model` 放在 `response.completed` 才首次给出）时不拦截，只打标记录；这是「本应拦截但拦不住」的行，零计费（见「计费口径」）。
- WS v2 的 HTTP 桥与 ingress 代理只在会话第 1 轮拦截（后续轮次 handler 会用首条消息重放，拦截会导致重复输出），其余轮次只记录。
- 池模式账号（`pool_mode = true`，中转自身是一池多 key）：偷换模型的多半只是池里某个坏节点，所以 `UpstreamFailoverError` 带 `RetryableOnSameAccount`，走 handler 现有的池模式分支——先在同一账号上重试，最多 `pool_mode_retry_count` 次（默认 3），用尽后再切号并降权。`502` 不加入 `pool_mode_retry_status_codes` 默认列表。非池模式账号行为不变，直接切号。WS v2：HTTP→WS v2 桥（`forwardOpenAIWSV2` / HTTP 桥）的 failover 错误经 `Forward` 抛回 HTTP Responses handler，随该 handler 的池模式分支走同账号重试；只有 WS ingress 代理（`ProxyResponsesWebSocketFromClient`）没有同账号重试机制，直接切号（不变）。成本提醒：对一个稳定偷换模型的池模式凭证，每个请求会向该上游发 1 + `pool_mode_retry_count` 次完整 prompt 后才切号，上线后关注该类账号的不一致行数 × 估算 `input_tokens`。
- 被拦截的尝试记一行 `usage_logs`：`upstream_model_mismatch = true`、`upstream_response_model = B`、`total_cost / actual_cost = 0`、不扣余额 / 订阅 / 配额；token 原样记录；`request_id` 为 `<原 request_id>:mismatch:<account_id>`，避免与随后成功重试的行撞唯一索引与计费去重键。池模式同账号重试后再次被拦截的第 n 次（n ≥ 1）记为 `<原 request_id>:mismatch:<account_id>:<n>`，同一账号的多行也不撞键；超过 64 字节时截原 request_id 部分、后缀保留。
- 拦截行的 `input_tokens`：流式拦截多发生在 `response.created`（上游尚未回报 usage），此时 `Mark.Usage` 全零，但 prompt 已经发出、输入侧消耗真实发生。网关按请求体估算（`EstimateOpenAIRequestInputTokens`：Responses `instructions` / `input`、Chat `messages`、Anthropic `system` / `messages` 里的文本 ≈ 4 字符/token，每条消息另加 4 token 结构开销，`tools[]` 的 `description` / `parameters` 也按文本估；工具调用 / 输出的文本也计入：Responses `function_call.arguments` / `function_call_output.output`、Chat `tool_calls[].function.arguments`、Anthropic `tool_use.input` / `tool_result.content`；图片 / 文件 / 音频等非文本跳过）写入 `input_tokens`，并打 Info 日志 `openai.upstream_model_mismatch_input_tokens_estimated`。上游已回报 usage（`input_tokens > 0`）时原样记录、不覆盖。估算值仅供评估上游侧消耗，不是上游口径；`output_tokens` 不估；成本列恒为 0、不扣费。注意估算值与审计行的其他 token 一样会进入后台用量图 / 仪表盘的 `SUM(input_tokens)` 类聚合（审计行 token 原样记录是既定口径），对账时用 `upstream_model_mismatch` 筛掉。
- 成功路径（观察模式、晚到的 `model`、grok 观察）`request_id` 不加后缀，但该行同样标记 `upstream_model_mismatch = true` 并写 `upstream_response_model = B`，后台「仅不一致」筛选与徽标可见；计费见下节「计费口径」。

## 计费口径

零计费只覆盖「本应拦截」的行；两类判定尚不确定的观察场景照常计费、只标记。

- **被拦截行**（`UpstreamModelMismatchBlocked = true`）：`total_cost / actual_cost = 0`，不扣余额 / 订阅 / key 额度 / 账号额度，完全跳过 `applyUsageBilling`（不占计费去重键），`request_id` 加 `:mismatch:<account_id>` 后缀。
- **晚到 `model` 行**（拦截开启、`A` 不在 grok 观察豁免内、只因内容已交付无法拦截，`upstream_response_model` 非空）：`RecordUsage` 由 `upstreamModelMismatchZeroCost` 判定，同样 `total_cost / actual_cost = 0`——用户不该为被换掉的模型买单；走正常 `applyUsageBilling` 路径，但零成本时该路径不扣任何余额 / 订阅 / key 额度 / 账号额度 / 平台额度（`buildUsageBillingCommand` / `postUsageBilling` 全部以 `ActualCost > 0` / `TotalCost > 0` 为前提）。`request_id` 不加后缀（这是真实请求行）。打 Info 日志 `openai.upstream_model_mismatch_late_zero_cost`（`account_id`、`sent`、`got`、`request_id`）便于核对。
- **grok 观察行**（`A` 以 `grok` 开头）：判定规则对 xAI 带日期的模型名尚未验证，只标记、照常计费。
- **总开关关闭**（`gateway.disable_upstream_model_mismatch_block = true`，观察模式）：用于线上误杀止血，此时所有带 `B` 的行只标记、照常计费——否则误杀时开关止得住拦截、止不住漏收费。
- 零计费行的 token 都原样记录供审计与对账，`billing_mode = token`。后台对账用 `upstream_model_mismatch` 筛选；「是否被拦截」看 `request_id` 后缀。

## 对外 / 对内文案分工

对外笼统、对内详尽：终端用户看到的错误文案不带任何内部名词（"upstream"、模型名、账号、request id）；内部记录一个不省。

- 对外固定文案：`UpstreamFailoverError.ResponseBody` 的 `error.message` = `UpstreamModelMismatchClientMessage`（`"Service temporarily unavailable, please retry later"`，沿用产品既有句，不新造）；`error.type = upstream_error`、`error.code = upstream_model_mismatch` 保留供内部识别与透传规则匹配。failover 耗尽后各入口都经 `ResolveUpstreamErrorResponse`：默认 502 映射给 `"Upstream service temporarily unavailable"`、Codex / Responses 入站给 Codex 官方文案（`response.failed` 的 `error.message` 留空）、Anthropic 入站同默认映射、WS v2 关闭帧用按状态码的固定串；管理员配置「透传 body」规则时客户端拿到的是 `ResponseBody.message` 原文，所以这句本身必须是笼统文案。`response.failed` 事件里的 `response.model` 是客户端自己请求的模型（Responses 协议字段），不是上游返回的模型。
- 对内字段清单（`ops_error_logs`、日志、`usage_logs`）：`ops_upstream_error_message` 与 `upstream_errors[].message` = `"upstream returned a different model than requested: sent=<A> got=<B>"`（failover 耗尽后 `ResolveUpstreamErrorResponse` 对 `error.code = upstream_model_mismatch` 的 body 只记状态码、不用笼统文案覆盖顶层消息）；`upstream_errors[]` 另带 `account_id`、`account_name`、`upstream_status_code = 502`、`upstream_request_id`、`upstream_headers`（白名单指纹）、`kind = failover`；WARN 日志 `openai.upstream_model_mismatch` 带 `account_id`、`sent_model`、`response_model`、`blocked`、`can_block`；审计 `usage_logs` 行带 `upstream_model`（A）、`upstream_response_model`（B）、`request_id` 后缀。

## 运维口径：如何判断某个上游是否掺假

- 只能用线上 `usage_logs` / ops 日志做被动统计：同一凭据、足够长窗口内的不一致比例（后台用量页「仅不一致」筛选，或 `upstream_model_mismatch = true` 按 `account_id` 聚合）。
- 不能靠发探测请求：一次探测只是账号池里某个节点的样本。同一凭据一次探测 24/24 全不一致、同一小时线上 127 次全部正常，两者曾同时出现过——探测抽到了池里的坏节点。这也是池模式账号先同账号重试而不是立刻切号的依据。
- 只有一家的全部凭据在足够长的窗口里持续不一致，才谈得上平台级重定向，再考虑对该上游做账号级处置。
- 向 a6（New API）追责时注意其 request id 的时间戳前缀是 UTC：去对方后台按时间搜要把北京时间减 8 小时。
- New API 系中转的 request id 常只在错误体里（`error.request_id` / 顶层 `request_id` / `error.message` 末尾的 `request_id: …`），响应头里没有；`appendOpsUpstreamError` 已兜底从错误体提取进 `upstream_errors[].upstream_request_id`（头里有值不覆盖）。
- 不一致相关的 `usage_logs` 行（审计行与观察模式行）必写 `upstream_model`（A），即使与 `model` 相等；普通行仍只在两者不同时写。

## 上游 request id 与上游头指纹

- 上游 request id 头名兼容：第三方中转不一定发 `x-request-id`（one-api / new-api 系如 a6api、rivoapi 发 `x-oneapi-request-id`，rix-api 系如 platform.ephone.chat 发 `x-rixapi-request-id`，Bedrock 发 `x-amzn-requestid`，过 Cloudflare 的有 `cf-ray`）。service 层统一用 `upstreamRequestIDFromHeader` 按 `x-request-id` → `x-oneapi-request-id` → `x-rixapi-request-id` → `x-amzn-requestid` → `cf-ray` 的顺序取第一个非空值，写入 `ops_error_logs.upstream_errors[].upstream_request_id`、`OpenAIForwardResult.RequestID` 等；只作用于读上游响应头，回写给客户端的 `x-request-id` 响应头只回显上游同名头（不会把 cf-ray / oneapi id 顶替进去），客户端请求头也不变。取到的值会进入 `OpenAIForwardResult.RequestID` / `ForwardResult.RequestID`，进而在 ctx 没有 request id 时（或 WS 模式）写入 `usage_logs.request_id`——对只发 cf-ray 的上游，该列从 `generated:<uuid>` 变为 cf-ray 形态（每请求唯一，不撞唯一索引）。
- 上游头指纹：模型不一致的 ops 事件带 `upstream_headers`（白名单 `server`、`x-new-api-version`、`cf-ray`、`x-oneapi-request-id`、`x-rixapi-request-id`、`x-request-id`、`x-amzn-requestid`、`via`；只取非空，值截到 128 字节；WS 路径没有 HTTP 响应头，不带该字段），用于识别中转实现与向厂商追责。不含 cookie / 凭证类头。

## 开关

`gateway.disable_upstream_model_mismatch_block`（env `GATEWAY_DISABLE_UPSTREAM_MODEL_MISMATCH_BLOCK`），默认 `false` = 拦截开启。设为 `true` 进入观察模式：仍解析 `B`、把行标记为不一致并写 `upstream_response_model`，只是不拦截、照常计费（见「计费口径」）。线上误杀时可不发版止血。

开关只有配置文件 / 环境变量两种来源，没有后台设置项，不做热加载：改完必须重启容器（`docker compose up -d` 重建或 `docker restart`）才生效。

## 已知边界

- **keepalive 先于首个带 `model` 的事件：仍然拦截**。Responses 主路径、Anthropic 入站、Chat 入站在等待上游首个事件期间会按 `gateway.stream_keepalive_interval`（默认 10 秒，0 关闭）向客户端写心跳（Responses / Chat 是 SSE 注释行 `:\n\n`，Anthropic 入站是 `event: ping`）。心跳是客户端丢弃的非语义字节，「只发过心跳」≠「内容已交付」：service 层按 context 累计心跳字节数（`addOpenAIStreamKeepaliveBytes`），`OpenAICompactKeepaliveAdjustedWrittenSize` 扣掉这些字节后仍视为「未写」，所以 `canBlock` 仍为 true，不一致照常拦截、打标 `Blocked=true`、记审计行、不计费；handler 按扣除心跳后的 Size 放行切号（不置 `SafeToFailoverAfterWrite`，不占首输出超时的单次切号额度，仍可切满 `max_account_switches`）。客户端表现：同一条 SSE 连接（响应头已是 200）不断开，直接继续下一账号的事件；全部账号耗尽时在流内收到终止事件（Responses / Chat 入站 `response.failed` / `error`，Anthropic 入站 `event: error`），不会再写 JSON 错误体。只有真正向客户端转发过上游业务事件（任何 `data:` 事件已 flush）之后才只记录不拦截：mark 打标、`upstream_model_mismatch = true`、作为晚到行零计费（见「计费口径」），WARN 日志 `openai.upstream_model_mismatch` 字段 `can_block=false`、`blocked=false`。
- grok 系列始终只记录（见豁免）。
- WS v2 第 2 轮及以后只记录（见拦截行为）。

## 客户端可见 model 对齐（v0.1.151）

拦不住的场景——内容已开始下发（model 只在 `response.completed` 才出现）、观察模式、grok 只记录不拦截、WS v2 第 2 轮及以后——客户端不能看到上游真实返回的模型名。规则：

- **发给客户端的一切响应里，`model` / `response.model` 一律等于下游客户端原始请求的模型名（originalModel）**，无论上游返回什么、无论是否配置 `model_mapping`。实现为 `alignClientVisibleModel` / `alignClientVisibleModelInSSELine` / `alignClientVisibleModelInSSEBody`（`openai_client_model_align.go`），无条件调用，替代原先只在「响应 model == mappedModel」时才反向改写的 `replaceModelIn*` / `replaceOpenAIWSMessageModel`（已删除）。originalModel 为空原样返回；字段不存在不添加；字段已相等零拷贝；`[DONE]` / 非 `data:` 行不动；非字符串类型的 `model` 不动。
- **界线：只改发给客户端的字节，审计保留真实值**。各路径都是「先比对、后对齐」：`checkUpstreamModelMismatch` 读到的 B、`UpstreamModelMismatchMark.ResponseModel`、`usage_logs.upstream_response_model`、ops 事件里的 sent/got、`OpenAIForwardResult.UpstreamModel` 全部不受对齐影响。拦截 / 切号行为不变。
- 覆盖路径（全部为 OpenAI 平台网关；Anthropic 平台 `gateway_service.go` 与 images 不在范围）：
  - Responses 主路径流式 `handleStreamingResponseWithReasoning`、passthrough 流式 `handleStreamingResponsePassthrough`：每个带 `response.model` / 顶层 `model` 的事件（created / in_progress / completed / failed / incomplete …）。
  - Responses 非流式 `handleNonStreamingResponse`、`handleNonStreamingResponsePassthrough`、SSE→JSON `handleSSEToJSON` / `handlePassthroughSSEToJSON`（含无终止事件、原样回写 SSE 的分支）。
  - Chat Completions 入站：Responses SSE→chat chunk 转换（`handleChatStreamingResponse` / `handleChatBufferedStreamingResponse`，转换器一直以请求侧 `originalModel` 为准）；raw 直转 `streamRawChatCompletions` 逐 chunk、`bufferRawChatCompletions` 整包（此前 raw 路径连 `model_mapping` 都不反向改写，现在一并对齐）。
  - Messages 入站 `openai_gateway_messages.go`（`message_start` 的 `message.model`、非流式 JSON `model`）、`openai_gateway_messages_chat_fallback.go`、`openai_gateway_responses_chat_fallback.go`（转换器以 `originalModel` 为准）。
  - grok `openai_gateway_grok.go`（复用 Responses 主路径处理函数）。
  - WS v2：`forwardOpenAIWSV2` 逐事件与非流式最终 JSON、`ProxyResponsesWebSocketFromClient` 每轮逐事件、`proxyOpenAIWSHTTPBridgeTurn` 逐事件。

## 数据与后台

- 迁移 `190_usage_log_upstream_model_mismatch.sql`：`usage_logs` 新增 `upstream_model_mismatch BOOLEAN NOT NULL DEFAULT FALSE`、`upstream_response_model VARCHAR(100)`。`upstream_model` 列语义不变（不一致行改为必写，见运维口径）。
- 管理员用量接口：`GET /api/v1/admin/usage` 与 `/stats` 支持 `upstream_model_mismatch=true|false` 筛选；`AdminUsageLog` 返回 `upstream_model_mismatch`、`upstream_response_model`。
- 后台用量页：不一致的行在模型列标红显示上游返回的模型并带「模型不一致」徽标；筛选栏新增「全部 / 仅不一致 / 仅一致」下拉。

## 不做的事

- 不做账号健康度联动：failover 已调用 `ReportOpenAIAccountScheduleResult(account.ID, false, nil)` 自然降权。
- Anthropic / Gemini 平台不在本期范围。
