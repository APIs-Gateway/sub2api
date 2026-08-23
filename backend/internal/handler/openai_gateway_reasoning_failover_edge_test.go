package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// openai_gateway_reasoning_failover_test.go already drives every
// deriveOpenAIForwardAttemptBody transition (passthrough-marks-seen,
// before-any-passthrough, after-passthrough strips, sticky-across-retries) but
// always passes a nil *zap.Logger, so the reqLog != nil branches (both the
// Warn and Info calls) and the sanitize-error fallback are never reached.
// This file adds exactly those.

func TestDeriveOpenAIForwardAttemptBody_SanitizeErrorLogsWarnAndFallsBackToCanonical(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	core, logs := observer.New(zap.WarnLevel)
	reqLog := zap.New(core)

	state := &openAIPassthroughFailoverState{passthroughSeen: true}
	account := &service.Account{ID: 7, Platform: service.PlatformOpenAI}
	// gjson finds "input" (so SanitizeOpenAICrossModeFailoverReasoning doesn't take
	// its early no-op exit) but the body is not valid JSON, so the full
	// encoding/json decode step errors -> must be logged and fall back to canonical.
	canonical := []byte(`{"input":[1,2],"tail":BAD}`)

	out := h.deriveOpenAIForwardAttemptBody(reqLog, canonical, account, state)

	require.Equal(t, canonical, out)
	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "openai.failover_cross_mode_reasoning_sanitize_failed", entries[0].Message)
	require.Equal(t, zap.WarnLevel, entries[0].Level)
}

func TestDeriveOpenAIForwardAttemptBody_SanitizeSuccessLogsInfo(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	core, logs := observer.New(zap.InfoLevel)
	reqLog := zap.New(core)

	state := &openAIPassthroughFailoverState{passthroughSeen: true}
	account := &service.Account{ID: 8, Platform: service.PlatformOpenAI}
	canonical := []byte(`{"input":[` +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"},` +
		`{"type":"message","content":"hi"}` +
		`]}`)

	out := h.deriveOpenAIForwardAttemptBody(reqLog, canonical, account, state)

	require.NotEqual(t, canonical, out)
	require.NotContains(t, string(out), "encrypted_content")
	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "openai.failover_cross_mode_reasoning_stripped", entries[0].Message)
	require.Equal(t, zap.InfoLevel, entries[0].Level)
}

func TestDeriveOpenAIForwardAttemptBody_NoChangeLogsNothing(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	core, logs := observer.New(zap.DebugLevel)
	reqLog := zap.New(core)

	state := &openAIPassthroughFailoverState{passthroughSeen: true}
	account := &service.Account{ID: 9, Platform: service.PlatformOpenAI}
	canonical := []byte(`{"input":[{"type":"message","content":"hi"}]}`)

	out := h.deriveOpenAIForwardAttemptBody(reqLog, canonical, account, state)

	require.Equal(t, canonical, out)
	require.Empty(t, logs.All())
}
