//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// reasoningErrorCache 的读写都返回错误，用于验证 reasoning 缓存 fail-open。
type reasoningErrorCache struct {
	stubGatewayCache
	mu       sync.Mutex
	setCalls int
	getCalls int
}

func (c *reasoningErrorCache) SetReasoningContent(context.Context, string, string, time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls++
	return errors.New("redis down")
}

func (c *reasoningErrorCache) GetReasoningContent(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	return "", errors.New("redis down")
}

const reasoningChatJSONTemplate = `{"id":"chatcmpl_rc_json","object":"chat.completion","model":"%s","choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"deep thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`

func reasoningChatJSON(model string) string {
	return fmt.Sprintf(reasoningChatJSONTemplate, model)
}

// 非流式响应里的 reasoning 也要按 reasoning item id 写缓存，且与返回给客户端的 id 一致。
func TestForwardResponses_ChatFallbackCachesNonStreamingReasoning(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_rc_json", reasoningChatJSON("gpt-5.4"))}
	cache := &reasoningRecordingCache{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, cache: cache}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)

	reasoningID := gjson.Get(rec.Body.String(), `output.#(type=="reasoning").id`).String()
	require.NotEmpty(t, reasoningID)
	require.Equal(t, map[string]string{reasoningID: "deep thought"}, cache.snapshotSets())
}

// 上游模型不一致被拦截（failover 切号）时，这份响应不会交给客户端，不得写入 reasoning 缓存。
func TestForwardResponses_ChatFallbackModelMismatchDoesNotCacheReasoning(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_rc_json_mismatch", reasoningChatJSON(upstreamModelMismatchGotModel))}
	cache := &reasoningRecordingCache{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, cache: cache}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.Nil(t, result)
	requireUpstreamModelMismatchPathFailover(t, c, rec, err, "gpt-5.4", upstreamModelMismatchGotModel, false)
	require.Empty(t, cache.snapshotSets())
}

// 缓存读写失败一律 fail-open：请求照常转发，encrypted-only item 不回注 reasoning_content。
func TestForwardResponses_ChatFallbackReasoningCacheErrorsFailOpen(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"stream":false,
		"input":[
			{"type":"reasoning","id":"item_plain","summary":[{"type":"summary_text","text":"plain thinking"}]},
			{"type":"reasoning","id":"item_enc","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"get_value","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_rc_json_err", reasoningChatJSON("gpt-5.4"))}
	cache := &reasoningErrorCache{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, cache: cache}

	result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	// 明文 summary 仍直接用于 reasoning_content（item_enc 覆盖不了已有的 pendingReasoning）。
	require.Equal(t, "plain thinking", gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").String())
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, 1, cache.getCalls, "encrypted-only item 应回查一次缓存")
	// 请求侧自愈回写 1 次 + 响应侧写入 1 次，均失败但不影响转发。
	require.Equal(t, 2, cache.setCalls)
}

func TestOpenAIGatewayService_ReasoningCacheHelpersNilCache(t *testing.T) {
	var nilSvc *OpenAIGatewayService
	require.Empty(t, nilSvc.reasoningContentByID("item"))
	nilSvc.recacheReasoningItemsFromInput(json.RawMessage(`[]`))
	nilSvc.setReasoningContent("item", "text")

	svc := &OpenAIGatewayService{}
	require.Empty(t, svc.reasoningContentByID("item"))
	svc.recacheReasoningItemsFromInput(json.RawMessage(`[{"type":"reasoning","id":"a","summary":[{"type":"summary_text","text":"x"}]}]`))
	svc.cacheReasoningItemsFromOutput([]apicompat.ResponsesOutput{{Type: "reasoning", ID: "a", Summary: []apicompat.ResponsesSummary{{Text: "x"}}}})
}

func TestOpenAIGatewayService_RecacheReasoningItemsFromInput(t *testing.T) {
	cache := &reasoningRecordingCache{}
	svc := &OpenAIGatewayService{cache: cache}

	// 非数组 / 空 / 非法 JSON：不写缓存。
	svc.recacheReasoningItemsFromInput(nil)
	svc.recacheReasoningItemsFromInput(json.RawMessage(`"plain string input"`))
	svc.recacheReasoningItemsFromInput(json.RawMessage(`[{"type":"reasoning"`))
	require.Empty(t, cache.snapshotSets())

	svc.recacheReasoningItemsFromInput(json.RawMessage(` [
		{"type":"reasoning","id":"item_a","summary":[{"type":"summary_text","text":"alpha"}]},
		{"type":"reasoning","id":"item_enc","summary":[],"encrypted_content":"opaque"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"no id"}]},
		{"type":"message","role":"user","content":"hi"}
	]`))
	require.Equal(t, map[string]string{"item_a": "alpha"}, cache.snapshotSets())
}

func TestOpenAIGatewayService_CacheReasoningItems(t *testing.T) {
	cache := &reasoningRecordingCache{}
	svc := &OpenAIGatewayService{cache: cache}

	svc.cacheReasoningItem(nil)
	svc.cacheReasoningItemsFromOutput([]apicompat.ResponsesOutput{
		{Type: "message", ID: "msg_1"},
		{Type: "reasoning", ID: ""},
		{Type: "reasoning", ID: "item_blank", Summary: []apicompat.ResponsesSummary{{Text: "  "}}},
		{Type: "reasoning", ID: "item_multi", Summary: []apicompat.ResponsesSummary{{Text: " one "}, {Text: ""}, {Text: "two"}}},
	})
	svc.cacheReasoningItemsFromEvents([]apicompat.ResponsesStreamEvent{
		{Type: "response.output_item.added", Item: &apicompat.ResponsesOutput{Type: "reasoning", ID: "item_added", Summary: []apicompat.ResponsesSummary{{Text: "early"}}}},
		{Type: "response.output_item.done"},
		{Type: "response.output_item.done", Item: &apicompat.ResponsesOutput{Type: "reasoning", ID: "item_done", Summary: []apicompat.ResponsesSummary{{Text: "final"}}}},
	})

	require.Equal(t, map[string]string{
		"item_multi": "one\ntwo",
		"item_done":  "final",
	}, cache.snapshotSets())
}
