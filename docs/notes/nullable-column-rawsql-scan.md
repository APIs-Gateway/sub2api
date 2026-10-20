# 可空列原始 SQL 扫描陷阱

迁移把一个原本 `NOT NULL` 的列改成可空后（本仓案例：`group_id` 在迁移 164 后可空，「无分组卡」该列为 NULL），**所有用原始 SQL `Scan` 到非空 Go 类型（`int64`/`string` 等）的读取路径都会在 NULL 行上崩**：`sql: Scan error ... converting NULL to int64 is unsupported`。

这是 P0 级资损隐患：结算 / 计费路径若走原始 SQL 且没改扫描类型，遇到无分组卡直接 panic / 报错，请求失败。

## 怎么修

- 凡是 `database/sql` 原始 SQL 读可空列，扫描目标改用 `sql.NullInt64` / `sql.NullString` 等，再判 `.Valid` 取值。
- ent 生成代码本身对可空列用指针类型，安全；**只有手写原始 SQL 才有此坑**。改可空列时，全局 grep 该列名的原始 SQL 读取点逐个排查（本仓重点：`user_subscriptions` 相关原始 SQL）。

## 集成测试如何复现

可空列 + NULL 行的崩溃**纯单测抓不到**，必须真实 PostgreSQL：

- 集成测试只在 `backend/internal/repository` 下，用 `-tags integration`，testcontainers 自起 pg + redis。
- 跑之前设两个环境变量：把 `DOCKER_HOST` 指向你本机的 Docker socket（用 colima 等非 Docker Desktop 时尤其要设），并 `export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock`，否则 testcontainers 连不上或 ryuk 起不来。
- 用例里要专门插一行该可空列为 NULL 的数据，覆盖结算 / 计费读取路径。
