package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 账号级强制思考档位（extra.openai_force_reasoning_effort），语义是「下限」。
//
// 背景：free 站 luna 号在请求不带 reasoning.effort 时上游基本不思考，没有 reasoning 项，
// 也就没有 encrypted_content（Anthropic thinking 块的 signature 来自它）。开启后，
// 请求体里的档位低于配置值的一律提升到配置值，高于或等于配置值的不动。
// 档位排序：缺失 / 空 / none / minimal = 0，low = 1，medium = 2，high = 3，xhigh = 4，max = 5
// （大小写、空白容错）；不认识的值不动。例：配置 high 时，缺失 / none / minimal / low / medium
// 都改成 high，high / xhigh / max / 未知值不动；配置 low 时只有缺失 / none / minimal 会改成 low。
// 其他字段（include、store、reasoning.summary 等）一律不碰。

// openAIReasoningEffortRank 返回档位的排序；ok=false 表示不认识的值。
func openAIReasoningEffortRank(effort string) (rank int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "none", "minimal":
		return 0, true
	case "low":
		return 1, true
	case "medium":
		return 2, true
	case "high":
		return 3, true
	case "xhigh":
		return 4, true
	case "max":
		return 5, true
	}
	return 0, false
}

// applyOpenAIForcedReasoningEffort 作用在最终发给上游的 Responses 格式请求体上。
// 返回 (新 body, 是否改写, error)；未配置 / 非 OpenAI 平台 / 当前档位已不低于配置值 /
// 当前档位不认识时原样返回同一份 body。
func applyOpenAIForcedReasoningEffort(account *Account, body []byte) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAI() {
		return body, false, nil
	}
	forced := account.GetOpenAIForcedReasoningEffort()
	if forced == "" {
		return body, false, nil
	}
	forcedRank, _ := openAIReasoningEffortRank(forced)
	currentRank, known := openAIReasoningEffortRank(gjson.GetBytes(body, "reasoning.effort").String())
	if !known || currentRank >= forcedRank {
		return body, false, nil
	}
	out, err := sjson.SetBytes(body, "reasoning.effort", forced)
	if err != nil {
		return body, false, fmt.Errorf("apply forced reasoning effort: %w", err)
	}
	return out, true, nil
}

// applyOpenAIForcedReasoningEffortForUpstream 在 applyOpenAIForcedReasoningEffort 之上，
// 提升真的发生后补跑 GPT-6 的采样参数清理：GPT-6 Sol/Luna、GPT-6.1 Sol 在思考时不接受
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
