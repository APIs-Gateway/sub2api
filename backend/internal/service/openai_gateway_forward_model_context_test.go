//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithOpenAIForwardModel_NilContextFallsBackToBackground(t *testing.T) {
	t.Parallel()

	ctx := WithOpenAIForwardModel(nil, "gpt-5.4-channel", true)
	require.NotNil(t, ctx)

	forwardModel, ok := openAIForwardModelFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "gpt-5.4-channel", forwardModel.model)
	require.True(t, forwardModel.useCompactModelMapping)
}

func TestOpenAIForwardModelFromContext_NilContextReturnsNotOK(t *testing.T) {
	t.Parallel()

	forwardModel, ok := openAIForwardModelFromContext(nil)
	require.False(t, ok)
	require.Equal(t, openAIForwardModel{}, forwardModel)
}

func TestOpenAIForwardModelFromContext_MissingValueReturnsNotOK(t *testing.T) {
	t.Parallel()

	forwardModel, ok := openAIForwardModelFromContext(context.Background())
	require.False(t, ok)
	require.Equal(t, openAIForwardModel{}, forwardModel)
}

func TestIsUpstreamModelRestrictedByChannel_NilChannelServiceReturnsFalse(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{channelService: nil}
	require.False(t, svc.isUpstreamModelRestrictedByChannel(context.Background(), 10, &Account{}, "gpt-5.4", false))
}

func TestResolveOpenAIAccountUpstreamModelForRequest_PassthroughEmptyRequestedModelReturnsEmpty(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_passthrough": true, "openai_responses_supported": true},
	}

	require.Equal(t, "", resolveOpenAIAccountUpstreamModelForRequest(account, "   ", false))
	require.Equal(t, "", resolveOpenAIAccountUpstreamModelForRequest(account, "   ", true))
}

func TestResolveOpenAIAccountUpstreamModelForRequest_DefaultBranchEmptyUpstreamModelReturnsEmpty(t *testing.T) {
	t.Parallel()

	// 非 passthrough、非 raw-chat-fallback 的普通账号（AccountTypeAPIKey +
	// openai_responses_supported:true 避免落入 raw-chat-fallback 分支）；
	// requestedModel 为空且账号没有任何 model_mapping 命中时，
	// resolveOpenAIForwardModel 会返回空串，落到 default 分支的
	// `if upstreamModel == "" { return "" }`。
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_supported": true},
	}

	require.Equal(t, "", resolveOpenAIAccountUpstreamModelForRequest(account, "", false))
}

func TestResolveOpenAIAccountUpstreamModelForRequest_DefaultBranchCompactModelEqualsUpstreamModel(t *testing.T) {
	t.Parallel()

	// compact_model_mapping 里没有该模型的映射时，resolveOpenAICompactForwardModel
	// 原样返回 upstreamModel，触发 `compactModel != upstreamModel` 为 false 的分支，
	// 落回 normalizeOpenAIModelForUpstream(upstreamModel) 而不是提前 return。
	// AccountTypeAPIKey 下 normalizeOpenAIModelForUpstream 只做 TrimSpace，
	// 避免 AccountTypeOAuth 走 normalizeCodexModel 引入不可预测的映射。
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_supported": true},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-account"},
		},
	}

	got := resolveOpenAIAccountUpstreamModelForRequest(account, "gpt-5.4", true)
	require.Equal(t, "gpt-5.4-account", got)
}
