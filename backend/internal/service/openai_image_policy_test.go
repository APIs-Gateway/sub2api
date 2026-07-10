package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAIResponsesWebSocketImageGenerationIntent(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		want  bool
	}{
		{
			name:  "response create image model",
			frame: `{"type":"response.create","model":"gpt-image-1"}`,
			want:  true,
		},
		{
			name:  "response create image tool",
			frame: `{"type":"response.create","model":"gpt-5.5","tools":[{"type":"image_generation"}]}`,
			want:  true,
		},
		{
			name:  "session update image model",
			frame: `{"type":"session.update","session":{"model":"gpt-image-1"}}`,
			want:  true,
		},
		{
			name:  "session update image tool",
			frame: `{"type":"session.update","session":{"model":"gpt-5.5","tools":[{"type":"image_generation"}]}}`,
			want:  true,
		},
		{
			name:  "ordinary text response",
			frame: `{"type":"response.create","model":"gpt-5.5","input":"hello"}`,
			want:  false,
		},
		{
			name:  "invalid json is not image intent",
			frame: `{`,
			want:  false,
		},
		{
			name:  "session update requires an object session",
			frame: `{"type":"session.update","session":[]}`,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsOpenAIResponsesWebSocketImageGenerationIntent([]byte(tt.frame)))
		})
	}
}

func TestRejectOpenAIResponsesWebSocketImageGeneration(t *testing.T) {
	frame := []byte(`{"type":"session.update","session":{"tools":[{"type":"image_generation"}]}}`)

	disabled := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		DisableOpenAIResponsesImageGeneration: true,
	}}}
	err := disabled.rejectOpenAIResponsesWebSocketImageGeneration(frame)
	require.Error(t, err)
	require.Contains(t, err.Error(), OpenAIResponsesImageGenerationDisabledMessage())

	enabled := &OpenAIGatewayService{cfg: &config.Config{}}
	require.NoError(t, enabled.rejectOpenAIResponsesWebSocketImageGeneration(frame))
}

func TestRejectOpenAIResponsesWebSocketImageGenerationFrame(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		DisableOpenAIResponsesImageGeneration: true,
	}}}
	frame := []byte(`{"type":"response.create","tools":[{"type":"image_generation"}]}`)

	// WebSocket JSON may be sent as binary, so passthrough mode must reject it
	// before its normal text-only Fast Policy processing.
	require.Error(t, svc.rejectOpenAIResponsesWebSocketImageGenerationFrame(coderws.MessageBinary, frame))
	require.NoError(t, svc.rejectOpenAIResponsesWebSocketImageGenerationFrame(coderws.MessageType(99), frame))
}
