package service

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

const openAIResponsesImageGenerationDisabledMessage = "Image generation via /v1/responses is disabled"

// OpenAIImagesStreamingDisabled reports whether Images API requests must be
// normalized to a single non-streaming JSON response.
func OpenAIImagesStreamingDisabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Gateway.DisableOpenAIImagesStreaming
}

// OpenAIResponsesImageGenerationDisabled reports whether the server must
// reject Responses image tools and keep Images API requests on direct API-key
// Images endpoints.
func OpenAIResponsesImageGenerationDisabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Gateway.DisableOpenAIResponsesImageGeneration
}

// OpenAIResponsesImageGenerationDisabledMessage is intentionally safe to
// expose in OpenAI-compatible HTTP and WebSocket error responses.
func OpenAIResponsesImageGenerationDisabledMessage() string {
	return openAIResponsesImageGenerationDisabledMessage
}

// IsOpenAIResponsesWebSocketImageGenerationIntent recognizes image-generation
// requests carried by a client->upstream Responses WebSocket frame. A
// session.update puts model/tools/tool_choice under session rather than at the
// top level, so it must be checked separately from response.create.
func IsOpenAIResponsesWebSocketImageGenerationIntent(frame []byte) bool {
	if len(frame) == 0 || !gjson.ValidBytes(frame) {
		return false
	}
	if IsImageGenerationIntent(openAIResponsesEndpoint, gjson.GetBytes(frame, "model").String(), frame) {
		return true
	}
	if gjson.GetBytes(frame, "type").String() != "session.update" {
		return false
	}
	session := gjson.GetBytes(frame, "session")
	if !session.IsObject() {
		return false
	}
	return IsImageGenerationIntent(
		openAIResponsesEndpoint,
		session.Get("model").String(),
		[]byte(session.Raw),
	)
}

func (s *OpenAIGatewayService) openAIImagesStreamingDisabled() bool {
	return s != nil && OpenAIImagesStreamingDisabled(s.cfg)
}

func (s *OpenAIGatewayService) openAIResponsesImageGenerationDisabled() bool {
	return s != nil && OpenAIResponsesImageGenerationDisabled(s.cfg)
}

// openAIImagesRequireDirectAPIKey means the Images API may not use a
// Responses-backed bridge. The non-streaming setting also uses this rule so
// it cannot be undermined by an upstream Responses SSE connection.
func (s *OpenAIGatewayService) openAIImagesRequireDirectAPIKey() bool {
	return s.openAIImagesStreamingDisabled() || s.openAIResponsesImageGenerationDisabled()
}

func (s *OpenAIGatewayService) rejectOpenAIResponsesWebSocketImageGeneration(frame []byte) error {
	if s.openAIResponsesImageGenerationDisabled() && IsOpenAIResponsesWebSocketImageGenerationIntent(frame) {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			OpenAIResponsesImageGenerationDisabledMessage(),
			nil,
		)
	}
	return nil
}

func (s *OpenAIGatewayService) rejectOpenAIResponsesWebSocketImageGenerationFrame(msgType coderws.MessageType, frame []byte) error {
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		return nil
	}
	return s.rejectOpenAIResponsesWebSocketImageGeneration(frame)
}
