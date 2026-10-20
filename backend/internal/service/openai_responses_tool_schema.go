package service

import (
	"bytes"
	"sort"

	"github.com/tidwall/gjson"
)

const (
	// 工具定义在多轮历史里最多再嵌套一层 tools，留出余量后截断，避免畸形请求体
	// 造成无界递归。
	openAIResponsesToolSchemaMaxDepth = 4
	// JSON Schema 里 type 只能是字符串或字符串数组；显式 null 无论哪个方言都非法，
	// 补成 object 与 upstream 对该工具的实际期望一致。
	openAIResponsesToolSchemaFallbackType = `"object"`
	// 显式 null 在 JSON 里只有这一种字面量形态。
	openAIResponsesToolSchemaNullLiteral = "null"
	// 工具 Schema 内部（properties / items / anyOf 等）的嵌套深度上限，防止畸形
	// 请求体造成无界递归。正常工具定义远低于该值。
	openAIResponsesToolSchemaMaxSchemaDepth = 64
)

// openAIResponsesToolSchemaEdit 记录一处待修正的片段，用原始 body 上的绝对字节
// 偏移表示，便于最后一次性拼接：replacement 为空表示整段删除。
type openAIResponsesToolSchemaEdit struct {
	offset      int
	length      int
	replacement string
}

// sanitizeOpenAIResponsesToolParameterTypes 修正请求体中工具 Schema 的两类
// 非法 null：Schema 根上显式为 null 的 type（补成 object），以及 Schema 树中任意
// 层级显式为 null 的 required（整键删除）。Schema 根包括 Responses 的
// parameters、ChatCompletions 的 function.parameters 与 Anthropic Messages 的
// input_schema。
//
// Codex Desktop 内置的 automation_update 工具会带 parameters.type = null，
// OpenAI 直接回 400 invalid_function_parameters，而网关把该状态归一成可重试的
// 502 upstream_error；该工具定义又会沉进多轮历史，导致之后每一轮继续失败并在
// 账号池里反复重放同一份坏 Schema。
//
// 只修正显式 null：缺失 type 的 Schema 本身合法（等价于不约束），补写会收窄
// 客户端语义，因此保持原样。
//
// JSON Schema 规定 required 必须是属性名数组，但部分客户端会发成 null，xAI、
// Moonshot 等严格校验的上游直接回 400。缺省 required 等价于无必填项，因此删除
// 该键既保留客户端原意又满足严格校验；置 [] 则无必要。只沿 Schema 关键字白名单
// 下降，default / examples / const / enum 等实例数据里的字面量 {"required":null}
// 属于客户端 payload，保持不动。
//
// 实现上先收集全部命中的绝对偏移，再一次性拼出新 body：逐个 sjson.SetBytes 每次
// 都会重扫并全量拷贝整个文档，命中 N 处就是 N 次全量拷贝，而 /v1/responses 的
// body 上限是 gateway.max_body_size（默认 256MB），构造请求能塞进百万级命中。
func sanitizeOpenAIResponsesToolParameterTypes(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}

	// required 清洗需要逐层遍历 Schema；body 里连 "required" 字样都没有时跳过，
	// 保持绝大多数请求只做根 type 检查的原有开销。
	dropNullRequired := bytes.Contains(body, []byte(`"required"`))
	hits := make([]openAIResponsesToolSchemaEdit, 0, 2)
	collectOpenAIResponsesToolSchemaEdits(body, gjson.GetBytes(body, "tools"), 0, dropNullRequired, &hits)
	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.IsObject() {
				collectOpenAIResponsesToolSchemaEdits(body, item.Get("tools"), 0, dropNullRequired, &hits)
			}
			return true
		})
	}
	if len(hits) == 0 {
		return body, false, nil
	}

	// tools 与 input 在 body 里的先后顺序由客户端决定，收集顺序不保证单调。
	sort.Slice(hits, func(i, j int) bool { return hits[i].offset < hits[j].offset })

	sanitized := make([]byte, 0, len(body)+len(hits)*len(openAIResponsesToolSchemaFallbackType))
	cursor := 0
	for _, hit := range hits {
		// 收集阶段已逐个校验过区间，这里再挡一次重叠，保证拼接严格单调向前。
		if hit.offset < cursor {
			continue
		}
		sanitized = append(sanitized, body[cursor:hit.offset]...)
		sanitized = append(sanitized, hit.replacement...)
		cursor = hit.offset + hit.length
	}
	sanitized = append(sanitized, body[cursor:]...)
	return sanitized, true, nil
}

// collectOpenAIResponsesToolSchemaEdits 收集一个 tools 数组里所有需要修正的
// 位置。不按 tool type 过滤：null 的 schema type / required 在 function、custom
// 以及任何 hosted 工具上都同样非法。
func collectOpenAIResponsesToolSchemaEdits(
	body []byte, tools gjson.Result, depth int, dropNullRequired bool, hits *[]openAIResponsesToolSchemaEdit,
) {
	if depth > openAIResponsesToolSchemaMaxDepth || !tools.IsArray() {
		return
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !tool.IsObject() {
			return true
		}
		// Responses 形态用顶层 parameters，ChatCompletions 形态用 function.parameters，
		// 两种都可能出现在 Responses 请求里（见 normalizeCodexTools）；Anthropic
		// Messages 的工具定义把同一份 JSON Schema 放在 input_schema 下。
		for _, suffix := range []string{"parameters", "function.parameters", "input_schema"} {
			params := tool.Get(suffix)
			if !params.IsObject() {
				continue
			}
			// gjson 用 Type==Null 同时表示「显式 null」和「路径不存在」，靠 Raw
			// 区分：不存在时 Raw 为空串。
			if typ := params.Get("type"); typ.Type == gjson.Null && typ.Raw == openAIResponsesToolSchemaNullLiteral {
				appendOpenAIResponsesToolSchemaNullType(body, typ, hits)
			}
			if dropNullRequired {
				collectOpenAIResponsesToolSchemaNullRequired(body, params, 0, hits)
			}
		}
		// 历史输入里的工具定义会再嵌套一层 tools（upstream 报错路径形如
		// input[234].tools[0].tools[3].parameters）。
		collectOpenAIResponsesToolSchemaEdits(body, tool.Get("tools"), depth+1, dropNullRequired, hits)
		return true
	})
}

// appendOpenAIResponsesToolSchemaNullType 先校验 gjson 给出的偏移确实指向原始
// body 上那段 null，再记录。gjson 对嵌套取值同样返回相对原始文档的绝对偏移，但
// Index 为 0 表示未知；偏移不可用时跳过该处而不是猜位置——少修一个工具只是维持
// 现状，拼错位置会损坏整个请求体。
func appendOpenAIResponsesToolSchemaNullType(
	body []byte, typ gjson.Result, hits *[]openAIResponsesToolSchemaEdit,
) {
	if !openAIResponsesToolSchemaSpanMatches(body, typ.Index, typ.Raw) {
		return
	}
	*hits = append(*hits, openAIResponsesToolSchemaEdit{
		offset: typ.Index, length: len(typ.Raw), replacement: openAIResponsesToolSchemaFallbackType,
	})
}

func openAIResponsesToolSchemaSpanMatches(body []byte, offset int, raw string) bool {
	end := offset + len(raw)
	return offset > 0 && end <= len(body) && bytes.Equal(body[offset:end], []byte(raw))
}

// openAIResponsesToolSchemaMember 是对象里一个成员在原始 body 上的区间：
// keyStart 指向键的左引号，valueEnd 指向值之后的第一个字节。
type openAIResponsesToolSchemaMember struct {
	keyStart int
	valueEnd int
	drop     bool
}

// collectOpenAIResponsesToolSchemaNullRequired 沿 JSON Schema 子 schema 关键字
// 白名单递归，收集所有显式为 null 的 required 成员的删除区间。
func collectOpenAIResponsesToolSchemaNullRequired(
	body []byte, schema gjson.Result, depth int, hits *[]openAIResponsesToolSchemaEdit,
) {
	if depth > openAIResponsesToolSchemaMaxSchemaDepth || !schema.IsObject() {
		return
	}
	hasNullRequired := false
	schema.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		switch name {
		case "required":
			if value.Type == gjson.Null && value.Raw == openAIResponsesToolSchemaNullLiteral {
				hasNullRequired = true
			}
		case "additionalProperties", "additionalItems", "contains", "not", "if", "then", "else",
			"propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
			collectOpenAIResponsesToolSchemaNullRequired(body, value, depth+1, hits)
		case "items":
			if value.IsArray() {
				collectOpenAIResponsesToolSchemaNullRequiredEach(body, value, depth+1, hits)
			} else {
				collectOpenAIResponsesToolSchemaNullRequired(body, value, depth+1, hits)
			}
		case "anyOf", "oneOf", "allOf", "prefixItems":
			if value.IsArray() {
				collectOpenAIResponsesToolSchemaNullRequiredEach(body, value, depth+1, hits)
			}
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies":
			if value.IsObject() {
				collectOpenAIResponsesToolSchemaNullRequiredEach(body, value, depth+1, hits)
			}
		}
		return true
	})
	if hasNullRequired {
		appendOpenAIResponsesToolSchemaMemberDeletes(body, schema, hits)
	}
}

// collectOpenAIResponsesToolSchemaNullRequiredEach 对数组元素或 map 的每个值
// 递归（properties 等 map 的值、anyOf 等数组的元素都是子 schema）。
func collectOpenAIResponsesToolSchemaNullRequiredEach(
	body []byte, container gjson.Result, depth int, hits *[]openAIResponsesToolSchemaEdit,
) {
	container.ForEach(func(_, child gjson.Result) bool {
		collectOpenAIResponsesToolSchemaNullRequired(body, child, depth, hits)
		return true
	})
}

// appendOpenAIResponsesToolSchemaMemberDeletes 为一个对象里所有 required:null
// 成员生成删除区间，并连带删掉分隔逗号，保证拼接结果仍是合法 JSON：
//   - 前面已有保留成员时，删除「上一个保留（或已删除）成员之后」到本成员值末尾，
//     即 `, "required": null`；
//   - 位于开头的一串待删成员，删除到下一个保留成员的键之前，即
//     `"required": null, `；全部成员都待删时删到最后一个值末尾。
//
// 任一成员的偏移无法与原始 body 对上时整对象放弃——少修一处只是维持现状，拼错
// 位置会损坏整个请求体。
func appendOpenAIResponsesToolSchemaMemberDeletes(
	body []byte, object gjson.Result, hits *[]openAIResponsesToolSchemaEdit,
) {
	members := make([]openAIResponsesToolSchemaMember, 0, 4)
	valid := true
	object.ForEach(func(key, value gjson.Result) bool {
		if !openAIResponsesToolSchemaSpanMatches(body, key.Index, key.Raw) {
			valid = false
			return false
		}
		valueStart := openAIResponsesToolSchemaValueStart(body, key.Index+len(key.Raw))
		if valueStart < 0 || !openAIResponsesToolSchemaSpanMatches(body, valueStart, value.Raw) {
			valid = false
			return false
		}
		members = append(members, openAIResponsesToolSchemaMember{
			keyStart: key.Index,
			valueEnd: valueStart + len(value.Raw),
			drop:     key.String() == "required" && value.Type == gjson.Null && value.Raw == openAIResponsesToolSchemaNullLiteral,
		})
		return true
	})
	if !valid {
		return
	}

	edits := make([]openAIResponsesToolSchemaEdit, 0, 1)
	previousEnd := -1 // 上一个保留成员（或已删除区间）的结束位置；-1 表示前面没有保留成员。
	for i := 0; i < len(members); i++ {
		member := members[i]
		if !member.drop {
			previousEnd = member.valueEnd
			continue
		}
		if previousEnd >= 0 {
			edits = append(edits, openAIResponsesToolSchemaEdit{offset: previousEnd, length: member.valueEnd - previousEnd})
			previousEnd = member.valueEnd
			continue
		}
		next := i + 1
		for next < len(members) && members[next].drop {
			next++
		}
		end := members[len(members)-1].valueEnd
		if next < len(members) {
			end = members[next].keyStart
		}
		edits = append(edits, openAIResponsesToolSchemaEdit{offset: member.keyStart, length: end - member.keyStart})
		i = next - 1
	}
	*hits = append(*hits, edits...)
}

// openAIResponsesToolSchemaValueStart 从键结束位置跳过空白和冒号，返回值的起始
// 偏移；格式不符时返回 -1。
func openAIResponsesToolSchemaValueStart(body []byte, pos int) int {
	pos = skipOpenAIResponsesToolSchemaWhitespace(body, pos)
	if pos >= len(body) || body[pos] != ':' {
		return -1
	}
	return skipOpenAIResponsesToolSchemaWhitespace(body, pos+1)
}

func skipOpenAIResponsesToolSchemaWhitespace(body []byte, pos int) int {
	for pos < len(body) {
		switch body[pos] {
		case ' ', '\t', '\n', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}
