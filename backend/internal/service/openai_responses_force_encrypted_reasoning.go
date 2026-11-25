package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIReasoningEncryptedContentInclude = "reasoning.encrypted_content"

// responsesForceEncryptedReasoningEnabled 报告 gateway.responses_force_encrypted_reasoning 是否开启。
func (s *OpenAIGatewayService) responsesForceEncryptedReasoningEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.ResponsesForceEncryptedReasoning
}

// ensureResponsesEncryptedReasoningInclude 给最终发往上游的 Responses 请求体补 include "reasoning.encrypted_content"。
// 只对推理模型生效；effort 为 none 时不加；include 不存在就新建，已有的其他元素保持不变；
// 已包含时原样返回。不动 store 等其他字段。返回 (新 body, 是否追加, error)。
func ensureResponsesEncryptedReasoningInclude(body []byte, upstreamModel string) ([]byte, bool, error) {
	if !apicompat.IsReasoningModel(upstreamModel) {
		return body, false, nil
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()), "none") {
		return body, false, nil
	}
	include := gjson.GetBytes(body, "include")
	var (
		out []byte
		err error
	)
	switch {
	case !include.Exists() || include.Type == gjson.Null:
		out, err = sjson.SetBytes(body, "include", []string{openAIReasoningEncryptedContentInclude})
	case include.IsArray():
		for _, v := range include.Array() {
			if v.Type == gjson.String && v.String() == openAIReasoningEncryptedContentInclude {
				return body, false, nil
			}
		}
		out, err = sjson.SetBytes(body, "include.-1", openAIReasoningEncryptedContentInclude)
	default:
		return body, false, nil
	}
	if err != nil {
		return body, false, fmt.Errorf("ensure encrypted reasoning include: %w", err)
	}
	return out, true, nil
}
