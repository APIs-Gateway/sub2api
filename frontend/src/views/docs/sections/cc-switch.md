# CC Switch

[CC Switch](https://github.com/farion1231/cc-switch) 是一个桌面工具，用来管理多个 API 配置，在 Claude Code、Codex 等工具之间一键切换。把 {{site}} 加进去有两种方式。

## 从密钥页导入（推荐） {#cc-switch-import}

1. 先安装 CC Switch。
2. 打开「API 密钥」页，点密钥那一行的「接入」。
3. 选 CC Switch 页签，点「打开 CC Switch」。也可以点旁边的「复制导入链接」，把链接粘贴到浏览器地址栏打开。
4. 浏览器问你是否打开 CC Switch 时，选允许。

导入后，CC Switch 里会多出一个 {{site}} 的配置。之后想换回别的配置，在 CC Switch 里点一下就行。

如果点了没反应，通常是 CC Switch 没装，或者系统没关联它的链接。装好后重试，或者点「复制导入链接」，把链接粘贴到浏览器地址栏打开。

## 手动添加 {#cc-switch-manual}

在 CC Switch 里新建配置，按下表填写：

| 工具 | 地址 | 密钥 |
|---|---|---|
| Claude Code | `{{base}}` | 你的密钥，`sk-` 开头 |
| Codex | `{{v1}}` | 你的密钥，`sk-` 开头 |

> 地址结尾不要加 `/`。CC Switch 对末尾的斜杠敏感，多一个会导致 401 或连接失败。

Codex 的接口类型选 `responses`。
