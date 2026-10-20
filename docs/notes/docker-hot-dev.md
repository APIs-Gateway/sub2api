# Docker 热开发约定

## 现象

本地频繁执行 `docker compose -f deploy/docker-compose.dev.yml up -d --build sub2api` 会很快占满 Docker 磁盘。

## 根因

`deploy/Dockerfile` 会在镜像构建阶段同时安装 pnpm 依赖、构建前端、下载 Go 模块并编译后端。每次完整镜像构建都会产生新的镜像层和中间层；旧层不会随着新镜像成功构建而立刻全部释放，Docker Desktop / Colima 的虚拟磁盘也不会自动缩小。

## 默认用法

高频开发默认不要重建应用镜像，改用：

```bash
make dev-local
```

这条命令会：

1. 用 Docker 启动真实 PostgreSQL 和 Redis。
2. 通过 `127.0.0.1:15432` 和 `127.0.0.1:16379` 连接真实依赖。
3. 停掉旧的 `sub2api-dev` 应用容器，避免端口被旧镜像占用。
4. 在宿主机执行 `pnpm --dir frontend run build` 和 `go run -tags embed ./cmd/server`，复用 pnpm / Go 的本地增量缓存。

只想先启动真实数据库时用：

```bash
make dev-db
```

## 什么时候才完整构建镜像

只有在验证 Dockerfile、镜像内容、最终部署形态或 CI 复现时，才运行完整镜像构建。

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.dev.yml up -d --build sub2api
```

如果完整构建多次导致磁盘压力，优先清理 dangling 层和已停止容器：

```bash
docker image prune
docker container prune
```

不要默认清理 volume；`deploy/postgres_data` 和 `deploy/redis_data` 是真实测试数据。
