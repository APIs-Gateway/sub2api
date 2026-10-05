package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// This is an internal protocol retry, not a new billing attempt or a claim that
// any transport failure is free. Both calls retain the caller's immutable lease.
func canRecoverConvertedResponsesInput(ctx context.Context, c *gin.Context, account *Account, responsesShape bool, outbound []byte) bool {
	if ctx == nil || ctx.Err() != nil || c == nil || c.Request == nil || c.Request.Context().Err() != nil || c.Writer == nil || c.Writer.Written() || account == nil || !account.IsOpenAIApiKey() || responsesShape {
		return false
	}
	supported, ok := account.Extra[openai_compat.ExtraKeyResponsesSupported].(bool)
	if !ok || !supported {
		return false
	}
	mode, _ := account.Extra[openai_compat.ExtraKeyResponsesMode].(string)
	if openai_compat.NormalizeResponsesSupportMode(mode) != openai_compat.ResponsesSupportModeAuto {
		return false
	}
	input := gjson.GetBytes(outbound, "input")
	return input.IsArray() || input.IsObject()
}

var structuredInputTypeMessage = regexp.MustCompile(`^(?:Invalid type for ['"]input['"]: expected a string|Expected a string(?: for input)?), but got an (?:array|object)(?: instead)?\.$`)

func isConvertedResponsesInputStringRejection(status int, body []byte, readErr error) bool {
	if status != http.StatusBadRequest || readErr != nil || len(body) > 64*1024 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	payload, err := decodeBillingInflightErrorValue(decoder, 0)
	if err != nil || billingInflightErrorHasUsage(payload) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	outer, ok := payload.(map[string]any)
	if !ok || !structuredErrorKeys(outer, "error", "type") {
		return false
	}
	if typ, exists := outer["type"]; exists && typ != "error" {
		return false
	}
	provider, ok := outer["error"].(map[string]any)
	if !ok || !structuredErrorKeys(provider, "message", "type", "code", "param") {
		return false
	}
	message, ok := provider["message"].(string)
	if !ok {
		return false
	}
	invalidType := false
	for _, key := range []string{"type", "code"} {
		value, exists := provider[key]
		if !exists || value == nil {
			continue
		}
		switch value {
		case "invalid_type":
			invalidType = true
		case "invalid_request_error", "BadRequestError":
		case float64(http.StatusBadRequest):
			if key != "code" {
				return false
			}
		default:
			return false
		}
	}
	param, hasParam := provider["param"]
	if hasParam && param != "input" {
		return false
	}
	if invalidType && hasParam && structuredInputTypeMessage.MatchString(message) {
		return true
	}
	return firstVLLMInputStringDiagnostic(message)
}

// An allowlist also rejects case/Unicode-fold aliases before any typed decoder
// can accept them. Duplicate decoded keys are rejected by the recursive parser.
func structuredErrorKeys(value map[string]any, allowed ...string) bool {
	for key := range value {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Only parse the first diagnostic, before its input/input_value echo. Never
// search a repr for loc/msg: those strings can be user-controlled history.
func firstVLLMInputStringDiagnostic(message string) bool {
	if len(message) > 32*1024 {
		return false
	}
	p := structuredDiagnosticParser{rest: strings.TrimSpace(message)}
	end := strings.IndexByte(p.rest, ' ')
	if end <= 0 {
		return false
	}
	count, err := strconv.Atoi(p.rest[:end])
	if err != nil || count < 1 || count > 1024 {
		return false
	}
	p.rest = p.rest[end:]
	if !p.take("validation errors:") && !p.take("validation error:") {
		return false
	}
	// Full Python list/dict representation; elided diagnostics are ambiguous.
	if !p.take("[") || !p.take("{") {
		return false
	}
	key, ok := p.quoted()
	if !ok {
		return false
	}
	if key == "type" {
		if !p.take(":") || !p.stringValue("string_type") || !p.take(",") {
			return false
		}
		key, ok = p.quoted()
	}
	if !ok || key != "loc" || !p.take(":") || !p.take("(") || !p.stringValue("body") || !p.take(",") || !p.stringValue("input") || !p.take(",") || !p.stringValue("str") || !p.take(")") || !p.take(",") || !p.stringValue("msg") || !p.take(":") || !p.stringValue("Input should be a valid string") {
		return false
	}
	if p.take("}") {
		return p.take("]") && strings.TrimSpace(p.rest) == "" && count == 1
	}
	if !p.take(",") {
		return false
	}
	key, ok = p.quoted()
	return ok && (key == "input" || key == "input_value") && p.take(":")
}

type structuredDiagnosticParser struct{ rest string }

func (p *structuredDiagnosticParser) take(token string) bool {
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if !strings.HasPrefix(p.rest, token) {
		return false
	}
	p.rest = p.rest[len(token):]
	return true
}

func (p *structuredDiagnosticParser) stringValue(want string) bool {
	value, ok := p.quoted()
	return ok && value == want
}

func (p *structuredDiagnosticParser) quoted() (string, bool) {
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if len(p.rest) < 2 || (p.rest[0] != '\'' && p.rest[0] != '"') {
		return "", false
	}
	quote := p.rest[0]
	for i := 1; i < len(p.rest); i++ {
		if p.rest[i] == '\\' || p.rest[i] < ' ' {
			return "", false
		}
		if p.rest[i] == quote {
			value := p.rest[1:i]
			p.rest = p.rest[i+1:]
			return value, true
		}
	}
	return "", false
}
