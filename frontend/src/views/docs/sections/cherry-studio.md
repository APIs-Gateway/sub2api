# Cherry Studio

[Cherry Studio](https://cherry-ai.com) 是多模型桌面聊天客户端，支持 Windows、macOS 和 Linux。

## 配置步骤 {#cherry-studio-steps}

1. 打开设置，进入「模型服务」，点「添加」。
2. 提供商类型选 OpenAI，名称随便填，比如 {{site}}。
3. API 地址填 `{{base}}`。Cherry Studio 会自己补上 `/v1`，这里不用再加。
4. API 密钥填你的密钥。
5. 点「管理」或「获取模型」，选要用的模型并启用。
6. 新建对话，选刚启用的模型，发一句话验证。

想用 Anthropic 格式的话，再添加一个类型为 Anthropic 的提供商，地址和密钥都一样。是否可用取决于密钥所在的分组。

## 切回其他服务 {#cherry-studio-revert}

在「模型服务」里关掉这个提供商，或者直接删除。
