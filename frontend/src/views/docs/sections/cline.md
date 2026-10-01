# Cline

[Cline](https://cline.bot) 是 VS Code 里的 AI 编程扩展。它有两种接法，推荐用 OpenAI 兼容方式。

## 用 OpenAI Compatible 接入（推荐） {#cline-openai}

1. 在 VS Code 里打开 Cline 面板，点右上角的设置图标（⚙️）。
2. 「API Provider」选 `OpenAI Compatible`。
3. 「Base URL」填 `{{v1}}`。
4. 「API Key」填你的密钥。
5. 「Model」填模型名，比如 `{{model}}`。模型名以「价格与计费」页为准。

| 配置项 | 填什么 |
|---|---|
| API Provider | OpenAI Compatible |
| Base URL | `{{v1}}` |
| API Key | `sk-你的密钥` |
| Model | `{{model}}` |

注意选的是 `OpenAI Compatible`，不是下拉里的 `OpenAI`。

## 用 Anthropic 格式接入 {#cline-anthropic}

是否可用取决于密钥所在的分组，不行就改用上面的 OpenAI Compatible 方式。

1. 设置里「API Provider」选 `Anthropic`。
2. 「Anthropic API Key」填你的密钥。
3. 勾选「Use custom base URL」，填 `{{base}}`，不带 `/v1`。
4. 在「Model」里选模型。

## 验证 {#cline-verify}

保存设置后，在 Cline 对话框里随便发一句话，能正常回复就接入成功。

## 常见问题 {#cline-faq}

- 返回 404：OpenAI Compatible 的地址要带 `/v1`，Anthropic 的不带。检查是不是填反了。
- 返回 401：密钥填错或多了空格。
- 返回 403：密钥所在的分组不支持这种调用，改用 OpenAI Compatible 方式，或在「API 密钥」页给密钥换一个分组。
- 找不到想用的模型：Anthropic 下拉里的模型名是固定的，需要别的模型就改用 OpenAI Compatible，手动填模型名。

## 切回原来的服务 {#cline-revert}

在设置里把「API Provider」换回原来的服务商，并填回对应的密钥即可。
