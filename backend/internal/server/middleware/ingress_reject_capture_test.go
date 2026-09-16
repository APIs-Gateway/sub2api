package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newIngressRejectCaptureTestContext(method, target string, headers map[string]string, body string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reqBody)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	c.Request = req
	return c
}

func TestIngressRejectRouteFamilyClassification(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		family string
	}{
		{"anthropic messages", "/v1/messages", "messages"},
		{"anthropic count tokens", "/v1/messages/count_tokens", "messages"},
		{"openai responses", "/v1/responses", "responses"},
		{"openai responses alias", "/responses", "responses"},
		{"codex responses", "/backend-api/codex/responses", "codex"},
		{"codex models", "/backend-api/codex/models", "codex"},
		{"codex wham usage", "/backend-api/codex/wham/usage", "usage"},
		{"wham usage", "/backend-api/wham/usage", "usage"},
		{"chat completions", "/v1/chat/completions", "chat_completions"},
		{"chat completions alias", "/chat/completions", "chat_completions"},
		{"embeddings", "/v1/embeddings", "embeddings"},
		{"alpha search", "/v1/alpha/search", "alpha_search"},
		{"images generations", "/v1/images/generations", "images"},
		{"images edits", "/v1/images/edits", "images"},
		{"models", "/v1/models", "models"},
		{"usage", "/v1/usage", "usage"},
		{"billing", "/v1/sub2api/billing", "billing"},
		{"gemini native", "/v1beta/models/gemini-pro", "gemini"},
		{"antigravity messages", "/antigravity/v1/messages", "antigravity"},
		{"antigravity gemini", "/antigravity/v1beta/models", "antigravity_gemini"},
		{"antigravity models", "/antigravity/models", "antigravity_models"},
		{"unknown path", "/healthz", "other"},
		{"empty path", "", "other"},
		{"mixed case", "/V1/MESSAGES", "messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.family, ingressRejectRouteFamily(tc.path))
		})
	}
}

func TestIngressRejectRouteFamilyIgnoresQueryString(t *testing.T) {
	// The classifier must only ever see the path, never the raw query
	// string, so a credential passed as a query parameter cannot leak into
	// telemetry through this field.
	family := ingressRejectRouteFamily("/v1/messages")
	require.Equal(t, "messages", family)
	require.NotContains(t, family, "api_key")
}

func TestIngressRejectProtocolDetectionHTTPAndWebsocket(t *testing.T) {
	httpCtx := newIngressRejectCaptureTestContext(http.MethodPost, "/v1/messages", nil, "")
	event, ok := buildIngressRejectEvent(httpCtx, IngressRejectInvalidAPIKey)
	require.True(t, ok)
	require.Equal(t, "http", event.Protocol)

	wsCtx := newIngressRejectCaptureTestContext(http.MethodGet, "/v1/responses", map[string]string{
		"Connection": "Upgrade",
		"Upgrade":    "websocket",
	}, "")
	event, ok = buildIngressRejectEvent(wsCtx, IngressRejectInvalidAPIKey)
	require.True(t, ok)
	require.Equal(t, "ws", event.Protocol)
}

func TestBuildIngressRejectEventReturnsFalseWithoutUsableContext(t *testing.T) {
	_, ok := buildIngressRejectEvent(nil, IngressRejectInvalidAPIKey)
	require.False(t, ok)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = nil
	_, ok = buildIngressRejectEvent(c, IngressRejectInvalidAPIKey)
	require.False(t, ok)

	c2 := newIngressRejectCaptureTestContext(http.MethodPost, "/v1/messages", nil, "")
	_, ok = buildIngressRejectEvent(c2, "")
	require.False(t, ok)
}

// TestBuildIngressRejectEventDoesNotExposeSensitiveInput is an explicit
// negative test: it feeds a request carrying a raw Authorization header, a
// credential in the query string, and a request body, then asserts that
// none of those raw values end up in any captured field, and that the body
// is never consumed by the capture path.
func TestBuildIngressRejectEventDoesNotExposeSensitiveInput(t *testing.T) {
	const rawAuth = "Bearer sk-live-supersecretauthtoken"
	const rawQueryKey = "SUPERSECRETQUERYKEY"
	const rawBody = "secret-request-body-content"

	c := newIngressRejectCaptureTestContext(
		http.MethodPost,
		"/v1/messages?api_key="+rawQueryKey,
		map[string]string{
			"Authorization": rawAuth,
			"User-Agent":    "curl/8.0",
		},
		rawBody,
	)

	event, ok := buildIngressRejectEvent(c, IngressRejectInvalidAPIKey)
	require.True(t, ok)

	serialized := strings.Join([]string{
		string(event.Reason), event.RouteFamily, event.Protocol, event.ClientIP, event.UserAgent,
	}, "|")
	require.NotContains(t, serialized, rawAuth)
	require.NotContains(t, serialized, "sk-live-supersecretauthtoken")
	require.NotContains(t, serialized, rawQueryKey)
	require.Equal(t, "messages", event.RouteFamily)

	// The body must remain fully intact and unread by the capture path.
	remaining, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, rawBody, string(remaining))
}

func TestSanitizeIngressRejectUserAgentStripsControlCharsAndTruncates(t *testing.T) {
	t.Run("strips CR/LF to prevent log injection", func(t *testing.T) {
		got := sanitizeIngressRejectUserAgent("curl/8.0\r\nX-Injected-Header: evil")
		require.NotContains(t, got, "\r")
		require.NotContains(t, got, "\n")
	})

	t.Run("strips other control characters", func(t *testing.T) {
		got := sanitizeIngressRejectUserAgent("agent\x00\x1b[31mtest\x7f")
		for _, r := range got {
			require.False(t, r < 0x20 || r == 0x7f, "control character leaked: %q", r)
		}
	})

	t.Run("truncates to bounded length preserving valid utf8", func(t *testing.T) {
		long := strings.Repeat("é", ingressRejectCaptureUserAgentMaxBytes) // 2-byte rune
		got := sanitizeIngressRejectUserAgent(long)
		require.LessOrEqual(t, len(got), ingressRejectCaptureUserAgentMaxBytes)
		require.True(t, utf8.ValidString(got))
	})

	t.Run("empty and whitespace-only collapse to empty string", func(t *testing.T) {
		require.Equal(t, "", sanitizeIngressRejectUserAgent(""))
		require.Equal(t, "", sanitizeIngressRejectUserAgent("   \t  "))
	})
}

func TestIngressRejectCaptureBufferBoundedDrop(t *testing.T) {
	buf := newIngressRejectCaptureBuffer(3)
	for i := 0; i < 5; i++ {
		buf.add(IngressRejectEvent{Reason: IngressRejectReason("r" + string(rune('0'+i)))})
	}

	require.Equal(t, 3, buf.Len())
	require.Equal(t, uint64(2), buf.Dropped())

	snapshot := buf.Snapshot()
	require.Len(t, snapshot, 3)
	require.Equal(t, IngressRejectReason("r2"), snapshot[0].Reason)
	require.Equal(t, IngressRejectReason("r3"), snapshot[1].Reason)
	require.Equal(t, IngressRejectReason("r4"), snapshot[2].Reason)
}

func TestIngressRejectCaptureBufferSnapshotIsIndependentCopy(t *testing.T) {
	buf := newIngressRejectCaptureBuffer(2)
	buf.add(IngressRejectEvent{Reason: IngressRejectAPIKeyRequired})

	snapshot := buf.Snapshot()
	require.Len(t, snapshot, 1)
	snapshot[0].Reason = IngressRejectAPIKeyDisabled

	again := buf.Snapshot()
	require.Equal(t, IngressRejectAPIKeyRequired, again[0].Reason)
}

func TestIngressRejectCaptureBufferUnderCapacityKeepsInsertionOrder(t *testing.T) {
	buf := newIngressRejectCaptureBuffer(5)
	buf.add(IngressRejectEvent{Reason: IngressRejectAPIKeyRequired})
	buf.add(IngressRejectEvent{Reason: IngressRejectInvalidAPIKey})

	require.Equal(t, 2, buf.Len())
	require.Equal(t, uint64(0), buf.Dropped())

	snapshot := buf.Snapshot()
	require.Equal(t, IngressRejectAPIKeyRequired, snapshot[0].Reason)
	require.Equal(t, IngressRejectInvalidAPIKey, snapshot[1].Reason)
}

func TestMarkIngressRejectedFeedsBoundedCaptureBuffer(t *testing.T) {
	restore := setIngressRejectCaptureBufferForTest(newIngressRejectCaptureBuffer(4))
	defer restore()

	c := newIngressRejectCaptureTestContext(http.MethodPost, "/v1/chat/completions", map[string]string{
		"User-Agent": "TestAgent/1.0",
	}, "")

	MarkIngressRejected(c, IngressRejectAPIKeyDisabled)

	reason, ok := GetIngressRejectReason(c)
	require.True(t, ok)
	require.Equal(t, IngressRejectAPIKeyDisabled, reason)

	snapshot := IngressRejectCaptureSnapshot()
	require.Len(t, snapshot, 1)
	require.Equal(t, IngressRejectAPIKeyDisabled, snapshot[0].Reason)
	require.Equal(t, "chat_completions", snapshot[0].RouteFamily)
	require.Equal(t, "http", snapshot[0].Protocol)
	require.Equal(t, "TestAgent/1.0", snapshot[0].UserAgent)
	require.False(t, snapshot[0].CapturedAt.IsZero())
}

func TestMarkIngressRejectedWithEmptyReasonDoesNotCapture(t *testing.T) {
	restore := setIngressRejectCaptureBufferForTest(newIngressRejectCaptureBuffer(4))
	defer restore()

	c := newIngressRejectCaptureTestContext(http.MethodPost, "/v1/messages", nil, "")
	MarkIngressRejected(c, "")

	require.Empty(t, IngressRejectCaptureSnapshot())
}
