# Cursor

Cursor 是一款 AI 代码编辑器。接入后，聊天、Agent 和 Cmd+K 都可以用你自己的密钥。

> Cursor 只在付费订阅里开放自定义 API 地址。免费版没有「Override OpenAI Base URL」这个开关，没法接入。在 Cursor 的账户页可以看订阅状态。

## 配置步骤 {#cursor-steps}

1. 打开 Cursor 的设置，进入 Cursor Settings，左侧选 Models。
2. 展开 API Keys，打开 OpenAI API Key 开关，粘贴你的密钥。注意不要带空格。
3. 打开 Override OpenAI Base URL，填 `{{v1}}`。这里必须带 `/v1`。
4. 在模型列表里添加自定义模型，名称填价格页上的模型名，确认开关是开着的。
5. 回到聊天面板，关掉顶部的 Auto，在模型下拉里选刚添加的模型，发一句话试试。

## 常见问题 {#cursor-trouble}

**提示 Rate Limit Exceeded。** Cursor 把很多错误都显示成这一句，多数时候请求根本没发出来。按顺序检查：地址有没有带 `/v1`，密钥是否复制完整，余额是否够用，密钥有没有被停用。

**提示模型名无效，或者找不到模型。** 确认模型名和价格页一字不差，并且已经在 Models 页里打开。聊天面板里还要关掉 Auto。

## 切回 Cursor 自带额度 {#cursor-revert}

在 Settings 的 Models 页：关掉 OpenAI API Key 开关，清空 Override OpenAI Base URL，删掉你添加的自定义模型，再打开 Auto。
