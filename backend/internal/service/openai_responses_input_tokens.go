package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tiktoken-go/tokenizer"
)

// IsOpenAIResponsesInputTokensPath accepts only the registered Responses aliases.
// Other subresources must retain their existing generation/compact dispatch.
func IsOpenAIResponsesInputTokensPath(path string) bool {
	switch strings.TrimSuffix(path, "/") {
	case "/v1/responses/input_tokens", "/responses/input_tokens", "/backend-api/codex/responses/input_tokens":
		return true
	default:
		return false
	}
}

func ResponsesInputTokensAttemptedUpstream(c *gin.Context) bool {
	return c.GetBool("_responses_input_tokens_upstream_attempted")
}

type openAIInputTokensRequest struct {
	Model              string            `json:"model"`
	Instructions       string            `json:"instructions,omitempty"`
	Input              json.RawMessage   `json:"input,omitempty"`
	Tools              []json.RawMessage `json:"tools,omitempty"`
	ToolChoice         json.RawMessage   `json:"tool_choice,omitempty"`
	Text               json.RawMessage   `json:"text,omitempty"`
	Conversation       json.RawMessage   `json:"conversation,omitempty"`
	PreviousResponseID json.RawMessage   `json:"previous_response_id,omitempty"`
}

// ResponsesInputTokensModel rejects duplicate top-level fields so that the
// model audited/selected with gjson cannot differ from the model parsed by an
// upstream JSON decoder. Unknown single fields remain intact on the wire.
func ResponsesInputTokensModel(body []byte) (string, error) {
	fields, err := decodeResponsesInputTokensObject(body)
	if err != nil {
		return "", err
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("input_tokens: model is required")
	}
	return model, nil
}

func decodeResponsesInputTokensObject(body []byte, canonicalFields ...string) (map[string]json.RawMessage, error) {
	if len(canonicalFields) == 0 {
		canonicalFields = []string{"model", "instructions", "input", "tools", "tool_choice", "text", "conversation", "previous_response_id", "object", "input_tokens"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("input_tokens: expected object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if _, exists := fields[name]; !ok || exists {
			return nil, errors.New("input_tokens: duplicate field")
		}
		// encoding/json accepts case-insensitive struct field names, while
		// policy inspection and the upstream API use the canonical JSON keys.
		// Reject aliases that could change a value after it was audited.
		for _, canonical := range canonicalFields {
			if strings.EqualFold(name, canonical) && name != canonical {
				return nil, errors.New("input_tokens: noncanonical field name")
			}
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("input_tokens: trailing input")
	}
	return fields, nil
}

// ForwardResponsesInputTokens is a preflight operation. It never constructs a
// generation request, returns ForwardResult, or writes billable usage.
func (s *OpenAIGatewayService) ForwardResponsesInputTokens(ctx context.Context, c *gin.Context, account *Account, body []byte) error {
	c.Set("_responses_input_tokens_upstream_attempted", false)
	writeError := func(status int, kind, message string, err error) error {
		c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
		return err
	}
	if account == nil {
		return writeError(http.StatusServiceUnavailable, "api_error", "No available accounts", errors.New("input_tokens: missing account"))
	}
	restriction := s.detectCodexClientRestriction(c, account)
	if restriction.Enabled && !restriction.Matched {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		return writeError(http.StatusForbidden, "forbidden_error", CodexOfficialClientsOnlyMessage, ErrCodexClientRestricted)
	}
	if _, err := ResponsesInputTokensModel(body); err != nil {
		return writeError(http.StatusBadRequest, "invalid_request_error", "Invalid input token count request", err)
	}
	var request openAIInputTokensRequest
	if err := json.Unmarshal(body, &request); err != nil || strings.TrimSpace(request.Model) == "" {
		return writeError(http.StatusBadRequest, "invalid_request_error", "Invalid input token count request", errors.New("input_tokens: invalid request"))
	}
	request.Model = normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, strings.TrimSpace(request.Model), ""))
	estimate := func() error {
		count, err := estimateResponsesInputTokens(request)
		if err != nil {
			return writeError(http.StatusBadRequest, "invalid_request_error", "This request requires native token counting; local estimation supports self-contained text and function tools", err)
		}
		c.Header("X-Sub2api-Token-Count", "estimated")
		c.JSON(http.StatusOK, gin.H{"object": "response.input_tokens", "input_tokens": count})
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !usesOfficialInputTokensEndpoint(account) {
		return estimate()
	}
	if s.cfg != nil {
		if _, err := s.validateUpstreamBaseURL("https://api.openai.com"); err != nil {
			return writeError(http.StatusBadGateway, "upstream_error", "Token counting endpoint is not allowed", err)
		}
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return writeError(http.StatusBadGateway, "upstream_error", "Failed to get access token", fmt.Errorf("input_tokens credentials: %w", err))
	}
	// OAuth's generation endpoint is ChatGPT, but native counting is a Platform
	// API endpoint. Never send this request to the paid generation endpoint.
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI), http.MethodPost,
		openaiPlatformAPIURL+"/input_tokens", bytes.NewReader(ReplaceModelInBody(body, request.Model)))
	if err != nil {
		return writeError(http.StatusInternalServerError, "api_error", "Failed to build request", err)
	}
	req.Header.Set("Authorization", buildOpenAIAuthorizationHeader(account, token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for _, name := range []string{"User-Agent", "Accept-Language"} {
		if value := c.GetHeader(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	start := time.Now()
	c.Set("_responses_input_tokens_upstream_attempted", true)
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(start).Milliseconds())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(err.Error()), "")
		return writeError(http.StatusBadGateway, "upstream_error", "Upstream request failed", errors.New("input_tokens: upstream request failed"))
	}
	if resp == nil || resp.Body == nil {
		return writeError(http.StatusBadGateway, "upstream_error", "Invalid upstream response", errors.New("input_tokens: missing response body"))
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return writeError(http.StatusBadGateway, "upstream_error", "Failed to read response", errors.New("input_tokens: response read failed"))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if resp.StatusCode == http.StatusNotFound || oauthInputTokensScopeUnsupported(account, resp.StatusCode, responseBody) {
		// Endpoint/scope incompatibility must not disable an otherwise healthy
		// generation account. Ordinary authentication and quota errors stay errors.
		return estimate()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody, request.Model)
		}
		setOpsUpstreamError(c, resp.StatusCode, sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(responseBody)), "")
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return writeError(status, "upstream_error", "Upstream request failed", fmt.Errorf("input_tokens: upstream status %d", resp.StatusCode))
	}
	fields, parseErr := decodeResponsesInputTokensObject(responseBody)
	var object string
	var inputTokens *int64
	if parseErr != nil || json.Unmarshal(fields["object"], &object) != nil || object != "response.input_tokens" ||
		json.Unmarshal(fields["input_tokens"], &inputTokens) != nil || inputTokens == nil || *inputTokens < 0 {
		return writeError(http.StatusBadGateway, "upstream_error", "Invalid upstream token count response", errors.New("input_tokens: invalid count response"))
	}
	if requestID := resp.Header.Get("X-Request-Id"); requestID != "" {
		c.Header("X-Request-Id", requestID)
	}
	c.Data(http.StatusOK, "application/json", responseBody)
	return nil
}

func usesOfficialInputTokensEndpoint(account *Account) bool {
	if account.Platform != PlatformOpenAI {
		return false
	}
	if account.Type == AccountTypeOAuth {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	base := strings.TrimSpace(account.GetCredential("base_url"))
	if base == "" {
		return true
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "api.openai.com") || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch strings.TrimRight(u.Path, "/") {
	case "", "/v1", "/v1/responses":
		return true
	default:
		return false
	}
}

func oauthInputTokensScopeUnsupported(account *Account, status int, body []byte) bool {
	if account.Type != AccountTypeOAuth || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return false
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	code := strings.ToLower(envelope.Error.Code)
	message := strings.ToLower(envelope.Error.Message)
	return code == "missing_scope" || code == "insufficient_scope" ||
		(strings.Contains(message, "api.responses.write") && (strings.Contains(message, "missing") || strings.Contains(message, "insufficient")))
}

// Local tiktoken counts are estimates: provider-specific framing differs.
// Stored conversation state and media cannot be reconstructed locally. Reject
// them explicitly rather than returning a misleading one-token success.
func estimateResponsesInputTokens(req openAIInputTokensRequest) (int, error) {
	hasValue := func(raw json.RawMessage) bool {
		value := strings.TrimSpace(string(raw))
		return value != "" && value != "null" && value != `""`
	}
	if hasValue(req.Conversation) || hasValue(req.PreviousResponseID) {
		return 0, errors.New("input_tokens: unresolved conversation state")
	}
	encoding := tokenizer.O200kBase
	model := strings.ToLower(req.Model)
	if strings.HasPrefix(model, "gpt-3.5") || model == "gpt-4" || strings.HasPrefix(model, "gpt-4-") || strings.HasPrefix(model, "text-embedding-") {
		encoding = tokenizer.Cl100kBase
	}
	codec, err := tokenizer.Get(encoding)
	if err != nil {
		return 0, err
	}
	total := 0
	add := func(text string) error { n, err := codec.Count(text); total += n; return err }
	addJSON := func(raw json.RawMessage) error {
		if len(raw) == 0 || string(raw) == "null" {
			return nil
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return err
		}
		return add(compact.String())
	}
	if err := add(req.Instructions); err != nil {
		return 0, err
	}
	var inputText string
	if len(req.Input) == 0 || bytes.Equal(bytes.TrimSpace(req.Input), []byte("null")) {
		// A request may consist solely of instructions/tools.
	} else if json.Unmarshal(req.Input, &inputText) == nil {
		if err := add(inputText); err != nil {
			return 0, err
		}
	} else {
		var items []json.RawMessage
		if err := json.Unmarshal(req.Input, &items); err != nil {
			return 0, fmt.Errorf("input_tokens: invalid input: %w", err)
		}
		for _, rawItem := range items {
			item, err := decodeResponsesInputTokensObject(rawItem, "type", "role", "content", "name", "arguments", "output", "call_id", "id")
			if err != nil {
				return 0, err
			}
			readString := func(name string) string { var v string; _ = json.Unmarshal(item[name], &v); return v }
			kind := readString("type")
			if raw := item["type"]; len(raw) > 0 && (json.Unmarshal(raw, &kind) != nil || kind == "") {
				return 0, errors.New("input_tokens: invalid input item type")
			}
			switch kind {
			case "", "message", "function_call", "function_call_output":
			default:
				return 0, fmt.Errorf("input_tokens: cannot estimate input type %q", kind)
			}
			hasNonNullField := func(name string) bool {
				return len(item[name]) > 0 && !bytes.Equal(bytes.TrimSpace(item[name]), []byte("null"))
			}
			switch kind {
			case "", "message":
				role := readString("role")
				if (role != "user" && role != "assistant" && role != "system" && role != "developer") || !hasNonNullField("content") {
					return 0, errors.New("input_tokens: invalid text message")
				}
			case "function_call":
				if strings.TrimSpace(readString("name")) == "" || strings.TrimSpace(readString("call_id")) == "" || !hasNonNullField("arguments") {
					return 0, errors.New("input_tokens: invalid function call")
				}
			case "function_call_output":
				if strings.TrimSpace(readString("call_id")) == "" || !hasNonNullField("output") {
					return 0, errors.New("input_tokens: invalid function output")
				}
			}
			total += 3
			for _, name := range []string{"role", "type", "name", "arguments", "output", "call_id", "id"} {
				if raw := item[name]; len(raw) > 0 {
					var value string
					if json.Unmarshal(raw, &value) != nil {
						return 0, fmt.Errorf("input_tokens: cannot estimate %s", name)
					}
					if err := add(value); err != nil {
						return 0, err
					}
				}
			}
			content := item["content"]
			if len(content) == 0 {
				continue
			}
			var text string
			if json.Unmarshal(content, &text) == nil {
				if err := add(text); err != nil {
					return 0, err
				}
				continue
			}
			var parts []json.RawMessage
			if err := json.Unmarshal(content, &parts); err != nil {
				return 0, err
			}
			for _, rawPart := range parts {
				part, err := decodeResponsesInputTokensObject(rawPart, "type", "text")
				if err != nil {
					return 0, err
				}
				var kind string
				var text *string
				if json.Unmarshal(part["type"], &kind) != nil || json.Unmarshal(part["text"], &text) != nil || text == nil || (kind != "input_text" && kind != "output_text" && kind != "text") {
					return 0, errors.New("input_tokens: media requires native counting")
				}
				total++
				if err := add(*text); err != nil {
					return 0, err
				}
			}
		}
	}
	for _, tool := range req.Tools {
		descriptor, err := decodeResponsesInputTokensObject(tool, "type", "name")
		if err != nil {
			return 0, err
		}
		var kind, name string
		if json.Unmarshal(descriptor["type"], &kind) != nil || json.Unmarshal(descriptor["name"], &name) != nil || kind != "function" || strings.TrimSpace(name) == "" {
			return 0, errors.New("input_tokens: hosted/custom tools require native counting")
		}
		if err := addJSON(tool); err != nil {
			return 0, err
		}
	}
	if err := addJSON(req.ToolChoice); err != nil {
		return 0, err
	}
	if err := addJSON(req.Text); err != nil {
		return 0, err
	}
	return total, nil
}
