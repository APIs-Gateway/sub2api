package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeGrokResponsesReasoningEffort keeps the three OpenAI-compatible
// spellings accepted at ingress, while emitting only xAI-supported values.
func normalizeGrokResponsesReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	supportsEffort := grokSupportsReasoningEffort(upstreamModel)
	out := body
	var err error
	for _, field := range []string{"reasoning.effort", "reasoning_effort"} {
		value := gjson.GetBytes(out, field)
		if !value.Exists() {
			continue
		}
		normalized, keep := normalizeGrokReasoningEffortValue(value.String(), upstreamModel)
		if !supportsEffort || !keep {
			out, err = sjson.DeleteBytes(out, field)
		} else {
			out, err = sjson.SetBytes(out, field, normalized)
		}
		if err != nil {
			return nil, fmt.Errorf("normalize Grok reasoning field %s: %w", field, err)
		}
	}
	if camel := gjson.GetBytes(out, "reasoningEffort"); camel.Exists() {
		normalized, keep := normalizeGrokReasoningEffortValue(camel.String(), upstreamModel)
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, fmt.Errorf("remove Grok reasoningEffort: %w", err)
		}
		if supportsEffort && keep && !gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.SetBytes(out, "reasoning_effort", normalized)
			if err != nil {
				return nil, fmt.Errorf("set Grok reasoning_effort: %w", err)
			}
		}
	}
	if reasoning := gjson.GetBytes(out, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
		out, err = sjson.DeleteBytes(out, "reasoning")
		if err != nil {
			return nil, fmt.Errorf("remove empty Grok reasoning: %w", err)
		}
	}
	return out, nil
}

func normalizeGrokChatReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoningEffort").String())
	}
	normalized, keep := normalizeGrokReasoningEffortValue(raw, upstreamModel)
	keep = keep && grokSupportsReasoningEffort(upstreamModel)
	out := body
	var err error
	if gjson.GetBytes(out, "reasoningEffort").Exists() {
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, err
		}
	}
	if !keep {
		if gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.DeleteBytes(out, "reasoning_effort")
		}
		return out, err
	}
	return sjson.SetBytes(out, "reasoning_effort", normalized)
}

func normalizeGrokReasoningEffortValue(raw, model string) (string, bool) {
	value := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "none", "low", "medium", "high":
		return value, true
	case "minimal":
		return "low", true
	case "xhigh", "extrahigh":
		if grokSupportsXHighReasoningEffort(model) {
			return "xhigh", true
		}
		return "high", true
	case "max", "ultra":
		return "high", true
	default:
		return "", false
	}
}

func grokSupportsXHighReasoningEffort(model string) bool {
	switch grokReasoningModelID(model) {
	case "grok-4.6", "grok-4.6-latest":
		return true
	default:
		return false
	}
}

func grokSupportsReasoningEffort(model string) bool {
	switch grokReasoningModelID(model) {
	case "grok-4.3", "grok-4.3-latest", "grok-4.5", "grok-4.5-latest",
		"grok-4.6", "grok-4.6-latest", "grok-3-mini", "grok-3-mini-fast",
		"grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20-multi-agent-0309":
		return true
	default:
		return false
	}
}

func grokReasoningModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"xai/", "x-ai/", "grok/"} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(model, prefix))
		}
	}
	return model
}
