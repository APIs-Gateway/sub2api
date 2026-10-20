# Aider

[Aider](https://aider.chat) 是在终端里运行的 AI 结对编程工具，能直接读写你的 git 仓库并自动提交。它通过 OpenAI 兼容接口接入 {{site}}。

## 配置步骤 {#aider-steps}

1. 按 Aider 官方说明安装：

```bash
python -m pip install aider-install
aider-install
```

2. 设置接入地址和密钥。macOS / Linux 写进 `~/.zshrc` 或 `~/.bashrc`：

```bash
export OPENAI_API_BASE="{{v1}}"
export OPENAI_API_KEY="sk-你的密钥"
```

Windows 在 PowerShell 或命令提示符里执行，然后重新打开终端：

```bat title="Windows cmd"
setx OPENAI_API_BASE "{{v1}}"
setx OPENAI_API_KEY "sk-你的密钥"
```

3. 进入项目目录，启动时给模型名加上 `openai/` 前缀：

```bash
cd /你的项目
aider --model openai/{{model}}
```

模型名必须带 `openai/`，Aider 靠这个前缀知道要走 OpenAI 兼容接口。前缀后面写价格与计费页上的模型名；能用哪些模型取决于密钥所在的分组。

## 用命令行参数 {#aider-cli}

不想设环境变量时，可以直接在命令里传：

```bash
aider --openai-api-base {{v1}} --openai-api-key sk-你的密钥 --model openai/{{model}}
```

密钥会留在 shell 历史里，日常使用更推荐环境变量或配置文件。

## 用配置文件 {#aider-config}

在家目录、git 仓库根目录或当前目录放一个 `.aider.conf.yml`，Aider 会按这个顺序依次读取，后读到的优先。

```yaml title=".aider.conf.yml"
openai-api-base: {{v1}}
openai-api-key: sk-你的密钥
model: openai/{{model}}
```

之后直接运行 `aider` 就行。密钥写进仓库里的配置文件有泄露风险，放在家目录的那份最稳妥；放进仓库的话记得加到 `.gitignore`。

也可以把同样的内容写进 `.env` 文件（同样按家目录、仓库根目录、当前目录的顺序读取）。变量名要加 `AIDER_` 前缀：

```bash title=".env"
AIDER_OPENAI_API_BASE={{v1}}
AIDER_OPENAI_API_KEY=sk-你的密钥
AIDER_MODEL=openai/{{model}}
```

## 验证 {#aider-verify}

启动后 Aider 会在开头显示正在使用的模型，输入一句话，比如「用一句话介绍这个项目」，能正常回复就说明接通了。

## 常用参数 {#aider-options}

| 参数 | 作用 |
|---|---|
| `--model` | 主对话使用的模型，记得带 `openai/` |
| `--weak-model` | 生成提交说明、压缩历史用的模型。提交说明阶段报模型不可用时，给它指定一个能用的模型，建议和主模型一样 |
| `--edit-format` | 修改代码的格式，一般不用动 |
| `--no-show-model-warnings` | 关掉「不认识这个模型」的提示 |
| `--no-stream` | 关闭流式输出 |
| `--list-models 关键词` | 列出 Aider 内置认识的模型 |

## 常见问题 {#aider-faq}

**提示不认识这个模型，或者上下文长度未知。** 这是 Aider 内置清单里没有这个名字，不影响使用，可以加 `--no-show-model-warnings` 关掉提示。

**提示模型不存在或没有权限。** 先确认模型名和价格与计费页一致、前缀是 `openai/`，再确认密钥所在的分组能用这个模型。如果只在生成提交说明时报错，给 `--weak-model` 也指定一个能用的模型。

**用 Anthropic 格式接入。** Aider 官方文档里 Anthropic 只说明了用 `ANTHROPIC_API_KEY` 或 `--anthropic-api-key` 填密钥，没有写可以改接入地址。要在 Aider 里用本站，请使用上面的 OpenAI 兼容方式。

## 切回原来的服务 {#aider-revert}

删掉上面的环境变量（或配置文件、`.env` 里对应的几行）。之后按 Aider 官方方式配置其他服务商的密钥即可。
