package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Grok has no native /responses/compact endpoint, so compact is emulated with
// a normal Responses turn whose final user item requests a structured summary.
const grokCompactSummaryPrompt = `Your task is to produce a faithful, concise summary of the conversation so far so that a successor assistant can continue the work seamlessly after the earlier turns are discarded. The successor will see the user's original query plus this summary. Capture what is needed to continue: the user's explicit requests, your most recent actions, key technical details, file paths, commands, configuration, and architectural decisions. Be economical and aim for at most a few thousand words.

If earlier turns include a prior compaction summary marked with <conversation_summary> tags or a "This session is being continued" preamble, treat it as authoritative for the early history and carry its still-relevant information forward.

Think through the conversation privately and output the final summary inside one <summary>...</summary> block. Include every numbered section heading even if a section is empty:

1. Primary Request and Intent
2. Key Technical Concepts
3. Files and Code Sections
4. Errors and Fixes
5. Problem Solving
6. All User Messages
7. Pending Tasks
8. Current Work
9. Optional Next Step

Respond with ONLY the <summary>...</summary> block.`

func buildGrokCompactRequestBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode compact request: %w", err)
	}

	input, err := normalizeGrokCompactInput(payload["input"])
	if err != nil {
		return nil, err
	}
	input = append(input, map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": grokCompactSummaryPrompt,
		}},
	})
	payload["input"] = input
	payload["include"] = []any{"reasoning.encrypted_content"}
	payload["store"] = false
	payload["stream"] = false
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		payload["tool_choice"] = "none"
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode compact request: %w", err)
	}
	return encoded, nil
}

func normalizeGrokCompactInput(value any) ([]any, error) {
	switch input := value.(type) {
	case nil:
		return []any{}, nil
	case []any:
		return input, nil
	case string:
		return []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": input,
			}},
		}}, nil
	case map[string]any:
		return []any{input}, nil
	default:
		return nil, fmt.Errorf("compact input must be a string, object, or array")
	}
}

// convertOpenAICompactInputsForGrok restores compaction items from previous
// turns to the reasoning plus summary representation accepted by xAI.
func convertOpenAICompactInputsForGrok(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	items, ok := payload["input"].([]any)
	if !ok {
		return body, nil
	}

	changed := false
	converted := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !isOpenAICompactionType(stringValue(item["type"])) {
			converted = append(converted, raw)
			continue
		}
		changed = true
		if encrypted := strings.TrimSpace(stringValue(item["encrypted_content"])); encrypted != "" {
			converted = append(converted, map[string]any{
				"type":              "reasoning",
				"summary":           []any{},
				"encrypted_content": encrypted,
			})
		}
		if summary := compactSummaryText(item["summary"]); summary != "" {
			converted = append(converted, map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{map[string]any{
					"type": "input_text",
					"text": "<conversation_summary>\n" + summary + "\n</conversation_summary>",
				}},
			})
		}
	}
	if !changed {
		return body, nil
	}
	payload["input"] = converted
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func convertGrokResponseToOpenAICompact(body []byte) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	output, ok := response["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("response has no output array")
	}

	var encrypted string
	var summaryParts []string
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringValue(item["type"])) {
		case "reasoning":
			if value := strings.TrimSpace(stringValue(item["encrypted_content"])); value != "" {
				encrypted = value
			}
		case "message":
			if content, ok := item["content"].([]any); ok {
				for _, rawContent := range content {
					part, ok := rawContent.(map[string]any)
					if !ok {
						continue
					}
					if text := strings.TrimSpace(stringValue(part["text"])); text != "" {
						summaryParts = append(summaryParts, text)
					}
				}
			}
		}
	}
	if encrypted == "" {
		return nil, fmt.Errorf("response has no reasoning.encrypted_content")
	}

	compactItem := map[string]any{
		"id":                "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":              "compaction",
		"status":            "completed",
		"encrypted_content": encrypted,
	}
	if summary := strings.TrimSpace(strings.Join(summaryParts, "\n")); summary != "" {
		compactItem["summary"] = []any{map[string]any{
			"type": "summary_text",
			"text": summary,
		}}
	}
	response["output"] = []any{compactItem}
	response["status"] = "completed"
	delete(response, "output_text")

	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode compact response: %w", err)
	}
	return encoded, nil
}

func compactSummaryText(value any) string {
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(stringValue(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func isOpenAICompactionType(value string) bool {
	switch strings.TrimSpace(value) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
