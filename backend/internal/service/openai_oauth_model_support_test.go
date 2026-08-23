//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newOpenAIOAuthAccountForModelTest() *Account {
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func TestIsModelSupported_OpenAIOAuthEmptyMappingServableModels(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()

	for _, model := range []string{
		"",
		"gpt-5.4",
		"gpt-5.4-high",
		"gpt-5.3-codex",
		"gpt-5.3-codex-xhigh",
		"gpt-5.1-codex-mini",
		"gpt-5",
		"codex-mini-latest",
		"gpt5.3codexspark",
		"gpt-image-1",
		"claude-sonnet-4-6",
		"claude-3-opus-20240229",
	} {
		require.True(t, account.IsModelSupported(model), "expected %q to be servable", model)
	}
}

func TestIsModelSupported_OpenAIOAuthEmptyMappingRejectsForeignModels(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()

	for _, model := range []string{"deepseek-v4", "deepseek-chat", "glm-4.7", "kimi-k2", "gemini-3.0-pro", "grok-4", "qwen3-max"} {
		require.False(t, account.IsModelSupported(model), "expected %q to be rejected", model)
	}
}

func TestIsModelSupported_OpenAIOAuthExplicitMappingUnchanged(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Credentials = map[string]any{"model_mapping": map[string]any{"deepseek-v4": "gpt-5.4"}}

	require.True(t, account.IsModelSupported("deepseek-v4"))
	require.False(t, account.IsModelSupported("glm-4.7"))
}

func TestIsModelSupported_OpenAIOAuthPassthroughAllowsAll(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Extra = map[string]any{"openai_passthrough": true}

	require.True(t, account.IsModelSupported("deepseek-v4"))
}

func TestIsModelSupported_OpenAIOAuthPassthroughIgnoresLeftoverMapping(t *testing.T) {
	account := newOpenAIOAuthAccountForModelTest()
	account.Extra = map[string]any{"openai_passthrough": true}
	// 账号从"白名单模式"切到透传后，credentials 里常残留旧的非空 model_mapping。
	// 透传应无视该白名单，放行不在其中的模型（issue #4936）；否则透传账号会被
	// 调度期的 IsModelSupported 排除，客户端收到 404 "not supported by any account"。
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"},
	}

	require.True(t, account.IsModelSupported("gpt-5.6-sol"), "透传应放行不在残留白名单中的新模型")
	require.True(t, account.IsModelSupported("deepseek-v4"), "透传应放行任意模型")
}

func TestIsModelSupported_EmptyMappingOtherAccountTypesUnchanged(t *testing.T) {
	apiKey := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.True(t, apiKey.IsModelSupported("deepseek-v4"))

	anthropic := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	require.True(t, anthropic.IsModelSupported("deepseek-v4"))
}

func TestIsOpenAIOAuthServableModel(t *testing.T) {
	require.True(t, isOpenAIOAuthServableModel("gpt-5.4-high"))
	require.True(t, isOpenAIOAuthServableModel("gpt-5.4_xhigh"))
	require.True(t, isOpenAIOAuthServableModel("  gpt-5.3-codex  "))
	require.True(t, isOpenAIOAuthServableModel("claude-3-5-haiku-20241022"))
	require.False(t, isOpenAIOAuthServableModel("claude-unknown-family"))
	require.False(t, isOpenAIOAuthServableModel("gpt-99-high"))
	require.False(t, isOpenAIOAuthServableModel("deepseek-v4"))
}
