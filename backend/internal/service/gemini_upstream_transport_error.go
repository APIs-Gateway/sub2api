package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The handler uses this Google error body if every account fails before a
// response is committed. The client-facing error stays generic; the sanitized
// transport detail is recorded in ops for diagnosis.
var geminiTransportFailoverBody = []byte(`{"error":{"code":502,"message":"Upstream request failed","status":"INTERNAL"}}`)

// handleGeminiUpstreamTransportError handles a failed RoundTrip before Gemini
// has returned an HTTP response. Once an upstream response is received, the
// existing HTTP status and streaming paths own retries and client output.
func (s *GeminiMessagesCompatService) handleGeminiUpstreamTransportError(ctx context.Context, c *gin.Context, account *Account, err error) error {
	var requestErr error
	if ctx != nil {
		requestErr = ctx.Err()
	}
	// A canceled or expired request can make RoundTrip return a matching context
	// error; this is not an upstream fault and must not enter ops attribution.
	if requestErr != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return requestErr
	}

	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: 0,
		Kind:               "request_error",
		Message:            safeErr,
	})

	// Preserve fork ops attribution for an independent transport failure (for
	// example EOF) that races with a client disconnect, but never retry or
	// unschedule an account after the request itself has ended.
	if requestErr != nil {
		return requestErr
	}
	// A provider or proxy may cancel its own operation while the client request
	// remains live. That failure must still fail over, or the handler would see
	// a plain error without a response to send.
	if classifyUpstreamTransportError(err).Persistent {
		tempUnscheduleAccountForTransportError(ctx, s.accountRepo, account, safeErr)
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: geminiTransportFailoverBody,
	}
}
