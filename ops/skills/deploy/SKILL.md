---
name: deploy
description: 把 sub2api 新版本发布到生产环境的标准流程：构建镜像 tag、迁移 dry-run、备份数据库、配置 diff、保留旧前端 assets、更新 compose、部署后验证计费非零、回滚预案。用户说「部署 / 发布 / 上线 / 升级 / 更新线上 / 编译替换」时使用。每一步都要人确认。
---

# Deploy 流程

站点相关的值一律写成占位符，**站点私有值见私有运维仓**：

| 占位符 | 含义 |
|---|---|
| `<ssh_target>` | 登录生产机的 SSH 目标 |
| `<deploy_dir>` | compose 文件、配置、数据目录所在目录 |
| `<compose_file>` / `<service>` / `<container>` | compose 文件、服务名、容器名 |
| `<image>` / `<old_tag>` / `<new_tag>` | 镜像名、现役 tag、新 tag |
| `<backup_dir>` | 备份目录，**不能**放在 nginx 或 compose 会扫描加载的目录里 |
| `<site_host>` | 各站点域名（有多个站点时逐个验证） |

**每一步执行前先说明：要做什么、影响面、怎么回滚；等人明确同意再执行。
一次确认不延续到下一步。** 不要自行合并 PR、重启服务、删除数据。

## 0. 前置

1. 要上线的 PR 已合并，CI 全绿，部署的 commit 和 PR 一致。
2. 记录当前线上版本：镜像 tag、commit、容器内二进制的 sha256。
   **线上跑的是镜像里的二进制**，判别方法：
   `docker exec <container> readlink -f /proc/1/exe`，再对照 compose 里的 `image:`。
   宿主机上残留的旧二进制和它的 `--version` 输出没有参考价值。

## 1. 构建镜像 tag

1. 从干净的 `origin/main`（新 worktree 或 clone）构建，避免带上本地脏改动。
2. 前端先构建，产物落到 `backend/internal/web/dist`，再 embed 进二进制。
   手工交叉编译**必须带 `-tags embed`**，否则二进制不含前端，整站返回
   `404 Frontend not embedded`。
   ```bash
   cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed -trimpath \
     -ldflags="-s -w -X main.Version=<version> -X main.Commit=<sha> -X main.Date=<utc> -X main.BuildType=source" \
     -o bin/server-linux-amd64 ./cmd/server
   ```
3. `BuildType` 会暴露给后台的版本/更新面板（`backend/internal/service/update_service.go`）。
   仓库 `Dockerfile` 写死 `release`；自建部署用 `source`，否则面板可能把发布源的新版本当成可更新版本。
4. 验证前端真的进了二进制：`strings bin/server-linux-amd64 | grep -c 'index-<hash>'`
   （`<hash>` 取自 `backend/internal/web/dist/index.html`）。
5. 构建镜像：用仓库 Dockerfile，或在现役镜像上只叠一层替换二进制
   （`FROM <image>:<old_tag>` + `COPY`，entrypoint、workdir、env 原样继承）。
6. **tag 用新名字**（如 `<version>-<用途>-<日期>`），不覆盖现役 tag，旧镜像不要删，它就是回滚路径。
7. 上传大文件用 `rsync --partial --inplace`，传完比对 sha256（本地、远端、镜像内三处）；
   工具超时被杀不代表没传完，先比字节数。

## 2. 迁移 dry-run

1. 列出本次新增迁移：`backend/migrations/*.sql` 与线上 `schema_migrations` 表按完整文件名对比。
   迁移在服务启动时自动执行；数字前缀重号是安全的（`docs/notes/db-migration-duplicate-numbers.md`）。
2. 逐个读新迁移，标出 `DROP`、`RENAME`、改数据的 `UPDATE`、加 `NOT NULL`、大表重写。
3. 在生产库的副本（或备份还原出的临时库）上先跑一遍，记录耗时、锁、受影响行数。
4. 破坏性或改钱的迁移不要混在普通发布里，单独安排窗口；蓝绿升级只允许向后兼容迁移。
5. 大表迁移前确认磁盘剩余空间足够，空间不足会写满磁盘并拖垮数据库。

## 3. 备份数据库

1. `pg_dump` 自定义格式，文件名带时间戳，存到 `<backup_dir>`。
2. 记录大小和 sha256，`pg_restore --list` 确认备份可读。
3. 备份完成并核对后，才进入下一步。

## 4. 配置 diff

1. `git diff <旧commit>..<新commit> -- deploy/config.example.yaml backend/internal/config/` 列出新增、改名、移除的配置项。
2. 对照线上 `<deploy_dir>` 下的配置文件和 compose，决定哪些要改。
3. 只有配置文件 / 环境变量来源的开关，改完必须重启才生效。
4. 改之前备份原文件；密钥不要贴进聊天、日志或 PR。

## 5. 保留旧前端 assets

前端文件名带 hash，换版必换 hash。部署前就开着页面的标签页还会去请求旧 chunk，
旧 chunk 随旧版本消失 -> 404 -> 白屏。
`backend/internal/web/embed_on.go` 的 `tryServeOverride` 会先查覆盖目录 `data/public`
（容器内 `/app/data/public`，对应宿主机 `<deploy_dir>/public`）。

1. 换版**之前**，把当前线上版本的 `dist/assets/*` 放进 `<deploy_dir>/public/assets/`，让新旧 hash 并存。
2. 缺失的 hash 资源后端返回 `no-store`，事后补文件也立即生效。
3. 探活 chunk 时，提取文件名的正则不能强制以点开头（vite 会产出名字中间带点的文件），
   否则会误报「N 个 chunk 缺失」；下结论前先去 `dist/assets` 里 `ls` 核对。

## 6. 更新 compose

1. **替换宿主机上的二进制文件无效**：命令全成功、容器 healthy，线上行为却不变。
2. 备份 `<compose_file>` 到 `<backup_dir>`，把 `image:` 改成 `<new_tag>`，
   `docker compose up -d <service>`。
3. 确认运行的是新二进制：`docker exec <container> /app/sub2api --version`（输出版本、commit、构建时间），
   `docker exec <container> sha256sum /app/sub2api` 与构建产物一致。
4. 需要零停机时，改走 `docs/notes/blue-green-zero-downtime-upgrade.md`。

## 7. 部署后验证（真实请求，计费非零）

1. 健康：`/health` 返回 200，容器不反复重启，日志无新增 ERROR。
2. 用测试 key 对每条主要协议路径各发一个真实请求，确认响应正常。
3. 查 `usage_logs` 新增行：`total_cost`、`actual_cost` 大于 0（免费分组或零价格模型除外），
   余额或订阅用量随之变化。**零计费是严重故障，立刻按回滚条件处理。**
4. 对每个 `<site_host>` 分别用 Host 头请求，防止站点串错；打开页面确认前端资源无 404。
5. 验证产生的测试数据用完清理。

## 8. 回滚预案（部署前写好并让人确认）

- 触发条件：`/health` 失败、计费为零、错误率明显升高、前端大面积 404。
- 镜像回滚：`<compose_file>` 的 `image:` 改回 `<old_tag>`，`docker compose up -d <service>`。
- 配置回滚：恢复第 4 步备份的配置文件。
- 数据回滚：迁移若向后兼容，旧版本可直接运行；否则从第 3 步备份还原，
  还原会丢失部署后写入的数据，必须由人决定。
- 旧前端 assets 不用清理。

## 9. 确认节奏

第 1 到第 8 步逐步确认：每一步前复述要执行的命令和回滚办法，得到明确同意再做，做完汇报结果再进入下一步。
涉及价格、删除数据、迁移的步骤额外确认。
