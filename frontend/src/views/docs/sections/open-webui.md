# Open WebUI

[Open WebUI](https://openwebui.com) 是可以自己部署的 AI 聊天界面，通过 OpenAI 兼容的接口连接模型服务。

## 在界面里添加连接 {#open-webui-ui}

1. 用管理员账号登录 Open WebUI，打开管理员设置（Admin Settings），进入「Connections」（连接）。
2. 在「Manage OpenAI API Connections」一栏点 **+**（Add Connection）。
3. 连接类型选 External。
4. URL 填 `{{v1}}`。
5. API Key 填你的密钥。
6. 保存。回到聊天页，模型列表里就能看到这个密钥所在分组可用的模型。

> 较早的版本里入口是：右上角头像，「管理员面板」，「设置」，「连接」，字段名为「OpenAI API 基础 URL」和「API 密钥」，填的内容一样。

## 用环境变量部署 {#open-webui-env}

Docker 部署时，启动容器时带上两个环境变量：

```bash title="docker run"
docker run -d -p 3000:8080 \
  -e OPENAI_API_BASE_URL={{v1}} \
  -e OPENAI_API_KEY=sk-你的密钥 \
  -v open-webui:/app/backend/data \
  --name open-webui \
  ghcr.io/open-webui/open-webui:main
```

用 Docker Compose 的话，写在 `environment` 里：

```yaml title="docker-compose.yml"
services:
  open-webui:
    image: ghcr.io/open-webui/open-webui:main
    ports:
      - "3000:8080"
    environment:
      - OPENAI_API_BASE_URL={{v1}}
      - OPENAI_API_KEY=sk-你的密钥
    volumes:
      - open-webui:/app/backend/data

volumes:
  open-webui:
```

> 环境变量只在 Open WebUI 第一次启动时生效，之后连接信息保存在数据里。已经启动过的实例，再改环境变量不会变化，请到「Connections」里修改。

## 验证 {#open-webui-verify}

新建对话，在顶部选一个模型（例如 `{{model}}`），发一句话，能正常回复就接入成功。

## 常见问题 {#open-webui-faq}

- **模型列表是空的**：确认地址带 `/v1`，密钥没有多余空格，并且密钥所在的分组有可用模型。模型名以「价格与计费」页为准。
