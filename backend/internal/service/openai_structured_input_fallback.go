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

const openAIStructuredInputRawFallbackKey = "openai_structured_input_raw_fallback"

// OpenAIStructuredInputRecoveredViaRawChat records the actual transport for this
// attempt. It never changes the account's persisted capability or billing model.
func OpenAIStructuredInputRecoveredViaRawChat(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(openAIStructuredInputRawFallbackKey)
	recovered, ok := value.(bool)
	return exists && ok && recovered
}

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
		case "invalid_request_error", "BadRequestError", "Bad Request":
		case float64(http.StatusBadRequest):
			if key != "code" {
				return false
			}
		default:
			return false
		}
	}
	param, hasParam := provider["param"]
	if hasParam && param != nil && param != "input" {
		return false
	}
	if invalidType && param == "input" && provider["type"] != "Bad Request" && structuredInputTypeMessage.MatchString(message) {
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
	// Parse a complete bounded repr without evaluating Python or searching echoes.
	value, ok := p.value(0)
	diagnostics, list := value.([]any)
	if !ok || !list || len(diagnostics) != count || strings.TrimSpace(p.rest) != "" {
		return false
	}
	for _, value := range diagnostics {
		diagnostic, ok := value.(map[string]any)
		if !ok || !structuredErrorKeys(diagnostic, "type", "loc", "msg", "input", "input_value", "ctx", "url") {
			return false
		}
	}
	first := diagnostics[0].(map[string]any)
	if typ, exists := first["type"]; exists && typ != "string_type" {
		return false
	}
	loc, ok := first["loc"].(structuredDiagnosticTuple)
	return ok && len(loc) == 3 && loc[0] == "body" && loc[1] == "input" && loc[2] == "str" && first["msg"] == "Input should be a valid string"
}

type structuredDiagnosticParser struct {
	rest  string
	nodes int
}

func (p *structuredDiagnosticParser) take(token string) bool {
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if !strings.HasPrefix(p.rest, token) {
		return false
	}
	p.rest = p.rest[len(token):]
	return true
}

func (p *structuredDiagnosticParser) quoted() (string, bool) {
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if len(p.rest) < 2 || (p.rest[0] != '\'' && p.rest[0] != '"') {
		return "", false
	}
	quote := p.rest[0]
	rest := p.rest[1:]
	var decoded strings.Builder
	for len(rest) > 0 {
		if rest[0] == quote {
			p.rest = rest[1:]
			return decoded.String(), true
		}
		if rest[0] < ' ' {
			return "", false
		}
		character, _, tail, err := strconv.UnquoteChar(rest, quote)
		if err != nil {
			return "", false
		}
		decoded.WriteRune(character)
		rest = tail
	}
	return "", false
}

// An opaque marker distinguishes a validated dataclass repr from strings/lists.
// Only syntax is consumed; no names, constructors or arguments are executed.
type structuredDiagnosticCall struct{}
type structuredDiagnosticTuple []any

func (p *structuredDiagnosticParser) value(depth int) (any, bool) {
	p.nodes++
	if depth > 32 || p.nodes > 4096 {
		return nil, false
	}
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if len(p.rest) == 0 {
		return nil, false
	}
	switch p.rest[0] {
	case '\'', '"':
		return p.quoted()
	case '{':
		p.rest = p.rest[1:]
		result := map[string]any{}
		if p.take("}") {
			return result, true
		}
		for {
			key, ok := p.quoted()
			if !ok || !p.take(":") {
				return nil, false
			}
			if _, duplicate := result[key]; duplicate {
				return nil, false
			}
			value, ok := p.value(depth + 1)
			if !ok {
				return nil, false
			}
			result[key] = value
			if p.take("}") {
				return result, true
			}
			if !p.take(",") {
				return nil, false
			}
			if p.take("}") {
				return result, true
			}
		}
	case '[', '(':
		close := "]"
		tuple := p.rest[0] == '('
		if tuple {
			close = ")"
		}
		p.rest = p.rest[1:]
		result := []any{}
		sequence := func() (any, bool) {
			if tuple {
				return structuredDiagnosticTuple(result), true
			}
			return result, true
		}
		if p.take(close) {
			return sequence()
		}
		for {
			value, ok := p.value(depth + 1)
			if !ok {
				return nil, false
			}
			result = append(result, value)
			if p.take(close) {
				return sequence()
			}
			if !p.take(",") {
				return nil, false
			}
			if p.take(close) {
				return sequence()
			}
		}
	default:
		if name := p.identifier(); name != "" {
			switch name {
			case "None":
				return nil, true
			case "True":
				return true, true
			case "False":
				return false, true
			}
			if !p.take("(") {
				return nil, false
			}
			if p.take(")") {
				return structuredDiagnosticCall{}, true
			}
			keywords := map[string]bool{}
			for {
				before := p.rest
				keyword := p.identifier()
				if keyword != "" && p.take("=") {
					if keywords[keyword] {
						return nil, false
					}
					keywords[keyword] = true
				} else {
					p.rest = before
				}
				if _, ok := p.value(depth + 1); !ok {
					return nil, false
				}
				if p.take(")") {
					return structuredDiagnosticCall{}, true
				}
				if !p.take(",") {
					return nil, false
				}
				if p.take(")") {
					return structuredDiagnosticCall{}, true
				}
			}
		}
		end := strings.IndexAny(p.rest, ",):]} \t\r\n")
		if end < 0 {
			end = len(p.rest)
		}
		if end == 0 {
			return nil, false
		}
		number, err := strconv.ParseFloat(p.rest[:end], 64)
		if err != nil {
			return nil, false
		}
		p.rest = p.rest[end:]
		return number, true
	}
}

func (p *structuredDiagnosticParser) identifier() string {
	p.rest = strings.TrimLeft(p.rest, " \t\r\n")
	if len(p.rest) == 0 || !structuredIdentifierStart(p.rest[0]) {
		return ""
	}
	end := 1
	for end < len(p.rest) && (structuredIdentifierStart(p.rest[end]) || p.rest[end] >= '0' && p.rest[end] <= '9') {
		end++
	}
	value := p.rest[:end]
	p.rest = p.rest[end:]
	return value
}
func structuredIdentifierStart(character byte) bool {
	return character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
