# Roo Code

[Roo Code](https://roocode.com) 是 AI 编程扩展，可以装在 VS Code、Cursor 和 Windsurf 里，配置界面完全一样。

## 安装 {#roo-code-install}

1. 打开编辑器的扩展面板，搜索 Roo Code（发布者 RooVeterinaryInc），点安装。
2. VS Code 和 Cursor 一般直接能搜到。Windsurf 等用 Open VSX 扩展库的编辑器，同样在扩展面板里搜 Roo Code 即可。
3. 装完后，编辑器侧边栏会出现 Roo Code 图标。

## 用 OpenAI 兼容方式接入（推荐） {#roo-code-openai}

1. 点开 Roo Code 面板，点右上角的齿轮图标进入设置。
2. API Provider 选 OpenAI Compatible。
3. Base URL 填 `{{v1}}`，这里要带 `/v1`。
4. API Key 填你的密钥。
5. Model 填价格页上的模型名，比如 `{{model}}`。
6. 点 Save 或 Done 保存。

> 选的模型需要支持工具调用，否则 Roo Code 没法读写文件、执行命令。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

## 用 Anthropic 格式接入 {#roo-code-anthropic}

是否可用取决于密钥所在的分组，不行就改用上面的 OpenAI 兼容方式。想用 Anthropic 格式时：

1. API Provider 选 Anthropic。
2. 在 Anthropic API Key 里填你的密钥。
3. 勾选 Use custom base URL，在 Custom Base URL 里填 `{{base}}`，不带 `/v1`。
4. 模型选择以界面实际显示的为准。下拉里没有想用的模型时，换成上面的 OpenAI Compatible 方式，直接手填模型名。

密钥所在的分组不支持时，请求会返回 403，这时改用 OpenAI 兼容方式，或在「API 密钥」页给密钥换一个分组。

## 在 Windsurf 里使用 {#roo-code-windsurf}

Windsurf 里的 Roo Code 和 VS Code、Cursor 里是同一个扩展，配置步骤和上面完全一致：从扩展面板装好 Roo Code，再按上面的 OpenAI Compatible 方式填写即可。

## 验证 {#roo-code-verify}

在 Roo Code 面板里发一句话，能正常回复就接入成功。

## 常见问题 {#roo-code-trouble}

**提示 404。** 先检查 Base URL：OpenAI Compatible 要带 `/v1`，Anthropic 自定义地址不带 `/v1`。

**提示模型不存在。** 确认 Model 和价格页上的名称一字不差，并且密钥所在分组有这个模型。

**提示 401。** 密钥没复制完整，或者已被删除，到「API 密钥」页核对。

## 切回原来的服务 {#roo-code-revert}

回到 Roo Code 设置，把 API Provider 换回原来用的那个服务，并填回对应的密钥即可。
