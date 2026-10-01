# AI 入口：接手本项目前先读这一页

写给以后管理、运营、修改本项目的 AI。本页只给地图和规矩，细节在它指向的文件里。

本页和 `ops/skills/` 都是公开内容，**不含任何站点私有事实**（域名、IP、端口、服务器路径、
账号/用户 ID、上游厂商、密钥）。站点私有值见私有运维仓；公开仓里一律写成 `<占位符>`。

## 1. 先看哪几份文档

| 文件 | 内容 |
|---|---|
| `AGENT.md` | 协作约定和工程笔记索引（笔记怎么分流、怎么登记） |
| `DEV_GUIDE.md` | 本地环境、常见坑、命令速查（偏人工开发视角，见 §5 的说明） |
| `docs/notes/` | 通用工程笔记：迁移重号、可空列原始 SQL 扫描、Docker 热开发、Tailwind 透明度坑等 |
| `docs/specs/` | 单个功能的 spec，改对应功能前先读 |
| `ops/skills/` | 通用流程 skill：`bugfix`、`deploy`、`incident`（见 §6） |
| `skills/sub2api-admin/` | 通过管理 API 操作账号、分组、兑换码的 CLI skill |

## 2. 仓库地图

```
backend/                 Go 后端（Gin + Ent + Wire）
  cmd/server/            入口与依赖注入（wire.go -> wire_gen.go）
  ent/schema/            数据模型定义；改完要 `go generate`，生成物要提交
  migrations/            手写 SQL 迁移，go:embed，服务启动时自动执行
  internal/
    server/routes/       路由注册（gateway.go、admin.go、user.go、auth.go ...）
    handler/             HTTP 处理层：解析请求、调 service、组装响应；handler/admin 是管理端，handler/dto 是对外结构
    service/             业务逻辑，几乎所有规则都在这里
    repository/          数据访问层：Ent 查询 + 手写 SQL，迁移执行器也在这里
    config/              配置文件 / 环境变量的结构与默认值
    payment/             支付渠道对接
  resources/model-pricing/  内置价格目录副本
frontend/                Vue 3 + TypeScript + Pinia + vue-i18n
  src/api/               后端接口封装（api/admin 是管理端）
  src/views/ components/ 页面与组件（views/admin 管理端，views/user 用户端）
  src/stores/            Pinia 状态
  src/i18n/locales/      三语文案：zh-CN、zh-HK、en（zh.ts 只是 zh-CN 的别名）
deploy/                  Dockerfile、compose、配置样例、安装脚本
docs/ tools/ .github/    文档、辅助脚本（secret_scan.py 等）、CI
```

分层依赖方向：`handler -> service -> repository`。不要让 handler 直接查库，也不要让
repository 反向依赖 handler。

## 3. 去哪找什么

路径都已核实存在；改动前仍然先 `rg` 一遍，代码会漂。

| 想改 / 想查 | 后端入口 | 前端入口 |
|---|---|---|
| 设置项（后台系统设置） | 键名常量 `backend/internal/service/domain_constants.go`（`SettingKey*`）；读写与校验 `backend/internal/service/setting_service.go`；管理接口 `backend/internal/handler/admin/setting_handler.go`；对外结构 `backend/internal/handler/dto/settings.go` | `frontend/src/views/admin/SettingsView.vue`、`frontend/src/api/admin/settings.ts`、`frontend/src/stores/adminSettings.ts` |
| 只能写配置文件 / 环境变量的开关 | `backend/internal/config/config.go`、`deploy/config.example.yaml` | 无（不在后台暴露） |
| 分组 | `backend/ent/schema/group.go`（含回退分组字段）、`backend/internal/service/group_service.go`、`backend/internal/handler/admin/group_handler.go`、`backend/internal/repository/group_repo.go` | `frontend/src/views/admin/GroupsView.vue`、`frontend/src/api/admin/groups.ts` |
| 账号（上游凭据、调度开关、与分组的关联） | `backend/ent/schema/account.go`、`backend/ent/schema/account_group.go`、`backend/internal/service/account_service.go`、`backend/internal/handler/admin/account_handler.go` | `frontend/src/views/admin/AccountsView.vue` |
| 计费（价格、成本、扣费） | 价格与成本 `backend/internal/service/billing_service.go`；价格目录 `backend/internal/service/pricing_service.go`；余额与订阅资格检查 `backend/internal/service/billing_cache_service.go`；落账 SQL `backend/internal/repository/usage_billing_repo.go` | `frontend/src/views/user/UsageView.vue`（用户侧用量）、`frontend/src/composables/useCurrencyDisplay.ts` |
| 订阅与套餐 | `backend/ent/schema/subscription_plan.go`、`backend/ent/schema/user_subscription.go`、`backend/internal/service/subscription_service.go`、`backend/internal/handler/admin/subscription_handler.go` | `frontend/src/views/admin/SubscriptionsView.vue` |
| 用量日志 | `backend/ent/schema/usage_log.go`、`backend/internal/repository/usage_log_repo.go` | `frontend/src/views/admin/UsageView.vue` |
| 请求路由与选号 | 入口路由 `backend/internal/server/routes/gateway.go`；处理 `backend/internal/handler/gateway_handler.go`、`backend/internal/handler/openai_gateway_handler.go`；选号 `backend/internal/service/gateway_service.go`（`SelectAccount*`）、`backend/internal/service/openai_account_scheduler.go`（`SelectAccountWithScheduler`） | 无 |
| 数据库迁移 | `backend/migrations/*.sql`、执行器 `backend/internal/repository/migrations_runner.go` | 无 |
| 支付 | `backend/internal/payment/`、`docs/PAYMENT.md` | `frontend/src/views/user/PaymentView.vue` |

一个典型的后端改动要依次碰：`ent/schema` -> `go generate` -> `migrations` -> `repository` ->
`service` -> `handler`（含 `dto`）-> `routes` -> wire（新增 provider 时）-> 前端 `api` / `types` /
`views` / 三语文案。漏掉任何一层都会在 CI 或线上才暴露。

## 4. 改之前必读的几条通用笔记

- `docs/notes/db-migration-duplicate-numbers.md`：迁移数字前缀重号是有意的，按完整文件名跟踪，不要为了连号去改名。
- `docs/notes/nullable-column-rawsql-scan.md`：列改可空后，手写 SQL 扫到 `int64`/`string` 会在 NULL 行崩，涉及钱的路径尤其危险。
- `docs/notes/tailwind-scoped-color-opacity.md`：`<style scoped>` 里定义的变量色不吃 `/NN` 透明度修饰符，会静默失效。
- `docs/notes/blue-green-zero-downtime-upgrade.md`：无损升级的通用方法，`ops/skills/deploy` 引用它。

## 5. 工程规矩

1. **CI 是编译器，本地不全量编译。** 改完就 commit、push、开 PR，让 CI 编译和跑测试，
   用 CI 输出驱动下一轮修复。本地只跑便宜的静态检查：
   - 后端：`gofmt -l backend`（有输出就先格式化）；
   - 前端：`pnpm --dir frontend run typecheck`（vue-tsc）、`pnpm --dir frontend run lint:check`（eslint）、
     相关 spec 的 `pnpm --dir frontend exec vitest run <spec>`；
   - 仓库根：`make secret-scan`。

   `DEV_GUIDE.md` 的提交前清单是人工全量验证的口径；AI 代理按本条执行。CI 里的 required
   检查包括 `codecov/patch`（见 `codecov.yml`，改动行覆盖率有下限），新代码要带测试。
2. **`main` 受保护，只走 PR。** 不要 `git push origin main`。新分支一律先
   `git fetch --all --prune`，再 `git switch -c <分支> origin/main`，不要用裸 `git checkout -b`。
   PR 的合并交给人，AI 不自行合并，除非被明确授权。
3. **并行 agent 用 worktree 隔离。** 每个 agent 一个 worktree、一个分支：
   `git worktree add -b <分支> <路径> origin/main`。多个 agent 写同一个工作树和分支会互相
   amend、推送不相关文件。提交和推送前先看 `git status` 和 `git log --oneline -5`，
   发现意外的提交或文件就停下来问人。
4. **新模型的价格有两条路径，都要改。**
   - 动态目录：LiteLLM 格式的价格 JSON，由 `backend/internal/service/pricing_service.go` 加载，
     本地副本在 `backend/resources/model-pricing/model_prices_and_context_window.json`；
   - 静态 fallback：代码里写死的兜底价，即 `pricing_service.go` 里的 `openAI*FallbackPricing`
     和 `backend/internal/service/billing_service.go` 的 `initFallbackPricing` / `getFallbackPricing`。

   只改一边会出现「动态目录对、兜底错」或反过来的计费不一致。缓存读写、长上下文分档这类
   特殊计费属性不能照搬别家模型的字段路径，要对着该厂商的用量字段单独核对。
5. **用户可见文案不暴露上游和内部机制。** 页面和 API 错误体里不写上游厂商、上游模型名、
   账号池、路由判定这类词；详细信息只进管理员侧日志和用量记录。写文案前先
   `rg` 一遍 `frontend/src/i18n/locales/zh-CN.ts`，沿用产品已有术语，不新造词，也不给已有词
   加第二层含义；实现口径写进代码注释或 PR 描述，不进界面。新增文案必须 zh-CN、zh-HK、en
   三套同时补齐（`pnpm --dir frontend run check:i18n` 会校验）。改前端界面前，如果环境里有
   `frontend-design` skill，先加载它。
6. **站点私有内容不进公开仓。** 密钥、域名、IP、端口、服务器目录、账号和用户 ID、
   上游厂商名一律不写进代码、文档、commit、PR。需要时写 `<占位符>`，并注明「站点私有值见私有运维仓」。
   `AGENT.md` 的「工程笔记保存流程」有密级分流规则。

## 6. 流程 skill

`ops/skills/` 下是通用流程，站点相关的值都是占位符：

| skill | 什么时候用 |
|---|---|
| `ops/skills/bugfix/SKILL.md` | 修 bug：复现 -> 查根因 -> 失败测试 -> 修复 -> 静态检查 -> PR -> 真实环境验证 |
| `ops/skills/deploy/SKILL.md` | 发布新版本：构建镜像 -> 迁移 dry-run -> 备份 -> 配置 diff -> 保留旧前端资源 -> 更新 compose -> 验证 -> 回滚预案 |
| `ops/skills/incident/SKILL.md` | 线上故障：先恢复站点，再查根因；按账号拆指标，分层定位 |

新增流程时，在 `ops/skills/<名字>/SKILL.md` 里写一个短文件（frontmatter 带 `name` 和
`description`，`description` 写清楚什么时候该用），并在上表登记一行。
