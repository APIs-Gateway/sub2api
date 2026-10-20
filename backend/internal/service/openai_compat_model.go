package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
)

func NormalizeOpenAICompatRequestedModel(model string) string {
	if openai.IsGPT61SolModelSpelling(model) {
		return "gpt-6.1-sol"
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}

	normalized, _, ok := splitOpenAICompatReasoningModel(trimmed)
	if !ok || normalized == "" {
		return trimmed
	}
	return normalized
}

func applyOpenAICompatModelNormalization(req *apicompat.AnthropicRequest) {
	if req == nil {
		return
	}

	// GPT-6.1 Sol 的 effort 后缀原样保留为 output_config.effort：通用映射会把 max
	// 丢掉、把 none/minimal 吞成默认档，前者降低用户选的强度，后者让下游校验拦不住。
	if openai.IsGPT61SolModelSpelling(req.Model) {
		canonical := strings.ToLower(strings.TrimSpace(req.Model))
		if idx := strings.LastIndex(canonical, "/"); idx >= 0 {
			canonical = strings.TrimSpace(canonical[idx+1:])
		}
		canonical = strings.ReplaceAll(canonical, "_", "-")
		effort, _ := strings.CutPrefix(canonical, "gpt-6.1-sol-")
		switch effort {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			req.Model = "gpt-6.1-sol"
			if req.OutputConfig == nil {
				req.OutputConfig = &apicompat.AnthropicOutputConfig{}
			}
			if req.OutputConfig.Effort == "" {
				req.OutputConfig.Effort = effort
			}
			return
		}
	}

	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return
	}

	normalizedModel, derivedEffort, hasReasoningSuffix := splitOpenAICompatReasoningModel(originalModel)
	if hasReasoningSuffix && normalizedModel != "" {
		req.Model = normalizedModel
	}

	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		return
	}

	claudeEffort := openAIReasoningEffortToClaudeOutputEffort(derivedEffort)
	if claudeEffort == "" {
		return
	}

	if req.OutputConfig == nil {
		req.OutputConfig = &apicompat.AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = claudeEffort
}

func splitOpenAICompatReasoningModel(model string) (normalizedModel string, reasoningEffort string, ok bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", "", false
	}

	modelID := trimmed
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	modelID = strings.TrimSpace(modelID)
	if !strings.HasPrefix(strings.ToLower(modelID), "gpt-") {
		return trimmed, "", false
	}

	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return trimmed, "", false
	}

	last := strings.NewReplacer("-", "", "_", "", " ", "").Replace(parts[len(parts)-1])
	switch last {
	case "none", "minimal":
	case "low", "medium", "high":
		reasoningEffort = last
	case "xhigh", "extrahigh":
		reasoningEffort = "xhigh"
	default:
		return trimmed, "", false
	}

	return normalizeCodexModel(modelID), reasoningEffort, true
}

func openAIReasoningEffortToClaudeOutputEffort(effort string) string {
	switch strings.TrimSpace(effort) {
	case "low", "medium", "high":
		return effort
	case "xhigh":
		return "max"
	default:
		return ""
	}
}

// openAICompatAnthropicReasoningEffort resolves the effort emitted by the
// Anthropic bridge after the final upstream model is known. Anthropic's max is
// normally translated to OpenAI xhigh, but GPT-5.6 accepts the original max
// value on Responses and Chat Completions.
func openAICompatAnthropicReasoningEffort(req *apicompat.AnthropicRequest, upstreamModel, convertedEffort string) string {
	if convertedEffort == "none" {
		return convertedEffort
	}
	if req == nil || req.OutputConfig == nil || !strings.EqualFold(strings.TrimSpace(req.OutputConfig.Effort), "max") {
		return convertedEffort
	}
	if normalized := normalizeOpenAIReasoningEffortForModel(req.OutputConfig.Effort, upstreamModel); normalized != "" {
		return normalized
	}
	return convertedEffort
}

// validateGPT61SolCompatRequest runs after model mapping on the compatibility
// paths, before conversion can discard an explicit none/minimal effort or a
// disabled thinking block that GPT-6.1 Sol does not support.
func validateGPT61SolCompatRequest(body []byte, model string) error {
	if !openai.IsGPT61SolModelSpelling(model) {
		return nil
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		if err := openai.ValidateGPT61SolReasoningEffort(model, gjson.GetBytes(body, path).String()); err != nil {
			return err
		}
	}
	if gjson.GetBytes(body, "thinking.type").String() == "disabled" {
		return openai.ValidateGPT61SolReasoningEffort(model, "none")
	}
	requestedModel := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	for _, effort := range []string{"none", "minimal"} {
		if strings.HasSuffix(requestedModel, "-"+effort) {
			return openai.ValidateGPT61SolReasoningEffort(model, effort)
		}
	}
	return nil
}

// errGPT61SolChatOnlyTools is returned when a GPT-6.1 Sol request with tools
// would be served through a Chat Completions-only account.
func errGPT61SolChatOnlyTools() error {
	return fmt.Errorf("gpt-6.1-sol requires Responses for tool calls; this account only supports Chat Completions")
}
