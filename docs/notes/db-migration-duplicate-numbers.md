# DB 迁移重号约定

`backend/migrations/` 里**重复的数字前缀是有意为之、且安全**（如 `142_stable_priority.sql` / `142_subscription_burndown_model.sql` / `142_user_platform_quotas.sql`）。重号在本仓历史里很普遍，是不同功能分支各自用「下一个号 + 描述后缀」的既定约定。

执行器 `backend/internal/repository/migrations_runner.go`：

- `schema_migrations.filename TEXT PRIMARY KEY` —— 按**完整文件名**跟踪，每个迁移独立记录、各跑一次，重号不会互相判重/跳过。
- `fs.Glob("*.sql")` + `sort.Strings` —— 按**完整文件名字典序**执行，顺序确定可复现。
- 每个迁移有 SHA256 防篡改：已应用的文件内容若变动会拒绝启动。

## 为什么

看到同号迁移容易误判为 bug 去「修」，但跟踪键是文件名不是号；真正要保证的是**有依赖的迁移其文件名字典序正确**（例：`142_subscription_burndown_model` 加列 → 必须早于 `143_subscription_burndown_backfill` 回填，142<143 已满足）。

## 怎么用

新增迁移只需保证文件名排序落在依赖之后即可，重号无所谓。**绝不要重命名已存在的迁移文件**去「连续化编号」—— 改名会被当成新迁移在已应用环境重跑，旧记录变孤儿，有害。
