package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 账号级强制思考档位（extra.openai_force_reasoning_effort）。
//
// 背景：free 站 luna 号在请求不带 reasoning.effort 时上游基本不思考，没有 reasoning 项，
// 也就没有 encrypted_content（Anthropic thinking 块的 signature 来自它）。开启后，
// 请求体里的档位缺失 / 空 / none / minimal 时改成配置值，已经是 low/medium/high/xhigh/max 的不动。
// 其他字段（include、store、reasoning.summary 等）一律不碰。

// applyOpenAIForcedReasoningEffort 作用在最终发给上游的 Responses 格式请求体上。
// 返回 (新 body, 是否改写, error)；未配置 / 非 OpenAI 平台 / 档位已有效时原样返回同一份 body。
func applyOpenAIForcedReasoningEffort(account *Account, body []byte) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAI() {
		return body, false, nil
	}
	forced := account.GetOpenAIForcedReasoningEffort()
	if forced == "" {
		return body, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())) {
	case "", "none", "minimal":
		// 需要强制
	default:
		return body, false, nil
	}
	out, err := sjson.SetBytes(body, "reasoning.effort", forced)
	if err != nil {
		return body, false, fmt.Errorf("apply forced reasoning effort: %w", err)
	}
	return out, true, nil
}

// applyOpenAIForcedReasoningEffortForUpstream 在 applyOpenAIForcedReasoningEffort 之上，
// 强制改写真的发生后补跑 GPT-6 的采样参数清理：GPT-6 Sol/Luna、GPT-6.1 Sol 在思考时不接受
// temperature/top_p/logprobs，而客户端原来带 effort=none 时这些参数是被保留的。
// upstreamModel 是最终发给上游的模型名。三个入口（Forward / Chat / Messages）统一调这个函数。
// 只有上游模型是推理模型（apicompat.IsReasoningModel，与转换器同一判断）时才强制；
// gpt-4.1 / gpt-4o 这类非推理模型字节保持不变。
func applyOpenAIForcedReasoningEffortForUpstream(account *Account, body []byte, upstreamModel string) ([]byte, bool, error) {
	if !apicompat.IsReasoningModel(upstreamModel) {
		return body, false, nil
	}
	out, changed, err := applyOpenAIForcedReasoningEffort(account, body)
	if err != nil || !changed {
		return body, false, err
	}
	cleaned, _, err := normalizeGPT6ResponsesSampling(out, upstreamModel)
	if err != nil {
		return body, false, err
	}
	return cleaned, true, nil
}
