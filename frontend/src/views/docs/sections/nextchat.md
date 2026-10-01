# NextChat

[NextChat](https://github.com/ChatGPTNextWeb/NextChat)（原 ChatGPT Next Web）是开源的网页聊天客户端，也有桌面版，可以自己部署。它走 OpenAI 格式的接口，地址不带 `/v1`，NextChat 会自己补上。

## 在设置里配置 {#nextchat-settings}

适用于网页版和桌面版：

1. 打开左下角「设置」。
2. 打开「自定义接口」开关。
3. 「模型服务商」选 OpenAI。
4. 「接口地址」填 `{{base}}`，必须带 `https://`，末尾不要加 `/v1`。
5. 「API Key」填你的密钥。
6. 在「自定义模型名」里填要用的模型名，多个用英文逗号隔开，比如 `{{model}}`。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。
7. 回到对话页，在模型选择里选刚添加的模型。

> 用别人部署的 NextChat 网页时，密钥只保存在你自己的浏览器里。公共电脑上用完记得清除。

## 自己部署时用环境变量 {#nextchat-env}

自己部署 NextChat，可以把接口地址和密钥写进环境变量，这样打开页面就能直接用，不用每人再填一遍。

| 变量 | 填什么 |
|---|---|
| `BASE_URL` | `{{base}}`，不带 `/v1` |
| `OPENAI_API_KEY` | 你的密钥，`sk-` 开头 |
| `CODE` | 访问密码，建议设置，多个用英文逗号隔开 |
| `CUSTOM_MODELS` | 可选，调整模型列表，写法见下 |

用 Docker 运行：

```bash title="docker run"
docker run -d -p 3000:3000 \
  -e BASE_URL={{base}} \
  -e OPENAI_API_KEY=sk-你的密钥 \
  -e CODE=你的访问密码 \
  -e CUSTOM_MODELS=-all,+{{model}} \
  yidadaa/chatgpt-next-web
```

`CUSTOM_MODELS` 的写法：`+模型名` 添加，`-模型名` 隐藏，`原名=显示名` 改显示名，`-all` 隐藏默认的全部模型。上面的例子是只保留 `{{model}}`。

> 没设置 `CODE` 的话，任何人打开你部署的页面都能消耗你的密钥额度。

## 验证 {#nextchat-verify}

新建对话，选好模型，发一句话，能正常回复就接入成功。

## 常见问题 {#nextchat-faq}

- 404：接口地址末尾多写了 `/v1`，去掉再试。
- 401：密钥没复制完整，或已被删除。
- 403 或提示模型不可用：模型名写错，或密钥所在的分组没有这个模型，到「API 密钥」页换分组。
- 模型选择里没有想用的模型：在「自定义模型名」里手动添加；自部署的看 `CUSTOM_MODELS` 有没有把它隐藏。
- 修改环境变量后没生效：重新部署或重启容器。

## 切回原来的服务 {#nextchat-revert}

在设置里关掉「自定义接口」即可。自部署的，去掉 `BASE_URL` 和 `OPENAI_API_KEY` 后重新部署。
