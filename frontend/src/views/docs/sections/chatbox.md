# Chatbox

[Chatbox](https://chatboxai.app) 是多平台 AI 客户端，支持 Windows、macOS、Linux、iOS、Android 和网页版。

## 配置步骤 {#chatbox-steps}

1. 打开设置，进入「模型提供方」，点底部的「添加」。
2. 名称随便填，比如 {{site}}；API 模式选「OpenAI API 兼容」。
3. API 域名（API Host）填 `{{base}}`，不带 `/v1`。
4. API 路径（API Path）保持默认的 `/v1/chat/completions`，不用改。
5. API 密钥填你的密钥。
6. 在模型列表里点「获取」，或手动添加一个模型名，比如 `{{model}}`。能用哪些模型取决于密钥所在的分组，具体名称见「价格与计费」页。
7. 点「检查」，提示连接成功后，新建对话，选刚添加的模型，发一句话验证。

## 常见问题 {#chatbox-faq}

**检查失败或报 404。** 把 API 域名改成 `{{v1}}`，API 路径改成 `/chat/completions`，再检查一次。域名里不要写完整的 `/chat/completions`。

**模型列表是空的。** 手动添加模型名即可，不影响使用。

## 切回原来的服务 {#chatbox-revert}

在「模型提供方」里关掉这个提供方，或者直接删除。
