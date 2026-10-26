# Kiro

[Kiro](https://kiro.dev) 是 AWS 推出的 AI IDE。Kiro 自带的对话和智能体只能用它自己提供的模型，没有填写自定义接入地址或自带密钥的入口，所以不能直接接入 {{site}}。

不过 Kiro 是完整的 IDE，自带终端，可以在里面运行 Claude Code，用 {{site}} 的密钥照常写代码。

## 在 Kiro 里使用 Claude Code {#kiro-claude-code}

1. 按 [Claude Code](#claude-code-config) 一节的步骤安装并写好配置。
2. 在 Kiro 里打开内置终端，进入你的项目目录。
3. 运行 `claude`。

Claude Code 走的是 {{site}}，不会占用 Kiro 自己的额度，Kiro 的对话面板也不受影响。

## 验证 {#kiro-verify}

在 Kiro 的终端里启动 `claude`，输入 `/status`，看到 Anthropic base URL 是 `{{base}}` 就说明生效了。

## 常见问题 {#kiro-faq}

**Kiro 的对话面板能不能改成用本站？**
不能。Kiro 目前没有自定义模型或自定义接入地址的设置。想在编辑器里用本站，可以换用 [Cursor](#cursor)、[Cline](#cline)、[Roo Code](#roo-code) 这类支持自定义服务商的工具。

**终端里 `claude` 提示还要登录？**
环境变量没生效。回到 Claude Code 一节检查 `~/.claude/settings.json`，改完重新打开终端。
