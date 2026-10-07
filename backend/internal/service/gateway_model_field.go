package service

import (
	"errors"
	"net/http"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var errAmbiguousGatewayModel = errors.New("model must appear at most once using the canonical field name")

// ValidateGatewayModelField prevents first-key routing and last-key typed
// decoding from selecting different models. It deliberately allows an absent
// model: required-field checks and later WebSocket inheritance belong to callers.
// Only top-level keys are inspected; nested metadata and unknown fields remain
// intact, without decoding the payload into a map or rebuilding its body.
func ValidateGatewayModelField(body []byte) error {
	_, err := gatewayModelObject(body)
	return err
}

func gatewayModelObject(body []byte) (gjson.Result, error) {
	if !gjson.ValidBytes(body) {
		return gjson.Result{}, errors.New("invalid JSON request body")
	}
	object := gjson.ParseBytes(body)
	if !object.IsObject() {
		return gjson.Result{}, errors.New("request body must be a JSON object")
	}
	return object, validateGatewayModelObject(object)
}

func validateGatewayModelObject(object gjson.Result) error {
	seen := false
	var err error
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if !strings.EqualFold(name, "model") {
			return true
		}
		if name != "model" || seen {
			err = errAmbiguousGatewayModel
			return false
		}
		seen = true
		return true
	})
	return err
}

// ValidateGatewayWSModelFields also checks session.model, which can change the
// inherited model in passthrough sessions. Duplicate session objects must not
// hide a different model from the per-frame routing and usage policy.
func ValidateGatewayWSModelFields(body []byte) error {
	object, err := gatewayModelObject(body)
	if err != nil {
		return err
	}
	seenSession := false
	object.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if !strings.EqualFold(name, "session") {
			return true
		}
		if name != "session" || seenSession {
			err = errors.New("session must appear at most once using the canonical field name")
			return false
		}
		seenSession = true
		if value.IsObject() {
			err = validateGatewayModelObject(value)
		}
		return err == nil
	})
	return err
}

func rejectAmbiguousGatewayModel(c *gin.Context, body []byte) error {
	if err := ValidateGatewayModelField(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": err.Error(), "param": "model",
		}})
		return err
	}
	return nil
}

func validateGatewayWSModelPayload(body []byte) error {
	if err := ValidateGatewayWSModelFields(body); err != nil {
		rejection := newOpenAIWSLocalRejection(http.StatusBadRequest, "invalid_request_error", "", err.Error(), err)
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, err.Error(), rejection)
	}
	return nil
}
