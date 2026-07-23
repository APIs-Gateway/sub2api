//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestDetectModelPlatform(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		platform string
		ok       bool
	}{
		{name: "claude", model: "claude-sonnet-4-5", platform: PlatformAnthropic, ok: true},
		{name: "anthropic prefix", model: "anthropic/claude-opus-4-5", platform: PlatformAnthropic, ok: true},
		{name: "anthropic dotted prefix", model: "anthropic.claude-sonnet-4-5", platform: PlatformAnthropic, ok: true},
		{name: "claude provider prefix", model: "claude/claude-sonnet-4-5", platform: PlatformAnthropic, ok: true},
		{name: "gpt", model: "gpt-5.1", platform: PlatformOpenAI, ok: true},
		{name: "o series", model: "o3-mini", platform: PlatformOpenAI, ok: true},
		{name: "embedding", model: "text-embedding-3-large", platform: PlatformOpenAI, ok: true},
		{name: "openai provider prefix", model: "openai/gpt-5.1", platform: PlatformOpenAI, ok: true},
		{name: "chatgpt provider prefix", model: "chatgpt/gpt-5.1", platform: PlatformOpenAI, ok: true},
		{name: "unknown provider falls back to model", model: "vendor/grok-4", platform: PlatformGrok, ok: true},
		{name: "gemini", model: "gemini-3-pro", platform: PlatformGemini, ok: true},
		{name: "gemini models prefix", model: "models/gemini-2.5-flash", platform: PlatformGemini, ok: true},
		{name: "learnlm", model: "learnlm-2.0-flash-experimental", platform: PlatformGemini, ok: true},
		{name: "google provider prefix", model: "google/gemini-2.5-flash", platform: PlatformGemini, ok: true},
		{name: "grok", model: "grok-4", platform: PlatformGrok, ok: true},
		{name: "grok exact", model: "grok", platform: PlatformGrok, ok: true},
		{name: "xai prefix", model: "xai/grok-4", platform: PlatformGrok, ok: true},
		{name: "x-ai prefix", model: "x-ai/grok-4", platform: PlatformGrok, ok: true},
		{name: "empty", model: "  ", ok: false},
		{name: "unknown", model: "llama-4-maverick", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform, ok := DetectModelPlatform(tt.model)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.platform, platform)
		})
	}
}

func TestResolvedTargetPlatformContext(t *testing.T) {
	require.Nil(t, WithResolvedTargetPlatform(nil, PlatformGrok))
	base := context.Background()
	require.Equal(t, base, WithResolvedTargetPlatform(base, "  "))

	platform, ok := ResolvedTargetPlatformFromContext(nil)
	require.False(t, ok)
	require.Empty(t, platform)

	platform, ok = ResolvedTargetPlatformFromContext(context.WithValue(base, ctxkey.ResolvedTargetPlatform, "  "))
	require.False(t, ok)
	require.Empty(t, platform)

	platform, ok = ResolvedTargetPlatformFromContext(WithResolvedTargetPlatform(base, " grok "))
	require.True(t, ok)
	require.Equal(t, PlatformGrok, platform)
}

func TestCompositePlatformResolutionHelpers(t *testing.T) {
	composite := &Group{Platform: PlatformComposite}
	platform, ok := resolveCompositeTargetPlatform(context.Background(), composite, "grok-4")
	require.True(t, ok)
	require.Equal(t, PlatformGrok, platform)

	platform, ok = resolveCompositeTargetPlatform(WithResolvedTargetPlatform(context.Background(), PlatformOpenAI), nil, "unknown")
	require.True(t, ok)
	require.Equal(t, PlatformOpenAI, platform)

	platform, ok = resolveCompositeTargetPlatform(context.Background(), nil, "grok-4")
	require.False(t, ok)
	require.Empty(t, platform)
	platform, ok = resolveCompositeTargetPlatform(context.Background(), &Group{Platform: PlatformOpenAI}, "grok-4")
	require.False(t, ok)
	require.Empty(t, platform)
}

func TestIsConcreteRequestPlatform(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		require.True(t, isConcreteRequestPlatform(platform), platform)
	}
	for _, platform := range []string{"", PlatformComposite, "unknown"} {
		require.False(t, isConcreteRequestPlatform(platform), platform)
	}
}

func TestQuotaPlatformCompositeUsesResolvedOrForceOnly(t *testing.T) {
	apiKey := &APIKey{Group: &Group{Platform: PlatformComposite}}

	require.Equal(t, PlatformComposite, QuotaPlatform(context.Background(), apiKey))
	require.Equal(t, PlatformGemini, QuotaPlatform(WithResolvedTargetPlatform(context.Background(), PlatformGemini), apiKey))
	require.Equal(t, PlatformAntigravity, QuotaPlatform(context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity), apiKey))

	ctx := WithResolvedTargetPlatform(context.Background(), PlatformAnthropic)
	ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAntigravity)
	require.Equal(t, PlatformAntigravity, QuotaPlatform(ctx, apiKey))
}

func TestSchedulerPlatformsForCompositeGroup(t *testing.T) {
	require.ElementsMatch(t,
		[]string{PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformAntigravity, PlatformGrok},
		schedulerPlatformsForGroup(PlatformComposite),
	)
	require.Equal(t, []string{PlatformAnthropic}, schedulerPlatformsForGroup(PlatformAnthropic))
}
