package apicompat

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → AnthropicResponse
// ---------------------------------------------------------------------------

// ResponsesToAnthropic converts a Responses API response directly into an
// Anthropic Messages response. Reasoning output items are mapped to thinking
// blocks; function_call items become tool_use blocks.
func ResponsesToAnthropic(resp *ResponsesResponse, model string) *AnthropicResponse {
	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	var blocks []AnthropicContentBlock

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			summaryText := ""
			for _, s := range item.Summary {
				if s.Type == "summary_text" && s.Text != "" {
					summaryText += s.Text
				}
			}
			// Always surface encrypted_content as thinking.signature so Claude
			// Code / multi-turn clients can send it back. Signature-only
			// thinking blocks are valid when the model omits a visible summary.
			if summaryText != "" || strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  summaryText,
					Signature: item.EncryptedContent,
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{
						Type: "text",
						Text: part.Text,
					})
				}
			}
		case "function_call":
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, item.Arguments),
			})
		case "custom_tool_call":
			input, _ := json.Marshal(map[string]string{"input": item.Input})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: input,
			})
		case "web_search_call":
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			inputJSON, _ := json.Marshal(map[string]string{"query": query})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "server_tool_use",
				ID:    toolUseID,
				Name:  "web_search",
				Input: inputJSON,
			})
			emptyResults, _ := json.Marshal([]struct{}{})
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "web_search_tool_result",
				ToolUseID: toolUseID,
				Content:   emptyResults,
			})
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	out.Content = blocks

	out.StopReason = AnthropicStopReasonPtr(responsesStatusToAnthropicStopReason(resp.Status, resp.IncompleteDetails, blocks))

	if resp.Usage != nil {
		out.Usage = anthropicUsageFromResponsesUsage(resp.Usage)
	}

	return out
}

func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	inputTokens := usage.InputTokens - cachedTokens - usage.CacheCreationInputTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

func responsesStatusToAnthropicStopReason(status string, details *ResponsesIncompleteDetails, blocks []AnthropicContentBlock) string {
	switch status {
	case "incomplete":
		if details != nil && details.Reason == "max_output_tokens" {
			return "max_tokens"
		}
		return "end_turn"
	case "completed":
		if containsAnthropicToolUseBlock(blocks) {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []AnthropicStreamEvent (stateful converter)
// ---------------------------------------------------------------------------

// responsesTextPart identifies one output_text part of a streamed response.
type responsesTextPart struct {
	OutputIndex  int
	ContentIndex int
}

type responsesAnthropicBlock struct {
	index               int
	kind                string
	toolType            string
	name                string
	itemID              string
	callID              string
	open                bool
	hadDelta            bool
	args                strings.Builder
	argsHash            hash.Hash
	argsBytes           int
	customInputStarted  bool
	customInputFinished bool
	pendingUTF8         []byte
	signature           string
}

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	MessageStartSent bool
	MessageStopSent  bool

	ContentBlockIndex int // next content block index; blocks may overlap by index

	HasToolCall bool

	// OutputIndexToBlockIdx maps Responses output_index → Anthropic content block index.
	OutputIndexToBlockIdx map[int]int
	blocksByOutput        map[int]*responsesAnthropicBlock
	textBlocks            map[responsesTextPart]*responsesAnthropicBlock
	openBlocks            map[int]*responsesAnthropicBlock
	announcedTextParts    map[responsesTextPart]bool

	// textByPart records the text already delivered for each output_text part
	// so that a done payload can be reconciled against it. It outlives the
	// content block; closing a block must not reset it.
	textByPart map[responsesTextPart]*strings.Builder
	// textDelivered records whether any assistant text reached the client.
	textDelivered bool

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// NewResponsesEventToAnthropicState returns an initialised stream state.
func NewResponsesEventToAnthropicState() *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OutputIndexToBlockIdx: make(map[int]int),
		blocksByOutput:        make(map[int]*responsesAnthropicBlock),
		textBlocks:            make(map[responsesTextPart]*responsesAnthropicBlock),
		openBlocks:            make(map[int]*responsesAnthropicBlock),
		announcedTextParts:    make(map[responsesTextPart]bool),
		textByPart:            make(map[responsesTextPart]*strings.Builder),
		Created:               time.Now().Unix(),
	}
}

// ResponsesEventToAnthropicEvents converts a single Responses SSE event into
// zero or more Anthropic SSE events, updating state as it goes.
func ResponsesEventToAnthropicEvents(
	evt *ResponsesStreamEvent,
	state *ResponsesEventToAnthropicState,
) []AnthropicStreamEvent {
	switch evt.Type {
	case "response.created":
		return resToAnthHandleCreated(evt, state)
	case "response.output_item.added":
		return resToAnthHandleOutputItemAdded(evt, state)
	case "response.output_text.delta":
		return resToAnthHandleTextDelta(evt, state)
	case "response.content_part.added":
		if evt.Part != nil && evt.Part.Type == "output_text" {
			state.announcedTextParts[resToAnthTextPartOf(evt)] = true
		}
		return nil
	case "response.output_text.done":
		return resToAnthHandleTextDone(evt, state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具的输入增量与 function_call 参数增量同形。
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.custom_tool_call_input.done":
		return resToAnthHandleFuncArgsDone(&ResponsesStreamEvent{OutputIndex: evt.OutputIndex, ItemID: evt.ItemID, Arguments: evt.Input}, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量，与 reasoning summary 一样映射为 thinking。
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		// Keep the thinking block open until response.output_item.done.
		// Grok/Codex attach encrypted_content on the finished reasoning item;
		// closing early would drop signature_delta and break multi-turn cache.
		return nil
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream emits synthetic termination events if the
// stream ended without a proper completion event.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, closeAllResponsesAnthropicBlocks(state)...)

	stopReason := "end_turn"
	if state.HasToolCall {
		stopReason = "tool_use"
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// ResponsesAnthropicEventToSSE formats an AnthropicStreamEvent as an SSE line pair.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// --- internal handlers ---

func resToAnthHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response != nil {
		state.ResponseID = evt.Response.ID
		// Only use upstream model if no override was set (e.g. originalModel)
		if state.Model == "" {
			state.Model = evt.Response.Model
		}
	}

	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true

	// Official Anthropic message_start uses stop_reason: null and usage with
	// input_tokens when known. We leave StopReason nil (JSON null) and usage
	// zeros until response.completed; never emit stop_reason:"" which breaks
	// strict clients' turn-finalization / session usage accounting.
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         state.ResponseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      state.Model,
			StopReason: nil,
			Usage: AnthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}}
}

func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// function_call 与 custom_tool_call（custom/freeform 工具，如新版 apply_patch）
	// 同样映射为 Anthropic 的 tool_use 块。
	case "function_call", "custom_tool_call":
		if block := state.blocksByOutput[evt.OutputIndex]; block != nil {
			return nil
		}
		idx := state.ContentBlockIndex
		state.ContentBlockIndex++
		block := &responsesAnthropicBlock{index: idx, kind: "tool_use", toolType: evt.Item.Type, name: evt.Item.Name, itemID: evt.Item.ID, callID: evt.Item.CallID, open: true}
		state.blocksByOutput[evt.OutputIndex] = block
		state.openBlocks[idx] = block
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		state.HasToolCall = true
		return []AnthropicStreamEvent{{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(evt.Item.CallID),
				Name:  evt.Item.Name,
				Input: json.RawMessage("{}"),
			},
		}}

	case "reasoning":
		if block := state.blocksByOutput[evt.OutputIndex]; block != nil {
			return nil
		}
		idx := state.ContentBlockIndex
		state.ContentBlockIndex++
		block := &responsesAnthropicBlock{index: idx, kind: "thinking", open: true, signature: strings.TrimSpace(evt.Item.EncryptedContent)}
		state.blocksByOutput[evt.OutputIndex] = block
		state.openBlocks[idx] = block
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		return []AnthropicStreamEvent{{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		}}

	case "message":
		return nil
	}

	return nil
}

func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	return resToAnthEmitText(evt.Delta, resToAnthTextPartOf(evt), state)
}

func resToAnthTextPartOf(evt *ResponsesStreamEvent) responsesTextPart {
	return responsesTextPart{OutputIndex: evt.OutputIndex, ContentIndex: evt.ContentIndex}
}

// resToAnthEmitText opens a text block when needed, emits text, and records it
// against its part so that a later payload for the same part is reconciled
// against what the client already received.
func resToAnthEmitText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if text == "" {
		return nil
	}

	var events []AnthropicStreamEvent

	block := state.textBlocks[part]
	if block == nil {
		idx := state.ContentBlockIndex
		state.ContentBlockIndex++
		block = &responsesAnthropicBlock{index: idx, kind: "text", open: true}
		state.textBlocks[part] = block
		state.openBlocks[idx] = block
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type: "text",
				Text: "",
			},
		})
	}
	if !block.open {
		return events
	}

	delivered, ok := state.textByPart[part]
	if !ok {
		delivered = &strings.Builder{}
		state.textByPart[part] = delivered
	}
	_, _ = delivered.WriteString(text)
	state.textDelivered = true

	idx := block.index
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: text,
		},
	})
	return events
}

// resToAnthRecoverText emits the tail of a finished text payload that never
// reached the client. Streamed text cannot be recalled, so a payload that does
// not extend what was already delivered is left alone.
func resToAnthRecoverText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	builder, known := state.textByPart[part]
	if !known && state.textDelivered && !state.announcedTextParts[part] {
		// The payload is indexed differently from every delta seen so far, so
		// which part it finishes cannot be established. Recovering it could
		// repeat an answer the client already has, which is worse than leaving
		// a partially delivered one alone.
		return nil
	}

	var delivered string
	if known {
		delivered = builder.String()
	}
	if text == delivered || !strings.HasPrefix(text, delivered) {
		return nil
	}
	return resToAnthEmitText(text[len(delivered):], part, state)
}

// resToAnthHandleTextDone recovers text that upstream carried only on the done
// event before closing the block, which some streams use instead of sending
// output_text.delta at all.
func resToAnthHandleTextDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		// The message is already terminated; a late payload cannot be delivered
		// without emitting a content block after message_stop.
		return nil
	}

	part := resToAnthTextPartOf(evt)
	events := resToAnthRecoverText(evt.Text, part, state)
	return append(events, closeResponsesAnthropicBlock(state, state.textBlocks[part])...)
}

func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}
	block := state.blocksByOutput[evt.OutputIndex]
	if block == nil || !block.open || block.kind != "tool_use" || (evt.ItemID != "" && block.itemID != "" && evt.ItemID != block.itemID) {
		return nil
	}
	if block.toolType == "function_call" && block.name == "Read" {
		_, _ = block.args.WriteString(evt.Delta)
		if block.hadDelta || !json.Valid([]byte(block.args.String())) {
			return nil
		}
		block.hadDelta = true
		sanitized := sanitizeAnthropicToolUseInput(block.name, block.args.String())
		blockIdx := block.index
		return []AnthropicStreamEvent{{
			Type:  "content_block_delta",
			Index: &blockIdx,
			Delta: &AnthropicDelta{
				Type:        "input_json_delta",
				PartialJSON: string(sanitized),
			},
		}}
	}
	block.hadDelta = true
	if block.argsHash == nil {
		block.argsHash = sha256.New()
	}
	_, _ = block.argsHash.Write([]byte(evt.Delta))
	block.argsBytes += len(evt.Delta)
	blockIdx := block.index
	fragment := evt.Delta
	if block.toolType == "custom_tool_call" {
		fragment = resToAnthCustomInputFragment(block, evt.Delta)
	}
	if fragment == "" {
		return nil
	}
	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: fragment,
		},
	}}
}

func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	block := state.blocksByOutput[evt.OutputIndex]
	if block == nil || !block.open || block.kind != "tool_use" || (evt.ItemID != "" && block.itemID != "" && evt.ItemID != block.itemID) {
		return nil
	}
	raw := evt.Arguments
	if raw == "" {
		raw = block.args.String()
	}
	if block.toolType == "custom_tool_call" {
		var events []AnthropicStreamEvent
		fragment := ""
		if block.hadDelta {
			if suffix, ok := resToAnthToolArgsSuffix(block, raw); ok {
				fragment = resToAnthCustomInputFragment(block, suffix)
			}
		} else if raw != "" {
			fragment = resToAnthCustomInputFragment(block, raw)
		}
		if fragment != "" {
			idx := block.index
			events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: fragment}})
		}
		return append(events, closeResponsesAnthropicBlock(state, block)...)
	}
	if block.hadDelta {
		var events []AnthropicStreamEvent
		if suffix, ok := resToAnthToolArgsSuffix(block, raw); ok {
			idx := block.index
			events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: suffix}})
		}
		return append(events, closeResponsesAnthropicBlock(state, block)...)
	}
	if raw == "" {
		raw = "{}"
	}
	if block.toolType == "function_call" && block.name == "Read" {
		sanitized := sanitizeAnthropicToolUseInput(block.name, raw)
		raw = string(sanitized)
	}
	blockIdx := block.index
	events := []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: raw,
		},
	}}
	events = append(events, closeResponsesAnthropicBlock(state, block)...)
	return events
}

// A Responses custom tool carries freeform text. Anthropic tool_use requires
// an object, so stream it as {"input":"..."}. Keep at most an incomplete
// UTF-8 rune across chunks; json.Marshal then escapes each complete fragment.
func resToAnthCustomInputFragment(block *responsesAnthropicBlock, raw string) string {
	combined := append(block.pendingUTF8, raw...)
	complete := 0
	for complete < len(combined) {
		if !utf8.FullRune(combined[complete:]) {
			break
		}
		_, size := utf8.DecodeRune(combined[complete:])
		complete += size
	}
	escaped, _ := json.Marshal(string(combined[:complete]))
	block.pendingUTF8 = append(block.pendingUTF8[:0], combined[complete:]...)
	fragment := string(escaped[1 : len(escaped)-1])
	if !block.customInputStarted {
		block.customInputStarted = true
		fragment = `{"input":"` + fragment
	}
	return fragment
}

func resToAnthFinishCustomInput(block *responsesAnthropicBlock) string {
	if block.customInputFinished {
		return ""
	}
	block.customInputFinished = true
	if !block.customInputStarted {
		return `{"input":""}`
	}
	escaped, _ := json.Marshal(string(block.pendingUTF8))
	block.pendingUTF8 = nil
	return string(escaped[1:len(escaped)-1]) + `"}`
}

// resToAnthToolArgsSuffix validates the already streamed prefix without
// retaining another full copy of the input. Read is deliberately excluded:
// its emitted JSON has already been sanitized and is not a raw prefix.
func resToAnthToolArgsSuffix(block *responsesAnthropicBlock, raw string) (string, bool) {
	if block == nil || (block.toolType == "function_call" && block.name == "Read") || block.argsHash == nil || len(raw) <= block.argsBytes {
		return "", false
	}
	h := sha256.New()
	for start := 0; start < block.argsBytes; start += 32 * 1024 {
		end := min(start+32*1024, block.argsBytes)
		_, _ = h.Write([]byte(raw[start:end]))
	}
	if !bytes.Equal(h.Sum(nil), block.argsHash.Sum(nil)) {
		return "", false
	}
	return raw[block.argsBytes:], true
}

func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	block := state.blocksByOutput[evt.OutputIndex]
	if block == nil || !block.open || block.kind != "thinking" {
		return nil
	}
	blockIdx := block.index
	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:     "thinking_delta",
			Thinking: evt.Delta,
		},
	}}
}

func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	// Handle web_search_call → synthesize server_tool_use + web_search_tool_result blocks.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	switch evt.Item.Type {
	case "reasoning":
		block := state.blocksByOutput[evt.OutputIndex]
		if block == nil || block.kind != "thinking" {
			return nil
		}
		if sig := strings.TrimSpace(evt.Item.EncryptedContent); sig != "" {
			block.signature = sig
		}
		return closeResponsesAnthropicBlock(state, block)
	case "function_call", "custom_tool_call":
		block := state.blocksByOutput[evt.OutputIndex]
		if block == nil || !block.open || block.kind != "tool_use" || block.toolType != evt.Item.Type {
			return nil
		}
		if evt.Item.ID != "" && block.itemID != "" && evt.Item.ID != block.itemID {
			return nil
		}
		if evt.Item.CallID != "" && block.callID != "" && evt.Item.CallID != block.callID {
			return nil
		}
		raw := evt.Item.Arguments
		if evt.Item.Type == "custom_tool_call" {
			raw = evt.Item.Input
		}
		return resToAnthHandleFuncArgsDone(&ResponsesStreamEvent{OutputIndex: evt.OutputIndex, Arguments: raw}, state)
	case "message":
		var events []AnthropicStreamEvent
		for contentIndex, content := range evt.Item.Content {
			if content.Type != "output_text" {
				continue
			}
			part := responsesTextPart{OutputIndex: evt.OutputIndex, ContentIndex: contentIndex}
			events = append(events, resToAnthRecoverText(content.Text, part, state)...)
			events = append(events, closeResponsesAnthropicBlock(state, state.textBlocks[part])...)
		}
		var remaining []*responsesAnthropicBlock
		for part, block := range state.textBlocks {
			if part.OutputIndex == evt.OutputIndex && block.open {
				remaining = append(remaining, block)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return remaining[i].index < remaining[j].index })
		for _, block := range remaining {
			events = append(events, closeResponsesAnthropicBlock(state, block)...)
		}
		return events
	}
	return nil
}

// resToAnthHandleWebSearchDone converts an OpenAI web_search_call output item
// into Anthropic server_tool_use + web_search_tool_result content block pairs.
// This allows Claude Code to count the searches performed.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	// Emit server_tool_use block (start + stop).
	idx1 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx1,
		ContentBlock: &AnthropicContentBlock{
			Type:  "server_tool_use",
			ID:    toolUseID,
			Name:  "web_search",
			Input: inputJSON,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx1,
	})
	state.ContentBlockIndex++

	// Emit web_search_tool_result block (start + stop).
	// Content is empty because OpenAI does not expose individual search results;
	// the model consumes them internally and produces text output.
	emptyResults, _ := json.Marshal([]struct{}{})
	idx2 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx2,
		ContentBlock: &AnthropicContentBlock{
			Type:      "web_search_tool_result",
			ToolUseID: toolUseID,
			Content:   emptyResults,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx2,
	})
	state.ContentBlockIndex++

	return events
}

// resToAnthRecoverTerminalText emits assistant text that only ever appeared in
// the terminal response payload, which some streams populate without sending
// any output_text event.
//
// It only runs when no text at all reached the client. Streamed events and the
// terminal output array carry no guaranteed common identity, so reconciling
// them part by part risks repeating an answer the client already has, which is
// worse than leaving a partially streamed response as it is.
func resToAnthRecoverTerminalText(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.textDelivered || evt.Response == nil {
		return nil
	}

	var events []AnthropicStreamEvent
	// Terminal-only content has no streamed part identity. Keep the previous
	// single text-block presentation even when the terminal array contains
	// several message items or content parts.
	part := responsesTextPart{OutputIndex: -1, ContentIndex: -1}
	for _, item := range evt.Response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			events = append(events, resToAnthEmitText(content.Text, part, state)...)
		}
	}
	if len(events) > 0 {
		events = append(events, closeResponsesAnthropicBlock(state, state.textBlocks[part])...)
	}
	return events
}

func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, resToAnthRecoverTerminalToolArgs(evt, state)...)
	events = append(events, closeAllResponsesAnthropicBlocks(state)...)
	events = append(events, resToAnthRecoverTerminalText(evt, state)...)

	stopReason := "end_turn"
	if evt.Usage != nil {
		usage := anthropicUsageFromResponsesUsage(evt.Usage)
		state.InputTokens = usage.InputTokens
		state.OutputTokens = usage.OutputTokens
		state.CacheReadInputTokens = usage.CacheReadInputTokens
		state.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			usage := anthropicUsageFromResponsesUsage(evt.Response.Usage)
			state.InputTokens = usage.InputTokens
			state.OutputTokens = usage.OutputTokens
			state.CacheReadInputTokens = usage.CacheReadInputTokens
			state.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
		switch evt.Response.Status {
		case "incomplete":
			if evt.Response.IncompleteDetails != nil && evt.Response.IncompleteDetails.Reason == "max_output_tokens" {
				stopReason = "max_tokens"
			}
		case "completed":
			if state.HasToolCall {
				stopReason = "tool_use"
			}
		}
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// Terminal output arrays may use different positions than their streamed
// output_index values. Recover a missing tool tail only when an item ID or
// call ID uniquely identifies an open block.
func resToAnthRecoverTerminalToolArgs(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response == nil || evt.Response.Status != "completed" {
		return nil
	}
	var events []AnthropicStreamEvent
	for _, item := range evt.Response.Output {
		if item.Type != "function_call" && item.Type != "custom_tool_call" {
			continue
		}
		if item.ID == "" && item.CallID == "" {
			continue
		}
		var matchedIndex int
		matches := 0
		for index, block := range state.blocksByOutput {
			if !block.open || block.kind != "tool_use" || block.toolType != item.Type {
				continue
			}
			matched := false
			if item.ID != "" && block.itemID != "" && item.ID != block.itemID {
				continue
			}
			if item.ID != "" && block.itemID != "" {
				matched = true
			}
			if item.CallID != "" && block.callID != "" && item.CallID != block.callID {
				continue
			}
			if item.CallID != "" && block.callID != "" {
				matched = true
			}
			if !matched {
				continue
			}
			matchedIndex = index
			matches++
		}
		if matches != 1 {
			continue
		}
		raw := item.Arguments
		if item.Type == "custom_tool_call" {
			raw = item.Input
		}
		events = append(events, resToAnthHandleFuncArgsDone(&ResponsesStreamEvent{OutputIndex: matchedIndex, Arguments: raw}, state)...)
	}
	return events
}

func closeResponsesAnthropicBlock(state *ResponsesEventToAnthropicState, block *responsesAnthropicBlock) []AnthropicStreamEvent {
	if block == nil || !block.open {
		return nil
	}
	idx := block.index
	var events []AnthropicStreamEvent
	if block.toolType == "custom_tool_call" {
		if fragment := resToAnthFinishCustomInput(block); fragment != "" {
			events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: fragment}})
		}
	}
	// Emit signature_delta before stop so Claude clients retain encrypted
	// reasoning for the next turn (required for Grok multi-turn cache).
	if block.kind == "thinking" {
		if sig := strings.TrimSpace(block.signature); sig != "" {
			events = append(events, AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &idx,
				Delta: &AnthropicDelta{
					Type:      "signature_delta",
					Signature: sig,
				},
			})
		}
	}
	block.open = false
	block.args.Reset()
	block.argsHash = nil
	delete(state.openBlocks, idx)
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx,
	})
	return events
}

func closeAllResponsesAnthropicBlocks(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	indices := make([]int, 0, len(state.openBlocks))
	for index := range state.openBlocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	var events []AnthropicStreamEvent
	for _, index := range indices {
		events = append(events, closeResponsesAnthropicBlock(state, state.openBlocks[index])...)
	}
	return events
}
