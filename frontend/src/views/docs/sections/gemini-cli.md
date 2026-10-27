# Gemini CLI

Gemini CLI 是 Google 官方的命令行 AI 助手。它用的是 Gemini 原生格式，地址不带 `/v1`，也不带 `/v1beta`，CLI 会自己补上。

> 只有 Gemini 分组的密钥才能用。密钥在其他分组时，请求会返回 400。这时在「API 密钥」页给密钥换一个 Gemini 分组。

## 安装 {#gemini-cli-install}

需要 Node.js 20 或更高版本。终端里运行 `node --version` 确认，没有的话到 [nodejs.org](https://nodejs.org) 下载 LTS 版本。

```bash
npm install -g @google/gemini-cli
```

装完运行 `gemini --version`，能看到版本号就行。提示权限不足时，不要用 `sudo`，把 npm 的全局目录改到自己有权限的位置。

## 写配置文件（推荐） {#gemini-cli-config}

配置文件是 `~/.gemini/.env`，没有就新建，目录不存在就先建目录。Windows 的路径是 `%USERPROFILE%\.gemini\.env`。

```bash title="~/.gemini/.env"
GOOGLE_GEMINI_BASE_URL="{{base}}"
GEMINI_API_KEY="sk-你的密钥"
```

把 `sk-你的密钥` 换成你自己的密钥。如果文件里已经有这两个变量，改掉原来的值，不要重复写；其他变量不用动。

> 这个文件里有明文密钥。截图或把配置发给别人之前，先把密钥遮住。

## 只想临时用一下 {#gemini-cli-env}

不改配置文件，在当前终端设置环境变量：

```bash title="macOS / Linux"
export GOOGLE_GEMINI_BASE_URL="{{base}}"
export GEMINI_API_KEY="sk-你的密钥"
gemini
```

```powershell title="Windows PowerShell"
$env:GOOGLE_GEMINI_BASE_URL = "{{base}}"
$env:GEMINI_API_KEY = "sk-你的密钥"
gemini
```

## 验证 {#gemini-cli-verify}

运行 `gemini`。如果出现选择登录方式的菜单，选 **Use Gemini API key**。进入对话后发一句「你好」，能正常回复就接入成功了。回到「使用记录」页，能看到刚才这次请求。

改完配置后要重新启动 `gemini`，已经打开的会话不会读取新配置。

## 切换模型 {#gemini-cli-model}

启动时用 `-m` 指定：

```bash
gemini -m 模型名
```

也可以在会话里输入 `/model` 选择，或设置环境变量 `GEMINI_MODEL`。能用哪些模型取决于密钥所在的分组，模型名以「价格与计费」页为准。

## 常见问题 {#gemini-cli-faq}

- **返回 400**：密钥不在 Gemini 分组。到「API 密钥」页换分组。
- **返回 401**：密钥没复制完整或已删除。
- **还是连到了 Google 官方**：检查终端里有没有残留的旧环境变量，也检查当前项目目录里有没有 `.env` 文件把设置覆盖了。
- **提示地址不合法**：Gemini CLI 要求自定义地址使用 HTTPS，请确认 `GOOGLE_GEMINI_BASE_URL` 以 `https://` 开头。

## 切回官方账号 {#gemini-cli-revert}

打开 `~/.gemini/.env`，删掉 `GOOGLE_GEMINI_BASE_URL` 和 `GEMINI_API_KEY` 两行，然后重新启动 `gemini`，按提示用 Google 账号登录。
