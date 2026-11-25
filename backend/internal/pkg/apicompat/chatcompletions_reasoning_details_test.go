package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 请求方向：reasoning_details → Responses reasoning 输入项
// ---------------------------------------------------------------------------

// chatInputText 返回 message 项里 output_text 的文本（避开 JSON 转义）。
func chatInputText(t *testing.T, item ResponsesInputItem) string {
	t.Helper()
	var parts []ResponsesContentPart
	require.NoError(t, json.Unmarshal(item.Content, &parts))
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func convertChatBodyForTest(t *testing.T, body string) []ResponsesInputItem {
	t.Helper()
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	items, err := convertChatMessagesToResponsesInput(req.Messages)
	require.NoError(t, err)
	return items
}

func TestChatReasoningDetailsRequest_InsertsReasoningItemBeforeAssistantMessage(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello","reasoning_content":"plain thought","reasoning_details":[
			{"type":"reasoning.encrypted","data":"ENC-A","id":"rs_1","format":"openai-responses-v1","index":0}]},
		{"role":"user","content":"next"}]}`)

	require.Len(t, items, 4)
	require.Equal(t, "message", items[0].Type)
	require.Equal(t, "reasoning", items[1].Type)
	require.Equal(t, "rs_1", items[1].ID)
	require.Equal(t, "ENC-A", items[1].EncryptedContent)
	require.JSONEq(t, `[]`, string(items[1].Summary))
	require.Equal(t, "assistant", items[2].Role)
	require.Equal(t, "user", items[3].Role)

	// 有有效加密项时不再拼 <thinking>。
	require.NotContains(t, chatInputText(t, items[2]), "<thinking>")
	require.NotContains(t, chatInputText(t, items[2]), "plain thought")
	require.Contains(t, chatInputText(t, items[2]), "hello")

	raw, err := json.Marshal(items[1])
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC-A"}`, string(raw))
}

func TestChatReasoningDetailsRequest_OmitsIDWhenMissingAndAcceptsMissingFormat(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"assistant","content":"x","reasoning_details":[{"type":"reasoning.encrypted","data":"ENC"}]}]}`)
	require.Len(t, items, 2)
	raw, err := json.Marshal(items[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"reasoning","summary":[],"encrypted_content":"ENC"}`, string(raw))
}

func TestChatReasoningDetailsRequest_OrdersByIndexThenArrayPosition(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"assistant","content":"x","reasoning_details":[
			{"type":"reasoning.encrypted","data":"C","index":2},
			{"type":"reasoning.encrypted","data":"A","index":0},
			{"type":"reasoning.encrypted","data":"B","index":1}]},
		{"role":"assistant","content":"y","reasoning_details":[
			{"type":"reasoning.encrypted","data":"P"},
			{"type":"reasoning.encrypted","data":"Q"}]}]}`)
	var got []string
	for _, it := range items {
		if it.Type == "reasoning" {
			got = append(got, it.EncryptedContent)
		}
	}
	require.Equal(t, []string{"A", "B", "C", "P", "Q"}, got)
}

func TestChatReasoningDetailsRequest_PlacedBeforeToolCalls(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.encrypted","data":"E","id":"rs_9"}],
		 "tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
	require.Len(t, items, 2)
	require.Equal(t, "reasoning", items[0].Type)
	require.Equal(t, "function_call", items[1].Type)
}

func TestChatReasoningDetailsRequest_IgnoresOtherTypesFormatsAndEmptyData(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"assistant","content":"answer","reasoning_content":"keep me","reasoning_details":[
			{"type":"reasoning.summary","summary":"s","format":"openai-responses-v1","index":0},
			{"type":"reasoning.text","text":"t","signature":"sig","format":"anthropic-claude-v1","index":1},
			{"type":"reasoning.encrypted","data":"X","format":"anthropic-claude-v1","index":2},
			{"type":"reasoning.encrypted","data":"","index":3},
			{"type":"reasoning.encrypted","data":123,"index":"bad"},
			"garbage", 7, null]}]}`)
	require.Len(t, items, 1, "no reasoning item may be created")
	require.Equal(t, "assistant", items[0].Role)
	// 没有有效 encrypted：保持原有 <thinking> 行为。
	require.Contains(t, chatInputText(t, items[0]), "<thinking>keep me</thinking>")
}

func TestChatReasoningDetailsRequest_NotAnArrayIsIgnored(t *testing.T) {
	items := convertChatBodyForTest(t, `{"messages":[
		{"role":"assistant","content":"a","reasoning_content":"r","reasoning_details":"oops"}]}`)
	require.Len(t, items, 1)
	require.Contains(t, chatInputText(t, items[0]), "<thinking>r</thinking>")
}

func TestChatReasoningDetailsRequest_AbsentFieldKeepsRequestBytes(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo","reasoning_content":"r"}]}`
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	out, err := ChatCompletionsToResponses(&req)
	require.NoError(t, err)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"summary"`)
	require.NotContains(t, string(raw), `"type":"reasoning"`)
	require.Contains(t, string(raw), `\u003cthinking\u003er\u003c/thinking\u003e`)
	require.Contains(t, string(raw), `"include":["reasoning.encrypted_content"]`)
}

// ---------------------------------------------------------------------------
// 响应方向（非流式）
// ---------------------------------------------------------------------------

func encryptedReasoningResponse() *ResponsesResponse {
	return &ResponsesResponse{
		ID:     "resp_1",
		Status: "completed",
		Output: []ResponsesOutput{
			{Type: "reasoning", ID: "rs_1", EncryptedContent: "ENC-1", Summary: []ResponsesSummary{{Type: "summary_text", Text: "thinking 1"}}},
			{Type: "reasoning", ID: "rs_empty"},
			{Type: "reasoning", EncryptedContent: "ENC-2"},
			{Type: "message", Role: "assistant", Content: []ResponsesContentPart{{Type: "output_text", Text: "answer"}}},
		},
	}
}

func marshalChatResponseForTest(t *testing.T, resp *ChatCompletionsResponse) string {
	t.Helper()
	resp.Created = 0
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	return string(raw)
}

func TestResponsesToChatCompletions_ReasoningDetailsOff_BytesUnchanged(t *testing.T) {
	got := marshalChatResponseForTest(t, ResponsesToChatCompletions(encryptedReasoningResponse(), "m"))
	require.Equal(t, `{"id":"resp_1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"thinking 1"},"finish_reason":"stop"}]}`, got)

	// Options 零值与旧入口逐字节一致。
	viaOptions := marshalChatResponseForTest(t, ResponsesToChatCompletionsWithOptions(encryptedReasoningResponse(), "m", ResponsesToChatCompletionsOptions{}))
	require.Equal(t, got, viaOptions)
}

func TestResponsesToChatCompletions_ReasoningDetailsOn(t *testing.T) {
	out := ResponsesToChatCompletionsWithOptions(encryptedReasoningResponse(), "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true})
	got := marshalChatResponseForTest(t, out)
	require.Equal(t, `{"id":"resp_1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"thinking 1","reasoning":"thinking 1","reasoning_details":[`+
		`{"type":"reasoning.summary","summary":"thinking 1","id":"rs_1","format":"openai-responses-v1","index":0},`+
		`{"type":"reasoning.encrypted","data":"ENC-1","id":"rs_1","format":"openai-responses-v1","index":1},`+
		`{"type":"reasoning.encrypted","data":"ENC-2","format":"openai-responses-v1","index":2}]},"finish_reason":"stop"}]}`, got)
}

func TestResponsesToChatCompletions_ReasoningDetailsOn_SummaryPartsJoinedAndSummaryOnly(t *testing.T) {
	out := ResponsesToChatCompletionsWithOptions(summaryResponse(), "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true})
	got := marshalChatResponseForTest(t, out)
	require.Contains(t, got, `{"type":"reasoning.summary","summary":"first\n\nsecond","id":"rs_1","format":"openai-responses-v1","index":0},`+
		`{"type":"reasoning.summary","summary":"third","id":"rs_2","format":"openai-responses-v1","index":1}]`)
	require.NotContains(t, got, "reasoning.encrypted")
	require.Contains(t, got, `"reasoning_content":"first\n\nsecond\n\nthird\n\nraw","reasoning":"first\n\nsecond\n\nthird\n\nraw"`)
}

func TestResponsesToChatCompletions_ReasoningDetailsOn_NoSummaryNoEncryptedOmitsKey(t *testing.T) {
	resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
		{Type: "reasoning", ID: "rs_1"},
		{Type: "message", Role: "assistant", Content: []ResponsesContentPart{{Type: "output_text", Text: "a"}}},
	}}
	got := marshalChatResponseForTest(t, ResponsesToChatCompletionsWithOptions(resp, "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true}))
	require.NotContains(t, got, "reasoning_details")
	require.NotContains(t, got, `"reasoning"`)
}

func TestResponsesToChatCompletions_ReasoningDetailsFallbackFromAccumulator(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", ID: "rs_a", EncryptedContent: "ENC-A"}})
	// 重复、无密文、非 reasoning 的 done 都不记。
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", ID: "rs_a", EncryptedContent: "ENC-A"}})
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", ID: "rs_b"}})
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "message", EncryptedContent: "NOPE"}})
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done"})
	require.Len(t, acc.DoneReasoningItems(), 1)

	resp := &ResponsesResponse{Status: "completed"} // 终止事件 output 为空
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "hi"})
	acc.SupplementResponseOutput(resp)
	out := ResponsesToChatCompletionsWithOptions(resp, "m", ResponsesToChatCompletionsOptions{
		ReasoningDetails:  true,
		FallbackReasoning: acc.DoneReasoningItems(),
	})
	got := marshalChatResponseForTest(t, out)
	require.Contains(t, got, `"reasoning_details":[{"type":"reasoning.encrypted","data":"ENC-A","id":"rs_a","format":"openai-responses-v1","index":0}]`)

	// 终止事件自己带密文时，以终止事件为准，不用 fallback。
	resp2 := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "reasoning", ID: "rs_t", EncryptedContent: "ENC-T"}}}
	got2 := marshalChatResponseForTest(t, ResponsesToChatCompletionsWithOptions(resp2, "m", ResponsesToChatCompletionsOptions{
		ReasoningDetails: true, FallbackReasoning: acc.DoneReasoningItems(),
	}))
	require.Contains(t, got2, "ENC-T")
	require.NotContains(t, got2, "ENC-A")
}

// ---------------------------------------------------------------------------
// 响应方向（流式）
// ---------------------------------------------------------------------------

func chunksJSON(t *testing.T, chunks []ChatCompletionsChunk) []string {
	t.Helper()
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		c.Created = 0
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		out = append(out, string(raw))
	}
	return out
}

func streamEventsWithEncryptedReasoning() []*ResponsesStreamEvent {
	return []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_s", Model: "up"}},
		{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1"}},
		{Type: "response.reasoning_summary_text.delta", Delta: "think"},
		{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1", EncryptedContent: "ENC-1", Summary: []ResponsesSummary{{Type: "summary_text", Text: "think"}}}},
		{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1", EncryptedContent: "ENC-1", Summary: []ResponsesSummary{{Type: "summary_text", Text: "think"}}}},
		{Type: "response.output_text.delta", Delta: "answer"},
		{Type: "response.output_item.done", OutputIndex: 1, Item: &ResponsesOutput{Type: "reasoning", ID: "rs_2", EncryptedContent: "ENC-2"}},
		{Type: "response.output_item.done", OutputIndex: 2, Item: &ResponsesOutput{Type: "message", Role: "assistant"}},
		{Type: "response.completed", Response: &ResponsesResponse{
			ID: "resp_s", Status: "completed",
			Output: []ResponsesOutput{
				{Type: "reasoning", ID: "rs_1", EncryptedContent: "ENC-1"},
				{Type: "reasoning", ID: "rs_2", EncryptedContent: "ENC-2"},
				{Type: "message", Role: "assistant"},
			},
		}},
	}
}

func runStreamForTest(t *testing.T, reasoningDetails bool, events []*ResponsesStreamEvent) []string {
	t.Helper()
	state := NewResponsesEventToChatState()
	state.ID = "chatcmpl-test"
	state.Model = "m"
	state.ReasoningDetails = reasoningDetails
	var all []ChatCompletionsChunk
	for _, ev := range events {
		all = append(all, ResponsesEventToChatChunks(ev, state)...)
	}
	all = append(all, FinalizeResponsesChatStream(state)...)
	return chunksJSON(t, all)
}

func TestResponsesEventToChatChunks_ReasoningDetailsOff_NoExtraChunks(t *testing.T) {
	got := runStreamForTest(t, false, streamEventsWithEncryptedReasoning())
	for _, line := range got {
		require.NotContains(t, line, "reasoning_details")
	}
	require.Equal(t, []string{
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"think"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
	}, got)
}

func TestResponsesEventToChatChunks_ReasoningDetailsOn_EmittedOnceAtItemDone(t *testing.T) {
	got := runStreamForTest(t, true, streamEventsWithEncryptedReasoning())
	require.Equal(t, []string{
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"think","reasoning":"think"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"think","id":"rs_1","format":"openai-responses-v1","index":0},{"type":"reasoning.encrypted","data":"ENC-1","id":"rs_1","format":"openai-responses-v1","index":1}]},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"ENC-2","id":"rs_2","format":"openai-responses-v1","index":2}]},"finish_reason":null}]}`,
		`{"id":"resp_s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
	}, got, "each item once, in order, before finish; completed must not repeat them")
}

// 上游没发 output_item.done、密文只出现在终止事件的 output 里：finish 之前补发，且不重复。
func TestResponsesEventToChatChunks_ReasoningDetailsOn_CompletedOnlyItems(t *testing.T) {
	events := []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_c"}},
		{Type: "response.output_text.delta", Delta: "a"},
		{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
			{Type: "reasoning", EncryptedContent: "ENC-X"},
			{Type: "reasoning", EncryptedContent: "ENC-X"}, // 无 id：按密文去重
			{Type: "reasoning", ID: "rs_noenc"},
		}}},
	}
	got := runStreamForTest(t, true, events)
	require.Len(t, got, 4)
	require.Contains(t, got[2], `"reasoning_details":[{"type":"reasoning.encrypted","data":"ENC-X","format":"openai-responses-v1","index":0}]`)
	require.Contains(t, got[3], `"finish_reason":"stop"`)
}

func TestResponsesEventToChatChunks_ReasoningDetailsOn_NoEncryptedNoChunk(t *testing.T) {
	events := []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_n"}},
		{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1"}},
		{Type: "response.output_item.done"},
		{Type: "response.completed"},
	}
	got := runStreamForTest(t, true, events)
	for _, line := range got {
		require.False(t, strings.Contains(line, "reasoning_details"), line)
	}
}

// ---------------------------------------------------------------------------
// 摘要映射到 reasoning_content：开关开启时相邻 part / 项之间补空行
// ---------------------------------------------------------------------------

func summaryResponse() *ResponsesResponse {
	return &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
		{Type: "reasoning", ID: "rs_1", Summary: []ResponsesSummary{
			{Type: "summary_text", Text: "first"}, {Type: "summary_text", Text: ""}, {Type: "summary_text", Text: "second"},
		}},
		{Type: "reasoning", ID: "rs_2", Summary: []ResponsesSummary{{Type: "summary_text", Text: "third"}}},
		{Type: "reasoning", ID: "rs_3", Content: []ResponsesContentPart{{Type: "reasoning_text", Text: "raw"}}},
	}}
}

func TestResponsesToChatCompletions_SummaryParts_SeparatorOnlyWhenSwitchOn(t *testing.T) {
	off := ResponsesToChatCompletions(summaryResponse(), "m")
	require.Equal(t, "firstsecondthirdraw", off.Choices[0].Message.ReasoningContent, "switch off: legacy concatenation unchanged")

	on := ResponsesToChatCompletionsWithOptions(summaryResponse(), "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true})
	require.Equal(t, "first\n\nsecond\n\nthird\n\nraw", on.Choices[0].Message.ReasoningContent)
}

func TestResponsesEventToChatChunks_SummaryDeltas_SeparatorOnlyWhenSwitchOn(t *testing.T) {
	events := []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "r"}},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 0, Delta: "a"},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 0, Delta: "b"},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 1, Delta: "c"},
		{Type: "response.reasoning_text.delta", OutputIndex: 0, Delta: "raw"},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 2, SummaryIndex: 0, Delta: "d"},
	}
	collect := func(on bool) string {
		state := NewResponsesEventToChatState()
		state.ReasoningDetails = on
		var sb strings.Builder
		for _, ev := range events {
			for _, c := range ResponsesEventToChatChunks(ev, state) {
				if rc := c.Choices[0].Delta.ReasoningContent; rc != nil {
					sb.WriteString(*rc)
				}
			}
		}
		return sb.String()
	}
	require.Equal(t, "abcrawd", collect(false))
	require.Equal(t, "ab\n\ncraw\n\nd", collect(true))
}

func TestBufferedAccumulator_SeparateReasoningSummary(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.reasoning_summary_text.delta", SummaryIndex: 0, Delta: "x"})
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.reasoning_summary_text.delta", SummaryIndex: 1, Delta: "y"})
	acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.reasoning_text.delta", Delta: "z"})

	resp := &ResponsesResponse{Status: "completed"}
	acc.SupplementResponseOutput(resp)
	require.Equal(t, "xyz", ResponsesToChatCompletions(resp, "m").Choices[0].Message.ReasoningContent)

	acc.SeparateReasoningSummary(resp)
	require.Equal(t, "x\n\nyz", ResponsesToChatCompletions(resp, "m").Choices[0].Message.ReasoningContent)

	// 终止事件自带 output 时不动。
	own := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "reasoning", Summary: []ResponsesSummary{{Type: "summary_text", Text: "own"}}}}}
	acc.SeparateReasoningSummary(own)
	require.Equal(t, "own", own.Output[0].Summary[0].Text)
	acc.SeparateReasoningSummary(nil)

	// 单 part（没有分隔差异）时不改。
	single := NewBufferedResponseAccumulator()
	single.ProcessEvent(&ResponsesStreamEvent{Type: "response.reasoning_summary_text.delta", Delta: "only"})
	r2 := &ResponsesResponse{}
	single.SupplementResponseOutput(r2)
	single.SeparateReasoningSummary(r2)
	require.Equal(t, "only", r2.Output[0].Summary[0].Text)
}

// ---------------------------------------------------------------------------
// 流式：summary + encrypted 同一个 delta；reasoning 别名
// ---------------------------------------------------------------------------

func TestResponsesEventToChatChunks_ReasoningDetailsOn_SummaryOnlyItemAndKeyFallbacks(t *testing.T) {
	events := []*ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "r"}},
		// 只有摘要、没有密文、没有 id：按摘要文本去重
		{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", Summary: []ResponsesSummary{{Type: "summary_text", Text: "s1"}, {Type: "summary_text", Text: "s2"}}}},
		{Type: "response.output_item.done", Item: &ResponsesOutput{Type: "reasoning", Summary: []ResponsesSummary{{Type: "summary_text", Text: "s1"}, {Type: "summary_text", Text: "s2"}}}},
		{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{
			{Type: "reasoning", Summary: []ResponsesSummary{{Type: "summary_text", Text: "s1"}, {Type: "summary_text", Text: "s2"}}},
		}}},
	}
	got := runStreamForTest(t, true, events)
	require.Len(t, got, 3)
	require.Contains(t, got[1], `"reasoning_details":[{"type":"reasoning.summary","summary":"s1\n\ns2","format":"openai-responses-v1","index":0}]`)
}

// ---------------------------------------------------------------------------
// usage.completion_tokens_details.reasoning_tokens（不受开关影响）
// ---------------------------------------------------------------------------

func TestChatUsage_ReasoningTokens_NonStreamAndStream(t *testing.T) {
	usage := &ResponsesUsage{InputTokens: 10, OutputTokens: 40, OutputTokensDetails: &ResponsesOutputTokensDetails{ReasoningTokens: 25}}
	for _, on := range []bool{false, true} {
		resp := &ResponsesResponse{Status: "completed", Usage: usage}
		out := ResponsesToChatCompletionsWithOptions(resp, "m", ResponsesToChatCompletionsOptions{ReasoningDetails: on})
		raw, err := json.Marshal(out.Usage)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"completion_tokens_details":{"reasoning_tokens":25}`)

		state := NewResponsesEventToChatState()
		state.IncludeUsage = true
		state.ReasoningDetails = on
		chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Usage: usage}}, state)
		last := chunks[len(chunks)-1]
		require.NotNil(t, last.Usage)
		require.Equal(t, 25, last.Usage.CompletionTokensDetails.ReasoningTokens)
	}
}

// ---------------------------------------------------------------------------
// 请求带回的兼容（NormalizeChatReasoningInput，仅开关开启时由网关调用）
// ---------------------------------------------------------------------------

func normalizeAndConvert(t *testing.T, body string) []ResponsesInputItem {
	t.Helper()
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	NormalizeChatReasoningInput(&req)
	items, err := convertChatMessagesToResponsesInput(req.Messages)
	require.NoError(t, err)
	return items
}

func TestNormalizeChatReasoningInput_AliasReasoningUsedAsThinking(t *testing.T) {
	items := normalizeAndConvert(t, `{"messages":[{"role":"assistant","content":"a","reasoning":"alias thought"}]}`)
	require.Len(t, items, 1)
	require.Contains(t, chatInputText(t, items[0]), "<thinking>alias thought</thinking>")
}

func TestNormalizeChatReasoningInput_ReasoningContentWinsOverAlias(t *testing.T) {
	items := normalizeAndConvert(t, `{"messages":[{"role":"assistant","content":"a","reasoning_content":"primary","reasoning":"alias"}]}`)
	require.Contains(t, chatInputText(t, items[0]), "<thinking>primary</thinking>")
}

func TestNormalizeChatReasoningInput_SummaryAndTextDetailsUsedAsThinking(t *testing.T) {
	items := normalizeAndConvert(t, `{"messages":[{"role":"assistant","content":"a","reasoning_details":[
		{"type":"reasoning.text","text":"raw","index":1},
		{"type":"reasoning.summary","summary":"sum","index":0},
		{"type":"reasoning.summary","summary":""},
		{"type":"reasoning.text","text":""}]}]}`)
	require.Len(t, items, 1, "no reasoning item for non-encrypted details")
	require.Contains(t, chatInputText(t, items[0]), "<thinking>sum\n\nraw</thinking>")
}

func TestNormalizeChatReasoningInput_EncryptedWinsAndOthersUntouched(t *testing.T) {
	items := normalizeAndConvert(t, `{"messages":[
		{"role":"assistant","content":"a","reasoning":"alias","reasoning_details":[
			{"type":"reasoning.summary","summary":"sum"},{"type":"reasoning.encrypted","data":"ENC"}]},
		{"role":"user","content":"u","reasoning":"ignored on user"},
		{"role":"assistant","content":"b"}]}`)
	require.Len(t, items, 4)
	require.Equal(t, "reasoning", items[0].Type)
	require.NotContains(t, chatInputText(t, items[1]), "<thinking>")
	require.Equal(t, "user", items[2].Role)
	require.NotContains(t, chatInputText(t, items[3]), "<thinking>")

	NormalizeChatReasoningInput(nil)
}

func TestResponsesToChatCompletions_ReasoningDetailsFallbackSelection(t *testing.T) {
	summaryOnly := []ResponsesOutput{{Type: "reasoning", ID: "rs_f", Summary: []ResponsesSummary{{Type: "summary_text", Text: "fb"}}}}
	// output 没有任何 reasoning 元素：fallback 即使只有摘要也用。
	out := ResponsesToChatCompletionsWithOptions(&ResponsesResponse{Status: "completed"}, "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true, FallbackReasoning: summaryOnly})
	require.Contains(t, string(out.Choices[0].Message.ReasoningDetails), `"summary":"fb"`)

	// output 已有摘要、fallback 也只有摘要：保持 output。
	resp := &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "reasoning", ID: "rs_o", Summary: []ResponsesSummary{{Type: "summary_text", Text: "own"}}}}}
	out = ResponsesToChatCompletionsWithOptions(resp, "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true, FallbackReasoning: summaryOnly})
	require.Contains(t, string(out.Choices[0].Message.ReasoningDetails), `"summary":"own"`)
	require.NotContains(t, string(out.Choices[0].Message.ReasoningDetails), "fb")

	// fallback 带密文：取代只有摘要的 output。
	withEnc := []ResponsesOutput{{Type: "reasoning", ID: "rs_e", EncryptedContent: "E", Summary: []ResponsesSummary{{Type: "summary_text", Text: "fb"}}}}
	out = ResponsesToChatCompletionsWithOptions(resp, "m", ResponsesToChatCompletionsOptions{ReasoningDetails: true, FallbackReasoning: withEnc})
	require.Contains(t, string(out.Choices[0].Message.ReasoningDetails), `"data":"E"`)
}
