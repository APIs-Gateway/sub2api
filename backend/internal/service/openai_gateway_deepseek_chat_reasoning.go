package service

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const deepSeekChatReasoningPlaceholderText = " "

// requiresDeepSeekChatReasoningPlaceholder limits this compatibility rewrite
// to DeepSeek models sent to the official DeepSeek or OpenCode Zen hosts.
// The URL and body are the final upstream values after account model mapping.
func requiresDeepSeekChatReasoningPlaceholder(targetURL string, body []byte) bool {
	u, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "api.deepseek.com" && host != "opencode.ai" {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	for _, prefix := range []string{"opencode-go/", "opencode_go/", "opencode/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return strings.HasPrefix(model, "deepseek")
}

// ensureDeepSeekChatReasoningPlaceholders fills only missing assistant
// reasoning_content. Existing plaintext reasoning, user and tool messages are
// preserved. DeepSeek thinking mode rejects an empty field, so use one space.
func ensureDeepSeekChatReasoningPlaceholders(targetURL string, body []byte) []byte {
	if !requiresDeepSeekChatReasoningPlaceholder(targetURL, body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	updated := body
	changed := false
	for i, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" || msg.Get("reasoning_content").String() != "" {
			continue
		}
		next, err := sjson.SetBytes(updated, "messages."+strconv.Itoa(i)+".reasoning_content", deepSeekChatReasoningPlaceholderText)
		if err != nil {
			return body
		}
		updated = next
		changed = true
	}
	if !changed {
		return body
	}
	return updated
}
