package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCommittedCompactKeepaliveContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)
	service.MarkOpenAICompactClientStream(c)
	stop := service.StartOpenAICompactSSEKeepalive(c, time.Millisecond)
	require.Eventually(t, func() bool {
		return c.Writer.Written()
	}, time.Second, time.Millisecond)
	return c, rec, stop
}

func TestOpenAIGatewayHandlerHandleStreamingAwareErrorAfterCompactKeepalive(t *testing.T) {
	c, rec, stop := newCommittedCompactKeepaliveContext(t)
	defer stop()

	h := &OpenAIGatewayHandler{}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "upstream failed", false)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: response.failed")
	require.Contains(t, rec.Body.String(), `"message":"upstream failed"`)
}

func TestOpenAIGatewayHandlerErrorResponseAfterCompactKeepalive(t *testing.T) {
	c, rec, stop := newCommittedCompactKeepaliveContext(t)
	defer stop()

	h := &OpenAIGatewayHandler{}
	h.errorResponse(c, http.StatusForbidden, "permission_error", "blocked")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: response.failed")
	require.Contains(t, rec.Body.String(), `"code":"permission_denied"`)
}

func TestOpenAIGatewayHandlerEnsureForwardErrorResponseAfterCompactKeepalive(t *testing.T) {
	c, rec, stop := newCommittedCompactKeepaliveContext(t)
	defer stop()

	h := &OpenAIGatewayHandler{}
	require.True(t, h.ensureForwardErrorResponse(c, false))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: response.failed")
}

func TestOpenAIGatewayHandlerCompactKeepaliveInterval(t *testing.T) {
	require.Zero(t, (&OpenAIGatewayHandler{}).openAICompactKeepaliveInterval())
	require.Zero(t, (&OpenAIGatewayHandler{cfg: &config.Config{}}).openAICompactKeepaliveInterval())

	h := &OpenAIGatewayHandler{cfg: &config.Config{}}
	h.cfg.Gateway.StreamKeepaliveInterval = 3
	require.Equal(t, 3*time.Second, h.openAICompactKeepaliveInterval())
}

func TestLogOpenAIRemoteCompactOutcomeKeepaliveFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Status(http.StatusOK)
	service.MarkOpsStreamError(c, "upstream_error", "failed after keepalive", http.StatusBadGateway)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now())

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "failed"))
}
