---
name: bugfix
description: 修 sub2api 的 bug 的标准流程。用户报「某功能不对 / 报 500 / 线上异常 / 数据对不上 / 用户投诉」并要求修复时使用；先复现，查到根因再汇报，然后按 失败测试 -> 修复 -> 本地静态检查 -> 开 PR -> 真实环境端到端验证 推进。单测通过不算验证通过。
---

# Bugfix 流程

站点相关的值（线上地址、SSH 目标、数据库连接、日志位置）一律写成 `<占位符>`，
站点私有值见私有运维仓。仓库地图和代码入口见 `docs/ai/README.md`。

两条硬规矩：

- **查到根因再汇报。** 不要拿未验证的推断，加一句「要不要我修」的选择题去打断用户。
  证据链没闭合就继续查。
- **单测、集成测试绿了不等于修好。** 最后必须在真实环境用真实请求走一遍（第 7 步）。

## 0. 准备

1. `git fetch --all --prune`，再 `git switch -c fix/<简述> --no-track origin/main`
   （不加 `--no-track` 会把上游设成 `origin/main`，裸 `git push` 有误推 main 的风险）。
   首次推送用 `git push -u origin fix/<简述>`。并行 agent 各用自己的 worktree。
2. 先读相关的 `docs/notes/`、`docs/specs/`，用 `docs/ai/README.md` 第 3 节定位入口文件。

## 1. 复现

1. 问清或查清：具体输入（请求、用户/账号 ID、时间窗）、预期结果、实际结果。
2. 线上问题**先拿运行时证据，再回头读代码解释它**：
   - 应用日志里带文件行号的 ERROR；
   - 访问日志里这次请求的真实 URL 和参数；
   - 数据库里的实际取值；
   - 用 `client_request_id` 或上游 request id 把整条链路的日志串起来。
3. 构造最小复现（本地或测试环境）。复现不了就继续收集证据，不要靠猜。

## 2. 查根因

1. 自顶向下排：入口（反代 / handler）-> service -> repository / SQL -> 上游。
2. 看到事务相关的次生报错（如 `current transaction is aborted`），往上翻同一请求里**第一条**失败语句，
   细节见 `ops/skills/incident/SKILL.md` 第 4 节。
3. 同一份规则在代码、Ent schema、迁移 CHECK 约束里各存一份时，逐份核对是否同步。
4. 用 `rg` 找同类位置：同一个模式还有哪些入口、哪些调用点有同样的问题。
5. 汇报格式：现象 -> 因果链（每一环给证据：日志行、SQL 结果、代码行）-> 影响范围 -> 修复方案。

## 3. 写失败测试

1. 先写能复现根因的测试，**确认它在修复前失败**。
2. 选对层：
   - 纯逻辑：单测，必须带 `-tags unit`（漏了会「没有测试可跑」，假绿）；
   - 涉及 SQL、NULL 行、并发、幂等、迁移、钱：用真实 PostgreSQL / Redis 的集成测试
     （`-tags integration`，位于 `backend/internal/repository`，testcontainers）。
3. 只跑单个包、几秒内能跑完的定向单测可以本地跑（跑不完就交给 CI）：`cd backend && go test -tags unit -run <名字> ./internal/<包>/`。
   需要容器的集成测试交给 CI。
4. 修复涉及的行要被测试覆盖，否则 `codecov/patch` 会红。

## 4. 修复

1. 改根因，不改症状；改动保持最小，不顺手重构。
2. 涉及钱：余额用相对自增 `balance = balance + X`；改完清对应缓存；迁移必须幂等（`IF NOT EXISTS`）。
3. 给 interface 加方法后，补全所有测试 stub / mock。
4. 改了 `ent/schema`：`cd backend && go generate ./ent`，生成物一并提交。
5. 用户可见文案和错误体遵守 `docs/ai/README.md` 第 5 节第 5 条。

## 5. 本地静态检查（不做全量编译）

```bash
# 仓库根目录运行，只查自己改过的 .go（新文件先 git add）；有输出先格式化。不要对整个 backend 跑，见 docs/ai/README.md
git diff --name-only --diff-filter=AM origin/main -- '*.go' | xargs -r gofmt -l
pnpm --dir frontend run typecheck                  # 改了前端时
pnpm --dir frontend run lint:check
pnpm --dir frontend exec vitest run <相关 spec>
make secret-scan
```

## 6. 开 PR

1. commit 说清「为什么」，只 push 自己的分支，**绝不 push main**。
2. PR 描述写：现象、根因（含证据）、修复、测试、风险与回滚、部署注意点（迁移、配置、是否需要重启）。
3. 看 CI：区分本分支引入的失败和 baseline 既有失败；编译 / lint 错误正常迭代，
   测试逻辑反复失败就停下来交给人看。
4. 合并交给人。

## 7. 真实环境端到端验证

1. 按 `ops/skills/deploy/SKILL.md` 部署到 `<target_env>`（每一步等人确认）。
2. 走一遍真实用户路径：用真实请求或真实页面复现原问题，确认结果正确，
   再用数据库里的实际数据佐证（例如 `usage_logs` 新行、余额变化）。
3. 验证需要临时改配置时：先查出并记录原值 -> 修改 -> 实测 -> 清理测试数据 -> 还原原值，
   把每一步的前后对比写进报告。
4. 验证前后用同一组探测做对照；测试产生的用户、订单、流水用完清理。
5. 最终汇报：根因、改动（PR 链接）、验证证据（请求、响应、数据库行）、遗留风险。
