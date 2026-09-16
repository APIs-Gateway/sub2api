# 单机 Docker 部署的无损升级（蓝绿 + nginx 平滑 reload）

适用于「一台机器、nginx 反代、每个站一个 sub2api 容器」的部署形态。目标是升级镜像时
**不出现 502、不砍掉正在生成的流式请求**。

## 为什么不能直接 `docker compose up -d`

直接换镜像重建容器，每个站会有 10–20 秒的 502，而且正在生成的请求会被砍掉：

- sub2api 收到 SIGTERM 后 `app.Server.Shutdown(ctx)` 只等 **5 秒**（`backend/cmd/server/main.go`），
  compose 的 `stop_grace_period` 也只有 10 秒；Codex 一次响应动辄几分钟，等不完。
- 新容器要跑迁移、过 healthcheck 才能接流量，这段时间上游没有实例。

## 现有链路

每个站都是 nginx → session-recorder-proxy → sub2api 三层：

| 站 | nginx `proxy_pass` | recorder-proxy 监听 | sub2api 监听 |
|---|---|---|---|
| codex 站 | `127.0.0.1:18080`（少数 location 直连 `:8080`） | `:18080`，host 网络 | `:8080`，host 网络 |
| free 站 | `127.0.0.1:18081`（少数 location 直连 `:8081`） | `:18081`，host 网络 | `:8081`，docker-proxy 映射到容器 8080 |

recorder-proxy 的上游写死在 `/etc/default/session-recorder-proxy-<站>` 的
`SESSION_RECORDER_UPSTREAM_URL` 里，改它必须重启 proxy，同样会断流。
所以**切换点只能放在 nginx**：`nginx -s reload` 是平滑的，老 worker 把手里的连接
（含正在流式输出的）跑完才退出，新连接全部走新配置。

## 步骤（逐站做，先 free 后 codex）

约定：老实例叫「蓝」，新实例叫「绿」。绿实例端口在老端口上 +10：
sub2api `8080→8090` / `8081→8091`，recorder-proxy `18080→18090` / `18081→18091`。

### 0. 改前探测（留底）

对老实例和公网域名各打一遍，输出存成 `probe-before.txt`：

```bash
for p in /health "/v1/responses" "/v1/models?client_version=0.1.0" "/v1/usage" /login; do
  printf '%-40s %s\n' "$p" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' \
    -H 'Authorization: Bearer sk-invalid' "http://127.0.0.1:8080$p")"
done
```

预期：`/health` 200，网关路径 401（说明打到后端而不是 SPA），`/login` 200。

### 1. 起绿实例（对线上零影响）

绿 sub2api 用同一个库、同一个 data 目录，只是换端口；绿 recorder-proxy 指向它，
`SESSION_RECORDER_URL` 仍是同一个 recorder，录制不受影响。

- codex 站（host 网络）：复制 compose 里的 `sub2api` 服务为 `sub2api-green`，
  镜像换新，`environment` 加 `SERVER_PORT=8090`；复制 `session-recorder-proxy-sub2api`
  为 `-green`，env 文件另存一份，只改 `PROXY_ADDR=127.0.0.1:18090`、
  `UPSTREAM_URL=http://127.0.0.1:8090`。
- free 站（端口映射）：同上，`ports` 改成 `127.0.0.1:8091:8080`。

```bash
docker compose up -d sub2api-green session-recorder-proxy-green
until curl -fsS http://127.0.0.1:8090/health >/dev/null; do sleep 2; done
```

绿实例启动时会把新版本的迁移跑掉。**升级前先确认新增迁移都是加列/加表这类
向后兼容的写法**（`ADD COLUMN IF NOT EXISTS` 等），蓝实例带着多出来的列继续跑没有问题；
有 `DROP`/`RENAME` 的迁移不能走蓝绿，要单独安排停机窗口。

### 2. 改后探测（打绿实例）

用第 0 步同一组探测打 `:8090`，和留底逐行对比。**只允许版本号不同**；其他任何差异
都视为回滚条件，直接停掉绿实例，线上什么都没变。

### 3. nginx 切流

```bash
ts=$(date -u +%Y%m%dT%H%M%SZ)
cp /etc/nginx/sites-enabled/<站>.conf /etc/nginx/sites-enabled/<站>.conf.bak-before-<版本>-$ts
sed -i 's/127\.0\.0\.1:18080/127.0.0.1:18090/g; s/127\.0\.0\.1:8080/127.0.0.1:8090/g' \
  /etc/nginx/sites-enabled/<站>.conf
nginx -t && nginx -s reload
```

只改这一个站的 server 块里的端口，其他站的 location、限流、日志一概不碰。
reload 后再打一遍公网域名的探测，应与留底一致。

### 4. 等蓝实例排空再停

```bash
watch -n 5 "ss -tn state established '( sport = :8080 )' | tail -n +2 | wc -l"
```

降到 0（或只剩 keepalive 空闲连接）后：

```bash
docker compose stop sub2api session-recorder-proxy-sub2api
```

健康判据只看 `/health`、容器不反复重启、错误计数不涨；**不要用请求量判断**，
低峰期本来就没请求。

### 5. 收尾

下次升级前把 compose 里的蓝绿角色对调（或把绿实例改回标准端口后再走一遍 3–4 步），
避免端口号越漂越远。

## 回滚

蓝实例在第 4 步之前一直活着，所以回滚只有一条命令：

```bash
cp /etc/nginx/sites-enabled/<站>.conf.bak-before-<版本>-<ts> /etc/nginx/sites-enabled/<站>.conf \
  && nginx -t && nginx -s reload
```

秒级切回，之后再停绿实例。

## 不在本方案范围内

- 把 recorder-proxy 的上游改成可热切换（就不用在 nginx 层绕了）。
- 给 sub2api 加更长的优雅退出时间（让普通重启也能等完流式请求）。

这两项能让以后的升级更省事，但改的是别的东西，单独提。
