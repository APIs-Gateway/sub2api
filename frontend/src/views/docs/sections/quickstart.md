# 快速开始

接入 {{site}} 要三步，用什么工具都一样。

1. 注册并登录 {{site}}。
2. 打开「API 密钥」页，创建一个密钥。密钥以 `sk-` 开头，点复制保存好，不要发给别人。
3. 在你的工具里填入 API 地址和密钥。各工具的填法在文档里对应的工具章节，翻到你用的工具名那一节就行，清单见下文「找到你用的工具」。

## 最省事的做法 {#quickstart-easy}

在「API 密钥」页，点密钥那一行的「接入」。弹窗里有「一键安装」「交给 AI」「CC Switch」「手动配置」四个页签，地址和密钥已经替你填好。选一种照着做就行。

如果你想自己动手，或者弹窗里没有你用的工具，继续往下看。

## 先选对分组 {#quickstart-group}

创建密钥时要选一个分组。分组决定这个密钥能用哪些模型，也决定单价。不确定选哪个，先打开「价格与计费」页看各分组的模型和价格。

> 密钥创建后可以在「API 密钥」页改分组。改完立即生效，不用重新填到工具里。

## 地址和密钥怎么填 {#quickstart-fill}

大多数工具只有两项要填：API 地址和密钥。

| 配置项 | 填什么 |
|---|---|
| API 地址（也叫 Base URL、API Base） | 大多数工具填 `{{v1}}`；Claude Code 这类 Anthropic 格式的工具填 `{{base}}`，不带 `/v1` |
| API Key（也叫密钥、Token） | 你的密钥，`sk-` 开头 |
| 模型 | 「价格与计费」页上列出的模型名，例如 `{{model}}` |

有的工具会自己在地址后面补 `/v1`，这时只填 `{{base}}`。具体写法以对应工具那一节为准。

## 找到你用的工具 {#quickstart-tools}

文档里按工具分了章节，找到你用的那一个：

| 类型 | 工具 |
|---|---|
| 命令行和编程工具 | Claude Code、Codex CLI 与 Codex Desktop、Gemini CLI、Aider、OpenCode |
| 编辑器和插件 | Cursor、Kiro、Windsurf、Cline、Roo Code、Continue |
| 聊天客户端 | Cherry Studio、Chatbox、Open WebUI、LobeChat（LobeHub）、NextChat |
| 翻译 | 沉浸式翻译 |
| 自己写代码 | 在代码里调用、API 调用示例 |

不在清单里的工具，只要支持「OpenAI 兼容」的自定义服务商，就按「其他兼容 OpenAI 的客户端」一节填。一键切换多套配置可以用 CC Switch，见「CC Switch」一节。

> Gemini CLI 用的是 Gemini 原生格式，只有 Gemini 分组的密钥能用。

## 验证 {#quickstart-verify}

填完后，先在工具里发一句话试试。想单独确认密钥和地址没问题，可以在终端运行：

```bash
curl {{base}}/v1/models -H "Authorization: Bearer sk-你的密钥"
```

返回一段含模型名的内容，就说明地址和密钥都对。出错时看「错误排查」一节，里面按状态码和提示文字列了原因和处理办法。
