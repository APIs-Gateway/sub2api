package apicompat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → ChatCompletionsResponse
// ---------------------------------------------------------------------------

// ResponsesToChatCompletions converts a Responses API response into a Chat
// Completions response. Text output items are concatenated into
// choices[0].message.content, refusal parts into choices[0].message.refusal;
// function_call items become tool_calls.
func ResponsesToChatCompletions(resp *ResponsesResponse, model string) *ChatCompletionsResponse {
	return ResponsesToChatCompletionsWithOptions(resp, model, ResponsesToChatCompletionsOptions{})
}

// ResponsesToChatCompletionsOptions carries optional behaviour for
// ResponsesToChatCompletionsWithOptions. The zero value behaves exactly like
// ResponsesToChatCompletions.
type ResponsesToChatCompletionsOptions struct {
	// ReasoningDetails 为 true 时：把 reasoning 项透出为 choices[0].message.reasoning_details
	// （摘要项 reasoning.summary + 密文项 reasoning.encrypted，index 统一从 0 递增；一个都没有时不输出该键），
	// 同时在 message.reasoning 里输出与 reasoning_content 相同的内容，并在相邻摘要 part 之间补空行。
	ReasoningDetails bool
	// FallbackReasoning 在 resp.Output 里生成不出任何 reasoning_details 元素时使用
	// （终止事件 output 为空、密文/摘要只出现在 response.output_item.done 的场景）。
	FallbackReasoning []ResponsesOutput
}

// ResponsesToChatCompletionsWithOptions is ResponsesToChatCompletions with
// optional extras; see ResponsesToChatCompletionsOptions.
func ResponsesToChatCompletionsWithOptions(resp *ResponsesResponse, model string, opts ResponsesToChatCompletionsOptions) *ChatCompletionsResponse {
	id := resp.ID
	if id == "" {
		id = generateChatCmplID()
	}

	out := &ChatCompletionsResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}

	var contentText string
	var refusalText string
	var reasoningText string
	var toolCalls []ChatToolCall

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch {
				case part.Type == "output_text" && part.Text != "":
					contentText += part.Text
				case part.Type == "refusal" && part.Refusal != "":
					refusalText += part.Refusal
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, ChatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		case "reasoning":
			if opts.ReasoningDetails {
				// 开关开启时相邻的摘要 part / reasoning 项之间用空行分隔，避免首尾直接粘连。
				reasoningText = joinReasoningParagraphs(reasoningText, responsesReasoningTextSeparated(item))
				break
			}
			reasoningText += responsesReasoningText(item)
		case "web_search_call":
			// silently consumed — results already incorporated into text output
		}
	}

	msg := ChatMessage{Role: "assistant", Refusal: refusalText}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if contentText != "" {
		raw, _ := json.Marshal(contentText)
		msg.Content = raw
	}
	if reasoningText != "" {
		msg.ReasoningContent = reasoningText
		if opts.ReasoningDetails {
			// OpenRouter / Vercel AI SDK 等客户端读 reasoning，内容与 reasoning_content 相同。
			msg.Reasoning = reasoningText
		}
	}
	if opts.ReasoningDetails {
		details := chatReasoningDetailsFromOutput(resp.Output)
		if !chatReasoningDetailsHaveEncrypted(details) {
			// 终止事件 output 为空时 reasoning 项是由增量重建的（只有摘要、没有密文）：
			// 以 output_item.done 里记下的项为准。
			if fb := chatReasoningDetailsFromOutput(opts.FallbackReasoning); len(fb) > 0 && (chatReasoningDetailsHaveEncrypted(fb) || len(details) == 0) {
				details = fb
			}
		}
		if len(details) > 0 {
			if raw, err := json.Marshal(details); err == nil {
				msg.ReasoningDetails = raw
			}
		}
	}

	finishReason := responsesStatusToChatFinishReason(resp.Status, resp.IncompleteDetails, toolCalls)

	out.Choices = []ChatChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	}}

	out.Usage = chatUsageFromResponsesUsage(resp.Usage)

	return out
}

const reasoningParagraphSeparator = "\n\n"

// joinReasoningParagraphs 把两段推理文本用空行连接；任一侧为空时不加分隔。
func joinReasoningParagraphs(prev, next string) string {
	if prev == "" || next == "" {
		return prev + next
	}
	return prev + reasoningParagraphSeparator + next
}

// responsesReasoningTextSeparated 同 responsesReasoningText，但多个 summary_text part 之间用空行分隔。
func responsesReasoningTextSeparated(item ResponsesOutput) string {
	text := ""
	for _, part := range item.Summary {
		if part.Type == "summary_text" && part.Text != "" {
			text = joinReasoningParagraphs(text, part.Text)
		}
	}
	if text != "" {
		return text
	}
	return responsesReasoningText(item)
}

// chatReasoningDetailsFromOutput 为每个 reasoning 项依次生成：有摘要文本时先一个 reasoning.summary 元素，
// 有密文时再一个 reasoning.encrypted 元素；index 在整个数组里统一从 0 递增。
func chatReasoningDetailsFromOutput(output []ResponsesOutput) []ChatReasoningDetail {
	var details []ChatReasoningDetail
	for i := range output {
		if output[i].Type != "reasoning" {
			continue
		}
		details = appendChatReasoningDetails(details, output[i])
	}
	return details
}

func chatReasoningDetailsHaveEncrypted(details []ChatReasoningDetail) bool {
	for i := range details {
		if details[i].Type == ChatReasoningDetailTypeEncrypted {
			return true
		}
	}
	return false
}

// reasoningSummaryJoined 把一个 reasoning 项全部 summary_text part 用空行连接（跳过空 part）。
func reasoningSummaryJoined(item ResponsesOutput) string {
	text := ""
	for _, part := range item.Summary {
		if part.Type == "summary_text" && part.Text != "" {
			text = joinReasoningParagraphs(text, part.Text)
		}
	}
	return text
}

// appendChatReasoningDetails 追加一个 reasoning 项对应的 [summary, encrypted] 元素，index 接着 details 现有长度。
func appendChatReasoningDetails(details []ChatReasoningDetail, item ResponsesOutput) []ChatReasoningDetail {
	if summary := reasoningSummaryJoined(item); summary != "" {
		details = append(details, ChatReasoningDetail{
			Type: ChatReasoningDetailTypeSummary, Summary: summary, ID: item.ID,
			Format: ChatReasoningDetailFormatResponsesV1, Index: len(details),
		})
	}
	if item.EncryptedContent != "" {
		details = append(details, ChatReasoningDetail{
			Type: ChatReasoningDetailTypeEncrypted, Data: item.EncryptedContent, ID: item.ID,
			Format: ChatReasoningDetailFormatResponsesV1, Index: len(details),
		})
	}
	return details
}

func responsesStatusToChatFinishReason(status string, details *ResponsesIncompleteDetails, toolCalls []ChatToolCall) string {
	switch status {
	case "incomplete":
		if details != nil {
			switch details.Reason {
			case "max_output_tokens":
				return "length"
			case "content_filter":
				return "content_filter"
			}
		}
		return "stop"
	case "completed":
		if len(toolCalls) > 0 {
			return "tool_calls"
		}
		return "stop"
	default:
		return "stop"
	}
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []ChatCompletionsChunk (stateful converter)
// ---------------------------------------------------------------------------

// ResponsesEventToChatState tracks state for converting a sequence of Responses
// SSE events into Chat Completions SSE chunks.
type ResponsesEventToChatState struct {
	ID                     string
	Model                  string
	Created                int64
	SentRole               bool
	SawToolCall            bool
	SawText                bool
	Finalized              bool        // true after finish chunk has been emitted
	NextToolCallIndex      int         // next sequential tool_call index to assign
	OutputIndexToToolIndex map[int]int // Responses output_index → Chat tool_calls index
	OutputIndexToArguments map[int]string
	IncludeUsage           bool
	Usage                  *ChatUsage

	// ReasoningDetails 为 true 时，reasoning 项完成（response.output_item.done）或随终止事件
	// 到达时下发 delta.reasoning_details（加密推理），同一个 item 只发一次。
	ReasoningDetails bool

	reasoningDetailsSent map[string]struct{}
	nextReasoningDetail  int

	// 开关开启时，相邻摘要 part（output_index/summary_index 变化）之间补一个空行分隔。
	haveSummaryKey bool
	lastSummaryKey [2]int
}

// NewResponsesEventToChatState returns an initialised stream state.
func NewResponsesEventToChatState() *ResponsesEventToChatState {
	return &ResponsesEventToChatState{
		ID:                     generateChatCmplID(),
		Created:                time.Now().Unix(),
		OutputIndexToToolIndex: make(map[int]int),
		OutputIndexToArguments: make(map[int]string),
	}
}

// ResponsesEventToChatChunks converts a single Responses SSE event into zero
// or more Chat Completions chunks, updating state as it goes.
func ResponsesEventToChatChunks(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	switch evt.Type {
	case "response.created":
		return resToChatHandleCreated(evt, state)
	case "response.output_text.delta":
		return resToChatHandleTextDelta(evt, state)
	case "response.refusal.delta":
		return resToChatHandleRefusalDelta(evt, state)
	case "response.output_item.added":
		return resToChatHandleOutputItemAdded(evt, state)
	case "response.output_item.done":
		return resToChatHandleOutputItemDone(evt, state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具（如新版 apply_patch）的输入增量与 function_call 参数增量同形，
		// 均按 OutputIndex 累加到对应工具调用。
		"response.custom_tool_call_input.delta":
		return resToChatHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		return resToChatHandleFuncArgsDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量（真实 Codex 客户端消费的 reasoning_text.delta），
		// 与 reasoning summary 一样映射为 reasoning_content。
		"response.reasoning_text.delta":
		return resToChatHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		return nil
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToChatHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesChatStream emits a final chunk with finish_reason if the
// stream ended without a proper completion event (e.g. upstream disconnect).
// It is idempotent: if a completion event already emitted the finish chunk,
// this returns nil.
func FinalizeResponsesChatStream(state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.Finalized {
		return nil
	}
	state.Finalized = true

	finishReason := "stop"
	if state.SawToolCall {
		finishReason = "tool_calls"
	}

	chunks := []ChatCompletionsChunk{makeChatFinishChunk(state, finishReason)}

	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:      state.ID,
			Object:  "chat.completion.chunk",
			Created: state.Created,
			Model:   state.Model,
			Choices: []ChatChunkChoice{},
			Usage:   state.Usage,
		})
	}

	return chunks
}

// ChatChunkToSSE formats a ChatCompletionsChunk as an SSE data line.
func ChatChunkToSSE(chunk ChatCompletionsChunk) (string, error) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("data: %s\n\n", data), nil
}

// --- internal handlers ---

func resToChatHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Response != nil {
		if evt.Response.ID != "" {
			state.ID = evt.Response.ID
		}
		if state.Model == "" && evt.Response.Model != "" {
			state.Model = evt.Response.Model
		}
	}
	// Emit the role chunk.
	if state.SentRole {
		return nil
	}
	state.SentRole = true

	role := "assistant"
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Role: role})}
}

func resToChatHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	state.SawText = true
	content := evt.Delta
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Content: &content})}
}

func resToChatHandleRefusalDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	refusal := evt.Delta
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Refusal: &refusal})}
}

func resToChatHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	// function_call 与 custom_tool_call（custom/freeform 工具）均按工具调用注册，
	// 以便后续 *_input.delta / *_arguments.delta 能映射到正确的工具索引。
	if evt.Item == nil || (evt.Item.Type != "function_call" && evt.Item.Type != "custom_tool_call") {
		return nil
	}

	state.SawToolCall = true
	idx := state.NextToolCallIndex
	state.OutputIndexToToolIndex[evt.OutputIndex] = idx
	state.NextToolCallIndex++

	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			ID:    evt.Item.CallID,
			Type:  "function",
			Function: ChatFunctionCall{
				Name: evt.Item.Name,
			},
		}},
	})}
}

// resToChatHandleOutputItemDone 在 reasoning 项完成且带密文时下发一个 reasoning_details delta。
// 开关关闭（默认）时恒为 nil，与此前 default 分支行为一致。
func resToChatHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if !state.ReasoningDetails || evt.Item == nil {
		return nil
	}
	return resToChatReasoningDetailChunks(state, []ResponsesOutput{*evt.Item})
}

// resToChatReasoningDetailChunks 为尚未发送过的 reasoning 项各生成一个 delta chunk，
// 一次带出该项的 [summary, encrypted]；没有摘要也没有密文的项不发。
// 去重键：item id，没有 id 时用密文，再没有用摘要文本；index 在整条流内统一从 0 递增。
func resToChatReasoningDetailChunks(state *ResponsesEventToChatState, items []ResponsesOutput) []ChatCompletionsChunk {
	var chunks []ChatCompletionsChunk
	for i := range items {
		item := items[i]
		if item.Type != "reasoning" {
			continue
		}
		summary := reasoningSummaryJoined(item)
		if summary == "" && item.EncryptedContent == "" {
			continue
		}
		key := item.ID
		if key == "" && item.EncryptedContent != "" {
			key = "enc:" + item.EncryptedContent
		}
		if key == "" {
			key = "sum:" + summary
		}
		if _, dup := state.reasoningDetailsSent[key]; dup {
			continue
		}
		if state.reasoningDetailsSent == nil {
			state.reasoningDetailsSent = make(map[string]struct{})
		}
		state.reasoningDetailsSent[key] = struct{}{}
		// appendChatReasoningDetails 的 index 取自切片长度；这里用整流计数器偏移。
		details := appendChatReasoningDetails(nil, item)
		for j := range details {
			details[j].Index += state.nextReasoningDetail
		}
		state.nextReasoningDetail += len(details)
		chunks = append(chunks, makeChatDeltaChunk(state, ChatDelta{ReasoningDetails: details}))
	}
	return chunks
}

func resToChatHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}

	idx, ok := state.OutputIndexToToolIndex[evt.OutputIndex]
	if !ok {
		return nil
	}
	state.OutputIndexToArguments[evt.OutputIndex] += evt.Delta

	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			Function: ChatFunctionCall{
				Arguments: evt.Delta,
			},
		}},
	})}
}

func resToChatHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	idx, ok := state.OutputIndexToToolIndex[evt.OutputIndex]
	if !ok {
		return nil
	}

	completed := evt.Arguments
	if evt.Type == "response.custom_tool_call_input.done" {
		completed = evt.Input
	}
	current := state.OutputIndexToArguments[evt.OutputIndex]
	if completed == "" || !strings.HasPrefix(completed, current) || completed == current {
		return nil
	}

	remainder := completed[len(current):]
	state.OutputIndexToArguments[evt.OutputIndex] = completed
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			Function: ChatFunctionCall{
				Arguments: remainder,
			},
		}},
	})}
}

func resToChatHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	reasoning := evt.Delta
	if state.ReasoningDetails && evt.Type == "response.reasoning_summary_text.delta" {
		key := [2]int{evt.OutputIndex, evt.SummaryIndex}
		if state.haveSummaryKey && key != state.lastSummaryKey {
			reasoning = reasoningParagraphSeparator + reasoning
		}
		state.haveSummaryKey, state.lastSummaryKey = true, key
	}
	delta := ChatDelta{ReasoningContent: &reasoning}
	if state.ReasoningDetails {
		alias := reasoning
		delta.Reasoning = &alias
	}
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, delta)}
}

func resToChatHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	state.Finalized = true
	finishReason := "stop"

	if evt.Usage != nil {
		state.Usage = chatUsageFromResponsesUsage(evt.Usage)
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			state.Usage = chatUsageFromResponsesUsage(evt.Response.Usage)
		}

		switch evt.Response.Status {
		case "incomplete":
			if evt.Response.IncompleteDetails != nil {
				switch evt.Response.IncompleteDetails.Reason {
				case "max_output_tokens":
					finishReason = "length"
				case "content_filter":
					finishReason = "content_filter"
				}
			}
		case "completed":
			if state.SawToolCall {
				finishReason = "tool_calls"
			}
		}
	} else if state.SawToolCall {
		finishReason = "tool_calls"
	}

	var chunks []ChatCompletionsChunk
	// 终止事件的 output 里带密文、却没有走过 output_item.done 的 reasoning 项，在 finish 之前补发（已发过的跳过）。
	if state.ReasoningDetails && evt.Response != nil {
		chunks = append(chunks, resToChatReasoningDetailChunks(state, evt.Response.Output)...)
	}
	chunks = append(chunks, makeChatFinishChunk(state, finishReason))

	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:      state.ID,
			Object:  "chat.completion.chunk",
			Created: state.Created,
			Model:   state.Model,
			Choices: []ChatChunkChoice{},
			Usage:   state.Usage,
		})
	}

	return chunks
}

func chatUsageFromResponsesUsage(u *ResponsesUsage) *ChatUsage {
	if u == nil {
		return nil
	}
	usage := &ChatUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
	usage.PromptTokensDetails = promptDetailsFromResponses(u.InputTokensDetails)
	if u.CacheCreationInputTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &ChatTokenDetails{}
		}
		if usage.PromptTokensDetails.CacheWriteTokens == 0 && usage.PromptTokensDetails.CacheCreationTokens == 0 {
			usage.PromptTokensDetails.CacheCreationTokens = u.CacheCreationInputTokens
		}
	}
	usage.CompletionTokensDetails = completionDetailsFromResponses(u.OutputTokensDetails)
	return usage
}

// promptDetailsFromResponses maps Responses-API input_tokens_details into a
// Chat-Completions prompt_tokens_details. Returns nil when nothing would be
// emitted, so upstreams that do not break down prompt usage stay clean.
func promptDetailsFromResponses(src *ResponsesInputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	if src.CachedTokens == 0 && src.AudioTokens == 0 && src.CacheCreationTokens == 0 && src.CacheWriteTokens == 0 {
		return nil
	}
	return &ChatTokenDetails{
		CachedTokens:        src.CachedTokens,
		AudioTokens:         src.AudioTokens,
		CacheCreationTokens: src.CacheCreationTokens,
		CacheWriteTokens:    src.CacheWriteTokens,
	}
}

// completionDetailsFromResponses maps Responses-API output_tokens_details
// into a Chat-Completions completion_tokens_details. Mirrors the OpenAI
// official CompletionUsage schema: reasoning_tokens, audio_tokens, and
// the predicted-outputs accepted/rejected counts. Returns nil when nothing
// would be emitted so non-reasoning, non-audio responses stay clean.
func completionDetailsFromResponses(src *ResponsesOutputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	if src.ReasoningTokens == 0 && src.AudioTokens == 0 &&
		src.AcceptedPredictionTokens == 0 && src.RejectedPredictionTokens == 0 {
		return nil
	}
	return &ChatTokenDetails{
		ReasoningTokens:          src.ReasoningTokens,
		AudioTokens:              src.AudioTokens,
		AcceptedPredictionTokens: src.AcceptedPredictionTokens,
		RejectedPredictionTokens: src.RejectedPredictionTokens,
	}
}

func makeChatDeltaChunk(state *ResponsesEventToChatState, delta ChatDelta) ChatCompletionsChunk {
	return ChatCompletionsChunk{
		ID:      state.ID,
		Object:  "chat.completion.chunk",
		Created: state.Created,
		Model:   state.Model,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: nil,
		}},
	}
}

func makeChatFinishChunk(state *ResponsesEventToChatState, finishReason string) ChatCompletionsChunk {
	empty := ""
	return ChatCompletionsChunk{
		ID:      state.ID,
		Object:  "chat.completion.chunk",
		Created: state.Created,
		Model:   state.Model,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        ChatDelta{Content: &empty},
			FinishReason: &finishReason,
		}},
	}
}

// generateChatCmplID returns a "chatcmpl-" prefixed random hex ID.
func generateChatCmplID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// BufferedResponseAccumulator: accumulates SSE delta events for non-streaming
// paths where the terminal event may have empty output.
// ---------------------------------------------------------------------------

type bufferedFuncCall struct {
	OutputIndex           int
	CallID                string
	Name                  string
	Namespace             string
	EncryptedFunctionArgs json.RawMessage
	EncryptedContent      string
	Args                  strings.Builder
}

// BufferedResponseAccumulator collects content from Responses SSE delta events
// so that non-streaming handlers can reconstruct output when the terminal event
// (response.completed / response.done) carries an empty output array.
type BufferedResponseAccumulator struct {
	text                 strings.Builder
	refusal              strings.Builder
	reasoning            strings.Builder
	funcCalls            []bufferedFuncCall
	outputIndexToFuncIdx map[int]int
	// doneReasoning 被动记录 response.output_item.done 里带密文或摘要的 reasoning 项，
	// 不参与 BuildOutput/SupplementResponseOutput，仅供 DoneReasoningItems 读取。
	doneReasoning []ResponsesOutput
	// reasoningSeparated 与 reasoning 同源，但相邻摘要 part 之间有空行；仅供 SeparatedReasoningText 读取。
	reasoningSeparated strings.Builder
	haveSummaryKey     bool
	lastSummaryKey     [2]int
}

// NewBufferedResponseAccumulator returns an initialised accumulator.
func NewBufferedResponseAccumulator() *BufferedResponseAccumulator {
	return &BufferedResponseAccumulator{
		outputIndexToFuncIdx: make(map[int]int),
	}
}

// ProcessEvent inspects a single Responses SSE event and accumulates any
// content it carries. Only delta events that contribute to the final output
// are handled; all other event types are silently ignored.
func (a *BufferedResponseAccumulator) ProcessEvent(event *ResponsesStreamEvent) {
	switch event.Type {
	case "response.output_text.delta":
		if event.Delta != "" {
			_, _ = a.text.WriteString(event.Delta)
		}
	case "response.refusal.delta":
		if event.Delta != "" {
			_, _ = a.refusal.WriteString(event.Delta)
		}
	case "response.output_item.added":
		if event.Item != nil && (event.Item.Type == "function_call" || event.Item.Type == "custom_tool_call") {
			idx := len(a.funcCalls)
			a.outputIndexToFuncIdx[event.OutputIndex] = idx
			a.funcCalls = append(a.funcCalls, bufferedFuncCall{
				OutputIndex:           event.OutputIndex,
				CallID:                event.Item.CallID,
				Name:                  event.Item.Name,
				Namespace:             event.Item.Namespace,
				EncryptedFunctionArgs: event.Item.EncryptedFunctionArgs,
				EncryptedContent:      event.Item.EncryptedContent,
			})
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if event.Delta != "" {
			if idx, ok := a.outputIndexToFuncIdx[event.OutputIndex]; ok {
				_, _ = a.funcCalls[idx].Args.WriteString(event.Delta)
			}
		}
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		completed := event.Arguments
		if event.Type == "response.custom_tool_call_input.done" {
			completed = event.Input
		}
		if completed != "" {
			if idx, ok := a.outputIndexToFuncIdx[event.OutputIndex]; ok {
				a.funcCalls[idx].Args.Reset()
				_, _ = a.funcCalls[idx].Args.WriteString(completed)
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			_, _ = a.reasoning.WriteString(event.Delta)
			if event.Type == "response.reasoning_summary_text.delta" {
				key := [2]int{event.OutputIndex, event.SummaryIndex}
				if a.haveSummaryKey && key != a.lastSummaryKey {
					_, _ = a.reasoningSeparated.WriteString(reasoningParagraphSeparator)
				}
				a.haveSummaryKey, a.lastSummaryKey = true, key
			}
			_, _ = a.reasoningSeparated.WriteString(event.Delta)
		}
	case "response.output_item.done":
		a.recordDoneReasoning(event.Item)
	}
}

func (a *BufferedResponseAccumulator) recordDoneReasoning(item *ResponsesOutput) {
	if item == nil || item.Type != "reasoning" {
		return
	}
	summary := reasoningSummaryJoined(*item)
	if item.EncryptedContent == "" && summary == "" {
		return
	}
	for i := range a.doneReasoning {
		prev := &a.doneReasoning[i]
		if prev.EncryptedContent == item.EncryptedContent && prev.ID == item.ID && reasoningSummaryJoined(*prev) == summary {
			return
		}
	}
	a.doneReasoning = append(a.doneReasoning, ResponsesOutput{
		Type:             "reasoning",
		ID:               item.ID,
		EncryptedContent: item.EncryptedContent,
		Summary:          item.Summary,
	})
}

// SeparateReasoningSummary 把 SupplementResponseOutput 用增量拼出的推理摘要（相邻 part 直接粘连）
// 换成带空行分隔的版本。只处理「output 整个由增量重建」留下的单 part 摘要，终止事件自带的 output 不动。
func (a *BufferedResponseAccumulator) SeparateReasoningSummary(resp *ResponsesResponse) {
	if resp == nil || a.reasoning.Len() == 0 || a.reasoningSeparated.Len() == a.reasoning.Len() {
		return
	}
	plain := a.reasoning.String()
	for i := range resp.Output {
		item := &resp.Output[i]
		if item.Type == "reasoning" && len(item.Summary) == 1 && item.Summary[0].Text == plain {
			item.Summary[0].Text = a.reasoningSeparated.String()
			return
		}
	}
}

// DoneReasoningItems returns the reasoning items (id, summary and
// encrypted_content only) seen in response.output_item.done events that carry
// a summary or ciphertext, in arrival order.
func (a *BufferedResponseAccumulator) DoneReasoningItems() []ResponsesOutput {
	return a.doneReasoning
}

// HasContent reports whether any content has been accumulated.
func (a *BufferedResponseAccumulator) HasContent() bool {
	return a.text.Len() > 0 || a.refusal.Len() > 0 || len(a.funcCalls) > 0 || a.reasoning.Len() > 0
}

// BuildOutput constructs a []ResponsesOutput from the accumulated delta
// content. The order matches what ResponsesToChatCompletions expects:
// reasoning → message → function_calls.
func (a *BufferedResponseAccumulator) BuildOutput() []ResponsesOutput {
	var out []ResponsesOutput

	if a.reasoning.Len() > 0 {
		out = append(out, ResponsesOutput{
			Type: "reasoning",
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: a.reasoning.String(),
			}},
		})
	}

	if a.text.Len() > 0 || a.refusal.Len() > 0 {
		var content []ResponsesContentPart
		if a.text.Len() > 0 {
			content = append(content, ResponsesContentPart{Type: "output_text", Text: a.text.String()})
		}
		if a.refusal.Len() > 0 {
			content = append(content, ResponsesContentPart{Type: "refusal", Refusal: a.refusal.String()})
		}
		out = append(out, ResponsesOutput{
			Type:    "message",
			Role:    "assistant",
			Content: content,
		})
	}

	for i := range a.funcCalls {
		out = append(out, ResponsesOutput{
			Type:                  "function_call",
			CallID:                a.funcCalls[i].CallID,
			Name:                  a.funcCalls[i].Name,
			Namespace:             a.funcCalls[i].Namespace,
			EncryptedFunctionArgs: a.funcCalls[i].EncryptedFunctionArgs,
			EncryptedContent:      a.funcCalls[i].EncryptedContent,
			Arguments:             a.funcCalls[i].Args.String(),
		})
	}

	return out
}

// SupplementResponseOutput fills missing terminal text from accumulated stream
// content and empty function-call arguments from authoritative argument-done
// events.
func (a *BufferedResponseAccumulator) SupplementResponseOutput(resp *ResponsesResponse) {
	if resp == nil {
		return
	}
	if len(resp.Output) == 0 {
		if a.HasContent() {
			resp.Output = a.BuildOutput()
		}
		return
	}

	// Fill arguments before inserting a message so the terminal output indexes
	// still match the indexes observed in streamed function-call events.
	for outputIndex := range resp.Output {
		item := &resp.Output[outputIndex]
		if item.Type != "function_call" || item.Arguments != "" {
			continue
		}
		for funcIndex := range a.funcCalls {
			call := &a.funcCalls[funcIndex]
			matchesCallID := item.CallID != "" && item.CallID == call.CallID
			if !matchesCallID && call.OutputIndex != outputIndex {
				continue
			}
			if call.Args.Len() > 0 {
				item.Arguments = call.Args.String()
			}
			break
		}
	}

	// The terminal event can carry a non-empty output array whose message has
	// no usable text. Trusting it as-is silently drops the text that already
	// streamed: the client gets an empty reply while usage still bills the
	// terminal output_tokens. Refill it from the accumulated deltas; non-empty
	// terminal text stays authoritative.
	if a.text.Len() > 0 && !responsesOutputHasText(resp.Output) {
		if !fillResponsesOutputText(resp.Output, a.text.String()) {
			message := ResponsesOutput{
				Type: "message",
				Role: "assistant",
				Content: []ResponsesContentPart{{
					Type: "output_text",
					Text: a.text.String(),
				}},
			}
			insertAt := len(resp.Output)
			for i := range resp.Output {
				if resp.Output[i].Type == "function_call" {
					insertAt = i
					break
				}
			}
			resp.Output = append(resp.Output, ResponsesOutput{})
			copy(resp.Output[insertAt+1:], resp.Output[insertAt:])
			resp.Output[insertAt] = message
		}
	}
	// A nonempty terminal output may omit the refusal already streamed.
	// Preserve any nonempty terminal refusal as authoritative.
	if a.refusal.Len() > 0 {
		hasRefusal := false
		for _, item := range resp.Output {
			if item.Type != "message" {
				continue
			}
			for _, part := range item.Content {
				if part.Type == "refusal" && part.Refusal != "" {
					hasRefusal = true
				}
			}
		}
		if !hasRefusal {
			part := ResponsesContentPart{Type: "refusal", Refusal: a.refusal.String()}
			messageIndex := -1
			for i := range resp.Output {
				if resp.Output[i].Type == "message" {
					messageIndex = i
					break
				}
			}
			if messageIndex >= 0 {
				filled := false
				for i := range resp.Output[messageIndex].Content {
					if resp.Output[messageIndex].Content[i].Type == "refusal" {
						resp.Output[messageIndex].Content[i] = part
						filled = true
						break
					}
				}
				if !filled {
					resp.Output[messageIndex].Content = append(resp.Output[messageIndex].Content, part)
				}
			} else {
				insertAt := len(resp.Output)
				for i := range resp.Output {
					if resp.Output[i].Type == "function_call" {
						insertAt = i
						break
					}
				}
				resp.Output = append(resp.Output, ResponsesOutput{})
				copy(resp.Output[insertAt+1:], resp.Output[insertAt:])
				resp.Output[insertAt] = ResponsesOutput{Type: "message", Role: "assistant", Content: []ResponsesContentPart{part}}
			}
		}
	}

}

// responsesOutputHasText reports whether the terminal output already carries
// usable message text. Whitespace-only text does not count.
func responsesOutputHasText(output []ResponsesOutput) bool {
	for i := range output {
		if output[i].Type != "message" {
			continue
		}
		for _, part := range output[i].Content {
			if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}

// fillResponsesOutputText writes text into the first empty output_text part of
// the first message item, adding an output_text part when that message has
// none. It returns false when the output holds no message item, leaving the
// caller to insert one before the first function call.
func fillResponsesOutputText(output []ResponsesOutput, text string) bool {
	for i := range output {
		if output[i].Type != "message" {
			continue
		}
		for j := range output[i].Content {
			if output[i].Content[j].Type != "output_text" {
				continue
			}
			if strings.TrimSpace(output[i].Content[j].Text) == "" {
				output[i].Content[j].Text = text
				return true
			}
		}
		output[i].Content = append(output[i].Content, ResponsesContentPart{
			Type: "output_text",
			Text: text,
		})
		return true
	}
	return false
}
