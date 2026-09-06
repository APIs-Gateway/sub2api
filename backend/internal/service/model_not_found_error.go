package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

var upstreamModelNotFoundKeywords = []string{"model not found", "unknown model", "not found"}

func isUpstreamModelNotFoundError(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" || !strings.Contains(normalized, "model") {
		return false
	}
	return containsModelNotFoundKeyword(normalized)
}

func isModelNotFoundError(statusCode int, body []byte) bool {
	return isUpstreamModelNotFoundError(statusCode, body) || statusCode == http.StatusNotFound
}

// openAICodexPlanGatedModelPhrase matches the deterministic Codex 400 returned
// when a ChatGPT OAuth account's plan cannot serve the requested model, e.g.
// {"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}
// The phrase is compared against the normalized body (lowercased, "_"/"-"
// folded to spaces), so it also matches the same message embedded in
// error.message-style payloads.
const openAICodexPlanGatedModelPhrase = "model is not supported when using codex"

// isOpenAICodexPlanGatedModelError reports whether the upstream response is the
// deterministic Codex rejection of a plan-gated model on a ChatGPT account.
// Unlike transient failures, retrying the same account cannot succeed until the
// account's plan changes, so callers should treat it like model-not-found and
// cool the (account, model) pair down instead of re-selecting the account.
func isOpenAICodexPlanGatedModelError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	normalized := normalizeModelNotFoundBody(body)
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, openAICodexPlanGatedModelPhrase)
}

// isOpenAICompatibleModelNotFound400 reports whether an OpenAI-compatible 400
// says the selected account/provider cannot serve the requested model. Such a
// response is account availability, not a malformed request, so a managed
// gateway fails it over to another account.
func isOpenAICompatibleModelNotFound400(respBody []byte) bool {
	return isOpenAICompatibleModelNotFoundBody(respBody)
}

// IsOpenAICompatibleModelNotFound400 exposes the 400 model-not-found
// classification to handlers that render exhausted failovers.
func IsOpenAICompatibleModelNotFound400(respBody []byte) bool {
	return isOpenAICompatibleModelNotFound400(respBody)
}

// isOpenAICompatibleModelNotFoundBody classifies the body only (status-agnostic).
// A structured error code is authoritative: any code other than model_not_found
// keeps the response terminal even if its message mentions a missing model.
// Only error-message fields are inspected so echoed request content cannot
// trigger a match; a non-JSON plain-text body is inspected as a whole.
func isOpenAICompatibleModelNotFoundBody(respBody []byte) bool {
	code := strings.TrimSpace(extractUpstreamErrorCode(respBody))
	if code != "" {
		return strings.EqualFold(code, "model_not_found")
	}

	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" && !gjson.ValidBytes(respBody) {
		msg = strings.ToLower(strings.TrimSpace(string(respBody)))
	}
	return strings.Contains(msg, "unknown provider for model") ||
		strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "model is not supported")
}

func containsModelNotFoundKeyword(normalizedBody string) bool {
	if normalizedBody == "" {
		return false
	}
	for _, keyword := range upstreamModelNotFoundKeywords {
		if strings.Contains(normalizedBody, keyword) {
			return true
		}
	}
	return false
}

func normalizeModelNotFoundBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	normalized := strings.ToLower(string(body))
	normalized = strings.NewReplacer("_", " ", "-", " ", "\n", " ", "\r", " ", "\t", " ").Replace(normalized)
	return strings.Join(strings.Fields(normalized), " ")
}
