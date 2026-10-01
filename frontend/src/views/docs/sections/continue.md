# Continue

[Continue](https://continue.dev) 是 VS Code 和 JetBrains 里的开源 AI 编程扩展，支持聊天、Agent 和编辑。它按 OpenAI 兼容的方式接入，地址带 `/v1`。

## 配置步骤 {#continue-steps}

1. 在 VS Code 或 JetBrains 里安装 Continue 扩展。
2. 打开配置文件：macOS 和 Linux 是 `~/.continue/config.yaml`，Windows 是 `%USERPROFILE%\.continue\config.yaml`。也可以在 Continue 的聊天面板里，点智能体选择器旁边的齿轮图标直接打开。第一次使用时会自动生成。
3. 在 `models` 下加一项，保存即可，Continue 会自动重新加载。

```yaml title="~/.continue/config.yaml"
name: Local Config
version: 1.0.0
schema: v1

models:
  - name: "{{providerName}}"
    provider: openai
    model: {{model}}
    apiBase: {{v1}}
    apiKey: sk-你的密钥
    roles:
      - chat
      - edit
      - apply
```

要用多个模型，就在 `models` 下多写几项，`name` 各不相同，`model` 填价格与计费页上的模型名。文件里已有 `name`、`version`、`schema` 这三行时不用重复添加。

> `provider` 固定写 `openai`，`apiBase` 必须带 `/v1`。能用哪些模型取决于密钥所在的分组。

## 验证 {#continue-verify}

打开 Continue 的聊天面板，在模型下拉里选刚加的模型，发一句话。能正常回复就接通了。

## 常见问题 {#continue-trouble}

**模型下拉里没有刚加的模型。** 检查 YAML 缩进是否正确，保存后看 Continue 面板有没有报配置错误。

**提示 404 或找不到模型。** 确认 `apiBase` 带了 `/v1`，`model` 和价格与计费页上的名称一字不差。

**提示 401 或认证失败。** 密钥复制完整，且没有多余空格；在「API 密钥」页确认密钥还在启用。

**网上教程写的是 `config.json`。** 那是旧版格式，Continue 已经改用 `config.yaml`，请按上面的写法配置。

## 切回原来的服务 {#continue-revert}

在 `config.yaml` 的 `models` 里删掉这一项，保存即可。
