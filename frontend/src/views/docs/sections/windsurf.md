# Windsurf

[Windsurf](https://windsurf.com)（现已更名为 Devin Desktop）是带 AI 助手的代码编辑器。它自带的模型服务不能改成自定义的 OpenAI 兼容地址，所以不能直接填 {{site}} 的接入地址。

## 怎么接入 {#windsurf-how}

Windsurf 基于 VS Code，可以安装扩展。推荐装 Roo Code 扩展，再在扩展里填 {{site}} 的地址和密钥：

1. 在 Windsurf 的扩展面板里搜索 Roo Code 并安装。
2. 按本站 [Roo Code](#roo-code) 一节的步骤配置：地址填 `{{v1}}`，密钥填你的密钥，模型填价格与计费页上列出的模型名。
3. 在 Roo Code 的对话框里发一句话验证。

> 用 Roo Code 时，请求走的是扩展里填的 {{site}} 配置，不经过 Windsurf 自带的模型和额度。

## 常见问题 {#windsurf-faq}

**Windsurf 自带的对话（Cascade）能用 {{site}} 吗？**
不能。自带对话只能用 Windsurf 提供的模型，需要用 {{site}} 请改用 Roo Code。
