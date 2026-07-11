package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type gatewayNoAccountResponseStyle uint8

const (
	gatewayNoAccountResponseStreaming gatewayNoAccountResponseStyle = iota
	gatewayNoAccountResponseClaude
	gatewayNoAccountResponseChatCompletions
	gatewayNoAccountResponseResponses
	gatewayNoAccountResponseGemini
)

// respondNoAccountError keeps GatewayHandler's selection-failure response
// protocol-specific while making all routes share the same 404-versus-503
// decision and capacity-marking semantics.
func (h *GatewayHandler) respondNoAccountError(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel, displayModel, platform, fallbackMessage string,
	selectionErr error,
	markMode noAccountCapacityMarkMode,
	style gatewayNoAccountResponseStyle,
	streamStarted bool,
) {
	cls := resolveNoAccountError(c, diag, apiKey, routingModel, displayModel, platform, fallbackMessage, selectionErr, markMode)
	switch style {
	case gatewayNoAccountResponseStreaming:
		h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
	case gatewayNoAccountResponseChatCompletions:
		h.chatCompletionsErrorResponse(c, cls.Status, cls.ErrType, cls.Message)
	case gatewayNoAccountResponseResponses:
		h.responsesErrorResponse(c, cls.Status, cls.ErrType, cls.Message)
	case gatewayNoAccountResponseGemini:
		googleError(c, cls.Status, cls.Message)
	case gatewayNoAccountResponseClaude:
		fallthrough
	default:
		h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
	}
}

type openAINoAccountResponseStyle uint8

const (
	openAINoAccountResponseStreaming openAINoAccountResponseStyle = iota
	openAINoAccountResponseJSON
	openAINoAccountResponseAnthropic
)

// respondNoAccountError keeps OpenAIGatewayHandler's three error envelopes
// consistent while delegating the account-state classification to the shared
// resolver.
func (h *OpenAIGatewayHandler) respondNoAccountError(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel, displayModel, platform, fallbackMessage string,
	selectionErr error,
	markMode noAccountCapacityMarkMode,
	style openAINoAccountResponseStyle,
	streamStarted bool,
) {
	cls := resolveNoAccountError(c, diag, apiKey, routingModel, displayModel, platform, fallbackMessage, selectionErr, markMode)
	switch style {
	case openAINoAccountResponseJSON:
		h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
	case openAINoAccountResponseAnthropic:
		h.anthropicStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
	case openAINoAccountResponseStreaming:
		fallthrough
	default:
		h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
	}
}
