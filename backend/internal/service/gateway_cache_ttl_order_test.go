package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Validate the wire format independently of the production path collector.
func requireAnthropicLongBeforeShort(t *testing.T, body []byte) {
	t.Helper()
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &root))
	shortSeen := false
	check := func(raw json.RawMessage) {
		var block map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &block))
		if marker, ok := block["cache_control"]; ok {
			var cc struct {
				Type string `json:"type"`
				TTL  string `json:"ttl"`
			}
			require.NoError(t, json.Unmarshal(marker, &cc))
			require.Equal(t, "ephemeral", cc.Type)
			if cc.TTL == "1h" {
				require.False(t, shortSeen, "1h follows a shorter marker in outbound body: %s", body)
			} else {
				require.Contains(t, []string{"", "5m"}, cc.TTL)
				shortSeen = true
			}
		}
	}
	for _, field := range []string{"tools", "system"} {
		var blocks []json.RawMessage
		if raw := root[field]; len(raw) > 0 && raw[0] == '[' {
			require.NoError(t, json.Unmarshal(raw, &blocks))
			for _, block := range blocks {
				check(block)
			}
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	require.NoError(t, json.Unmarshal(root["messages"], &messages))
	for _, message := range messages {
		if len(message.Content) > 0 && message.Content[0] == '[' {
			var blocks []json.RawMessage
			require.NoError(t, json.Unmarshal(message.Content, &blocks))
			for _, block := range blocks {
				check(block)
			}
		}
	}
	if marker, ok := root["cache_control"]; ok {
		check(json.RawMessage(`{"cache_control":` + string(marker) + `}`))
	}
}

func TestCacheTTLOrder_PromotesOnlyShortPrefix(t *testing.T) {
	body := []byte(`{"tools":[{"name":"probe","input_schema":{"type":"object","const":9007199254740993},"cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"long","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"later","cache_control":{"type":"ephemeral","ttl":"5m"}}]}],"metadata":{"opaque":"keep"}}`)
	original := append([]byte(nil), body...)
	out := enforceCacheControlLimit(body)
	require.Equal(t, "1h", gjson.GetBytes(out, "tools.0.cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(out, "system.0.cache_control.ttl").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "messages.0.content.1.cache_control.ttl").String())
	require.Contains(t, string(out), `"const":9007199254740993`)
	require.Contains(t, string(out), `"metadata":{"opaque":"keep"}`)
	require.Equal(t, original, body, "caller-owned bytes must remain immutable")
	requireAnthropicLongBeforeShort(t, out)
}

func TestCacheTTLOrder_TopLevelAndMultipleLongBoundaries(t *testing.T) {
	for _, suffix := range []string{`,"cache_control":{"type":"ephemeral","ttl":"1h"}`, ``} {
		t.Run(fmt.Sprintf("auto=%t", suffix != ""), func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"d","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]` + suffix + `}`)
			out := enforceCacheControlLimit(body)
			require.Equal(t, "1h", gjson.GetBytes(out, "messages.0.content.1.cache_control.ttl").String())
			last := "5m"
			if suffix != "" {
				last = "1h"
			}
			require.Equal(t, last, gjson.GetBytes(out, "messages.0.content.3.cache_control.ttl").String())
			requireAnthropicLongBeforeShort(t, out)
		})
	}
}

func TestCacheTTLOrder_ValidAndInvalidValuesStayByteIdentical(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": ``, "malformed": `{"messages":[`,
		"no_marker":           `{ "messages": [{"role":"user","content":"hi"}], "opaque": 9007199254740993 }`,
		"all_short":           `{"system":[{"type":"text","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`,
		"long_before_short":   `{"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`,
		"unknown_type_anchor": `{"tools":[{"name":"t","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"unknown","ttl":"1h"}}]}]}`,
		"invalid_prefix":      `{"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"bad"}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"null_prefix":         `{"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":null}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"numeric_prefix":      `{"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":5}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"unknown_prefix":      `{"system":[{"type":"text","cache_control":{"type":"unknown","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"invalid_marker":      `{"system":[{"type":"text","cache_control":["ephemeral","5m"]}],"messages":[{"role":"user","content":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { require.Equal(t, raw, string(enforceCacheControlLimit([]byte(raw)))) })
	}
}

func TestCacheTTLOrder_PreservesBlockLimitAndThinkingCleanup(t *testing.T) {
	body := []byte(`{"tools":[{"name":"removed","cache_control":{"type":"ephemeral","ttl":"1h"}}],"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"thinking","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`)
	out := enforceCacheControlLimit(body)
	require.False(t, gjson.GetBytes(out, "tools.0.cache_control").Exists(), "original tool-first eviction must remain")
	require.False(t, gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists())
	require.Equal(t, "5m", gjson.GetBytes(out, "system.0.cache_control.ttl").String(), "removed illegal/evicted 1h markers must not promote surviving markers")
	require.False(t, gjson.GetBytes(out, "messages.0.content.1.cache_control.ttl").Exists())
}

const cacheTTLWireUsage = `"input_tokens":11,"cache_read_input_tokens":7,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":30}`

func cacheTTLWireUpstream(stream bool) *anthropicHTTPUpstreamRecorder {
	payload := `{"id":"msg_ttl","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{` + cacheTTLWireUsage + `,"output_tokens":3}}`
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
		payload = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_ttl","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{` + cacheTTLWireUsage + `}}}` + "\n\nevent: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\nevent: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}` + "\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}` + "\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(payload))}}
}

func TestCacheTTLOrder_MimicActualMessagesAndCountTokens(t *testing.T) {
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, stream := range []bool{false, true} {
			if endpoint == "count_tokens" && stream {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				c.Request.Header.Set("User-Agent", "third-party/1.0")
				body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-6","stream":%t,"tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, stream))
				original := append([]byte(nil), body...)
				parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-6", Stream: stream}
				upstream := cacheTTLWireUpstream(stream)
				svc := newForwardPartialUsageServiceForTest(upstream)
				account := &Account{ID: 7844, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Concurrency: 1, Credentials: map[string]any{"access_token": "ttl-test-token"}}
				if endpoint == "count_tokens" {
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
					c.Request.Header.Set("User-Agent", "third-party/1.0")
					upstream.resp.Body = io.NopCloser(strings.NewReader(`{"input_tokens":42}`))
					require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, parsed))
					require.JSONEq(t, `{"input_tokens":42}`, rec.Body.String())
				} else {
					result, err := svc.Forward(context.Background(), c, account, parsed)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 40, result.Usage.CacheCreationInputTokens)
					require.Equal(t, 10, result.Usage.CacheCreation5mTokens)
					require.Equal(t, 30, result.Usage.CacheCreation1hTokens)
				}
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "Bearer ttl-test-token", getHeaderRaw(upstream.lastReq.Header, "authorization"))
				require.True(t, gjson.GetBytes(upstream.lastBody, "tools.0.cache_control").Exists())
				require.Equal(t, "1h", gjson.GetBytes(upstream.lastBody, "tools.0.cache_control.ttl").String())
				requireAnthropicLongBeforeShort(t, upstream.lastBody)
				require.Equal(t, original, body)
			})
		}
	}
}

func TestCacheTTLOrder_APIKeyPassthroughRemainsOpaque(t *testing.T) {
	for _, endpoint := range []string{"messages", "count_tokens"} {
		t.Run(endpoint, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
			upstream := cacheTTLWireUpstream(false)
			svc := newForwardPartialUsageServiceForTest(upstream)
			parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-6"}
			account := newAnthropicAPIKeyAccountForTest()
			if endpoint == "count_tokens" {
				upstream.resp.Body = io.NopCloser(strings.NewReader(`{"input_tokens":42}`))
				require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, parsed))
			} else {
				_, err := svc.Forward(context.Background(), c, account, parsed)
				require.NoError(t, err)
			}
			require.Equal(t, body, upstream.lastBody)
			require.Equal(t, "upstream-anthropic-key", getHeaderRaw(upstream.lastReq.Header, "x-api-key"))
			require.Empty(t, getHeaderRaw(upstream.lastReq.Header, "authorization"))
		})
	}
}

func TestCacheTTLOrder_MimicMovedSystemAndToolNames(t *testing.T) {
	for name, raw := range map[string]string{
		"message": `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
		"system":  `{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"keep instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`,
		"tools":   `{"model":"claude-sonnet-4-6","tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(raw)
			var system any
			if value := gjson.GetBytes(body, "system"); value.Exists() {
				require.NoError(t, json.Unmarshal([]byte(value.Raw), &system))
			}
			body = rewriteSystemForNonClaudeCode(body, system)
			body = applyCountTokensMimicToolBreakpoints(body)
			requireAnthropicLongBeforeShort(t, body)
			if name == "system" {
				require.Contains(t, string(body), "keep instructions")
			}
		})
	}
}
