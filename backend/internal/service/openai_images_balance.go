package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	OpenAIImagesInsufficientBalanceCode    = "insufficient_balance"
	OpenAIImagesInsufficientBalanceMessage = "Upstream image account has insufficient balance"
	openAIImagesBalanceCooldown            = 5 * time.Minute
	openAIImagesBalanceRateLimitReason     = "openai_images_insufficient_balance"
)

// The production account repository implements this without replacing a later
// reset already stored for the same image scope. Other repository substitutes
// must opt in explicitly; a failed cooldown must never block this request's
// account failover.
type openAIImagesBalanceRateLimitExtender interface {
	ExtendModelRateLimit(context.Context, int64, string, time.Time, ...string) error
}

// Only explicit machine-readable error fields may trigger account cooling.
// Messages can contain a client's prompt or arbitrary upstream prose.
func isOpenAIImagesInsufficientBalance(body []byte) bool {
	var value any
	if len(body) == 0 || json.Unmarshal(body, &value) != nil {
		return false
	}
	return hasOpenAIImagesInsufficientBalance(value, 0)
}

func hasOpenAIImagesInsufficientBalance(value any, depth int) bool {
	if depth > 6 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if text, ok := child.(string); ok {
				normalizedValue := strings.ToLower(strings.Trim(strings.TrimSpace(text), "."))
				switch normalizedKey {
				case "errorkey", "error_key", "code":
					if normalizedValue == OpenAIImagesInsufficientBalanceCode {
						return true
					}
				}
			}
			if isOpenAIImagesErrorContainer(normalizedKey) && hasOpenAIImagesInsufficientBalance(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasOpenAIImagesInsufficientBalance(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func isOpenAIImagesErrorContainer(key string) bool {
	switch key {
	case "error", "errors", "cause", "detail", "details", "inner", "innererror", "inner_error", "response":
		return true
	default:
		return false
	}
}

// Persist an image-only cooldown. A storage error must not suppress failover.
func (s *OpenAIGatewayService) coolOpenAIImagesInsufficientBalance(ctx context.Context, account *Account) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	extender, ok := s.accountRepo.(openAIImagesBalanceRateLimitExtender)
	if !ok {
		slog.Warn("openai_images_balance_cooldown_unsupported", "account_id", account.ID)
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	until := time.Now().Add(openAIImagesBalanceCooldown)
	if err := extender.ExtendModelRateLimit(stateCtx, account.ID, openAIImageGenerationRateLimitKey, until, openAIImagesBalanceRateLimitReason); err != nil {
		slog.Warn("openai_images_balance_cooldown_failed", "account_id", account.ID, "error", err)
	}
}

func newOpenAIImagesInsufficientBalanceFailoverError(statusCode int, headers http.Header, body []byte) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:                      statusCode,
		ResponseBody:                    body,
		ResponseHeaders:                 headers.Clone(),
		OpenAIImagesInsufficientBalance: true,
	}
}

// WriteOpenAIImagesInsufficientBalanceError uses the Images writer so an
// already-committed JSON whitespace keepalive remains one valid JSON document.
// It returns false after semantic output, leaving the caller's SSE error path.
func WriteOpenAIImagesInsufficientBalanceError(c *gin.Context) bool {
	return writeOpenAIImagesUpstreamErrorResponse(c, &OpenAIImagesUpstreamError{
		StatusCode: http.StatusPaymentRequired,
		ErrorType:  "upstream_error",
		Code:       OpenAIImagesInsufficientBalanceCode,
		Message:    OpenAIImagesInsufficientBalanceMessage,
	})
}
