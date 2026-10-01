# Cursor

Cursor 是一款 AI 代码编辑器。接入后，聊天可以用你自己的密钥。

> 自定义 API 地址属于 Cursor 的「自带密钥」功能，不同版本和订阅下是否显示「Override OpenAI Base URL」开关并不一致。如果在 Models 页找不到这个开关，说明你当前的 Cursor 版本或订阅不支持，没法接入。在 Cursor 的账户页可以看订阅状态。

## 配置步骤 {#cursor-steps}

1. 打开 Cursor 的设置，进入 Cursor Settings，左侧选 Models。
2. 展开 API Keys，打开 OpenAI API Key 开关，粘贴你的密钥。注意不要带空格。
3. 打开 Override OpenAI Base URL，填 `{{v1}}`。这里必须带 `/v1`。
4. 在模型列表里添加自定义模型（Add Custom Model），名称填价格与计费页上的模型名，比如 `{{model}}`，确认开关是开着的。
5. 回到聊天面板，关掉顶部的 Auto，在模型下拉里选刚添加的模型，发一句话试试。

## 哪些功能能用 {#cursor-scope}

- 聊天用的是你添加的自定义模型，走本站的接入地址。
- Tab 补全始终使用 Cursor 自带的模型，不会走你的密钥和地址。
- Agent 等功能是否使用自定义地址，取决于 Cursor 的版本。如果发现 Agent 里报错、而普通聊天正常，可以先用聊天模式。

## 常见问题 {#cursor-trouble}

**提示 Rate Limit Exceeded。** Cursor 把很多错误都显示成这一句，多数时候请求根本没发出来。按顺序检查：地址有没有带 `/v1`，密钥是否复制完整，余额是否够用，密钥有没有被停用。

**提示模型名无效，或者找不到模型。** 确认模型名和价格与计费页一字不差，并且已经在 Models 页里打开。聊天面板里还要关掉 Auto。该模型还要是你这把密钥所在的分组能用的。

**提示 401 或密钥无效。** 重新复制密钥，确认没有多余的空格或换行，并且填在 OpenAI API Key 里。

**提示 404。** 多半是地址少了 `/v1`，改成 `{{v1}}`。

**回复很短，或者只有一两个字。** 检查项目里的 `.cursor/rules` 规则文件，有设置为始终应用的规则时，内容会被加进每次请求，可能影响输出。

**更新 Cursor 后设置不见了。** Cursor 改版时会调整 Models 页的布局，回到 Models 页按上面的步骤重新检查一遍。

## 切回 Cursor 自带额度 {#cursor-revert}

在 Settings 的 Models 页：关掉 OpenAI API Key 开关，清空 Override OpenAI Base URL，删掉你添加的自定义模型，再打开 Auto。
