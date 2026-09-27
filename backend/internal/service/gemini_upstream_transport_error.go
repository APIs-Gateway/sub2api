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
	if isClientCanceledTransportError(ctx, err) {
		return err
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

	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}
	if classifyUpstreamTransportError(err).Persistent {
		tempUnscheduleAccountForTransportError(ctx, s.accountRepo, account, safeErr)
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: geminiTransportFailoverBody,
	}
}
