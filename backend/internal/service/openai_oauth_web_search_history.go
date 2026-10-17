package service

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The ChatGPT internal Codex endpoint rejects a request whose input replays a
// hosted web_search_call item unless the request also declares the web_search
// tool; the stream then fails with "response protection is unavailable".
// Codex's local context compaction sends the full history with tools:[], so
// every compaction after a web search fails (#7927).
//
// Declare a cached-only web_search tool for such requests. Responses Lite
// rejects hosted tools at the top level, so Lite requests carry it in an
// input additional_tools item instead. When the caller declared no tools at
// all, also pin tool_choice to "none" so the injected tool cannot be invoked
// and the request keeps its no-tools semantics.

const (
	openAIWebSearchCallItemType      = "web_search_call"
	openAIAdditionalToolsItemType    = "additional_tools"
	openAICompactionTriggerItemType  = "compaction_trigger"
	openAIAdditionalToolsDefaultRole = "developer"
)

var openAIWebSearchHistoryTool = map[string]any{
	"type":                "web_search",
	"external_web_access": false,
}

func isOpenAIWebSearchToolType(toolType string) bool {
	return strings.HasPrefix(strings.TrimSpace(toolType), "web_search")
}

func openAIToolsContainWebSearch(rawTools any) bool {
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if ok && isOpenAIWebSearchToolType(firstNonEmptyString(tool["type"])) {
			return true
		}
	}
	return false
}

// shouldPinOpenAIWebSearchHistoryToolChoice reports whether tool_choice may be
// forced to "none": only when the caller offered no tools, so the choice
// cannot meaningfully be anything else.
func shouldPinOpenAIWebSearchHistoryToolChoice(choice any) bool {
	switch typed := choice.(type) {
	case nil:
		return true
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "" || normalized == "auto" || normalized == "none"
	default:
		return false
	}
}

// openAIAdditionalToolsInsertIndex keeps a trailing compaction trigger last,
// as required by the remote compaction v2 wire format.
func openAIAdditionalToolsInsertIndex(itemTypes []string) int {
	if n := len(itemTypes); n > 0 && itemTypes[n-1] == openAICompactionTriggerItemType {
		return n - 1
	}
	return len(itemTypes)
}

// ensureOpenAIOAuthWebSearchToolForHistory is the map variant used by the
// Codex OAuth transform.
func ensureOpenAIOAuthWebSearchToolForHistory(reqBody map[string]any, responsesLite bool) bool {
	if reqBody == nil {
		return false
	}
	if rawTools, exists := reqBody["tools"]; exists && rawTools != nil {
		if _, ok := rawTools.([]any); !ok {
			return false // Preserve the caller's existing invalid declaration.
		}
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	hasWebSearchCall := false
	callerDeclaredTools := false
	additionalToolsIndex := -1
	itemTypes := make([]string, len(input))
	for i, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemTypes[i] = strings.TrimSpace(firstNonEmptyString(item["type"]))
		switch itemTypes[i] {
		case openAIWebSearchCallItemType:
			hasWebSearchCall = true
		case openAIAdditionalToolsItemType:
			if rawTools, exists := item["tools"]; exists && rawTools != nil {
				if _, ok := rawTools.([]any); !ok {
					return false
				}
			}
			if openAIToolsContainWebSearch(item["tools"]) {
				return false
			}
			if tools, _ := item["tools"].([]any); len(tools) > 0 {
				callerDeclaredTools = true
			}
			if additionalToolsIndex < 0 {
				additionalToolsIndex = i
			}
		}
	}
	if !hasWebSearchCall || openAIToolsContainWebSearch(reqBody["tools"]) {
		return false
	}
	tools, _ := reqBody["tools"].([]any)
	callerDeclaredTools = callerDeclaredTools || len(tools) > 0

	switch {
	case !responsesLite:
		reqBody["tools"] = append(tools, cloneOpenAIWebSearchHistoryTool())
	case additionalToolsIndex >= 0:
		// additionalToolsIndex is only recorded for map items.
		item, _ := input[additionalToolsIndex].(map[string]any)
		existing, _ := item["tools"].([]any)
		item["tools"] = append(existing, cloneOpenAIWebSearchHistoryTool())
	default:
		at := openAIAdditionalToolsInsertIndex(itemTypes)
		additional := map[string]any{
			"type":  openAIAdditionalToolsItemType,
			"role":  openAIAdditionalToolsDefaultRole,
			"tools": []any{cloneOpenAIWebSearchHistoryTool()},
		}
		next := make([]any, 0, len(input)+1)
		next = append(next, input[:at]...)
		next = append(next, additional)
		next = append(next, input[at:]...)
		reqBody["input"] = next
	}
	if !callerDeclaredTools {
		if choice, exists := reqBody["tool_choice"]; !exists || shouldPinOpenAIWebSearchHistoryToolChoice(choice) {
			reqBody["tool_choice"] = "none"
		}
	}
	return true
}

// ensureOpenAIOAuthWebSearchToolForHistoryBody is the raw-body variant used by
// the passthrough and WebSocket paths; it avoids decoding the whole body.
func ensureOpenAIOAuthWebSearchToolForHistoryBody(body []byte, responsesLite bool) ([]byte, bool, error) {
	if len(body) == 0 || !bytes.Contains(body, []byte(openAIWebSearchCallItemType)) || !gjson.ValidBytes(body) {
		return body, false, nil
	}
	rawTools := gjson.GetBytes(body, "tools")
	if rawTools.Exists() && rawTools.Type != gjson.Null && !rawTools.IsArray() {
		return body, false, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}
	items := input.Array()
	hasWebSearchCall := false
	callerDeclaredTools := false
	additionalToolsIndex := -1
	itemTypes := make([]string, len(items))
	for i, item := range items {
		itemTypes[i] = strings.TrimSpace(item.Get("type").String())
		switch itemTypes[i] {
		case openAIWebSearchCallItemType:
			hasWebSearchCall = true
		case openAIAdditionalToolsItemType:
			itemTools := item.Get("tools")
			if itemTools.Exists() && itemTools.Type != gjson.Null && !itemTools.IsArray() {
				return body, false, nil
			}
			if gjsonToolsContainWebSearch(itemTools) {
				return body, false, nil
			}
			if itemTools.IsArray() && len(itemTools.Array()) > 0 {
				callerDeclaredTools = true
			}
			if additionalToolsIndex < 0 {
				additionalToolsIndex = i
			}
		}
	}
	if !hasWebSearchCall {
		return body, false, nil
	}
	tools := gjson.GetBytes(body, "tools")
	if gjsonToolsContainWebSearch(tools) {
		return body, false, nil
	}
	topLevelTools := tools.IsArray() && len(tools.Array()) > 0
	callerDeclaredTools = callerDeclaredTools || topLevelTools

	var (
		next []byte
		err  error
	)
	switch {
	case !responsesLite && topLevelTools:
		next, err = sjson.SetBytes(body, "tools.-1", cloneOpenAIWebSearchHistoryTool())
	case !responsesLite:
		next, err = sjson.SetBytes(body, "tools", []any{cloneOpenAIWebSearchHistoryTool()})
	case additionalToolsIndex >= 0 && items[additionalToolsIndex].Get("tools").IsArray():
		next, err = sjson.SetBytes(body, fmt.Sprintf("input.%d.tools.-1", additionalToolsIndex), cloneOpenAIWebSearchHistoryTool())
	case additionalToolsIndex >= 0:
		next, err = sjson.SetBytes(body, fmt.Sprintf("input.%d.tools", additionalToolsIndex), []any{cloneOpenAIWebSearchHistoryTool()})
	default:
		next, err = insertOpenAIAdditionalToolsItemRaw(body, items, openAIAdditionalToolsInsertIndex(itemTypes))
	}
	if err != nil {
		return body, false, fmt.Errorf("declare web_search tool for web_search_call history: %w", err)
	}
	if !callerDeclaredTools {
		choice := gjson.GetBytes(next, "tool_choice")
		if !choice.Exists() || choice.Type == gjson.Null || (choice.Type == gjson.String && shouldPinOpenAIWebSearchHistoryToolChoice(choice.String())) {
			next, err = sjson.SetBytes(next, "tool_choice", "none")
			if err != nil {
				return body, false, fmt.Errorf("pin tool_choice for web_search_call history: %w", err)
			}
		}
	}
	return next, true, nil
}

func insertOpenAIAdditionalToolsItemRaw(body []byte, items []gjson.Result, at int) ([]byte, error) {
	additional, err := marshalOpenAIUpstreamJSON(map[string]any{
		"type":  openAIAdditionalToolsItemType,
		"role":  openAIAdditionalToolsDefaultRole,
		"tools": []any{cloneOpenAIWebSearchHistoryTool()},
	})
	if err != nil {
		return nil, err
	}
	rawItems := make([][]byte, 0, len(items)+1)
	for i, item := range items {
		if i == at {
			rawItems = append(rawItems, additional)
		}
		rawItems = append(rawItems, []byte(item.Raw))
	}
	if at >= len(items) {
		rawItems = append(rawItems, additional)
	}
	raw := append([]byte{'['}, bytes.Join(rawItems, []byte{','})...)
	raw = append(raw, ']')
	return sjson.SetRawBytes(body, "input", raw)
}

func gjsonToolsContainWebSearch(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isOpenAIWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

func cloneOpenAIWebSearchHistoryTool() map[string]any {
	tool := make(map[string]any, len(openAIWebSearchHistoryTool))
	for key, value := range openAIWebSearchHistoryTool {
		tool[key] = value
	}
	return tool
}

// normalizeOpenAIOAuthWebSearchHistoryForAccount confines the raw rewrite to
// OAuth Responses. Lite callers run it before namespace normalization so that
// the existing normalizer extends the injected carrier before the final trigger.
func normalizeOpenAIOAuthWebSearchHistoryForAccount(body []byte, account *Account, responsesLite, compact bool) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAIOAuth() || compact {
		return body, false, nil
	}
	return ensureOpenAIOAuthWebSearchToolForHistoryBody(body, responsesLite)
}

// A v2 response.create may omit declarations supplied by session.update.
// Project those effective fields only when this frame needs history repair;
// otherwise retain the original frame and let the upstream inherit them.
func normalizeOpenAIOAuthWebSearchHistoryForSession(body []byte, account *Account, responsesLite bool, sessionTools, sessionChoice string) ([]byte, bool, error) {
	out, changed, err := normalizeOpenAIOAuthWebSearchHistoryForAccount(body, account, responsesLite, false)
	if err != nil || !changed || (sessionTools == "" && sessionChoice == "") {
		return out, changed, err
	}
	effective := body
	if !gjson.GetBytes(body, "tools").Exists() && sessionTools != "" {
		effective, err = sjson.SetRawBytes(effective, "tools", []byte(sessionTools))
		if err != nil {
			return body, false, fmt.Errorf("inherit session tools for web search history: %w", err)
		}
	}
	if !gjson.GetBytes(body, "tool_choice").Exists() && sessionChoice != "" {
		effective, err = sjson.SetRawBytes(effective, "tool_choice", []byte(sessionChoice))
		if err != nil {
			return body, false, fmt.Errorf("inherit session tool choice for web search history: %w", err)
		}
	}
	out, changed, err = ensureOpenAIOAuthWebSearchToolForHistoryBody(effective, responsesLite)
	if err != nil || !changed {
		return body, false, err
	}
	return out, true, nil
}
