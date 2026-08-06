package handler

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenest"
)

// gatewayMaxInputTokens returns the configured input-token ceiling for gateway
// requests. Zero (the default) disables the check.
func gatewayMaxInputTokens(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return cfg.Gateway.MaxInputTokens
}

// inputTokensWithinLimit reports whether a request body stays within the
// configured input-token ceiling.
//
// Rejecting oversized prompts here — before account selection and before the
// request is forwarded — is what makes the check worthwhile: an over-limit
// prompt is still billed for its input tokens upstream even though the
// provider returns nothing usable, and clients retry such failures, so the
// cost multiplies.
func inputTokensWithinLimit(body []byte, cfg *config.Config) (estimated, limit int, ok bool) {
	limit = gatewayMaxInputTokens(cfg)
	estimated, ok = tokenest.WithinLimit(body, limit)
	return estimated, limit, ok
}

// buildInputTokensTooLargeMessage renders the client-facing rejection message.
// The estimate is reported so callers can tell how far over the limit they are.
func buildInputTokensTooLargeMessage(estimated, limit int) string {
	return fmt.Sprintf(
		"Request input is too large: approximately %d tokens, limit is %d. Reduce the prompt or context size and retry.",
		estimated, limit,
	)
}
