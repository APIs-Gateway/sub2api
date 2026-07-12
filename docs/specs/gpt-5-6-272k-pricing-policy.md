# GPT-5.6 272K 长上下文计费

## §1 规则

对 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` 及其已支持的别名统一规定：

- `total_context_tokens <= 272000` 时，使用基础价格；
- `total_context_tokens > 272000` 时，整笔请求使用长上下文价格：输入侧（普通输入、cache read、cache write）为基础价的 2 倍，输出为基础价的 1.5 倍；
- 判断按整笔请求的 `input_tokens + cache_read_tokens + cache_creation_tokens` 执行，而非只对超出阈值的 token 加价。

该规则只改变 GPT-5.6 的长上下文语义；GPT-5.4 和其他模型保持各自既有规则。Priority 使用上游公布的独立价格，不与此长上下文倍率叠加。

## §2 定价来源

远程 LiteLLM 目录、内置资源和离线回退都应保留并优先使用以下显式高档价格：

- `input_cost_per_token_above_272k_tokens`；
- `output_cost_per_token_above_272k_tokens`；
- `cache_read_input_token_cost_above_272k_tokens`；
- `cache_creation_input_token_cost_above_272k_tokens`。

若目录缺失倍率元数据，系统补齐 `272000`、输入侧 `2x`、输出 `1.5x`。仅当 cache-write 价格缺失时，才从输入价推导为 1.25 倍；显式渠道 cache-write 价格优先。

## §3 渠道与账号统计

Token 模式的渠道自定义区间不做 GPT-5.6 截断或重写：`(272K,∞)` 的显式高档区间必须参与实际计费、价格展示和账号统计。`per_request` 与 `image` 模式不适用本规则。

## §4 验证矩阵

1. 超过 272K 的标准 GPT-5.6 请求对普通输入、cache read、cache write 应用 2x，对输出应用 1.5x。
2. 恰好 272K 不触发长上下文价。
3. 显式 `*_above_272k_tokens` 价格和渠道高档 interval 不被清零或截断。
4. Priority 使用独立价格，不再叠加 2x/1.5x。
5. GPT-5.4 长上下文加价和非 GPT-5.6 模型行为保持不变。
