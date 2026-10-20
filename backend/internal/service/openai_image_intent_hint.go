package service

import "github.com/gin-gonic/gin"

// openAIImageIntentHintContextKey caches, for the lifetime of a single inbound
// HTTP /v1/responses request, whether the client's *original* request body was
// classified as an image-generation request by IsImageGenerationIntent.
//
// Forward() runs once per account-switch failover attempt for the same logical
// request. Each attempt independently re-derives account-specific state
// (channel/compact model mapping, Codex image-generation bridge injection,
// Codex Spark tool stripping, ...) from the same canonical body, and some of
// that per-attempt state legitimately changes what a *given* attempt should be
// billed/gated as (e.g. an account whose model mapping happens to resolve to an
// image-capable model must still be gated for that attempt, see
// TestOpenAIGatewayService_Forward_MappedImageModelUsesImageGate /
// TestOpenAIGatewayServiceForward_ServerPolicyRejectsImageGenerationAfterModelMapping).
//
// What must NOT happen is the openAIResponsesImageGenerationDisabled() gate
// silently flipping from "blocked" to "allowed" purely because a later attempt's
// own account-specific body normalization happened to read the *client-supplied*
// portion of the request differently than an earlier attempt did (upstream
// 2ceaa4783c20 hit exactly this via per-attempt Codex tool stripping; see #754).
// This hint makes that portion of the classification sticky and request-scoped:
// once any attempt observes an image-generation signal in the client-supplied
// body, the disabled gate keeps treating the logical request as image intent for
// every subsequent attempt, regardless of what any single attempt's own
// normalization does to its local copy of the body.
const openAIImageIntentHintContextKey = "openai_image_intent_hint"

// SetOpenAIImageIntentHint records the canonical (client-body-derived) image
// intent for the current request. Scoped to HTTP-transport requests only: the
// Responses WebSocket handler classifies image intent independently up front
// (IsExplicitOpenAIResponsesWebSocketImageGenerationIntent) and never calls
// Forward(), so there is nothing for it to share this cache with.
func SetOpenAIImageIntentHint(c *gin.Context, imageIntent bool) {
	if c == nil || GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP {
		return
	}
	c.Set(openAIImageIntentHintContextKey, imageIntent)
}

// getOpenAIImageIntentHint reads the cached canonical image intent, if any.
func getOpenAIImageIntentHint(c *gin.Context) (imageIntent bool, known bool) {
	if c == nil || GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP {
		return false, false
	}
	value, ok := c.Get(openAIImageIntentHintContextKey)
	if !ok {
		return false, false
	}
	imageIntent, ok = value.(bool)
	return imageIntent, ok
}

// resolveOpenAIResponsesImageIntentHint classifies originalBody/originalModel
// (the request view captured before any account-specific mapping, bridging or
// tool stripping runs in this attempt) and merges the result into the
// request-scoped canonical hint, sticky-true across attempts: once any attempt
// observes image intent in the client-supplied request, every later attempt
// keeps observing it here too, even if that later attempt's own local
// classification of the same canonical inputs would (for whatever incidental
// reason) come back false. It only ever adds true, never removes an
// already-established true, and it is intentionally blind to account-specific
// state (model mapping, Codex bridge injection, Spark stripping) — those stay
// fully attempt-local by design, see callers in Forward().
func resolveOpenAIResponsesImageIntentHint(c *gin.Context, originalModel string, originalBody []byte) bool {
	rawIntent := IsImageGenerationIntent(openAIResponsesEndpoint, originalModel, originalBody)
	cached, known := getOpenAIImageIntentHint(c)
	imageIntent := rawIntent || (known && cached)
	SetOpenAIImageIntentHint(c, imageIntent)
	return imageIntent
}
