//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_IsCNProvider(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		expected bool
	}{
		{
			name:     "nil_account",
			account:  nil,
			expected: false,
		},
		{
			name: "not_openai_platform",
			account: &Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"cn_provider": true},
			},
			expected: false,
		},
		{
			name: "openai_oauth_not_apikey",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Credentials: map[string]any{"cn_provider": true},
			},
			expected: false,
		},
		{
			name: "openai_apikey_without_flag",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
			},
			expected: false,
		},
		{
			name: "openai_apikey_nil_credentials",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
			},
			expected: false,
		},
		{
			name: "openai_apikey_flag_wrong_type",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"cn_provider": "true"},
			},
			expected: false,
		},
		{
			name: "openai_apikey_flag_false",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"cn_provider": false},
			},
			expected: false,
		},
		{
			name: "openai_apikey_flag_true",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"cn_provider": true},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.account.IsCNProvider())
		})
	}
}

func TestAccount_GetAPIProtocol(t *testing.T) {
	cnAccount := func(apiProtocol any) *Account {
		creds := map[string]any{"cn_provider": true}
		if apiProtocol != nil {
			creds["api_protocol"] = apiProtocol
		}
		return &Account{
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: creds,
		}
	}

	tests := []struct {
		name     string
		account  *Account
		expected APIProtocol
	}{
		{
			name:     "nil_account",
			account:  nil,
			expected: APIProtocolChatCompletions,
		},
		{
			name: "non_cn_provider_defaults_to_chat_completions",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_protocol": "anthropic"},
			},
			expected: APIProtocolChatCompletions,
		},
		{
			name:     "cn_provider_unset_defaults_to_chat_completions",
			account:  cnAccount(nil),
			expected: APIProtocolChatCompletions,
		},
		{
			name:     "cn_provider_explicit_chat_completions",
			account:  cnAccount("chat_completions"),
			expected: APIProtocolChatCompletions,
		},
		{
			name:     "cn_provider_explicit_anthropic",
			account:  cnAccount("anthropic"),
			expected: APIProtocolAnthropic,
		},
		{
			name:     "cn_provider_explicit_responses",
			account:  cnAccount("responses"),
			expected: APIProtocolResponses,
		},
		{
			name:     "cn_provider_explicit_adaptive",
			account:  cnAccount("adaptive"),
			expected: APIProtocolAdaptive,
		},
		{
			name:     "cn_provider_invalid_value_falls_back_to_chat_completions",
			account:  cnAccount("not-a-real-protocol"),
			expected: APIProtocolChatCompletions,
		},
		{
			name:     "cn_provider_whitespace_is_trimmed",
			account:  cnAccount("  anthropic  "),
			expected: APIProtocolAnthropic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.account.GetAPIProtocol())
		})
	}
}

func TestAccount_IsAnthropicAPIProtocol(t *testing.T) {
	anthropicAccount := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"cn_provider": true, "api_protocol": "anthropic"},
	}
	require.True(t, anthropicAccount.IsAnthropicAPIProtocol())

	chatCompletionsAccount := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"cn_provider": true, "api_protocol": "chat_completions"},
	}
	require.False(t, chatCompletionsAccount.IsAnthropicAPIProtocol())
}

func TestAccount_GetAnthropicProtocolBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		expected string
	}{
		{
			name:     "nil_account",
			account:  nil,
			expected: "",
		},
		{
			name: "not_cn_provider",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_protocol": "anthropic", "base_url": "https://open.bigmodel.cn/api/anthropic"},
			},
			expected: "",
		},
		{
			name: "cn_provider_but_chat_completions_protocol",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"cn_provider":  true,
					"api_protocol": "chat_completions",
					"base_url":     "https://open.bigmodel.cn/api/anthropic",
				},
			},
			expected: "",
		},
		{
			name: "cn_provider_anthropic_protocol_missing_base_url",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"cn_provider":  true,
					"api_protocol": "anthropic",
				},
			},
			expected: "",
		},
		{
			name: "cn_provider_anthropic_protocol_returns_base_url",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"cn_provider":  true,
					"api_protocol": "anthropic",
					"base_url":     "  https://open.bigmodel.cn/api/anthropic  ",
				},
			},
			expected: "https://open.bigmodel.cn/api/anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.account.GetAnthropicProtocolBaseURL())
		})
	}
}
