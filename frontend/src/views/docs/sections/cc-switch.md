# CC Switch

[CC Switch](https://github.com/farion1231/cc-switch) 是一个桌面工具，用来管理多个 API 配置，在 Claude Code、Codex、Gemini CLI 等工具之间一键切换。把 {{site}} 加进去有两种方式。

## 从密钥页导入（推荐） {#cc-switch-import}

1. 先安装 CC Switch。
2. 打开「API 密钥」页，点密钥那一行的「接入」。
3. 选 CC Switch 页签，点「打开 CC Switch」。也可以点旁边的「复制导入链接」，把链接粘贴到浏览器地址栏打开。
4. 浏览器问你是否打开 CC Switch 时，选允许。

导入后，CC Switch 里会多出一个 {{site}} 的配置。之后想换回别的配置，在 CC Switch 里点一下就行。

导入到哪个工具，由密钥所在的分组决定：Anthropic 分组导入到 Claude Code，OpenAI 分组导入到 Codex（默认模型是 `{{model}}`），Gemini 分组导入到 Gemini CLI。想在别的工具里用，用下面的手动添加。

导入的配置会带上用量查询，在 CC Switch 的这个配置上可以看到剩余额度。

如果点了没反应，通常是 CC Switch 没装，或者系统没关联它的链接。装好后重试，或者点「复制导入链接」，把链接粘贴到浏览器地址栏打开。

## 手动添加 {#cc-switch-manual}

在 CC Switch 里选好工具，新建配置，按下表填写：

| 工具 | 地址 | 密钥 |
|---|---|---|
| Claude Code | `{{base}}` | 你的密钥，`sk-` 开头 |
| Codex | `{{v1}}` | 你的密钥，`sk-` 开头 |
| Gemini CLI | `{{base}}` | 你的密钥，`sk-` 开头 |

> 地址结尾不要加 `/`。CC Switch 对末尾的斜杠敏感，多一个会导致 401 或连接失败。

Codex 的接口类型选 `responses`，模型填 `{{model}}` 或价格与计费页上列出的其他模型。Gemini CLI 只有 Gemini 分组的密钥能用。

## 验证 {#cc-switch-verify}

在 CC Switch 里点选 {{site}} 的配置使它生效，然后重新打开对应的终端或工具，发一句话试试。如果工具已经开着，需要重启一次才会读到新配置。

## 切回原来的配置 {#cc-switch-switch-back}

CC Switch 第一次启动时，会把你原有的配置保存成名为 default 的配置。想切回，在 CC Switch 里点选 default 即可。
