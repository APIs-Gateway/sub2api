# Codex PAT Backend Foundation

父 issue：#291
子 issue：#477
依赖：#475（Alpha/Search API-key foundation）

## §1 账号模型

- PAT 账号继续使用 `platform=openai` 与 `type=oauth`，不新增数据库账号类型。
- `credentials.auth_mode=personalAccessToken` 是规范标识；读取时兼容 `openai_auth_mode=personal_access_token`。
- PAT 不携带 OAuth refresh-only 字段；access token 保留为唯一上游 bearer credential。

## §2 PAT 校验

- 只接受去空格后以 `at-` 开头的 token。
- 通过 OpenAI whoami endpoint 获取 email、ChatGPT user/account id、plan type 和 FedRAMP 标志。
- 401/403 映射为可理解的无效 PAT；其他非 2xx、网络错误、JSON 畸形或字段缺失都不得创建账号。
- proxy 解析与请求超时沿用现有 HTTP client 约定。

## §3 创建与持久化

- admin endpoint 调用现有 proxy 查询和 account 创建服务，不绕过 group、并发、优先级、倍率或 mixed-channel 校验。
- 创建时写入校验后的 token metadata、auth mode、来源标识和不可逆 fingerprint；credential extras 不得覆盖受保护字段。
- 未提供名称时按 email、ChatGPT account id、user id 的稳定顺序生成名称。

## §4 运行时

- token provider 和 refresher 必须直接使用 PAT，不以 refresh token 或过期时间触发 OAuth refresh。
- HTTP、WS、quota、usage probe 统一设置 ChatGPT account id 与 FedRAMP header；普通 OAuth 行为保持不变。
- CRS 和 admin refresh 路径必须经过 PAT normalization，避免把 OAuth-only 字段重新写回。

## §5 验收

- service 测试覆盖 §2 的成功与失败分支、§1/§4 的 normalization 和 refresh bypass。
- 受影响 backend package 的 focused tests、gofmt、diff check 通过。
- 不在本 PR 加入 frontend UI/i18n、Alpha/Search web_search fallback、billing 或无关 upstream commit。
- 最终以远端 CI、Security、Codecov 全绿为准；本地不启动 Docker 或全量 integration。
