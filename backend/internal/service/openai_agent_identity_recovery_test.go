//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAgentIdentityTaskInvalidResponseRequiresTarget401(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{name: "invalid task code", statusCode: http.StatusUnauthorized, body: `{"error":{"code":"invalid_task_id"}}`, want: true},
		{name: "task expired text", statusCode: http.StatusUnauthorized, body: `task expired`, want: true},
		{name: "other unauthorized", statusCode: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`},
		{name: "invalid task wrong status", statusCode: http.StatusForbidden, body: `{"error":{"code":"invalid_task_id"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isAgentIdentityTaskInvalidHTTPResponse(tt.statusCode, []byte(tt.body)))
		})
	}
}

func TestAgentIdentitySensitiveBodyRedaction(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-secret"
	account.Credentials["agent_runtime_id"] = "runtime-secret"
	account.Credentials["access_token"] = "access-secret"
	body := []byte(`{"message":"task-secret runtime-secret access-secret AgentAssertion assertion-secret"}`)

	redacted := string(redactAgentIdentitySensitiveBodyForAccount(account, body))
	require.NotContains(t, redacted, "task-secret")
	require.NotContains(t, redacted, "runtime-secret")
	require.NotContains(t, redacted, "access-secret")
	require.NotContains(t, redacted, "assertion-secret")
	require.Contains(t, redacted, "[redacted]")
}

func TestOpenAIPassthroughRecoversAgentIdentityTaskOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Name = "agent"
	account.Concurrency = 1
	account.Status = StatusActive
	account.Schedulable = true
	account.Credentials["task_id"] = "task-old"
	account.Credentials["chatgpt_account_id"] = "chatgpt-test"
	account.Extra = map[string]any{
		"openai_passthrough":                        true,
		"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
	}
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	registrationCalls := 0
	registrationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registrationCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-new"}`)
	}))
	defer registrationServer.Close()
	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registrationServer.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL }()

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp-recovered","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}}
	svc := &OpenAIGatewayService{
		accountRepo:  repo,
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":[{"type":"text","text":"hello"}]}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp-recovered", result.RequestID)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "task-old", agentTaskIDFromAuthorization(t, upstream.requests[0].Header.Get("Authorization")))
	require.Equal(t, "task-new", agentTaskIDFromAuthorization(t, upstream.requests[1].Header.Get("Authorization")))
	require.Equal(t, 1, registrationCalls)
}

func TestOpenAIPassthroughDoesNotRetryAgentRecoveryAfterSecondInvalidTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-old"
	account.Extra = map[string]any{
		"openai_passthrough":                        true,
		"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
	}
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	registrationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"task-new"}`)
	}))
	defer registrationServer.Close()
	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registrationServer.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL }()

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id","message":"task-new AgentAssertion leaked"}}`))},
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":[{"type":"text","text":"hello"}]}`)

	_, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Len(t, upstream.requests, 2)
	require.NotContains(t, rec.Body.String(), "task-new")
	require.NotContains(t, rec.Body.String(), "AgentAssertion leaked")
}

func TestOpenAIPassthroughDoesNotRecoverOrdinaryOAuth401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          43,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"openai_passthrough":                        true,
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":[{"type":"text","text":"hello"}]}`)

	_, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "Bearer oauth-token", upstream.requests[0].Header.Get("Authorization"))
}

func TestOpenAIWSDialErrorCarriesOnlyBoundedHandshakeBody(t *testing.T) {
	invalid := &openAIWSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"code":"invalid_task_id"}}`),
	}
	require.True(t, isAgentIdentityTaskInvalidWSDialError(invalid))
	invalid.ResponseBody = []byte(`{"error":{"code":"invalid_api_key"}}`)
	require.False(t, isAgentIdentityTaskInvalidWSDialError(invalid))
}

func TestOpenAIWSConnPoolClearAccountInvalidatesOldConnections(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	accountID := int64(42)
	ap := pool.getOrCreateAccountPool(accountID)
	conn := newOpenAIWSConn("old", accountID, &openAIWSFakeConn{}, nil)
	ap.conns[conn.id] = conn
	oldGeneration := ap.generation

	pool.ClearAccount(accountID)

	ap.mu.Lock()
	defer ap.mu.Unlock()
	require.Equal(t, oldGeneration+1, ap.generation)
	require.Empty(t, ap.conns)
	require.Empty(t, ap.pinnedConns)
	require.Nil(t, ap.lastAcquire)
}

func TestOpenAIWSForwardRecoversAgentIdentityHandshakeOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-old"
	account.Credentials["base_url"] = "https://api.example.test"
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	registrationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"task-new"}`)
	}))
	defer registrationServer.Close()
	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registrationServer.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL }()

	created := []byte(`{"type":"response.created","response":{"id":"resp-agent-recovered","model":"gpt-5.4"}}`)
	completed := []byte(`{"type":"response.completed","response":{"id":"resp-agent-recovered","model":"gpt-5.4","usage":{"input_tokens":2,"output_tokens":1}}}`)
	conn := &openAIWSCaptureConn{events: [][]byte{created, completed}}
	dialer := &agentIdentityRecoveryDialer{conn: conn}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	defer pool.Close()
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp-agent-recovered", result.RequestID)
	require.Equal(t, 2, dialer.DialCount())
	require.Equal(t, "task-old", agentTaskIDFromAuthorization(t, dialer.Authorization(0)))
	require.Equal(t, "task-new", agentTaskIDFromAuthorization(t, dialer.Authorization(1)))
}

type agentIdentityRecoveryDialer struct {
	mu            sync.Mutex
	conn          openAIWSClientConn
	authorization []string
	dialCount     int
}

func (d *agentIdentityRecoveryDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = proxyURL
	d.mu.Lock()
	d.authorization = append(d.authorization, headers.Get("Authorization"))
	d.dialCount++
	call := d.dialCount
	d.mu.Unlock()
	if call == 1 {
		return nil, http.StatusUnauthorized, nil, &openAIWSHandshakeError{
			Body: []byte(`{"error":{"code":"invalid_task_id"}}`),
			Err:  io.ErrUnexpectedEOF,
		}
	}
	return d.conn, http.StatusSwitchingProtocols, nil, nil
}

func (d *agentIdentityRecoveryDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (d *agentIdentityRecoveryDialer) Authorization(index int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if index < 0 || index >= len(d.authorization) {
		return ""
	}
	return d.authorization[index]
}

func agentTaskIDFromAuthorization(t *testing.T, authorization string) string {
	t.Helper()
	const prefix = "AgentAssertion "
	require.True(t, strings.HasPrefix(authorization, prefix))
	encoded := strings.TrimPrefix(authorization, prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var payload struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal(decoded, &payload))
	return payload.TaskID
}
