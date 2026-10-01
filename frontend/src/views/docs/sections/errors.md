# 错误排查

按你看到的现象找对应的条目。接口返回的错误里都带一段说明文字，可以直接对照下面的关键词。

## 401 提示密钥无效 {#errors-invalid-key}

**现象。** 返回 401，说明里有 `Invalid API key`，或者 `API key is required`（请求里没有密钥）。

**原因。** 密钥没复制完整，前后多了空格或换行；密钥已被删除；请求里根本没带密钥。用 Claude Code 的话，还可能是电脑上残留了旧的 `ANTHROPIC_API_KEY` 环境变量，把新密钥顶掉了。

**解决。** 回到「API 密钥」页，用复制按钮重新复制密钥，不要手打，`l`、`1`、`O`、`0` 很容易看错。确认开头是 `sk-`。Claude Code 用户用 `ANTHROPIC_AUTH_TOKEN`，并检查有没有残留的 `ANTHROPIC_API_KEY`，有就删掉。多次用错密钥后会被暂时限制，返回 429；先改对密钥，再等一会儿重试。

## 密钥被停用、已过期，或被限制了来源 {#errors-key-disabled}

**现象。** 返回 401 或 403，说明里提到密钥已停用、已过期，或者 `Access denied` 并带着你的 IP。

**原因。** 密钥被你自己停用了、到了设定的过期时间，或者设置了 IP 限制，而当前请求的 IP 不在允许范围内。

**解决。** 在「API 密钥」页检查这个密钥的状态、过期时间和 IP 限制。需要的话改一下，或者新建一个密钥。

## 账户被停用 {#errors-user-inactive}

**现象。** 返回 401，说明里有 `User account is not active`。

**原因。** 密钥所属的账户已被停用。

**解决。** 密钥本身没有问题，换密钥也没用。请联系站点管理员确认账户状态。

## 余额不足 {#errors-balance}

**现象。** 返回 403，错误类型是 `billing_error`，说明里有 `insufficient balance`。

**原因。** 你的余额用完了，而且没有可用的套餐额度。

**解决。** 到「充值/订阅」页充值或购买套餐，也可以在「兑换」页使用兑换码。到账后马上就能继续用。

## 套餐额度用完 {#errors-plan-limit}

**现象。** 返回 429，说明里有 `usage limit exceeded`，前面是 `daily`、`weekly` 或 `monthly`。如果返回的是 403，说明里提到订阅无效或已过期，见下面的解决办法。

**原因。** 套餐按日、周、月三档限制额度，其中一档用完了。套餐额度用完后，请求会改扣余额。只有余额也不够时，才会看到这个错误。

**解决。** 等对应的额度周期重置，或者给账户充值。在「我的订阅」页可以看到各档额度的使用情况和重置时间。订阅已过期的话，到「充值/订阅」页续费或重新购买。

## 密钥额度或限额用完 {#errors-key-quota}

**现象。** 返回 429，说明里有「api key 额度已用完」，或者「5小时限额」「日限额」「7天限额已用完」。

**原因。** 你给这个密钥单独设置了额度或时间窗口限额，已经用完。这和账户余额无关。

**解决。** 在「API 密钥」页调高这个密钥的额度或限额，或者等限额窗口过去。

## 分组不可用 {#errors-group}

**现象。** 返回 403，说明里提到分组不再允许使用、分组已停用，或者密钥没有分配分组。

**原因。** 密钥所在的分组被关闭，或者你已经没有权限使用它。刚创建、还没选分组的密钥也会这样。

**解决。** 在「API 密钥」页给密钥换一个可用的分组。

## 用 Claude Code 返回 403，提示分组不允许 {#errors-claude-code-group}

**现象。** 返回 403，说明里有 `does not allow /v1/messages dispatch`。

**原因。** 密钥所在的分组没有开放 Claude Code 使用的接口。

**解决。** 到「API 密钥」页，把密钥换到一个支持 Claude Code 的分组。

## 找不到模型，或接口不支持 {#errors-model}

**现象。** 返回 404，错误类型是 `model_not_found`；或者说明里写着该分组不支持某个接口，比如图片接口。

**原因。** 模型名写错了，或者密钥所在的分组没有这个模型、没有开放这个接口。

**解决。** 到「价格与计费」页核对模型名，一字不差地复制。确认没问题的话，换一个有这个模型的分组。

## 图片生成没有开放 {#errors-image}

**现象。** 返回 403，错误类型是 `permission_error`，说明里有 `Image generation is not enabled for this group`；或者返回 400，说明里有 `Image generation via /v1/responses is disabled`。

**原因。** 密钥所在的分组没有开放图片生成，或者没有开放通过 `/v1/responses` 生成图片。

**解决。** 到「API 密钥」页，把密钥换到开放了图片生成的分组。

## 请求内容有问题 {#errors-bad-request}

**现象。** 返回 400，错误类型是 `invalid_request_error`，说明里有 `Failed to parse request body`、`Request body is empty` 或 `model is required`。

**原因。** 请求体不是合法的 JSON、是空的，或者没有填 `model` 字段。

**解决。** 检查请求体的 JSON 格式，确认带了 `model`，并且请求头里有 `Content-Type: application/json`。用客户端的话，确认接入地址是否填对，见「API 地址与鉴权」一节。

## 请求内容太大 {#errors-too-large}

**现象。** 返回 413，说明里有 `Request body too large`，后面带着大小上限。

**原因。** 一次请求发出的内容超过了上限，常见于对话历史很长、带了很多或很大的图片和文件。

**解决。** 开一个新对话，或者精简上下文、去掉不必要的附件后重试。

## 429 请求太频繁 {#errors-rate-limit}

**现象。** 返回 429，说明里有 `requests-per-minute limit exceeded`、`Too many pending requests`、`Concurrency limit exceeded`、`rate limit exceeded`，或者 `quota exhausted for this platform`。

**原因。** 一分钟内发出的请求太多，或者同时进行的请求太多；也可能是这个平台的日、周、月用量达到了上限。服务繁忙时，请求也可能因排队等待的请求太多而被拒绝。

**解决。** 降低请求频率，减少并发。响应里带 `Retry-After` 时，按它给的秒数等待再重试。用量上限要等下个周期，或者联系站点管理员。

## 503 暂时无法处理 {#errors-unavailable}

**现象。** 返回 503，提示服务暂时不可用，说明里有 `Service temporarily unavailable`、`service overloaded` 或 `Billing service temporarily unavailable`。

**原因。** 服务暂时繁忙，或者正在短暂维护。

**解决。** 稍等一会儿再重试。一直出现的话，换一个模型或分组试试。

## 502 请求失败 {#errors-bad-gateway}

**现象。** 返回 502，错误类型是 `upstream_error`，没有收到正常回复。

**原因。** 服务这一侧处理请求时暂时出了问题，不是你的密钥或请求写错了。

**解决。** 等几十秒后重试。反复出现、换模型或分组也没用时，请联系站点管理员。

## 524 或请求中途断开 {#errors-timeout}

**现象。** 请求等了很久，返回 524、504，或者连接被中断，没有收到任何内容。

**原因。** 请求长时间没有任何输出时，连接会被断开。回复很长又没开流式时最容易遇到。

**解决。** 用流式（`stream: true`），内容会边生成边返回，连接不会因为等待太久而断开。也可以把任务拆小一些。

## 请求被拒，说密钥不能放在网址里 {#errors-query-key}

**现象。** 返回 400，说明里有 `api_key_in_query_deprecated`。

**原因。** 请求把密钥放在了网址参数里（`?key=...`）。

**解决。** 改成放在请求头里，见「API 地址与鉴权」一节。
