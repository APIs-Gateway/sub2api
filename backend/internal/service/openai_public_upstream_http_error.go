package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// openAIPublicUpstreamHTTPError maps an upstream HTTP error status to the
// (status, error type, message) a downstream client may see on endpoints that
// used to relay the upstream error body verbatim (embeddings, alpha/search).
//
// The message is always a fixed gateway-owned string; the upstream message,
// body and request id never reach the client. Status codes follow the
// upstream 1:1 (same table as codexCanonicalStatus), with the single
// exception of an upstream 401, which becomes 503 so a client does not treat
// the upstream account's auth failure as its own API key being invalid.
func openAIPublicUpstreamHTTPError(upstreamStatus int) (status int, errType, message string) {
	switch {
	case upstreamStatus == http.StatusUnauthorized:
		status = http.StatusServiceUnavailable
	case upstreamStatus < 400 || upstreamStatus > 599:
		status = http.StatusBadGateway
	default:
		status = codexCanonicalStatus(upstreamStatus)
	}
	_, errType, message, _ = MapUpstreamErrorDefault(upstreamStatus)
	return status, errType, message
}

// writeOpenAIPublicUpstreamHTTPError records the real upstream error in the
// ops error log, then writes the sanitized client response through write.
func (s *OpenAIGatewayService) writeOpenAIPublicUpstreamHTTPError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	body []byte,
	write func(c *gin.Context, status int, errType, message string),
) {
	if c == nil || resp == nil {
		return
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	upstreamDetail := ""
	if s != nil && s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	event := OpsUpstreamErrorEvent{
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  upstreamRequestIDFromHeader(resp.Header),
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)

	status, errType, message := openAIPublicUpstreamHTTPError(resp.StatusCode)
	// Retry-After is client-actionable and carries no upstream wording.
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" && c.Writer.Header().Get("Retry-After") == "" {
		c.Header("Retry-After", ra)
	}
	write(c, status, errType, message)
}
