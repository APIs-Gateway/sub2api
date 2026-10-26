package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// isGrokContentPolicyRejection identifies request-scoped safety refusals from
// xAI. These failures are caused by the prompt or media, so retrying another
// OAuth account cannot change the outcome and would incorrectly drain a pool.
// Keep this matcher deliberately narrow: account entitlement and suspension
// messages may mention policy but must retain the normal account failover path.
func isGrokContentPolicyRejection(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusForbidden || len(responseBody) == 0 {
		return false
	}
	if grokAccountAccessMessage(string(responseBody)) {
		return false
	}

	var payload any
	if json.Unmarshal(responseBody, &payload) == nil {
		if grokExplicitRequestRefusal(responseBody) {
			return true
		}
		if grokStructuredAccountAccessMarker(payload) {
			return false
		}
		if grokStructuredContentPolicyMarker(payload) {
			return true
		}
	}

	return grokContentPolicyMessage(string(responseBody))
}

func grokStructuredAccountAccessMarker(value any) bool {
	return grokStructuredAccountAccessMarkerWithPermission(value, true)
}

func grokStructuredAccountAccessMarkerWithPermission(value any, includePermission bool) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokAccountAccessCode(marker) &&
					(includePermission || normalizeGrokErrorMarker(marker) != "permission_denied") {
					return true
				}
			}
			if grokStructuredAccountAccessMarkerWithPermission(child, includePermission) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredAccountAccessMarkerWithPermission(child, includePermission) {
				return true
			}
		}
	case string:
		// The refusal exception must inspect decoded text as well: JSON
		// escapes must not hide a suspension/entitlement phrase from the
		// existing raw-body account guard.
		return !includePermission && grokAccountAccessMessage(node)
	}
	return false
}

// Only a structured permission denial with a complete, recognized refusal
// sentence may bypass account handling. Duplicate keys are ambiguous: reject
// this exception rather than letting a later value conceal an account fault.
func grokExplicitRequestRefusal(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	payload, err := decodeGrokUniqueJSON(decoder)
	if err != nil {
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return false
	}
	root, ok := payload.(map[string]any)
	if !ok || grokStructuredAccountAccessMarkerWithPermission(root, false) {
		return false
	}
	permission := func(node map[string]any) bool {
		for _, key := range []string{"code", "error_code", "type", "category", "reason"} {
			if value, ok := node[key].(string); ok && normalizeGrokErrorMarker(value) == "permission_denied" {
				return true
			}
		}
		return false
	}
	nested, _ := root["error"].(map[string]any)
	if !permission(root) && !permission(nested) {
		return false
	}
	for _, candidate := range []any{root["error"], root["message"], root["detail"], nested["message"], nested["error"]} {
		value, ok := candidate.(string)
		if !ok {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.NewReplacer("’", "'", "‘", "'").Replace(value)
		if value == "i'm sorry, i can't help with that request." || value == "i'm sorry, i cannot help with that request." {
			return true
		}
	}
	return false
}

func decodeGrokUniqueJSON(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate object key")
			}
			value, err := decodeGrokUniqueJSON(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		_, err := decoder.Token()
		return object, err
	case json.Delim('['):
		var array []any
		for decoder.More() {
			value, err := decodeGrokUniqueJSON(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := decoder.Token()
		return array, err
	default:
		return token, nil
	}
}

func grokStructuredContentPolicyMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokContentPolicyCode(marker) {
					return true
				}
			}
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	}
	return false
}

func normalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func isGrokContentPolicyCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "content_filter",
		"content_policy",
		"content_policy_violation",
		"content_moderation",
		"cyber_policy",
		"new_sensitive":
		return true
	default:
		return false
	}
}

func isGrokAccountAccessCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "account_suspended",
		"account_disabled",
		"user_suspended",
		"user_disabled",
		"subscription_required",
		"entitlement_required",
		"not_entitled",
		"plan_required",
		"permission_denied":
		return true
	default:
		return false
	}
}

func grokAccountAccessMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, phrase := range []string{
		"account suspended",
		"account has been suspended",
		"account disabled",
		"account has been disabled",
		"user suspended",
		"user has been suspended",
		"subscription required",
		"entitlement required",
		"not entitled",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func grokContentPolicyMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}

	// xAI's media safety responses use these exact phrases. They are specific
	// enough not to classify a generic account-policy or entitlement message.
	for _, phrase := range []string{
		"the moderation feature is not available",
		"image is sensitive",
		"text is sensitive",
		"prohibited content",
		"forbidden content",
		"content policy violation",
		"content policy rejection",
		"content policy rejected",
		"content moderation rejection",
		"content moderation rejected",
		"content moderation blocked",
		"request blocked by content moderation",
		"request rejected by content moderation",
		"request blocked by policy",
		"request rejected by policy",
		"request violates policy",
		"prompt violates content policy",
		"prompt violates policy",
		"input violates content policy",
		"input violates policy",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}

// shouldFailoverGrokUpstreamError is the body-aware counterpart of the
// status-only failover helper. Grok content refusals must stay on the current
// account and be returned to the caller instead of consuming the account pool.
func (s *OpenAIGatewayService) shouldFailoverGrokUpstreamError(statusCode int, responseBody []byte) bool {
	if isGrokContentPolicyRejection(statusCode, responseBody) {
		return false
	}
	return s.shouldFailoverUpstreamError(statusCode)
}

// applyGrokForbiddenPolicy applies an administrator's existing temporary
// unschedulable rules to a non-content 403. It reports true only when a rule
// matched; unmatched responses retain the legacy entitlement cooldown.
func (s *OpenAIGatewayService) applyGrokForbiddenPolicy(ctx context.Context, account *Account, responseBody []byte) bool {
	if account == nil || !account.IsTempUnschedulableEnabled() {
		return false
	}

	matches := matchTempUnschedulableRules(account, http.StatusForbidden, responseBody)
	if len(matches) == 0 {
		return false
	}

	match := matches[0]
	// Reuse the central policy implementation when it has a repository. This
	// preserves the existing reason/cache format and avoids duplicating writes.
	if s != nil && s.rateLimitService != nil && s.rateLimitService.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		handled := s.rateLimitService.tryTempUnschedulable(
			stateCtx,
			account,
			http.StatusForbidden,
			responseBody,
		)
		cancel()
		if handled {
			return true
		}
	}

	// A partially constructed service (for example a unit-test gateway) still
	// honors the configured duration instead of silently falling back to 30m.
	cooldown := time.Duration(match.rule.DurationMinutes) * time.Minute
	if cooldown > 0 {
		s.tempUnscheduleGrok(ctx, account, cooldown, "grok configured forbidden rule")
	}
	return true
}
