# 上游模型不一致拦截

## 目标

第三方中转 / 账号池对端会偷换模型：网关把 `gpt-5.6-sol` 发给上游，上游响应体里的 `model` 却是 `gpt-6-sol`。此前网关不拦截、照常计费。本功能在 OpenAI 平台的全部转发路径（Responses 主路径与 passthrough、Chat Completions 入站、Anthropic Messages 入站、raw chat 上游、各 chat fallback、Grok、WS v2 三条路径）检测并拦截这类响应。

## 判定口径

- `A` = 实际发给上游的模型：账号级 `model_mapping` + 规范化之后写进上游请求体的模型（与 `usage_logs.upstream_model` 同源）。passthrough 路径请求体原样转发时 `A` = 客户端请求的模型。
- `B` = 上游响应体里的 `model`（Responses 事件取 `response.model`，Chat chunk / 整包取顶层 `model`）。
- `A != B` 即不一致。账号级刻意映射（如 `sol → luna`）的 `A` 已经是 `luna`，与 `B` 相等，天然放行。

豁免（`upstreamModelMatches`）：忽略大小写与首尾空白；`B` 为空放行；`B == A + "-" + 日期快照`（`YYYY-MM-DD` / `YYYYMMDD`）放行；`A` 以 `-latest` 结尾且 `B` 以去掉 `-latest` 的前缀开头放行。

## 拦截行为

- 只在客户端尚未收到任何字节时拦截：返回 `UpstreamFailoverError`（502，`error.code = upstream_model_mismatch`），走现有切号重试；全部账号耗尽后客户端收到 502（流式已开始则补 `response.failed`）。
- 客户端已收到输出（上游把 `model` 放在 `response.completed` 才首次给出）时不拦截，只打标记录。
- WS v2 的 HTTP 桥与 ingress 代理只在会话第 1 轮拦截（后续轮次 handler 会用首条消息重放，拦截会导致重复输出），其余轮次只记录。
- 被拦截的尝试记一行 `usage_logs`：`upstream_model_mismatch = true`、`upstream_response_model = B`、`total_cost / actual_cost = 0`、不扣余额 / 订阅 / 配额；token 原样记录；`request_id` 为 `<原 request_id>:mismatch:<account_id>`，避免与随后成功重试的行撞唯一索引与计费去重键。
- 成功路径（观察模式或晚到的 `model`）照常计费，只把 `B` 写进 `upstream_response_model`。

## 开关

`gateway.disable_upstream_model_mismatch_block`（env `GATEWAY_DISABLE_UPSTREAM_MODEL_MISMATCH_BLOCK`），默认 `false` = 拦截开启。设为 `true` 进入观察模式：仍解析 `B` 并写入两列，只是不拦截、照常计费。线上误杀时可不发版止血。

## 数据与后台

- 迁移 `190_usage_log_upstream_model_mismatch.sql`：`usage_logs` 新增 `upstream_model_mismatch BOOLEAN NOT NULL DEFAULT FALSE`、`upstream_response_model VARCHAR(100)`。`upstream_model` 列语义不变。
- 管理员用量接口：`GET /api/v1/admin/usage` 与 `/stats` 支持 `upstream_model_mismatch=true|false` 筛选；`AdminUsageLog` 返回 `upstream_model_mismatch`、`upstream_response_model`。
- 后台用量页：不一致的行在模型列标红显示上游返回的模型并带「模型不一致」徽标；筛选栏新增「全部 / 仅不一致 / 仅一致」下拉。

## 不做的事

- 不做账号健康度联动：failover 已调用 `ReportOpenAIAccountScheduleResult(account.ID, false, nil)` 自然降权。
- Anthropic / Gemini 平台不在本期范围。
