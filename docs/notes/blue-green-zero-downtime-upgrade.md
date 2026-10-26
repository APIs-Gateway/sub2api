# 单机 Docker 部署的无损升级（蓝绿 + nginx 平滑 reload）

适用于「一台机器、nginx 反代、每个站一个 sub2api 容器」的部署形态。目标是升级镜像时
**不出现 502、不砍掉正在生成的流式请求**。

> 本文只写通用方法。站点相关的值（端口、目录、compose 服务名、域名、中间代理的环境文件）
> 一律写成 `<占位符>`，真实值见私有运维仓。

## 占位符

| 占位符 | 含义 |
|---|---|
| `<site_conf>` | 该站的 nginx 站点配置文件（含 `proxy_pass` 的那个 server 块所在文件） |
| `<blue_port>` / `<green_port>` | sub2api 蓝/绿实例的监听端口，约定 `<green_port>` = `<blue_port>` + 10 |
| `<proxy_blue_port>` / `<proxy_green_port>` | 中间代理层（如有）蓝/绿实例的监听端口，同样 +10 |
| `<sub2api_service>` / `<sub2api_green_service>` | compose 里蓝/绿 sub2api 服务名 |
| `<proxy_service>` / `<proxy_green_service>` | compose 里蓝/绿中间代理服务名（没有中间代理则忽略） |
| `<deploy_dir>` | compose 文件和数据目录所在目录 |
| `<backup_dir>` | 备份目录，**不能**放在 nginx 会加载的目录里（含 `sites-enabled/`、`conf.d/` 这类 `include` 通配的目录） |
| `<version>` | 本次升级的版本标识，用于备份文件名 |

## 为什么不能直接 `docker compose up -d`

直接换镜像重建容器，每个站会有十几秒的 502，而且正在生成的请求会被砍掉：

- sub2api 收到 SIGTERM 后 `app.Server.Shutdown(ctx)` 只等 **5 秒**（`backend/cmd/server/main.go`），
  `deploy/docker-compose.yml` 没有设置 `stop_grace_period`，沿用 Docker 默认的 10 秒；
  Codex 一次响应动辄几分钟，等不完。
- 新容器要跑迁移、过 healthcheck 才能接流量，这段时间上游没有实例。

## 链路与切换点

典型链路是 nginx →（可选的中间代理层）→ sub2api。若中间代理的上游地址写死在它自己的
环境文件里，改它必须重启代理，同样会断流。所以**切换点只能放在 nginx**：
`nginx -s reload` 是平滑的，老 worker 把手里的连接（含正在流式输出的）跑完才退出，
新连接全部走新配置。

sub2api 可以是 host 网络（直接监听 `SERVER_PORT`），也可以是端口映射
（`ports: 127.0.0.1:<port>:8080`）。下面两种形态的区别只在第 1 步改哪个字段。

## 步骤（逐站做；有多个站时先做流量小的）

约定：老实例叫「蓝」，新实例叫「绿」。

### 0. 改前探测（留底）

对老实例和公网域名各打一遍，输出存成 `probe-before.txt`：

```bash
for p in /health "/v1/responses" "/v1/models?client_version=0.1.0" "/v1/usage" /login; do
  printf '%-40s %s\n' "$p" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' \
    -H 'Authorization: Bearer sk-invalid' "http://127.0.0.1:<blue_port>$p")"
done
```

预期：`/health` 200，网关路径 401（说明打到后端而不是 SPA），`/login` 200。

### 1. 起绿实例（对线上零影响）

绿 sub2api 用同一个库、同一个 data 目录，只是换端口；绿中间代理（如有）指向它，
其余环境变量（如录制/审计的后端地址）与蓝实例保持一致。

- host 网络：复制 compose 里的 `<sub2api_service>` 为 `<sub2api_green_service>`，
  镜像换新，`environment` 加 `SERVER_PORT=<green_port>`；中间代理同理复制一份，
  环境文件另存一份，只改监听地址和上游地址。
- 端口映射：同上，`ports` 改成 `127.0.0.1:<green_port>:8080`。

```bash
docker compose up -d <sub2api_green_service> <proxy_green_service>
until curl -fsS http://127.0.0.1:<green_port>/health >/dev/null; do sleep 2; done
```

绿实例启动时会把新版本的迁移跑掉。**升级前先确认新增迁移都是加列/加表这类
向后兼容的写法**（`ADD COLUMN IF NOT EXISTS` 等），蓝实例带着多出来的列继续跑没有问题；
有 `DROP`/`RENAME` 的迁移不能走蓝绿，要单独安排停机窗口。

### 2. 改后探测（打绿实例）

用第 0 步同一组探测打 `<green_port>`，和留底逐行对比。**只允许版本号不同**；其他任何差异
都视为回滚条件，直接停掉绿实例，线上什么都没变。

### 3. nginx 切流

切流前确认已经保留上一版前端 assets（见 `ops/skills/deploy`），否则切流后部署前就开着的
旧标签页会因旧 chunk 404 而白屏。

```bash
ts=$(date -u +%Y%m%dT%H%M%SZ)
cp <site_conf> <backup_dir>/$(basename <site_conf>).bak-before-<version>-$ts
# 只改这个站的 server 块里的端口；用编辑器或有备份的 sed 改，改完必须目视 diff
sed -i 's/127\.0\.0\.1:<proxy_blue_port>/127.0.0.1:<proxy_green_port>/g; s/127\.0\.0\.1:<blue_port>/127.0.0.1:<green_port>/g' <site_conf>
nginx -t && nginx -s reload
```

备份文件**不要**留在 nginx 会加载的目录里（`sites-enabled/` 下的 `.bak*` 如果被 include
通配到，会被当成配置加载），所以上面备份写到 `<backup_dir>`，回滚也从那里恢复。
其他站的 location、限流、日志一概不碰。
reload 后再打一遍公网域名的探测，应与留底一致。

### 4. 等蓝实例排空再停

```bash
watch -n 5 "ss -tn state established '( sport = :<blue_port> )' | tail -n +2 | wc -l"
```

降到 0（或只剩 keepalive 空闲连接）后：

```bash
docker compose stop <sub2api_service> <proxy_service>
```

健康判据只看 `/health`、容器不反复重启、错误计数不涨；**不要用请求量判断**，
低峰期本来就没请求。

### 5. 收尾

下次升级前把 compose 里的蓝绿角色对调（或把绿实例改回标准端口后再走一遍 3–4 步），
避免端口号越漂越远。

## 回滚

蓝实例在第 4 步之前一直活着，所以回滚只有一条命令：

```bash
cp <backup_dir>/$(basename <site_conf>).bak-before-<version>-<ts> <site_conf> && nginx -t && nginx -s reload
```

秒级切回，之后再停绿实例。

## 不在本方案范围内

- 把中间代理的上游改成可热切换（就不用在 nginx 层绕了）。
- 给 sub2api 加更长的优雅退出时间（让普通重启也能等完流式请求）。

这两项能让以后的升级更省事，但改的是别的东西，单独提。
