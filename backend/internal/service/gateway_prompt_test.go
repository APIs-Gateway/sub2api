package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsClaudeCodeClient(t *testing.T) {
	// 合法的 legacy 格式 metadata.user_id（64位 hex + account uuid + session uuid）
	legacyUserID := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	// 合法的 JSON 格式 metadata.user_id（2.1.78+ 版本）
	jsonUserID := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"550e8400-e29b-41d4-a716-446655440000","session_id":"123e4567-e89b-12d3-a456-426614174000"}`

	tests := []struct {
		name           string
		userAgent      string
		metadataUserID string
		want           bool
	}{
		{
			name:           "Claude Code client with legacy user_id",
			userAgent:      "claude-cli/1.0.62 (darwin; arm64)",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Claude Code client with JSON user_id",
			userAgent:      "claude-cli/2.1.92 (external, cli)",
			metadataUserID: jsonUserID,
			want:           true,
		},
		{
			name:           "Claude Code case insensitive UA",
			userAgent:      "Claude-CLI/2.0.0",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Missing metadata user_id",
			userAgent:      "claude-cli/1.0.0",
			metadataUserID: "",
			want:           false,
		},
		{
			name:           "Claude CLI UA with invalid user_id format",
			userAgent:      "claude-cli/2.0.0",
			metadataUserID: "fake-user-id-12345",
			want:           false,
		},
		{
			name:           "Different user agent with valid user_id",
			userAgent:      "curl/7.68.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Empty user agent",
			userAgent:      "",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Similar but not Claude CLI",
			userAgent:      "claude-api/1.0.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Opencode spoofing UA with arbitrary user_id",
			userAgent:      "claude-cli/2.1.92",
			metadataUserID: "session_abc",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isClaudeCodeClient(tt.userAgent, tt.metadataUserID)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSystemIncludesClaudeCodePrompt(t *testing.T) {
	tests := []struct {
		name   string
		system any
		want   bool
	}{
		{
			name:   "nil system",
			system: nil,
			want:   false,
		},
		{
			name:   "empty string",
			system: "",
			want:   false,
		},
		{
			name:   "string with Claude Code prompt",
			system: claudeCodeSystemPrompt,
			want:   true,
		},
		{
			name:   "string with different content",
			system: "You are a helpful assistant.",
			want:   false,
		},
		{
			name:   "empty array",
			system: []any{},
			want:   false,
		},
		{
			name: "array with Claude Code prompt",
			system: []any{
				map[string]any{
					"type": "text",
					"text": claudeCodeSystemPrompt,
				},
			},
			want: true,
		},
		{
			name: "array with Claude Code prompt in second position",
			system: []any{
				map[string]any{"type": "text", "text": "First prompt"},
				map[string]any{"type": "text", "text": claudeCodeSystemPrompt},
			},
			want: true,
		},
		{
			name: "array without Claude Code prompt",
			system: []any{
				map[string]any{"type": "text", "text": "Custom prompt"},
			},
			want: false,
		},
		{
			name: "array with partial match (should not match)",
			system: []any{
				map[string]any{"type": "text", "text": "You are Claude"},
			},
			want: false,
		},
		// json.RawMessage cases (conversion path: ForwardAsResponses / ForwardAsChatCompletions)
		{
			name:   "json.RawMessage string with Claude Code prompt",
			system: json.RawMessage(`"` + claudeCodeSystemPrompt + `"`),
			want:   true,
		},
		{
			name:   "json.RawMessage string without Claude Code prompt",
			system: json.RawMessage(`"You are a helpful assistant"`),
			want:   false,
		},
		{
			name:   "json.RawMessage nil (empty)",
			system: json.RawMessage(nil),
			want:   false,
		},
		{
			name:   "json.RawMessage empty string",
			system: json.RawMessage(`""`),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := systemIncludesClaudeCodePrompt(tt.system)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInjectClaudeCodePrompt(t *testing.T) {
	claudePrefix := strings.TrimSpace(claudeCodeSystemPrompt)

	tests := []struct {
		name           string
		body           string
		system         any
		wantSystemLen  int
		wantFirstText  string
		wantSecondText string
	}{
		{
			name:          "nil system",
			body:          `{"model":"claude-3"}`,
			system:        nil,
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:          "empty string system",
			body:          `{"model":"claude-3"}`,
			system:        "",
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:           "string system",
			body:           `{"model":"claude-3"}`,
			system:         "Custom prompt",
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "string system equals Claude Code prompt",
			body:          `{"model":"claude-3"}`,
			system:        claudeCodeSystemPrompt,
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:   "array system",
			body:   `{"model":"claude-3"}`,
			system: []any{map[string]any{"type": "text", "text": "Custom"}},
			// Claude Code + Custom = 2
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom",
		},
		{
			name: "array system with existing Claude Code prompt (should dedupe)",
			body: `{"model":"claude-3"}`,
			system: []any{
				map[string]any{"type": "text", "text": claudeCodeSystemPrompt},
				map[string]any{"type": "text", "text": "Other"},
			},
			// Claude Code at start + Other = 2 (deduped)
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nOther",
		},
		{
			name:          "empty array",
			body:          `{"model":"claude-3"}`,
			system:        []any{},
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		// json.RawMessage cases (conversion path: ForwardAsResponses / ForwardAsChatCompletions)
		{
			name:           "json.RawMessage string system",
			body:           `{"model":"claude-3","system":"Custom prompt"}`,
			system:         json.RawMessage(`"Custom prompt"`),
			wantSystemLen:  2,
			wantFirstText:  claudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "json.RawMessage nil system",
			body:          `{"model":"claude-3"}`,
			system:        json.RawMessage(nil),
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
		{
			name:          "json.RawMessage Claude Code prompt (should not duplicate)",
			body:          `{"model":"claude-3","system":"` + claudeCodeSystemPrompt + `"}`,
			system:        json.RawMessage(`"` + claudeCodeSystemPrompt + `"`),
			wantSystemLen: 1,
			wantFirstText: claudeCodeSystemPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectClaudeCodePrompt([]byte(tt.body), tt.system)

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			system, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array")
			require.Len(t, system, tt.wantSystemLen)

			first, ok := system[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tt.wantFirstText, first["text"])
			require.Equal(t, "text", first["type"])

			// Check cache_control
			cc, ok := first["cache_control"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "ephemeral", cc["type"])

			if tt.wantSecondText != "" && len(system) > 1 {
				second, ok := system[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantSecondText, second["text"])
			}
		})
	}
}

func TestRewriteSystemForNonClaudeCode(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		system           any
		wantSystemText   string // system array 第一个 block 的 text
		wantMessagesLen  int    // messages 数组长度
		wantFirstMsgRole string // 第一条消息的 role
		wantFirstMsgText string // 第一条消息的 content[0].text
		wantAckMsgText   string // 第二条消息的 content[0].text
	}{
		{
			name:            "nil system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          nil,
			wantSystemText:  claudeCodeSystemPrompt,
			wantMessagesLen: 1, // 原始 1 条消息，不注入
		},
		{
			name:            "empty string system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          "",
			wantSystemText:  claudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "custom string system - migrated to messages",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           "You are a personal assistant running inside OpenClaw.",
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  3, // instruction + ack + original
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nYou are a personal assistant running inside OpenClaw.",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "system equals Claude Code prompt - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          claudeCodeSystemPrompt,
			wantSystemText:  claudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name: "array system with custom blocks - text joined and migrated",
			body: `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system: []any{
				map[string]any{"type": "text", "text": "First instruction"},
				map[string]any{"type": "text", "text": "Second instruction"},
			},
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  3,
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nFirst instruction\n\nSecond instruction",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "empty array system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          []any{},
			wantSystemText:  claudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "json.RawMessage string system",
			body:             `{"model":"claude-3","system":"Custom prompt","messages":[{"role":"user","content":"hello"}]}`,
			system:           json.RawMessage(`"Custom prompt"`),
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  3,
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nCustom prompt",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "json.RawMessage nil system",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          json.RawMessage(nil),
			wantSystemText:  claudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "multiple original messages preserved",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"resp1"},{"role":"user","content":"msg2"}]}`,
			system:           "Be helpful",
			wantSystemText:   claudeCodeSystemPrompt,
			wantMessagesLen:  5, // 2 injected + 3 original
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nBe helpful",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rewriteSystemForNonClaudeCode([]byte(tt.body), tt.system)

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			// system 应为 array 格式，对齐真实 Claude Code CLI 的 3-block 形态：
			//   [0] billing attribution block (x-anthropic-billing-header: cc_version=...;)
			//   [1] Claude Code 身份前缀 block (不带 cache_control)
			//   [2] 工具无关的通用提示词扩充 block (带 cache_control，作为缓存断点)
			systemArr, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array, got %T", parsed["system"])
			require.Len(t, systemArr, 3, "system array should have exactly 3 blocks (billing + cc prompt + expansion)")

			billingBlock, ok := systemArr[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", billingBlock["type"])
			require.Contains(t, billingBlock["text"], "x-anthropic-billing-header:")
			require.Contains(t, billingBlock["text"], "cc_version=")
			require.Contains(t, billingBlock["text"], "cc_entrypoint=cli")
			require.NotContains(t, billingBlock["text"], "cch=")

			systemBlock, ok := systemArr[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", systemBlock["type"])
			require.Equal(t, tt.wantSystemText, systemBlock["text"])
			_, hasCC := systemBlock["cache_control"]
			require.False(t, hasCC, "身份前缀 block 不应带 cache_control（断点落在扩充块）")

			expansionBlock, ok := systemArr[2].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", expansionBlock["type"])
			require.Equal(t, claudeCodeSystemPromptExpansion, expansionBlock["text"])
			cc, ok := expansionBlock["cache_control"].(map[string]any)
			require.True(t, ok, "expansion block should have cache_control")
			require.Equal(t, "ephemeral", cc["type"])

			// 检查 messages
			messages, ok := parsed["messages"].([]any)
			require.True(t, ok, "messages should be an array")
			require.Len(t, messages, tt.wantMessagesLen)

			if tt.wantFirstMsgRole != "" && len(messages) >= 2 {
				// 检查注入的 instruction 消息
				firstMsg, ok := messages[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantFirstMsgRole, firstMsg["role"])

				firstContent, ok := firstMsg["content"].([]any)
				require.True(t, ok)
				require.Len(t, firstContent, 1)
				firstBlock, ok := firstContent[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantFirstMsgText, firstBlock["text"])

				// 检查注入的 ack 消息
				ackMsg, ok := messages[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "assistant", ackMsg["role"])

				ackContent, ok := ackMsg["content"].([]any)
				require.True(t, ok)
				require.Len(t, ackContent, 1)
				ackBlock, ok := ackContent[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantAckMsgText, ackBlock["text"])
			}
		})
	}
}

func TestRewriteSystemForNonClaudeCode_PreservesSystemCacheControlOnMigratedMessage(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":[{"type":"text","text":"Stable project instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hello"}]}`)
	system := []any{
		map[string]any{
			"type":          "text",
			"text":          "Stable project instructions",
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		},
	}

	result := rewriteSystemForNonClaudeCode(body, system)

	require.Equal(t, "[System Instructions]\nStable project instructions", gjson.GetBytes(result, "messages.0.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(result, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "messages.0.content.0.cache_control.ttl").String())
}

func TestSystemHasBillingAttributionBlock(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"session"},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}]}`)
	require.True(t, systemHasBillingAttributionBlock(body))
	require.False(t, systemHasBillingAttributionBlock([]byte(`{"system":[{"type":"text","text":"You are Claude Code"}]}`)))
}

func TestIsClaudeCodeForwardRequest_RecognizesProxiedClaudeCodeForCacheBypass(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "Go-http-client/1.1")
	parsed := &ParsedRequest{MetadataUserID: "proxied-claude-code-session"}
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}]}`)

	require.True(t, isClaudeCodeForwardRequest(context.Background(), c, parsed, body))
	require.False(t, isClaudeCodeForwardRequest(context.Background(), c, parsed, []byte(`{"system":[]}`)))
}

func TestGatewayService_ShouldNormalizeAnthropicClientDateline_FailsClosedWithoutSettings(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	var nilService *GatewayService
	require.False(t, nilService.shouldNormalizeAnthropicClientDateline(context.Background(), account))
	require.False(t, (&GatewayService{}).shouldNormalizeAnthropicClientDateline(context.Background(), account))
}

func TestNormalizeAnthropicClientDateline(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Today’s date is 2026/07/01."}],"messages":[{"role":"user","content":"keep Today’s date is 2026/07/01. <system-reminder>Todayʼs date is 2026/07/01.</system-reminder>"}]}`)
	result := normalizeAnthropicClientDateline(body)
	require.Equal(t, "Today's date is 2026-07-01.", gjson.GetBytes(result, "system.0.text").String())
	content := gjson.GetBytes(result, "messages.0.content").String()
	require.Contains(t, content, "keep Today’s date is 2026/07/01.")
	require.Contains(t, content, "<system-reminder>Today's date is 2026-07-01.</system-reminder>")
}

func TestRewriteSystemForNonClaudeCodeWithPrompt_UsesCustomExpansionPrompt(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	customPrompt := "Custom Claude OAuth expansion prompt"

	result := rewriteSystemForNonClaudeCodeWithPrompt(body, "Project instructions", customPrompt)

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	require.Len(t, system.Array(), 3)
	require.Equal(t, customPrompt, system.Array()[2].Get("text").String())
	require.Equal(t, "ephemeral", system.Array()[2].Get("cache_control.type").String())
}

func TestRewriteSystemForNonClaudeCodeWithPromptBlocks_UsesConfiguredBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	blocks := `{
		"blocks": [
			{"type":"text","text":"prefix {cc_version}.{fp}","cache_control":true},
			{"enabled":false,"type":"text","text":"disabled"},
			{"type":"text","text":"{claude_code_system_prompt}"},
			{"type":"text","text":"tail","cache_control":{"type":"ephemeral","ttl":"1h"}}
		]
	}`

	result := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, "Project instructions", "", blocks)

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	arr := system.Array()
	require.Len(t, arr, 3)
	require.Contains(t, arr[0].Get("text").String(), "prefix "+claude.CLICurrentVersion+".")
	require.Equal(t, "ephemeral", arr[0].Get("cache_control.type").String())
	require.Equal(t, claude.DefaultCacheControlTTL, arr[0].Get("cache_control.ttl").String())
	require.Equal(t, claudeCodeSystemPrompt, arr[1].Get("text").String())
	require.False(t, arr[1].Get("cache_control").Exists())
	require.Equal(t, "tail", arr[2].Get("text").String())
	require.Equal(t, "1h", arr[2].Get("cache_control.ttl").String())
}

func TestNormalizeAnthropicClientDateline_NormalizesApostropheAndSeparatorVariants(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"ASCII apostrophe with slash", "Today's date is 2026/07/01."},
		{"U+2019 apostrophe with hyphen", "Today\u2019s date is 2026-07-01."},
		{"U+02BC apostrophe with slash", "Today\u02bcs date is 2026/07/01."},
		{"U+02B9 apostrophe with hyphen", "Today\u02b9s date is 2026-07-01."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"system": tt.input})
			require.NoError(t, err)
			result := normalizeAnthropicClientDateline(body)
			require.Equal(t, "Today's date is 2026-07-01.", gjson.GetBytes(result, "system").String())
		})
	}
}

func TestNormalizeAnthropicClientDateline_OnlyTouchesSupportedScopes(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Today\u2019s date is 2026/07/01."}],"messages":[{"role":"user","content":"keep Today\u2019s date is 2026/07/01. <system-reminder>Today\u02bcs date is 2026/07/01.</system-reminder>"},{"role":"user","content":[{"type":"text","text":"<system-reminder>Today\u02b9s date is 2026-07-01.</system-reminder>"},{"type":"tool_result","content":"Today\u2019s date is 2026/07/01."}]}]}`)
	result := normalizeAnthropicClientDateline(body)
	require.Equal(t, "Today's date is 2026-07-01.", gjson.GetBytes(result, "system.0.text").String())
	content := gjson.GetBytes(result, "messages.0.content").String()
	require.Contains(t, content, "keep Today\u2019s date is 2026/07/01.")
	require.Contains(t, content, "<system-reminder>Today's date is 2026-07-01.</system-reminder>")
	require.Equal(t, "<system-reminder>Today's date is 2026-07-01.</system-reminder>", gjson.GetBytes(result, "messages.1.content.0.text").String())
	require.Equal(t, "Today\u2019s date is 2026/07/01.", gjson.GetBytes(result, "messages.1.content.1.content").String())
}

func TestNormalizeAnthropicClientDateline_MixedSeparatorsAreLeftUntouched(t *testing.T) {
	body := []byte(`{"system":"Today\u2019s date is 2026-07/01."}`)
	require.Equal(t, body, normalizeAnthropicClientDateline(body))
}

func TestNormalizeAnthropicClientDateline_NormalizesMultipleOccurrencesAndIsIdempotent(t *testing.T) {
	body := []byte(`{"system":"Today\u2019s date is 2026/07/01. Next: Today\u02bcs date is 2026-07-02."}`)
	first := normalizeAnthropicClientDateline(body)
	require.Equal(t, "Today's date is 2026-07-01. Next: Today's date is 2026-07-02.", gjson.GetBytes(first, "system").String())
	require.Equal(t, first, normalizeAnthropicClientDateline(first))
}

func TestNormalizeAnthropicClientDateline_FailsClosedForMalformedJSON(t *testing.T) {
	body := []byte(`{"system":`)
	require.Equal(t, body, normalizeAnthropicClientDateline(body))
}
