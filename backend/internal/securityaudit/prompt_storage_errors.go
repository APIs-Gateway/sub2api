package securityaudit

import "strings"

// Stored job errors are deliberately stable, short, and independent of raw
// dependency errors so prompt contents and credentials cannot reach PostgreSQL.
func stableErrorCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "unknown_error"
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return "redacted_error"
	}
	return TrimRunes(code, 64)
}

func stableErrorMessage(code string) string {
	switch stableErrorCode(code) {
	case ErrorCodeBlocked:
		return "Prompt Guard blocked the request"
	case ErrorCodeUnavailable, "payload_store_unavailable", "payload_missing":
		return "Prompt Audit dependency is unavailable"
	case ErrorCodeInvalidResponse:
		return "Prompt Guard returned an invalid response"
	case "queue_full", "queue_admission_busy":
		return "Prompt Audit queue is unavailable"
	case "config_load_failed", "config_ttl_reload_failed", "config_invalidation_reload_failed":
		return "Prompt Audit configuration could not be loaded"
	default:
		return "Prompt Audit operation failed"
	}
}

func sanitizeStoredError(code string) (string, string) {
	stableCode := stableErrorCode(code)
	return stableCode, TrimRunes(stableErrorMessage(stableCode), 160)
}
