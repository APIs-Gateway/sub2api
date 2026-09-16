package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 客户端可见 model 对齐：发给下游客户端的一切响应里，model / response.model 必须等于
// 客户端原始请求的模型名（originalModel），无论上游实际返回什么。
//
// 这与 model_mapping 的反向改写（mappedModel → originalModel）不同：上游偷换模型
// （请求 gpt-5.6-sol 却回 gpt-6-sol）而又拦不住时——内容已开始下发、观察模式开关关闭、
// grok 类只记录不拦截——旧逻辑只在「响应 model == mappedModel」时改回，其余名字原样透出。
//
// 界线：只改发给客户端的字节。上游真实模型早在此前已被 checkUpstreamModelMismatch 读走
// 并记入 UpstreamModelMismatchMark.ResponseModel / usage_logs.upstream_response_model /
// ops 事件，各调用点必须保持「先比对、后对齐」的顺序。

var clientVisibleModelKeyBytes = []byte(`"model"`)

// alignClientVisibleModel 把 JSON 载荷里的顶层 model 与嵌套 response.model 强制置为 originalModel。
// originalModel 为空 → 原样返回；字段不存在 → 不添加；字段已相等 → 原样返回（零拷贝）；
// 非字符串类型的 model 字段不动。
func alignClientVisibleModel(payload []byte, originalModel string) []byte {
	if strings.TrimSpace(originalModel) == "" || len(payload) == 0 || !bytes.Contains(payload, clientVisibleModelKeyBytes) {
		return payload
	}
	values := gjson.GetManyBytes(payload, "model", "response.model")
	alignTop := values[0].Type == gjson.String && values[0].Str != originalModel
	alignNested := values[1].Type == gjson.String && values[1].Str != originalModel
	if !alignTop && !alignNested {
		return payload
	}
	updated := payload
	if alignTop {
		if next, err := sjson.SetBytes(updated, "model", originalModel); err == nil {
			updated = next
		}
	}
	if alignNested {
		if next, err := sjson.SetBytes(updated, "response.model", originalModel); err == nil {
			updated = next
		}
	}
	return updated
}

// alignClientVisibleModelInSSELine 对单行 SSE 做同样的对齐：只处理 `data:` 行；
// 空 data / [DONE] / 注释行 / event 行原样返回。
func alignClientVisibleModelInSSELine(line, originalModel string) string {
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || data == "" || data == "[DONE]" || !strings.Contains(data, `"model"`) {
		return line
	}
	aligned := alignClientVisibleModel([]byte(data), originalModel)
	if len(aligned) == len(data) && string(aligned) == data {
		return line
	}
	return "data: " + string(aligned)
}

// alignClientVisibleModelInSSEBody 对整段 SSE 文本逐行对齐（非流式请求收到上游 SSE 且无法转成 JSON 时原样回写的分支）。
func alignClientVisibleModelInSSEBody(body, originalModel string) string {
	if strings.TrimSpace(originalModel) == "" || !strings.Contains(body, `"model"`) {
		return body
	}
	lines := strings.Split(body, "\n")
	changed := false
	for i, line := range lines {
		if aligned := alignClientVisibleModelInSSELine(line, originalModel); aligned != line {
			lines[i] = aligned
			changed = true
		}
	}
	if !changed {
		return body
	}
	return strings.Join(lines, "\n")
}
