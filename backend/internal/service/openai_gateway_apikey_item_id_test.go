//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newAPIKeyItemIDTestAccount(extra map[string]any) *Account {
	if extra == nil {
		extra = map[string]any{}
	}
	if _, ok := extra["openai_passthrough"]; !ok {
		extra["openai_passthrough"] = true
	}
	if _, ok := extra["openai_responses_supported"]; !ok {
		extra["openai_responses_supported"] = true
	}
	return &Account{
		ID:             789,
		Name:           "apikey-item-id-acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeAPIKey,
		Concurrency:    1,
		Credentials:    map[string]any{"api_key": "sk-item-id-test", "base_url": "https://api.openai.com"},
		Extra:          extra,
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}
}

func newAPIKeyItemIDTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
}

func newAPIKeyItemIDTestContext() *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "curl/8.0")
	return c
}

// TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidInputItemIDs verifies
// that message and function-call input items replayed with an internal
// item_* id (instead of the msg_*/fc_* prefix OpenAI upstream requires) are
// stripped before the request reaches the upstream API-key account, while
// valid ids and every other field on every item are preserved untouched.
func TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidInputItemIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newAPIKeyItemIDTestService(upstream)
	c := newAPIKeyItemIDTestContext()
	account := newAPIKeyItemIDTestAccount(nil)

	body := []byte(`{
		"model":"gpt-5.2",
		"stream":false,
		"input":[
			{"type":"message","id":"item_bad_message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
			{"type":"function_call","id":"item_bad_call","call_id":"call_123","name":"exec_command","arguments":"{}"},
			{"type":"message","id":"msg_valid","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call","id":"fc_valid","call_id":"call_456","name":"apply_patch","arguments":"{}"},
			{"type":"function_call_output","id":"item_output","call_id":"call_123","output":"done"},
			{"type":"web_search_call","id":"item_unconstrained"}
		]
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	forwarded := upstream.lastBody
	require.False(t, gjson.GetBytes(forwarded, "input.0.id").Exists(), "item_* id must be stripped from message")
	require.Equal(t, "hello", gjson.GetBytes(forwarded, "input.0.content.0.text").String(), "non-id fields must survive sanitization")
	require.False(t, gjson.GetBytes(forwarded, "input.1.id").Exists(), "item_* id must be stripped from function_call")
	require.Equal(t, "call_123", gjson.GetBytes(forwarded, "input.1.call_id").String())
	require.Equal(t, "exec_command", gjson.GetBytes(forwarded, "input.1.name").String())
	require.Equal(t, "{}", gjson.GetBytes(forwarded, "input.1.arguments").String())
	require.Equal(t, "msg_valid", gjson.GetBytes(forwarded, "input.2.id").String(), "valid msg_* id must remain usable for the client/upstream")
	require.Equal(t, "fc_valid", gjson.GetBytes(forwarded, "input.3.id").String(), "valid fc_* id must remain usable for the client/upstream")
	require.Equal(t, "item_output", gjson.GetBytes(forwarded, "input.4.id").String(), "function_call_output is not constrained by shouldStripOpenAIResponsesInputItemID and must be untouched")
	require.Equal(t, "call_123", gjson.GetBytes(forwarded, "input.4.call_id").String())
	require.Equal(t, "item_unconstrained", gjson.GetBytes(forwarded, "input.5.id").String(), "unconstrained item types (e.g. web_search_call) must be untouched")
}

// TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidReasoningItemIDs
// verifies that reasoning items with a non-rs id (e.g. item_*) are stripped
// before forwarding. OpenAI upstream requires reasoning ids to begin with
// "rs" and rejects item_* with 400: "Expected an ID that begins with 'rs'."
func TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidReasoningItemIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newAPIKeyItemIDTestService(upstream)
	c := newAPIKeyItemIDTestContext()
	account := newAPIKeyItemIDTestAccount(nil)

	body := []byte(`{
		"model":"gpt-5.2",
		"stream":false,
		"input":[
			{"type":"reasoning","id":"item_bad_reasoning","summary":[]},
			{"type":"reasoning","id":"rs_valid","summary":[]},
			{"type":"message","id":"msg_valid","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	forwarded := upstream.lastBody
	require.False(t, gjson.GetBytes(forwarded, "input.0.id").Exists(), "item_* id should be stripped from reasoning")
	require.Equal(t, "rs_valid", gjson.GetBytes(forwarded, "input.1.id").String(), "valid rs* id must be preserved")
	require.Equal(t, "msg_valid", gjson.GetBytes(forwarded, "input.2.id").String())
}

// TestOpenAIGatewayService_APIKeyPassthrough_NoSanitizationNeededPreservesBodyExactly
// verifies the sanitizer is a strict no-op (byte-for-byte forwarded body) when
// there is nothing to strip, so the hot path for well-formed requests is not
// penalized by an unnecessary re-encode.
func TestOpenAIGatewayService_APIKeyPassthrough_NoSanitizationNeededPreservesBodyExactly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newAPIKeyItemIDTestService(upstream)
	c := newAPIKeyItemIDTestContext()
	account := newAPIKeyItemIDTestAccount(nil)

	body := []byte(`{"model":"gpt-5.2","stream":false,"input":[{"type":"message","id":"msg_valid","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, body, upstream.lastBody)
}

// TestShouldStripOpenAIResponsesInputItemID covers the per-item-type prefix
// rules directly, including the reasoning case added on top of the
// message/tool-call cases.
func TestShouldStripOpenAIResponsesInputItemID(t *testing.T) {
	cases := []struct {
		name     string
		itemType string
		id       string
		want     bool
	}{
		{"reasoning item_* id", "reasoning", "item_bad_reasoning", true},
		{"reasoning rs id", "reasoning", "rs_abc123", false},
		{"reasoning empty id", "reasoning", "", false},
		{"message msg id", "message", "msg_abc", false},
		{"message item id", "message", "item_x", true},
		{"message empty id", "message", "", false},
		{"function_call fc id", "function_call", "fc_abc", false},
		{"function_call item id", "function_call", "item_x", true},
		{"local_shell_call fc id", "local_shell_call", "fc_abc", false},
		{"local_shell_call item id", "local_shell_call", "item_x", true},
		{"function_call_output unconstrained", "function_call_output", "item_x", false},
		{"unconstrained type", "web_search_call", "ws_001", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldStripOpenAIResponsesInputItemID(tc.itemType, tc.id))
		})
	}
}

// TestSanitizeOpenAIResponsesInputItemIDs_NonArrayInputIsNoOp verifies bodies
// without a top-level "input" array (or with input missing entirely) are
// returned unchanged rather than erroring out.
func TestSanitizeOpenAIResponsesInputItemIDs_NonArrayInputIsNoOp(t *testing.T) {
	body := []byte(`{"model":"gpt-5.2","stream":false}`)
	sanitized, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, sanitized)
}

// TestSanitizeOpenAIResponsesInputItemIDs_AllocationGrowthIsLinear guards
// against a quadratic re-implementation: sanitizing 10x more input items must
// not cause more than roughly 10x the allocation, which would be the case if
// each stripped id triggered a whole-body sjson rewrite instead of a
// single rebuild pass.
func TestSanitizeOpenAIResponsesInputItemIDs_AllocationGrowthIsLinear(t *testing.T) {
	makeBody := func(itemCount int) []byte {
		items := make([]string, itemCount)
		for i := range items {
			items[i] = fmt.Sprintf(`{"type":"message","id":"item_%d","role":"user","content":[{"type":"input_text","text":"hello"}]}`, i)
		}
		return []byte(`{"model":"gpt-5.2","input":[` + strings.Join(items, ",") + `]}`)
	}
	allocatedBytes := func(body []byte) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		sanitized, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		require.True(t, changed)
		require.NotEmpty(t, sanitized)
		return after.TotalAlloc - before.TotalAlloc
	}

	smallAllocated := allocatedBytes(makeBody(20))
	largeAllocated := allocatedBytes(makeBody(200))
	require.Less(t, largeAllocated, smallAllocated*30,
		"10x more input items must not cause quadratic whole-body allocation growth")
}

// TestFilterCodexInputWithOptions_OAuthReasoningHandlingUnaffected is a
// regression guard proving the OAuth-only continuation-mode path
// (filterCodexInputWithOptions, gated on account.Type == AccountTypeOAuth) is
// untouched by the new API-key sanitizer: reasoning items still have their id
// unconditionally stripped (independent of any msg/rs/fc prefix) and still
// get an empty summary backfilled, exactly as required by issue #1957 for
// store=false ChatGPT Codex sessions. This is a deliberately different rule
// from shouldStripOpenAIResponsesInputItemID's prefix-based reasoning check,
// and the two must keep evolving independently since they run on mutually
// exclusive account types.
func TestFilterCodexInputWithOptions_OAuthReasoningHandlingUnaffected(t *testing.T) {
	input := []any{
		map[string]any{
			"type":              "reasoning",
			"id":                "rs_valid_would_survive_apikey_path",
			"encrypted_content": "opaque",
		},
		map[string]any{
			"type": "message",
			"id":   "item_bad_message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "hello"},
			},
		},
	}

	filtered := filterCodexInputWithOptions(input, codexInputFilterOptions{PreserveReferences: true})
	require.Len(t, filtered, 2)

	reasoning, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	_, hasID := reasoning["id"]
	require.False(t, hasID, "OAuth path must still unconditionally strip reasoning id, even a valid-looking rs_* one")
	require.Equal(t, "opaque", reasoning["encrypted_content"], "encrypted_content must be preserved")
	require.Equal(t, []any{}, reasoning["summary"], "missing summary must still be backfilled to an empty array")

	msg, ok := filtered[1].(map[string]any)
	require.True(t, ok)
	_, hasMsgID := msg["id"]
	require.False(t, hasMsgID, "OAuth path message id prefix stripping must be unaffected")
}
