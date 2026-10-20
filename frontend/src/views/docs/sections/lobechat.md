# LobeChat（LobeHub）

[LobeHub](https://lobehub.com) 是开源的 AI 聊天框架，旧名 LobeChat，界面和文档里两个名字都可能出现。它按 OpenAI 格式接入，地址带 `/v1`。

## 在网页里配置 {#lobechat-web}

1. 打开设置，进入「AI 服务商」，选 OpenAI。较旧的版本里这一项叫「语言模型」。
2. API Key 填你的密钥。
3. API 代理地址填 `{{v1}}`。
4. 点「检查」按钮，通过后在模型列表里启用要用的模型。
5. 新建会话，选刚启用的模型，发一句话验证。

## 自部署时用环境变量 {#lobechat-env}

自己用 Docker 部署的话，可以在启动时把密钥和地址写进环境变量，所有用户打开就能用：

```bash title="docker run"
docker run -d -p 3210:3210 \
  -e OPENAI_API_KEY=sk-你的密钥 \
  -e OPENAI_PROXY_URL={{v1}} \
  lobehub/lobe-chat
```

| 变量 | 填什么 |
|---|---|
| `OPENAI_API_KEY` | 你的密钥 |
| `OPENAI_PROXY_URL` | `{{v1}}` |
| `OPENAI_MODEL_LIST` | 可选。用 `+模型名` 添加、`-模型名` 隐藏，逗号分隔 |

用 Docker Compose 或其他方式部署时，把这几个变量加到服务的环境变量里即可。

> 环境变量写在服务端，部署的人能看到密钥。多人共用的部署要注意别让无关的人访问。

## 常见问题 {#lobechat-faq}

- 检查失败或对话没有回复：先确认地址带了 `/v1`；仍不行就去掉 `/v1` 再试。
- 模型列表里没有想要的模型：能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。可以在 `OPENAI_MODEL_LIST` 或模型列表里手动添加。
