# Cherry Studio

[Cherry Studio](https://cherry-ai.com) 是多模型桌面聊天客户端，支持 Windows、macOS 和 Linux。

## 配置步骤 {#cherry-studio-steps}

1. 点左侧栏的「设置」（齿轮图标），进入「模型服务」，点列表下方的「+ 添加服务商」。
2. 提供商名称随便填，比如 {{site}}。端点选 OpenAI，API 密钥填你的密钥，点「添加」。
3. 在新建的服务商页面，API 地址填 `{{base}}`。Cherry Studio 会自己补上 `/v1/chat/completions`，这里不用再加 `/v1`。
4. 点「获取模型列表」，在弹出的列表里点要用的模型右侧的 `+`。拉不到列表时，也可以点「+」手动填模型名，模型名以「价格与计费」页为准。
5. 打开服务商页面右上角的启用开关，否则模型不会出现在选择列表里。
6. 点「检测」，选一个模型，确认连接成功。
7. 新建对话，选刚添加的模型，发一句话验证。

## 使用 Anthropic 格式 {#cherry-studio-anthropic}

想用 Anthropic 格式的话，在同一个服务商里添加 Anthropic 端点（添加时的端点设置，或之后在服务商页面的「更多设置」里），或者另建一个服务商，地址和密钥都一样，地址填 `{{base}}`。Cherry Studio 会自己补上 `/v1/messages`。是否可用取决于密钥所在的分组，不行就改用上面的 OpenAI 方式。

## 常见问题 {#cherry-studio-faq}

- **报 404**：先确认 API 地址没有多写 `/v1`，也没有带 `/chat/completions` 这类完整路径。
- **想自己控制完整路径**：在地址末尾加 `#`，Cherry Studio 就不再自动拼接路径，只用你填的地址。
- **模型选择器里没有模型**：检查模型有没有用 `+` 加进列表，以及服务商右上角的开关是否已打开。

## 切回原来的服务 {#cherry-studio-revert}

在「模型服务」里关掉这个提供商，或者直接删除。
