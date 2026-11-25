package apicompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

type chatMessageContent struct {
	Text  *string
	Parts []ChatContentPart
}

// ChatCompletionsToResponses converts a Chat Completions request into a
// Responses API request. The upstream always streams, so Stream is forced to
// true. store is always false and reasoning.encrypted_content is always
// included so that the response translator has full context.
func ChatCompletionsToResponses(req *ChatCompletionsRequest) (*ResponsesRequest, error) {
	converted, err := normalizeChatInputAudio(req, false)
	if err != nil {
		return nil, err
	}
	return chatCompletionsToResponses(converted)
}

func chatCompletionsToResponses(req *ChatCompletionsRequest) (*ResponsesRequest, error) {
	if err := openai.ValidateGPT61SolReasoningEffort(req.Model, req.ReasoningEffort); err != nil {
		return nil, err
	}
	input, err := convertChatMessagesToResponsesInput(req.Messages)
	if err != nil {
		return nil, err
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	out := &ResponsesRequest{
		Model:              req.Model,
		Instructions:       req.Instructions,
		Input:              inputJSON,
		Stream:             true, // upstream always streams
		Include:            []string{"reasoning.encrypted_content"},
		ServiceTier:        req.ServiceTier,
		PromptCacheOptions: req.PromptCacheOptions,
		ParallelToolCalls:  req.ParallelToolCalls,
	}

	// GPT-5 and later reasoning models do not accept sampling
	// parameters. See isReasoningModel in anthropic_to_responses.go.
	// GPT-6 Sol/Luna accept them again when reasoning_effort is "none".
	if !isReasoningModel(req.Model) || (openai.IsGPT6SolOrLunaModelSpelling(req.Model) && req.ReasoningEffort == "none") {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}

	storeFalse := false
	out.Store = &storeFalse

	// max_tokens / max_completion_tokens → max_output_tokens, prefer max_completion_tokens
	maxTokens := 0
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	if req.MaxCompletionTokens != nil {
		maxTokens = *req.MaxCompletionTokens
	}
	if maxTokens > 0 {
		v := maxTokens
		if v < minMaxOutputTokens {
			v = minMaxOutputTokens
		}
		out.MaxOutputTokens = &v
	}

	// reasoning_effort → reasoning.effort + reasoning.summary="auto"
	if req.ReasoningEffort != "" {
		out.Reasoning = &ResponsesReasoning{
			Effort:  req.ReasoningEffort,
			Summary: "auto",
		}
	}

	if format := chatResponseFormatToResponsesTextFormat(req.ResponseFormat); len(format) > 0 {
		if out.Text == nil {
			out.Text = &ResponsesText{}
		}
		out.Text.Format = format
	}

	// tools[] and legacy functions[] → ResponsesTool[]
	if len(req.Tools) > 0 || len(req.Functions) > 0 {
		out.Tools = convertChatToolsToResponses(req.Tools, req.Functions)
	}

	// tool_choice: strings and Responses-shaped objects pass through; a named
	// Chat choice nests the name under "function" and must be flattened.
	// Legacy function_call needs mapping.
	if len(req.ToolChoice) > 0 {
		out.ToolChoice = convertChatToolChoiceToResponses(req.ToolChoice)
	} else if len(req.FunctionCall) > 0 {
		tc, err := convertChatFunctionCallToToolChoice(req.FunctionCall)
		if err != nil {
			return nil, fmt.Errorf("convert function_call: %w", err)
		}
		out.ToolChoice = tc
	}

	return out, nil
}

// convertChatMessagesToResponsesInput converts the Chat Completions messages
// array into a Responses API input items array.
func convertChatMessagesToResponsesInput(msgs []ChatMessage) ([]ResponsesInputItem, error) {
	var out []ResponsesInputItem
	legacyIDs := legacyFunctionCallIDs{msgs: msgs}
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && m.FunctionCall != nil && len(m.ToolCalls) == 0:
			m.ToolCalls = []ChatToolCall{{
				ID:       legacyIDs.assign(m.FunctionCall.Name),
				Type:     "function",
				Function: *m.FunctionCall,
			}}
		case m.Role == "function" && m.ToolCallID == "":
			m.ToolCallID = legacyIDs.claim(m.Name)
		}
		items, err := chatMessageToResponsesItems(m)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// legacyFunctionCallIDs pairs legacy function calls with their results. Legacy
// function calling carries no call IDs: the assistant turn has function_call
// and the role=function result refers to it only by name, while the Responses
// API pairs function_call and function_call_output by call_id. IDs follow
// message order, so a replayed history converts to the same input, and skip
// IDs already used by tool_calls in the same conversation.
type legacyFunctionCallIDs struct {
	msgs    []ChatMessage
	used    map[string]bool
	next    int
	pending map[string][]string
}

// assign returns a new call ID for a legacy function call and queues it for
// the next result with the same function name.
func (ids *legacyFunctionCallIDs) assign(name string) string {
	if ids.used == nil {
		ids.used = make(map[string]bool)
		ids.pending = make(map[string][]string)
		for _, m := range ids.msgs {
			for _, tc := range m.ToolCalls {
				ids.used[tc.ID] = true
			}
			if m.ToolCallID != "" {
				ids.used[m.ToolCallID] = true
			}
		}
	}
	var id string
	for {
		ids.next++
		id = fmt.Sprintf("call_legacy_%d", ids.next)
		if !ids.used[id] {
			break
		}
	}
	ids.used[id] = true
	ids.pending[name] = append(ids.pending[name], id)
	return id
}

// claim returns the oldest unanswered call ID for name, or "" when no legacy
// call with that name precedes the result.
func (ids *legacyFunctionCallIDs) claim(name string) string {
	queue := ids.pending[name]
	if len(queue) == 0 {
		return ""
	}
	ids.pending[name] = queue[1:]
	return queue[0]
}

// chatMessageToResponsesItems converts a single ChatMessage into one or more
// ResponsesInputItem values.
func chatMessageToResponsesItems(m ChatMessage) ([]ResponsesInputItem, error) {
	switch m.Role {
	case "system", "developer":
		return chatSystemToResponses(m)
	case "user":
		return chatUserToResponses(m)
	case "assistant":
		return chatAssistantToResponses(m)
	case "tool":
		return chatToolToResponses(m)
	case "function":
		return chatFunctionToResponses(m)
	default:
		return chatUserToResponses(m)
	}
}

// chatSystemToResponses converts a system or developer message, keeping its
// role: the Responses API accepts both, and developer must not be demoted to
// a user turn.
func chatSystemToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	parsed, err := parseChatMessageContent(m.Content)
	if err != nil {
		return nil, err
	}
	content, err := marshalChatInputContent(parsed)
	if err != nil {
		return nil, err
	}
	return []ResponsesInputItem{{Type: "message", Role: m.Role, Content: content}}, nil
}

// chatUserToResponses converts a user message, handling both plain strings and
// multi-modal content arrays.
func chatUserToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	parsed, err := parseChatMessageContent(m.Content)
	if err != nil {
		return nil, fmt.Errorf("parse user content: %w", err)
	}
	content, err := marshalChatInputContent(parsed)
	if err != nil {
		return nil, err
	}
	return []ResponsesInputItem{{Type: "message", Role: "user", Content: content}}, nil
}

// chatAssistantToResponses converts an assistant message. If there is both
// text content and tool_calls, the text is emitted as an assistant message
// first, then each tool_call becomes a function_call item. If the content is
// empty/nil and there are tool_calls, only function_call items are emitted.
func chatAssistantToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	var items []ResponsesInputItem
	content := ""

	// 客户端带回的 reasoning_details（type=reasoning.encrypted）还原成 Responses reasoning 项，
	// 排在本条 assistant 消息对应的所有 input 项之前。已有有效加密项时不再把
	// reasoning_content 拼成 <thinking> 文本（避免同一段推理重复）；没有时保持原行为。
	reasoningItems := reasoningEncryptedInputItems(m.ReasoningDetails)
	if m.ReasoningContent != "" && len(reasoningItems) == 0 {
		content = "<thinking>" + m.ReasoningContent + "</thinking>"
	}

	// Emit assistant message with output_text if content is non-empty.
	if len(m.Content) > 0 {
		s, err := parseAssistantContent(m.Content)
		if err != nil {
			return nil, err
		}
		if s != "" {
			if content != "" {
				content += "\n"
			}
			content += s
		}
	}

	if content != "" {
		parts := []ResponsesContentPart{{Type: "output_text", Text: content}}
		partsJSON, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		items = append(items, ResponsesInputItem{Type: "message", Role: "assistant", Content: partsJSON})
	}

	// Emit one function_call item per tool_call.
	for _, tc := range m.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		items = append(items, ResponsesInputItem{
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	if len(reasoningItems) == 0 {
		return items, nil
	}
	if len(items) == 0 {
		// 除了 reasoning 项之外没有 message / function_call：孤立的 reasoning 项上游不接受，
		// 在它后面补一条空的 assistant message 来保留密文（signature），不再拼 <thinking>。
		items = append(items, ResponsesInputItem{
			Type:    "message",
			Role:    "assistant",
			Content: json.RawMessage(`[{"type":"output_text","text":""}]`),
		})
	}
	return append(reasoningItems, items...), nil
}

// parseAssistantContent returns assistant content as plain text.
//
// Supported formats:
// - JSON string
// - JSON array of typed parts (e.g. [{"type":"text","text":"..."}])
//
// For structured thinking/reasoning parts, it preserves semantics by wrapping
// the text in explicit tags so downstream can still distinguish it from normal text.
func parseAssistantContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		// Keep compatibility with prior behavior: unsupported assistant content
		// formats are ignored instead of failing the whole request conversion.
		return "", nil
	}

	var b strings.Builder
	write := func(v string) error {
		_, err := b.WriteString(v)
		return err
	}
	for _, p := range parts {
		typ, _ := p["type"].(string)
		text, _ := p["text"].(string)
		thinking, _ := p["thinking"].(string)

		switch typ {
		case "thinking", "reasoning":
			if thinking != "" {
				if err := write("<thinking>"); err != nil {
					return "", err
				}
				if err := write(thinking); err != nil {
					return "", err
				}
				if err := write("</thinking>"); err != nil {
					return "", err
				}
			} else if text != "" {
				if err := write("<thinking>"); err != nil {
					return "", err
				}
				if err := write(text); err != nil {
					return "", err
				}
				if err := write("</thinking>"); err != nil {
					return "", err
				}
			}
		default:
			if text != "" {
				if err := write(text); err != nil {
					return "", err
				}
			}
		}
	}

	return b.String(), nil
}

// chatToolToResponses converts a tool result message (role=tool) into a
// function_call_output item.
func chatToolToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	output, err := parseChatContent(m.Content)
	if err != nil {
		return nil, err
	}
	if output == "" {
		output = "(empty)"
	}
	return []ResponsesInputItem{{
		Type:   "function_call_output",
		CallID: m.ToolCallID,
		Output: output,
	}}, nil
}

// chatFunctionToResponses converts a legacy function result message
// (role=function) into a function_call_output item. ToolCallID holds the ID
// assigned to the matching legacy call (see legacyFunctionCallIDs); without a
// matching call the Name field is used as call_id.
func chatFunctionToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	output, err := parseChatContent(m.Content)
	if err != nil {
		return nil, err
	}
	if output == "" {
		output = "(empty)"
	}
	callID := m.ToolCallID
	if callID == "" {
		callID = m.Name
	}
	return []ResponsesInputItem{{
		Type:   "function_call_output",
		CallID: callID,
		Output: output,
	}}, nil
}

// parseChatContent returns the string value of a ChatMessage Content field.
// Content can be a JSON string or an array of typed parts. Array content is
// flattened to text by concatenating text parts and ignoring non-text parts.
func parseChatContent(raw json.RawMessage) (string, error) {
	parsed, err := parseChatMessageContent(raw)
	if err != nil {
		return "", err
	}
	if parsed.Text != nil {
		return *parsed.Text, nil
	}
	return flattenChatContentParts(parsed.Parts), nil
}

func parseChatMessageContent(raw json.RawMessage) (chatMessageContent, error) {
	if len(raw) == 0 {
		return chatMessageContent{Text: stringPtr("")}, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return chatMessageContent{Text: &s}, nil
	}

	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return chatMessageContent{Parts: parts}, nil
	}

	return chatMessageContent{}, fmt.Errorf("parse content as string or parts array")
}

func marshalChatInputContent(content chatMessageContent) (json.RawMessage, error) {
	if content.Text != nil {
		return json.Marshal(*content.Text)
	}
	parts := convertChatContentPartsToResponses(content.Parts)
	if len(parts) == 0 {
		// A nil slice marshals to JSON null, which the upstream Responses API
		// rejects ("expected an array of objects or string, but got null").
		// Fall back to an empty string when no usable parts remain.
		return json.Marshal("")
	}
	return json.Marshal(parts)
}

func convertChatContentPartsToResponses(parts []ChatContentPart) []ResponsesContentPart {
	var responseParts []ResponsesContentPart
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" || len(p.PromptCacheBreakpoint) > 0 {
				responseParts = append(responseParts, ResponsesContentPart{
					PromptCacheBreakpoint: p.PromptCacheBreakpoint,
					Type:                  "input_text",
					Text:                  p.Text,
				})
			}
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" && !isEmptyBase64DataURI(p.ImageURL.URL) {
				responseParts = append(responseParts, ResponsesContentPart{
					PromptCacheBreakpoint: p.PromptCacheBreakpoint,
					Type:                  "input_image",
					ImageURL:              p.ImageURL.URL,
				})
			}
		case "file", geminiChatAudioFileType:
			if p.File != nil && (p.File.FileData != "" || p.File.FileID != "") {
				partType := "input_file"
				if p.Type == geminiChatAudioFileType {
					partType = geminiChatAudioFileType
				}
				responseParts = append(responseParts, ResponsesContentPart{
					PromptCacheBreakpoint: p.PromptCacheBreakpoint,
					Type:                  partType,
					Filename:              p.File.Filename,
					FileData:              p.File.FileData,
					FileID:                p.File.FileID,
				})
			}
		}
	}
	return responseParts
}

func isEmptyBase64DataURI(raw string) bool {
	if !strings.HasPrefix(raw, "data:") {
		return false
	}
	rest := strings.TrimPrefix(raw, "data:")
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return false
	}
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, "base64,")) == ""
}

func flattenChatContentParts(parts []ChatContentPart) string {
	var textParts []string
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			textParts = append(textParts, p.Text)
		}
	}
	return strings.Join(textParts, "")
}

func stringPtr(s string) *string {
	return &s
}

// convertChatToolsToResponses maps Chat Completions tool definitions and legacy
// function definitions to Responses API tool definitions.
func convertChatToolsToResponses(tools []ChatTool, functions []ChatFunction) []ResponsesTool {
	var out []ResponsesTool

	for _, t := range tools {
		if t.Type != "function" || t.Function == nil {
			continue
		}
		rt := ResponsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      defaultStrictFalse(t.Function.Strict),
		}
		out = append(out, rt)
	}

	// Legacy functions[] are treated as function-type tools.
	for _, f := range functions {
		rt := ResponsesTool{
			Type:        "function",
			Name:        f.Name,
			Description: f.Description,
			Parameters:  f.Parameters,
			Strict:      defaultStrictFalse(f.Strict),
		}
		out = append(out, rt)
	}

	return out
}

func defaultStrictFalse(src *bool) *bool {
	if src == nil {
		value := false
		return &value
	}
	return src
}

// convertChatToolChoiceToResponses maps a Chat Completions tool_choice to the
// Responses API shape.
//
//	{"type":"function","function":{"name":"X"}} → {"type":"function","name":"X"}
//
// Strings ("auto", "none", "required") and objects that already use the
// Responses shape are returned unchanged.
func convertChatToolChoiceToResponses(raw json.RawMessage) json.RawMessage {
	var choice struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil || choice.Type != "function" || choice.Function == nil {
		return raw
	}
	name := strings.TrimSpace(choice.Function.Name)
	if name == "" {
		return raw
	}
	flat, err := json.Marshal(map[string]string{"type": "function", "name": name})
	if err != nil {
		return raw
	}
	return flat
}

// convertChatFunctionCallToToolChoice maps the legacy function_call field to a
// Responses API tool_choice value.
//
//	"auto" → "auto"
//	"none" → "none"
//	{"name":"X"} → {"type":"function","name":"X"}
func convertChatFunctionCallToToolChoice(raw json.RawMessage) (json.RawMessage, error) {
	// Try string first ("auto", "none", etc.) — pass through as-is.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal(s)
	}

	// Object form: {"name":"X"}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"type": "function",
		"name": obj.Name,
	})
}

// chatReasoningDetailIn 是请求方向宽松解析出的 reasoning_details 元素。
type chatReasoningDetailIn struct {
	key     float64
	Type    string
	Data    string
	ID      string
	Format  string
	Summary string
	Text    string
}

// lenientString 取 JSON 字符串值；缺失或类型不对时按缺省（空串）处理。
func lenientString(raw json.RawMessage) string {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v
}

// lenientIndex 接受 JSON 数字或数字字符串；其他一律视为缺省。
func lenientIndex(raw json.RawMessage) (float64, bool) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(str), 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseChatReasoningDetails 宽松解析 reasoning_details：不是数组、或元素不是对象、或 type/data 不是字符串时跳过该元素；
// summary / text / id / format / index 类型不对时当作缺省，元素本身保留。
// 结果按 index 排序（缺 index 的按其在数组中的位置），相同时保持数组顺序。
func parseChatReasoningDetails(raw json.RawMessage) []chatReasoningDetailIn {
	if len(raw) == 0 {
		return nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil
	}
	var out []chatReasoningDetailIn
	for pos, elem := range elems {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(elem, &fields); err != nil {
			continue
		}
		var d chatReasoningDetailIn
		if v, ok := fields["type"]; ok {
			if err := json.Unmarshal(v, &d.Type); err != nil {
				continue
			}
		}
		if v, ok := fields["data"]; ok {
			if err := json.Unmarshal(v, &d.Data); err != nil {
				continue
			}
		}
		d.ID = lenientString(fields["id"])
		d.Format = lenientString(fields["format"])
		d.Summary = lenientString(fields["summary"])
		d.Text = lenientString(fields["text"])
		d.key = float64(pos)
		if idx, ok := lenientIndex(fields["index"]); ok {
			d.key = idx
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// reasoningEncryptedInputItems 把 assistant 消息上的 reasoning_details 还原成 Responses reasoning 输入项。
// 只认 type=="reasoning.encrypted"、data 非空、format 为 "openai-responses-v1" 或缺省的元素；
// 其他 type（reasoning.summary / reasoning.text 等）和其他 format 一律忽略，无法解析的元素同样忽略。
func reasoningEncryptedInputItems(raw json.RawMessage) []ResponsesInputItem {
	var out []ResponsesInputItem
	for _, d := range parseChatReasoningDetails(raw) {
		if d.Type != ChatReasoningDetailTypeEncrypted || d.Data == "" {
			continue
		}
		if d.Format != "" && d.Format != ChatReasoningDetailFormatResponsesV1 {
			continue
		}
		id := d.ID
		if !validReasoningInputItemID(id) {
			// 上游对 input item id 有格式要求，不合规的 id 会整包 400；省略 id，密文项照样保留。
			id = ""
		}
		out = append(out, ResponsesInputItem{
			Type:             "reasoning",
			ID:               id,
			Summary:          json.RawMessage("[]"),
			EncryptedContent: d.Data,
		})
	}
	return out
}

// validReasoningInputItemID 与 service.shouldStripOpenAIResponsesInputItemID 对 reasoning 项的口径一致：
// 前缀 "rs"、长度不超过 64。
func validReasoningInputItemID(id string) bool {
	return len(id) <= 64 && strings.HasPrefix(id, "rs")
}

// reasoningDetailsPlainText 取 reasoning_details 里 reasoning.summary / reasoning.text 元素的文本，按顺序用空行连接。
func reasoningDetailsPlainText(raw json.RawMessage) string {
	text := ""
	for _, d := range parseChatReasoningDetails(raw) {
		switch d.Type {
		case ChatReasoningDetailTypeSummary:
			if d.Summary != "" {
				text = joinReasoningParagraphs(text, d.Summary)
			}
		case ChatReasoningDetailTypeText:
			if d.Text != "" {
				text = joinReasoningParagraphs(text, d.Text)
			}
		}
	}
	return text
}

// NormalizeChatReasoningInput 把 OpenRouter 风格的推理回传统一折进 reasoning_content，供 <thinking> 文本方式使用
// （仅在 gateway.chat_completions_reasoning_details 开启时由网关调用）：
//   - assistant 消息没有 reasoning_content 时，先取别名 reasoning；
//   - 仍为空时，取 reasoning_details 里 reasoning.summary / reasoning.text 元素的文本；
//   - 消息带有有效的 reasoning.encrypted 元素时不动（由 reasoning 项还原，不拼 <thinking>）。
//
// 就地修改 req.Messages。
func NormalizeChatReasoningInput(req *ChatCompletionsRequest) {
	if req == nil {
		return
	}
	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Role != "assistant" || m.ReasoningContent != "" || len(reasoningEncryptedInputItems(m.ReasoningDetails)) > 0 {
			continue
		}
		if m.Reasoning != "" {
			m.ReasoningContent = m.Reasoning
			continue
		}
		m.ReasoningContent = reasoningDetailsPlainText(m.ReasoningDetails)
	}
}
