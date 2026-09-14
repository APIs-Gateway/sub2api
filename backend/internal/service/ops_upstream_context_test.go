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
