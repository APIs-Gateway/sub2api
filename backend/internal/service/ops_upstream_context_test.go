package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}

func TestMarkOpsUpstreamFailoverRecovered(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "failover", Message: "first account failed"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "retry_exhausted_failover", Message: "retry failed before failover"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "final upstream error"})

	MarkOpsUpstreamFailoverRecovered(c)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 3)
	require.True(t, events[0].FailoverRecovered)
	require.True(t, events[1].FailoverRecovered)
	require.False(t, events[2].FailoverRecovered)

	payload, err := json.Marshal(events)
	require.NoError(t, err)
	jsonText := string(payload)
	require.Contains(t, jsonText, `"failover_recovered":true`)
	require.True(t, strings.Count(jsonText, `"failover_recovered":false`) >= 1)
}

func TestMarkOpsUpstreamFailoverRecoveredGuards(t *testing.T) {
	MarkOpsUpstreamFailoverRecovered(nil)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	MarkOpsUpstreamFailoverRecovered(c)
	_, ok := c.Get(OpsUpstreamErrorsKey)
	require.False(t, ok)

	c.Set(OpsUpstreamErrorsKey, "bad-type")
	MarkOpsUpstreamFailoverRecovered(c)
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	require.Equal(t, "bad-type", raw)

	events := []*OpsUpstreamErrorEvent{
		nil,
		{Kind: "  FAILOVER  ", Message: "spaced and upper case"},
	}
	c.Set(OpsUpstreamErrorsKey, events)
	MarkOpsUpstreamFailoverRecovered(c)
	require.True(t, events[1].FailoverRecovered)
}

func TestMarkOpsStreamFailure_TrimsAndPreservesClassification(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	MarkOpsStreamFailure(c, " upstream_error ", " upstream_http2_stream_error ", " upstream failed ", 502)

	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "upstream_error", streamErr.ErrType)
	require.Equal(t, "upstream_http2_stream_error", streamErr.Code)
	require.Equal(t, "upstream failed", streamErr.Message)
	require.Equal(t, 502, streamErr.IntendedStatus)
	require.True(t, streamErr.CountTowardsSLA)

	MarkOpsStreamError(c, "later_error", "must not replace", 500)
	streamErr, ok = GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "upstream_error", streamErr.ErrType)
	require.Equal(t, "upstream_http2_stream_error", streamErr.Code)
}

func TestUpstreamRequestIDFromHeader(t *testing.T) {
	mk := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name string
		h    http.Header
		want string
	}{
		{"nil header", nil, ""},
		{"empty header", mk(), ""},
		{"x-request-id first", mk("x-request-id", " req-1 ", "x-oneapi-request-id", "one-1", "cf-ray", "ray-1"), "req-1"},
		{"blank x-request-id falls through", mk("x-request-id", "   ", "x-oneapi-request-id", "one-1"), "one-1"},
		{"oneapi (a6api / rivoapi)", mk("X-Oneapi-Request-Id", "one-1", "cf-ray", "ray-1"), "one-1"},
		{"rixapi (platform.ephone.chat)", mk("x-rixapi-request-id", "rix-1", "cf-ray", "ray-1"), "rix-1"},
		{"bedrock", mk("x-amzn-requestid", "aws-1", "cf-ray", "ray-1"), "aws-1"},
		{"cf-ray last resort", mk("cf-ray", "8a1b2c3d-HKG"), "8a1b2c3d-HKG"},
		{"unknown headers only", mk("xai-request-id", "xai-1"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, upstreamRequestIDFromHeader(tc.h))
		})
	}
}

func TestOpsUpstreamHeaderFingerprint(t *testing.T) {
	require.Nil(t, opsUpstreamHeaderFingerprint(nil))
	require.Nil(t, opsUpstreamHeaderFingerprint(http.Header{}))

	h := http.Header{}
	h.Set("Server", " cloudflare ")
	h.Set("Via", "1.1 google")
	h.Set("X-New-Api-Version", "")
	h.Set("Cf-Ray", strings.Repeat("r", 200))
	h.Set("Set-Cookie", "a=b")
	h.Set("Authorization", "Bearer x")
	h.Set("Content-Type", "application/json")
	got := opsUpstreamHeaderFingerprint(h)
	require.Equal(t, "cloudflare", got["server"])
	require.Equal(t, "1.1 google", got["via"])
	require.Len(t, got["cf-ray"], 128, "值截到 128 字节")
	require.NotContains(t, got, "x-new-api-version", "空值不记")
	require.NotContains(t, got, "set-cookie")
	require.NotContains(t, got, "authorization")
	require.NotContains(t, got, "content-type")
	require.Len(t, got, 3)

	// 序列化：omitempty，nil 不占字段。
	raw, err := json.Marshal(OpsUpstreamErrorEvent{Kind: "failover"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "upstream_headers")
	raw, err = json.Marshal(OpsUpstreamErrorEvent{Kind: "failover", UpstreamHeaders: got})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"upstream_headers":{`)
}

func TestUpstreamRequestIDFromErrorBody(t *testing.T) {
	const rid = "2026091312450379123456"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"error.request_id", `{"error":{"code":"bad_response","message":"boom","request_id":" ` + rid + ` "}}`, rid},
		{"top-level request_id", `{"request_id":"` + rid + `","error":{"message":"boom"}}`, rid},
		{"only embedded in message", `{"error":{"message":"上游服务暂时不可用。 request_id: ` + rid + `"}}`, rid},
		{"error.request_id wins over message", `{"error":{"message":"request_id: other-000001","request_id":"` + rid + `"}}`, rid},
		{"message id too short is ignored", `{"error":{"message":"request_id: abc"}}`, ""},
		{"non-string request_id ignored", `{"error":{"request_id":12345678}}`, ""},
		{"not json", `upstream exploded request_id: ` + rid, ""},
		{"empty", ``, ""},
		{"blank", `   `, ""},
		{"oversized body", `{"error":{"request_id":"` + rid + `","message":"` + strings.Repeat("x", 70*1024) + `"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, upstreamRequestIDFromErrorBody(tc.body))
		})
	}
	long := upstreamRequestIDFromErrorBody(`{"request_id":"` + strings.Repeat("r", 300) + `"}`)
	require.Len(t, long, upstreamRequestIDMaxBytes, "截到 128 字节")
}

func TestAppendOpsUpstreamError_FallsBackToRequestIDInErrorBody(t *testing.T) {
	newCtx := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		return c
	}
	events := func(c *gin.Context) []*OpsUpstreamErrorEvent {
		v, ok := c.Get(OpsUpstreamErrorsKey)
		require.True(t, ok)
		return v.([]*OpsUpstreamErrorEvent)
	}

	// 头里没有：从响应体兜底。
	c := newCtx()
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", UpstreamResponseBody: `{"error":{"message":"boom","request_id":"body-rid-0001"}}`})
	require.Equal(t, "body-rid-0001", events(c)[0].UpstreamRequestID)

	// 响应体没有、Detail 里有。
	c = newCtx()
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", UpstreamResponseBody: `not json`, Detail: `{"request_id":"detail-rid-0001"}`})
	require.Equal(t, "detail-rid-0001", events(c)[0].UpstreamRequestID)

	// 头里已有值：不覆盖。
	c = newCtx()
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", UpstreamRequestID: " header-rid ", UpstreamResponseBody: `{"error":{"request_id":"body-rid-0001"}}`})
	require.Equal(t, "header-rid", events(c)[0].UpstreamRequestID)

	// 都没有：保持空。
	c = newCtx()
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", UpstreamResponseBody: `{"error":{"message":"boom"}}`})
	require.Empty(t, events(c)[0].UpstreamRequestID)
}
