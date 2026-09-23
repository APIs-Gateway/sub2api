package service

import (
	"fmt"
	"reflect"
	"strings"
)

type openAIResponsesLiteValidationError struct {
	param   string
	message string
}

func (e *openAIResponsesLiteValidationError) Error() string { return e.message }

func newOpenAIResponsesLiteValidationError(param, format string, args ...any) error {
	return &openAIResponsesLiteValidationError{param: param, message: fmt.Sprintf(format, args...)}
}

// normalizeOpenAIResponsesLiteTools adapts namespace declarations to the
// input.additional_tools carrier required by the Responses Lite endpoint, and
// pins parallel_tool_calls=false, since the Responses Lite endpoint rejects
// any other value.
func normalizeOpenAIResponsesLiteTools(reqBody map[string]any) (bool, error) {
	if reqBody == nil {
		return false, nil
	}
	if err := validateOpenAIResponsesLiteParallelToolCalls(reqBody); err != nil {
		return false, err
	}
	if rawReasoning, exists := reqBody["reasoning"]; exists && rawReasoning != nil {
		if _, ok := rawReasoning.(map[string]any); !ok {
			return false, newOpenAIResponsesLiteValidationError("reasoning", "responses Lite requires reasoning to be an object")
		}
	}
	rawTools, exists := reqBody["tools"]
	if !exists || rawTools == nil {
		changed := ensureOpenAIResponsesLiteReasoningContext(reqBody)
		return ensureOpenAIResponsesLiteParallelToolCalls(reqBody, changed), nil
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false, newOpenAIResponsesLiteValidationError("tools", "responses Lite requires tools to be an array")
	}

	topLevelTools := make([]any, 0, len(tools))
	namespaceTools := make([]any, 0, len(tools))
	for index, rawTool := range tools {
		if customTool, ok := rawTool.(string); ok {
			if strings.TrimSpace(customTool) == "" {
				return false, fmt.Errorf("responses Lite custom tool at index %d must not be empty", index)
			}
			topLevelTools = append(topLevelTools, rawTool)
			continue
		}
		tool, ok := rawTool.(map[string]any)
		if !ok {
			return false, fmt.Errorf("responses Lite tool at index %d must be an object", index)
		}
		toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
		switch toolType {
		case "function", "custom", "tool_search":
			topLevelTools = append(topLevelTools, rawTool)
		case "namespace":
			namespaceTools = append(namespaceTools, rawTool)
		case "":
			return false, fmt.Errorf("responses Lite tool at index %d is missing type", index)
		default:
			return false, fmt.Errorf("responses Lite does not support top-level tool type %q at index %d", toolType, index)
		}
	}
	if len(namespaceTools) == 0 {
		changed := ensureOpenAIResponsesLiteReasoningContext(reqBody)
		return ensureOpenAIResponsesLiteParallelToolCalls(reqBody, changed), nil
	}

	input, err := appendOpenAIResponsesLiteAdditionalTools(reqBody["input"], namespaceTools)
	if err != nil {
		return false, err
	}
	reqBody["input"] = input
	if len(topLevelTools) == 0 {
		delete(reqBody, "tools")
	} else {
		reqBody["tools"] = topLevelTools
	}
	ensureOpenAIResponsesLiteReasoningContext(reqBody)
	ensureOpenAIResponsesLiteParallelToolCalls(reqBody, true)
	return true, nil
}

// ensureOpenAIResponsesLiteParallelToolCalls pins parallel_tool_calls to
// false on every Responses Lite request, with or without tools: the Lite
// endpoint rejects any other value (including the implicit default true when
// the field is absent) with 400 "X-OpenAI-Internal-Codex-Responses-Lite
// requires parallel_tool_calls to be false". The boolean-ness of an existing
// value has already been validated by the caller.
func ensureOpenAIResponsesLiteParallelToolCalls(reqBody map[string]any, changed bool) bool {
	if parallel, ok := reqBody["parallel_tool_calls"].(bool); ok && !parallel {
		return changed
	}
	reqBody["parallel_tool_calls"] = false
	return true
}

// validateOpenAIResponsesLiteParallelToolCalls rejects a present but
// non-boolean parallel_tool_calls before any Lite normalization mutates the
// request, so the client gets a 400 instead of a silently rewritten value.
func validateOpenAIResponsesLiteParallelToolCalls(reqBody map[string]any) error {
	parallel, exists := reqBody["parallel_tool_calls"]
	if !exists {
		return nil
	}
	if _, ok := parallel.(bool); !ok {
		return newOpenAIResponsesLiteValidationError("parallel_tool_calls", "responses Lite requires parallel_tool_calls to be a boolean")
	}
	return nil
}

func ensureOpenAIResponsesLiteReasoningContext(reqBody map[string]any) bool {
	rawReasoning, exists := reqBody["reasoning"]
	if !exists || rawReasoning == nil {
		reqBody["reasoning"] = map[string]any{"context": "all_turns"}
		return true
	}
	reasoning, ok := rawReasoning.(map[string]any)
	if !ok {
		return false
	}
	if context, ok := reasoning["context"].(string); ok && context == "all_turns" {
		return false
	}
	reasoning["context"] = "all_turns"
	return true
}

func appendOpenAIResponsesLiteAdditionalTools(input any, namespaceTools []any) ([]any, error) {
	var items []any
	switch typed := input.(type) {
	case nil:
		items = make([]any, 0, 1)
	case string:
		items = []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": typed,
		}}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("responses Lite namespace tools require input to be a string or array")
	}

	var target map[string]any
	var targetTools []any
	var allAdditionalTools []any
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		rawAdditionalTools, exists := item["tools"]
		additionalTools := []any(nil)
		toolsOK := true
		if exists && rawAdditionalTools != nil {
			additionalTools, toolsOK = rawAdditionalTools.([]any)
		}
		if !toolsOK {
			return nil, fmt.Errorf("responses Lite input.additional_tools tools must be an array")
		}
		if target == nil {
			target = item
			targetTools = additionalTools
		}
		allAdditionalTools = append(allAdditionalTools, additionalTools...)
	}

	merged, err := mergeOpenAIResponsesLiteAdditionalTools(allAdditionalTools, namespaceTools)
	if err != nil {
		return nil, err
	}
	newTools := merged[len(allAdditionalTools):]
	if target != nil {
		if len(newTools) > 0 {
			target["tools"] = append(append([]any(nil), targetTools...), newTools...)
		}
		return items, nil
	}

	return append(items, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": newTools,
	}), nil
}

func mergeOpenAIResponsesLiteAdditionalTools(existing []any, moved []any) ([]any, error) {
	merged := append([]any(nil), existing...)
	seen := make(map[string]any, len(existing)+len(moved))
	for _, rawTool := range existing {
		if identity := openAIResponsesLiteToolIdentity(rawTool); identity != "" {
			if previous, exists := seen[identity]; exists && !reflect.DeepEqual(previous, rawTool) {
				return nil, fmt.Errorf("responses Lite additional_tools contains conflicting definitions for %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
	}
	for _, rawTool := range moved {
		identity := openAIResponsesLiteToolIdentity(rawTool)
		if identity != "" {
			if previous, exists := seen[identity]; exists {
				if reflect.DeepEqual(previous, rawTool) {
					continue
				}
				return nil, fmt.Errorf("responses Lite additional_tools conflicts with migrated %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
		merged = append(merged, rawTool)
	}
	return merged, nil
}

func openAIResponsesLiteToolIdentity(rawTool any) string {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return ""
	}
	toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
	name := strings.TrimSpace(firstNonEmptyString(tool["name"]))
	if toolType == "" || name == "" {
		return ""
	}
	return toolType + "\x00" + name
}

func openAIResponsesLiteToolIdentityForError(rawTool any) string {
	tool, _ := rawTool.(map[string]any)
	return fmt.Sprintf("tool type %q name %q", strings.TrimSpace(firstNonEmptyString(tool["type"])), strings.TrimSpace(firstNonEmptyString(tool["name"])))
}

func normalizeOpenAIResponsesLiteToolsPayload(body []byte) ([]byte, bool, error) {
	var requestBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &requestBody); err != nil {
		return body, false, fmt.Errorf("decode responses Lite request body: %w", err)
	}
	changed, err := normalizeOpenAIResponsesLiteTools(requestBody)
	if err != nil || !changed {
		return body, false, err
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}

// normalizeOpenAIResponsesLiteParallelToolCallsPayload is the API-key
// counterpart of normalizeOpenAIResponsesLiteToolsPayload. OpenAI API-key
// upstreams accept the Lite header as well and reject it with 400 unless
// parallel_tool_calls is false, but they do not use the ChatGPT-internal
// additional_tools carrier, so only parallel_tool_calls is validated and
// pinned here; tools and reasoning are forwarded unchanged.
func normalizeOpenAIResponsesLiteParallelToolCallsPayload(body []byte) ([]byte, bool, error) {
	var requestBody map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &requestBody); err != nil {
		return body, false, fmt.Errorf("decode responses Lite request body: %w", err)
	}
	if requestBody == nil {
		return body, false, nil
	}
	if err := validateOpenAIResponsesLiteParallelToolCalls(requestBody); err != nil {
		return body, false, err
	}
	if !ensureOpenAIResponsesLiteParallelToolCalls(requestBody, false) {
		return body, false, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}

// normalizeOpenAIResponsesLitePayloadForAccount applies the Responses Lite
// request contract for the selected account: OAuth accounts get the full
// ChatGPT-internal tool normalization, OpenAI API-key accounts only get the
// parallel_tool_calls pin. Non-OpenAI platforms (e.g. Grok) are untouched.
func normalizeOpenAIResponsesLitePayloadForAccount(body []byte, account *Account) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAI() {
		return body, false, nil
	}
	if account.IsOpenAIOAuth() {
		return normalizeOpenAIResponsesLiteToolsPayload(body)
	}
	return normalizeOpenAIResponsesLiteParallelToolCallsPayload(body)
}
