package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAccountSupportsOpenAIEndpointCapability_EmptyContainer covers the fix for
// upstream #5530: an empty openai_capabilities container ({} / [] / []string{})
// must be treated the same as an unconfigured value (i.e. no restriction),
// instead of silently excluding OAuth accounts from text-endpoint scheduling.
// Non-empty-but-all-false and type-mismatched values must keep being treated
// as "configured but grants nothing" (original behavior preserved).
//
// Companion coverage lives in openai_images_test.go's
// TestAccountSupportsOpenAIEndpointCapability; kept in a separate file here to
// stay within the sched batch's account*/scheduler*/group* scope.
func TestAccountSupportsOpenAIEndpointCapability_EmptyContainer(t *testing.T) {
	t.Run("空 openai_capabilities（{}）与未配置一致，不排除 OAuth 文本调度", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": map[string]any{},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})

	t.Run("空 openai_capabilities（[]any）与未配置一致，不排除 OAuth 文本调度", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": []any{},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	})

	t.Run("空 openai_capabilities（[]string）与未配置一致，不排除 OAuth 文本调度", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": []string{},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	})

	t.Run("非空但全 false 的 map 仍按显式禁用处理，不默认放行", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": map[string]any{"chat_completions": false},
			},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	})

	t.Run("类型异常（字符串）仍视为已配置但不含能力，不默认放行", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": "chat_completions",
			},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	})
}
