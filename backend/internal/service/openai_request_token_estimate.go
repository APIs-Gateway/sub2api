package service

import (
	"github.com/tidwall/gjson"
)

// openAIRequestMessageOverheadTokens 每条 message 的结构开销（role / 分隔符），与 OpenAI 官方
// "每条消息约 4 token" 的口径一致。
const openAIRequestMessageOverheadTokens = 4

// EstimateOpenAIRequestInputTokens 按请求体粗估输入侧 token：上游模型不一致在 response.created
// 就被拦截时上游还没回报 usage，但 prompt 已经发出去、输入侧消耗真实发生，审计行的 input_tokens
// 用这个估算值而不是 0。只是网关侧的近似（estimateTokensForText ≈ 4 字符/token，每条消息 +4），
// 不是上游口径，也不参与计费（审计行成本恒为 0）。
//
// 覆盖三种入站形态，文本以外（图片 / 文件 / 音频等）一律跳过：
//   - Responses：instructions 字符串；input 为字符串（视为一条消息）或数组，元素的 content 为
//     字符串或 [{type: input_text/output_text/text, text}]；type=function_call_output 的 output
//     （字符串或文本块数组）、type=function_call 的 arguments 也计入（agent 场景往往是 prompt 大头）；
//   - Chat Completions：messages[].content 为字符串或 [{type: text, text}]；messages[].tool_calls[]
//     .function.arguments 计入；
//   - Anthropic Messages：system 为字符串或 [{text}]；messages[].content 同上，其中 type=tool_result
//     的 content（字符串或文本块数组）、type=tool_use 的 input（JSON 原文）计入。
//
// tools[] 的 description 与 parameters（JSON 原文）同样占 prompt，按文本估算；兼容 Responses 扁平
// 形态与 Chat 的 tools[].function 嵌套形态。非法 JSON / 空体返回 0。
func EstimateOpenAIRequestInputTokens(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return 0
	}
	total := 0
	// Responses: instructions；Anthropic: system（字符串或 blocks）。
	total += estimateOpenAIRequestTextOrBlocks(root.Get("instructions"))
	total += estimateOpenAIRequestTextOrBlocks(root.Get("system"))
	// Responses input：字符串视为一条 user 消息；数组按消息逐条累加。
	if input := root.Get("input"); input.Exists() {
		switch {
		case input.Type == gjson.String:
			total += estimateTokensForText(input.String()) + openAIRequestMessageOverheadTokens
		case input.IsArray():
			input.ForEach(func(_, item gjson.Result) bool {
				total += estimateOpenAIRequestMessageTokens(item)
				return true
			})
		}
	}
	// Chat Completions / Anthropic messages。
	if messages := root.Get("messages"); messages.IsArray() {
		messages.ForEach(func(_, item gjson.Result) bool {
			total += estimateOpenAIRequestMessageTokens(item)
			return true
		})
	}
	if tools := root.Get("tools"); tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			def := tool
			if fn := tool.Get("function"); fn.IsObject() {
				def = fn
			}
			total += estimateTokensForText(def.Get("description").String())
			if params := def.Get("parameters"); params.Exists() {
				total += estimateTokensForText(params.Raw)
			}
			if schema := def.Get("input_schema"); schema.Exists() {
				total += estimateTokensForText(schema.Raw)
			}
			return true
		})
	}
	return total
}

// estimateOpenAIRequestMessageTokens 一条消息：content 文本 + 工具调用 / 工具输出文本 + 固定结构开销。
func estimateOpenAIRequestMessageTokens(msg gjson.Result) int {
	if !msg.IsObject() {
		return 0
	}
	total := estimateOpenAIRequestTextOrBlocks(msg.Get("content"))
	// Responses input：function_call_output.output / function_call.arguments。
	total += estimateOpenAIRequestTextOrBlocks(msg.Get("output"))
	total += estimateTokensForText(msg.Get("arguments").String())
	// Chat Completions：assistant 消息里的 tool_calls[].function.arguments。
	if calls := msg.Get("tool_calls"); calls.IsArray() {
		calls.ForEach(func(_, call gjson.Result) bool {
			total += estimateTokensForText(call.Get("function.arguments").String())
			return true
		})
	}
	return total + openAIRequestMessageOverheadTokens
}

// estimateOpenAIRequestTextOrBlocks 字符串直接估；数组累加文本块（input_text / output_text / text，
// 或任何带 text 字段的块）、Anthropic tool_result 的 content（递归）与 tool_use 的 input（JSON 原文），
// 图片、文件、音频等跳过；其他类型返回 0。
func estimateOpenAIRequestTextOrBlocks(v gjson.Result) int {
	switch {
	case !v.Exists():
		return 0
	case v.Type == gjson.String:
		return estimateTokensForText(v.String())
	case v.IsArray():
		total := 0
		v.ForEach(func(_, block gjson.Result) bool {
			if block.Type == gjson.String {
				total += estimateTokensForText(block.String())
				return true
			}
			if !block.IsObject() {
				return true
			}
			switch block.Get("type").String() {
			case "", "text", "input_text", "output_text":
				total += estimateTokensForText(block.Get("text").String())
			case "tool_result":
				total += estimateOpenAIRequestTextOrBlocks(block.Get("content"))
			case "tool_use":
				if input := block.Get("input"); input.Exists() {
					total += estimateTokensForText(input.Raw)
				}
			}
			return true
		})
		return total
	default:
		return 0
	}
}
