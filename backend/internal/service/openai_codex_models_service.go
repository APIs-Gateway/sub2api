package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/common"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"golang.org/x/net/http2"
)

// chatgptCodexModelsURL is the ChatGPT Codex models manifest endpoint.
// Package-level variable so tests can point it at a stub server.
var chatgptCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

const codexModelsManifestBodyLimit int64 = 8 << 20

// CodexModelsManifest carries the raw upstream manifest payload plus caching
// metadata so handlers can pass both through to the client untouched.
type CodexModelsManifest struct {
	Body        []byte
	ETag        string
	NotModified bool
}

type codexModelsManifestUpstreamError struct {
	err        error
	retryable  bool
	statusCode int
	headers    http.Header
	body       []byte
}

func (e *codexModelsManifestUpstreamError) Error() string { return e.err.Error() }

func (e *codexModelsManifestUpstreamError) Unwrap() error { return e.err }

// IsRetryableCodexModelsManifestError reports whether another selected account
// may succeed without changing the request. Configuration and upstream 4xx
// responses, except 429 and ChatGPT-backend 401, are intentionally not
// retried. A manifest 401 from the ChatGPT Codex backend reflects the
// selected OAuth account's upstream token rather than the client request, so
// a different account may still serve the manifest. Custom API key upstreams
// keep the old no-failover 401 behavior because their /models auth semantics
// are not authoritative for the account.
func IsRetryableCodexModelsManifestError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.retryable
	}
	// Keep the classification stable for callers that preserve the canonical
	// application error but do not retain the private wrapper type.
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) {
		if appErr.Reason == "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST" {
			return true
		}
		if appErr.Reason == "OPENAI_CODEX_MODELS_UPSTREAM_FAILED" {
			code := int(appErr.Code)
			return code == http.StatusTooManyRequests || (code >= http.StatusInternalServerError && code < 600)
		}
	}
	return false
}

func isRetryableCodexModelsManifestTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var goAwayErr http2.GoAwayError
	if errors.As(err, &goAwayErr) {
		return true
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return true
	}
	var connectionErr http2.ConnectionError
	if errors.As(err, &connectionErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// net/http uses unexported HTTP/2 error types, so typed matching is not
	// possible for errors produced by the standard library transport.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "http2:") &&
		(strings.Contains(message, "goaway") ||
			strings.Contains(message, "refused_stream") ||
			strings.Contains(message, "frame too large")) {
		return true
	}
	if strings.Contains(message, "stream error: stream id ") {
		return true
	}
	for _, code := range []http2.ErrCode{
		http2.ErrCodeNo,
		http2.ErrCodeProtocol,
		http2.ErrCodeInternal,
		http2.ErrCodeFlowControl,
		http2.ErrCodeSettingsTimeout,
		http2.ErrCodeStreamClosed,
		http2.ErrCodeFrameSize,
		http2.ErrCodeRefusedStream,
		http2.ErrCodeCancel,
		http2.ErrCodeCompression,
		http2.ErrCodeConnect,
		http2.ErrCodeEnhanceYourCalm,
		http2.ErrCodeInadequateSecurity,
		http2.ErrCodeHTTP11Required,
	} {
		if strings.Contains(message, "connection error: "+strings.ToLower(code.String())) {
			return true
		}
	}
	return false
}

// FetchCodexModelsManifest fetches the live Codex models manifest from the
// ChatGPT backend for OAuth accounts or from the configured OpenAI-compatible
// upstream for API-key accounts.
//
// After validating the stable top-level envelope, the response body is passed
// through verbatim: the manifest schema evolves with Codex client releases,
// and interpreting model entries here would force the gateway to chase
// upstream changes. Passing it through keeps the gateway schema-agnostic and
// always reflects the account's real entitlements.
func (s *OpenAIGatewayService) FetchCodexModelsManifest(ctx context.Context, account *Account, clientVersion, ifNoneMatch string) (*CodexModelsManifest, error) {
	if account == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_ACCOUNT_REQUIRED", "account is required")
	}

	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = openAICodexProbeVersion
	}
	manifest, fetchErr := s.fetchCodexModelsManifestUpstream(ctx, account, clientVersion, ifNoneMatch)
	if !account.IsOpenAIAgentIdentity() || !isAgentIdentityTaskInvalidCodexModelsError(fetchErr) {
		s.handleCodexModelsManifestAccountAuthError(ctx, account, account, fetchErr)
		return manifest, fetchErr
	}
	expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
	if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_AUTH_FAILED", "agent identity task recovery failed: %v", recoverErr)
	}
	return s.fetchCodexModelsManifestUpstream(ctx, account, clientVersion, ifNoneMatch)
}

func isAgentIdentityTaskInvalidCodexModelsError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	return errors.As(err, &upstreamErr) &&
		isAgentIdentityTaskInvalidHTTPResponse(upstreamErr.statusCode, upstreamErr.body)
}

// handleCodexModelsManifestAccountAuthError feeds manifest 401s from the
// ChatGPT Codex backend into the shared upstream-error state machinery.
// Plain OAuth accounts use the same token for the manifest and chat requests,
// while Agent Identity accounts have task-scoped 401 recovery and API-key
// manifests come from custom upstreams with independent auth semantics.
func (s *OpenAIGatewayService) handleCodexModelsManifestAccountAuthError(ctx context.Context, account, credAccount *Account, err error) {
	if s == nil || account == nil || err == nil {
		return
	}
	if credAccount == nil || !credAccount.IsOpenAIOAuth() || credAccount.IsOpenAIAgentIdentity() {
		return
	}
	var upstreamErr *codexModelsManifestUpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.statusCode != http.StatusUnauthorized {
		return
	}
	headers := upstreamErr.headers
	if headers == nil {
		headers = http.Header{}
	}
	s.handleOpenAIAccountUpstreamError(ctx, account, upstreamErr.statusCode, headers, upstreamErr.body)
}

func (s *OpenAIGatewayService) fetchCodexModelsManifestUpstream(ctx context.Context, account *Account, clientVersion, ifNoneMatch string) (*CodexModelsManifest, error) {
	requestURL := chatgptCodexModelsURL
	authToken := ""
	apiKeyUpstream := false
	switch {
	case account.IsOpenAIAgentIdentity():
		requestURL += "?client_version=" + url.QueryEscape(clientVersion)
	case account.IsOpenAIOAuth():
		authToken = account.GetOpenAIAccessToken()
		if authToken == "" {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_MISSING", "account has no Codex backend access token")
		}
		requestURL += "?client_version=" + url.QueryEscape(clientVersion)
	case account.IsOpenAIApiKey():
		authToken = account.GetOpenAIApiKey()
		if authToken == "" {
			return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_MISSING", "account has no API key for the Codex models upstream")
		}
		validatedURL, err := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID", "invalid Codex models upstream base URL: %v", err)
		}
		requestURL = buildOpenAIModelsURL(validatedURL)
		parsedURL, err := url.Parse(requestURL)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID", "invalid Codex models upstream URL: %v", err)
		}
		query := parsedURL.Query()
		query.Set("client_version", clientVersion)
		parsedURL.RawQuery = query.Encode()
		requestURL = parsedURL.String()
		apiKeyUpstream = true
	default:
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_ACCOUNT_TYPE_UNSUPPORTED", "account type %q cannot fetch the Codex models manifest", account.Type)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "create codex models request: %v", err)
	}
	if account.IsOpenAIAgentIdentity() {
		authHeaders, authErr := BuildOpenAIAgentIdentityAuthenticationHeaders(reqCtx, s.accountRepo, account)
		if authErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_AUTH_FAILED", "build Codex models authentication: %v", authErr)
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Version", clientVersion)
	req.Header.Set("User-Agent", codexCLIUserAgent)
	enforceCodexIdentityHeaders(req.Header)
	if ifNoneMatch = strings.TrimSpace(ifNoneMatch); ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
		req.Header.Set("chatgpt-account-id", chatgptAccountID)
	}
	if apiKeyUpstream {
		if userAgent := account.GetOpenAIUserAgent(); userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	}
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	var client *http.Client
	if !apiKeyUpstream || s.httpUpstream == nil {
		client, err = httpclient.GetClient(httpclient.Options{
			ProxyURL:              proxyURL,
			Timeout:               15 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		})
		if err != nil {
			return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_PROXY_INVALID", "invalid proxy configuration: %v", err)
		}
	}

	var resp *http.Response
	if apiKeyUpstream && s.httpUpstream != nil {
		resp, err = s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	} else {
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "codex models manifest request failed: %v", err),
			retryable: isRetryableCodexModelsManifestTransportError(err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return &CodexModelsManifest{ETag: resp.Header.Get("ETag"), NotModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		body = s.redactAgentIdentitySensitiveBody(reqCtx, account, body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, &codexModelsManifestUpstreamError{
			err:        infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "codex models manifest upstream error %d: %s", resp.StatusCode, message),
			statusCode: resp.StatusCode,
			body:       body,
			headers:    resp.Header.Clone(),
			retryable: (resp.StatusCode == http.StatusUnauthorized && !apiKeyUpstream) ||
				resp.StatusCode == http.StatusTooManyRequests ||
				(resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode < 600),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, codexModelsManifestBodyLimit))
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "read codex models manifest response: %v", err),
			retryable: isRetryableCodexModelsManifestTransportError(err),
		}
	}
	if apiKeyUpstream {
		body = convertOpenAIModelListToCodexManifest(body)
	}
	if err := validateCodexModelsManifestEnvelope(body); err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err: infraerrors.Newf(
				http.StatusBadGateway,
				"OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST",
				"codex models manifest upstream returned an invalid envelope: %v",
				err,
			),
			retryable: true,
		}
	}
	return &CodexModelsManifest{Body: body, ETag: resp.Header.Get("ETag")}, nil
}

// convertOpenAIModelListToCodexManifest rewrites a standard OpenAI
// GET /v1/models response ({"object":"list","data":[{"id":...},...]}) into the
// Codex manifest envelope ({"models":[{"slug":...},...]}) so custom API key
// upstreams that only implement the standard endpoint can serve Codex model
// discovery. Bodies that already carry a top-level models field, are not the
// standard list shape, or yield no usable model IDs are returned unchanged so
// envelope validation reports the original payload.
func convertOpenAIModelListToCodexManifest(body []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return body
	}
	if _, ok := envelope["models"]; ok {
		return body
	}
	data, ok := envelope["data"]
	if !ok {
		return body
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return body
	}
	type codexModelEntry struct {
		Slug string `json:"slug"`
	}
	models := make([]codexModelEntry, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, codexModelEntry{Slug: id})
	}
	if len(models) == 0 {
		return body
	}
	converted, err := json.Marshal(map[string][]codexModelEntry{"models": models})
	if err != nil {
		return body
	}
	return converted
}

func validateCodexModelsManifestEnvelope(body []byte) error {
	var envelope map[string]json.RawMessage
	if err := common.Unmarshal(body, &envelope); err != nil {
		return errors.New("decode JSON object: " + err.Error())
	}
	if envelope == nil {
		return errors.New("expected a JSON object")
	}

	models, ok := envelope["models"]
	if !ok {
		return errors.New("missing top-level models array")
	}
	models = bytes.TrimSpace(models)
	if len(models) == 0 || models[0] != '[' {
		return errors.New("top-level models field is not an array")
	}
	var entries []json.RawMessage
	if err := common.Unmarshal(models, &entries); err != nil {
		return errors.New("decode top-level models array: " + err.Error())
	}
	return nil
}
