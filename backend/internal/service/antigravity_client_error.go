package service

import (
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

// antigravitySafeGeminiError recognizes an unwrapped provider error envelope.
// Successful model content is never searched or rewritten. Provider-specific
// details/metadata are not client-visible; genuine candidates and usage remain.
// This presentation rewrite does not establish retry or no-charge eligibility.
func antigravitySafeGeminiError(body []byte) ([]byte, int, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return nil, 0, false
	}
	errBody, exists := root["error"]
	if !exists || string(errBody) == "null" {
		return nil, 0, false
	}
	var provider struct {
		Code int `json:"code"`
	}
	status := http.StatusBadGateway
	if json.Unmarshal(errBody, &provider) == nil && provider.Code >= 400 && provider.Code <= 599 {
		status = provider.Code
	}
	_, _, message, _ := MapUpstreamErrorDefault(status)
	errorJSON, _ := json.Marshal(map[string]any{
		"code": status, "message": message, "status": googleapi.HTTPStatusToGoogleStatus(status),
	})
	safe := map[string]json.RawMessage{"error": errorJSON}
	for _, key := range []string{"candidates", "usageMetadata"} {
		if value, ok := root[key]; ok {
			safe[key] = value
		}
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return nil, 0, false
	}
	return encoded, status, true
}

func (s *AntigravityGatewayService) recordAntigravityGeminiClientError(c *gin.Context, status int, body []byte, stream bool) {
	setOpsUpstreamError(c, status, sanitizeUpstreamErrorMessage(extractAntigravityErrorMessage(body)), s.getUpstreamErrorDetail(body))
	_, errType, message, _ := MapUpstreamErrorDefault(status)
	// These errors retain HTTP 200 on the wire. Without upstream attribution,
	// Ops would treat the raw error context as a recovered failover attempt.
	MarkOpsStreamErrorValue(c, OpsStreamError{
		ErrType: errType, Code: "upstream_error_envelope", Message: message,
		IntendedStatus: status, CountTowardsSLA: true, NonStream: !stream, UpstreamAttributed: true,
	})
}
