package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"golang.org/x/net/http2"
)

func newCodexModelsTestAccount() *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "test-access-token",
			"chatgpt_account_id": "acc-123",
		},
	}
}

func TestFetchCodexModelsManifestPassthrough(t *testing.T) {
	manifestBody := `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5"}]}`

	var gotAuth, gotAccountID, gotOriginator, gotVersion, gotClientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("chatgpt-account-id")
		gotOriginator = r.Header.Get("Originator")
		gotVersion = r.Header.Get("Version")
		gotClientVersion = r.URL.Query().Get("client_version")
		w.Header().Set("ETag", `W/"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	manifest, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}

	if string(manifest.Body) != manifestBody {
		t.Errorf("body not passed through verbatim: got %q", manifest.Body)
	}
	if manifest.ETag != `W/"abc123"` {
		t.Errorf("etag not passed through: got %q", manifest.ETag)
	}
	if gotAuth != "Bearer test-access-token" {
		t.Errorf("authorization header: got %q", gotAuth)
	}
	if gotAccountID != "acc-123" {
		t.Errorf("chatgpt-account-id header: got %q", gotAccountID)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Errorf("originator header: got %q", gotOriginator)
	}
	if gotVersion != codexCLIVersion {
		t.Errorf("version header: got %q, want %q", gotVersion, codexCLIVersion)
	}
	if gotClientVersion != "0.137.0" {
		t.Errorf("client_version query: got %q", gotClientVersion)
	}
}

func TestFetchCodexModelsManifestDefaultClientVersion(t *testing.T) {
	var gotClientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClientVersion = r.URL.Query().Get("client_version")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "", ""); err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if gotClientVersion != openAICodexProbeVersion {
		t.Errorf("default client_version: got %q, want %q", gotClientVersion, openAICodexProbeVersion)
	}
}

func TestFetchCodexModelsManifestUsesAPIKeyUpstream(t *testing.T) {
	const manifestBody = `{"object":"list","data":[{"id":"gpt-5.6"}]}`
	var gotPath, gotClientVersion, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotClientVersion = r.URL.Query().Get("client_version")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	}))
	defer server.Close()

	account := &Account{
		ID:       2,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":    "test-api-key",
			"base_url":   server.URL,
			"user_agent": "test-api-key-agent",
		},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	s := &OpenAIGatewayService{cfg: cfg}

	manifest, err := s.FetchCodexModelsManifest(context.Background(), account, "0.144.2", "")
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if string(manifest.Body) != manifestBody {
		t.Fatalf("manifest body: got %q, want %q", manifest.Body, manifestBody)
	}
	if gotPath != "/v1/models" {
		t.Errorf("request path: got %q, want /v1/models", gotPath)
	}
	if gotClientVersion != "0.144.2" {
		t.Errorf("client_version: got %q, want 0.144.2", gotClientVersion)
	}
	if gotAuthorization != "Bearer test-api-key" {
		t.Errorf("authorization: got %q", gotAuthorization)
	}
}

func TestFetchCodexModelsManifestAPIKeyMissing(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://example.com",
		},
	}

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), account, "0.144.2", ""); err == nil {
		t.Fatal("expected error for missing API key, got nil")
	} else if !strings.Contains(err.Error(), "OPENAI_CODEX_MODELS_API_KEY_MISSING") {
		t.Fatalf("unexpected missing API key error: %v", err)
	}
}

func TestFetchCodexModelsManifestAPIKeyInvalidUpstream(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "://bad-url",
		},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	s := &OpenAIGatewayService{cfg: cfg}

	if _, err := s.FetchCodexModelsManifest(context.Background(), account, "0.144.2", ""); err == nil {
		t.Fatal("expected error for invalid API key upstream, got nil")
	} else if !strings.Contains(err.Error(), "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID") {
		t.Fatalf("unexpected invalid upstream error: %v", err)
	}
}

func TestFetchCodexModelsManifestUnsupportedAccountType(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeSetupToken,
	}

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), account, "0.144.2", ""); err == nil {
		t.Fatal("expected error for unsupported account type, got nil")
	} else if !strings.Contains(err.Error(), "OPENAI_CODEX_MODELS_ACCOUNT_TYPE_UNSUPPORTED") {
		t.Fatalf("unexpected unsupported account error: %v", err)
	}
}

func TestFetchCodexModelsManifestUsesAPIKeyHTTPUpstream(t *testing.T) {
	const manifestBody = `{"object":"list","data":[{"id":"gpt-5.6"}]}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(manifestBody)),
	}}
	account := &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 3,
		Credentials: map[string]any{
			"api_key":    "test-api-key",
			"base_url":   "https://example.com",
			"user_agent": "test-api-key-agent",
		},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}

	manifest, err := s.FetchCodexModelsManifest(context.Background(), account, "0.144.2", "")
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if string(manifest.Body) != manifestBody {
		t.Fatalf("manifest body: got %q, want %q", manifest.Body, manifestBody)
	}
	if upstream.lastReq == nil {
		t.Fatal("expected HTTP upstream to receive a request")
	}
	if upstream.lastReq.URL.String() != "https://example.com/v1/models?client_version=0.144.2" {
		t.Errorf("request URL: got %q", upstream.lastReq.URL.String())
	}
	if got := upstream.lastReq.Header.Get("Authorization"); got != "Bearer test-api-key" {
		t.Errorf("authorization header: got %q", got)
	}
	if got := upstream.lastReq.Header.Get("User-Agent"); got != "test-api-key-agent" {
		t.Errorf("user-agent header: got %q", got)
	}
	if got := HTTPUpstreamProfileFromContext(upstream.lastReq.Context()); got != HTTPUpstreamProfileOpenAI {
		t.Errorf("upstream profile: got %q, want %q", got, HTTPUpstreamProfileOpenAI)
	}
}

func TestFetchCodexModelsManifestNotModified(t *testing.T) {
	var gotIfNoneMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `W/"abc123"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	manifest, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", `W/"abc123"`)
	if err != nil {
		t.Fatalf("FetchCodexModelsManifest returned error: %v", err)
	}
	if !manifest.NotModified {
		t.Error("expected NotModified to be true")
	}
	if gotIfNoneMatch != `W/"abc123"` {
		t.Errorf("if-none-match header: got %q", gotIfNoneMatch)
	}
}

func TestFetchCodexModelsManifestUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected error for upstream 500, got nil")
	} else if !IsRetryableCodexModelsManifestError(err) {
		t.Fatalf("expected upstream 500 to be retryable, got %v", err)
	}
}

func TestFetchCodexModelsManifestUpstreamErrorUsesStatusWhenBodyEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected error for empty upstream error body, got nil")
	} else if !IsRetryableCodexModelsManifestError(err) {
		t.Fatalf("expected upstream 429 to be retryable, got %v", err)
	}
}

func TestFetchCodexModelsManifestPermanentUpstreamErrorIsNotRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"bad request"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected error for permanent upstream status, got nil")
	} else if IsRetryableCodexModelsManifestError(err) {
		t.Fatalf("expected upstream 400 not to be retryable, got %v", err)
	}
}

func TestFetchCodexModelsManifestNilAccount(t *testing.T) {
	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), nil, "0.137.0", ""); err == nil {
		t.Fatal("expected error for nil account, got nil")
	}
}

func TestFetchCodexModelsManifestInvalidEndpoint(t *testing.T) {
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = "://bad-url"
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected error for invalid Codex models endpoint, got nil")
	}
}

func TestFetchCodexModelsManifestRequestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("closed server should not receive a request")
	}))
	requestURL := server.URL
	server.Close()

	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = requestURL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", ""); err == nil {
		t.Fatal("expected request failure, got nil")
	}
}

func TestFetchCodexModelsManifestMissingToken(t *testing.T) {
	account := newCodexModelsTestAccount()
	delete(account.Credentials, "access_token")

	s := &OpenAIGatewayService{}
	if _, err := s.FetchCodexModelsManifest(context.Background(), account, "0.137.0", ""); err == nil {
		t.Fatal("expected error for missing access token, got nil")
	}
}

func TestIsRetryableCodexModelsManifestTransportError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "nil", err: nil},
		{name: "configuration error", err: errors.New("invalid proxy URL")},
		{name: "upstream configuration error", err: errors.New("upstream error: invalid proxy")},
		{name: "redirect policy error", err: &url.Error{Op: "Get", URL: "https://upstream.example/v1/models", Err: errors.New("stopped after 10 redirects")}},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, retryable: true},
		{name: "eof", err: io.EOF, retryable: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, retryable: true},
		{name: "closed connection", err: net.ErrClosed, retryable: true},
		{name: "network operation", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}, retryable: true},
		{name: "dns", err: &net.DNSError{Err: "temporary failure", Name: "upstream.example"}, retryable: true},
		{name: "typed goaway", err: http2.GoAwayError{ErrCode: http2.ErrCodeNo}, retryable: true},
		{name: "typed stream error", err: http2.StreamError{Code: http2.ErrCodeRefusedStream}, retryable: true},
		{name: "typed connection error", err: http2.ConnectionError(http2.ErrCodeProtocol), retryable: true},
		{name: "stdlib goaway", err: errors.New("http2: server sent GOAWAY and closed the connection"), retryable: true},
		{name: "stdlib refused stream", err: errors.New("stream error: stream ID 3; REFUSED_STREAM"), retryable: true},
		{name: "stdlib frame too large", err: errors.New("http2: frame too large"), retryable: true},
		{name: "stdlib connection error", err: errors.New("connection error: PROTOCOL_ERROR"), retryable: true},
		{name: "timeout error", err: codexModelsTimeoutError{}, retryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableCodexModelsManifestTransportError(tt.err); got != tt.retryable {
				t.Fatalf("retryable: got %v, want %v", got, tt.retryable)
			}
		})
	}
}

func TestIsRetryableCodexModelsManifestHTTP2ConnectionCodes(t *testing.T) {
	codes := []http2.ErrCode{
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
	}
	for _, code := range codes {
		t.Run(code.String(), func(t *testing.T) {
			err := errors.New("connection error: " + strings.ToLower(code.String()))
			if !isRetryableCodexModelsManifestTransportError(err) {
				t.Fatalf("expected %s to be retryable", code)
			}
		})
	}
}

type codexModelsTimeoutError struct{}

func (codexModelsTimeoutError) Error() string   { return "request timed out" }
func (codexModelsTimeoutError) Timeout() bool   { return true }
func (codexModelsTimeoutError) Temporary() bool { return true }

func TestIsRetryableCodexModelsManifestErrorUsesWrapper(t *testing.T) {
	if !IsRetryableCodexModelsManifestError(&codexModelsManifestUpstreamError{err: errors.New("temporary"), retryable: true}) {
		t.Fatal("expected retryable wrapped error")
	}
	if IsRetryableCodexModelsManifestError(errors.New("temporary")) {
		t.Fatal("unwrapped error must not be retryable")
	}
}
