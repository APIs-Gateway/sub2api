# API 地址与鉴权

这一节列出填配置时会用到的地址和规则。只想照着教程配的话，可以跳过。

## 地址 {#api-info-urls}

| 用途 | 地址 |
|---|---|
| OpenAI 兼容客户端（OpenAI SDK、Codex、Cursor 等） | `{{v1}}` |
| Anthropic 兼容客户端（Claude Code 等） | `{{base}}` |

两个地址指向同一个服务，用同一个密钥。区别只是有没有 `/v1`：OpenAI 兼容客户端要带，Anthropic 兼容客户端不带，它们会自己补上路径。

## 鉴权方式 {#api-info-auth}

在请求头里带上密钥，三种写法任选其一：

- `Authorization: Bearer sk-你的密钥`，OpenAI SDK 默认用这种。
- `x-api-key: sk-你的密钥`，Anthropic SDK 默认用这种。
- `x-goog-api-key: sk-你的密钥`，Gemini 客户端用这种。

不要把密钥放进网址参数。在 `/v1` 接口上，带 `?key=` 的请求会返回 400。

## 验证密钥可用 {#api-info-verify}

打开终端（macOS 的「终端」，Windows 的 PowerShell），运行：

```bash title="macOS / Linux"
curl {{v1}}/models \
  -H "Authorization: Bearer sk-你的密钥"
```

```powershell title="Windows PowerShell"
curl.exe {{v1}}/models -H "Authorization: Bearer sk-你的密钥"
```

返回一段 JSON，里面有模型名，说明密钥和地址都没问题。返回错误的话，看「错误排查」一节。
