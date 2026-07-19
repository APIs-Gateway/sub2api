package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type blockedPromptEngine struct{}

func (blockedPromptEngine) EffectiveMode() securityaudit.Mode                    { return securityaudit.ModeBlocking }
func (blockedPromptEngine) Enqueue(context.Context, securityaudit.Request) error { return nil }
func (blockedPromptEngine) Evaluate(context.Context, securityaudit.Request) (*securityaudit.PromptDecision, error) {
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionBlock, ErrorCode: securityaudit.ErrorCodeBlocked}, nil
}

func newBlockedOpenAIHandler() *OpenAIGatewayHandler {
	return &OpenAIGatewayHandler{
		securityAuditCoordinator: securityaudit.NewCoordinator(nil, blockedPromptEngine{}),
		gatewayService:           &service.OpenAIGatewayService{},
		billingCacheService:      &service.BillingCacheService{},
		apiKeyService:            &service.APIKeyService{},
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(nil), SSEPingFormatComment, 0),
	}
}

func newBlockedGatewayHandler() *GatewayHandler {
	return &GatewayHandler{
		gatewayService:           &service.GatewayService{},
		openAIGatewayService:     &service.OpenAIGatewayService{},
		billingCacheService:      &service.BillingCacheService{},
		apiKeyService:            &service.APIKeyService{},
		securityAuditCoordinator: securityaudit.NewCoordinator(nil, blockedPromptEngine{}),
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(nil), SSEPingFormatClaude, 0),
	}
}

func newPromptAuditBlockedContext(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	groupID := int64(3)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 9, UserID: 7, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowMessagesDispatch: true},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7, Concurrency: 1})
	return c, recorder
}

func newPromptAuditBlockedGatewayContext(path, body, platform string) (*gin.Context, *httptest.ResponseRecorder) {
	c, recorder := newPromptAuditBlockedContext(path, body)
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	apiKey.Group.Platform = platform
	return c, recorder
}

func TestPromptAuditBlockedOpenAIRoutesStopBeforeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		path   string
		body   string
		invoke func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{name: "messages", path: "/v1/messages", body: `{"model":"gpt-test","messages":[{"role":"user","content":"blocked"}]}`, invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Messages(c) }},
		{name: "responses", path: "/v1/responses", body: `{"model":"gpt-test","input":"blocked"}`, invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Responses(c) }},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"gpt-test","messages":[{"role":"user","content":"blocked"}]}`, invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.ChatCompletions(c) }},
		{name: "embeddings", path: "/v1/embeddings", body: `{"model":"text-embedding","input":"blocked"}`, invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Embeddings(c) }},
		{name: "alpha-search", path: "/v1/alpha/search", body: `{"model":"gpt-test","input":"blocked"}`, invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.AlphaSearch(c) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBlockedOpenAIHandler()
			c, recorder := newPromptAuditBlockedContext(tt.path, tt.body)
			tt.invoke(h, c)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Body.String(), securityaudit.ErrorCodeBlocked)
		})
	}
}

func TestPromptAuditBlockedGatewayRoutesStopBeforeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		path   string
		body   string
		invoke func(*GatewayHandler, *gin.Context)
	}{
		{name: "messages", path: "/v1/messages", body: `{"model":"claude-test","messages":[{"role":"user","content":"blocked"}]}`, invoke: func(h *GatewayHandler, c *gin.Context) { h.Messages(c) }},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"claude-test","messages":[{"role":"user","content":"blocked"}]}`, invoke: func(h *GatewayHandler, c *gin.Context) { h.ChatCompletions(c) }},
		{name: "responses", path: "/v1/responses", body: `{"model":"claude-test","input":"blocked"}`, invoke: func(h *GatewayHandler, c *gin.Context) { h.Responses(c) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBlockedGatewayHandler()
			c, recorder := newPromptAuditBlockedGatewayContext(tt.path, tt.body, service.PlatformAnthropic)
			tt.invoke(h, c)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Body.String(), securityaudit.ErrorCodeBlocked)
		})
	}
}

func TestPromptAuditBlockedGeminiRouteStopsBeforeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newBlockedGatewayHandler()
	c, recorder := newPromptAuditBlockedGatewayContext("/v1beta/models/gemini-test:generateContent", `{"contents":[{"role":"user","parts":[{"text":"blocked"}]}]}`, service.PlatformGemini)
	c.Params = gin.Params{{Key: "modelAction", Value: "models/gemini-test:generateContent"}}
	h.GeminiV1BetaModels(c)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), securityaudit.ErrorCodeBlocked)
}

func TestPromptAuditProviderWrappersInjectCoordinator(t *testing.T) {
	coordinator := securityaudit.NewCoordinator(nil, nil)
	gateway := ProvideGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil, coordinator)
	openAI := ProvideOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, &config.Config{}, coordinator)
	require.Same(t, coordinator, gateway.securityAuditCoordinator)
	require.Same(t, coordinator, openAI.securityAuditCoordinator)
}

func TestBuildSecurityAuditRequestUsesUserMetadataAndDefaultsStage(t *testing.T) {
	c, _ := newPromptAuditBlockedContext("/v1/chat/completions", `{"model":"gpt-test"}`)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	require.True(t, ok)
	apiKey.User = &service.User{Username: "audit-user", Email: "audit@example.com"}
	request := buildSecurityAuditRequest(c, apiKey, middleware2.AuthSubject{UserID: 7}, "openai_chat_completions", "gpt-test", []byte("body"), "")
	require.Equal(t, "http", request.Stage)
	require.Equal(t, "gpt-test", request.Model)
	require.Equal(t, int64(7), request.UserID)
	require.Equal(t, int64(3), *request.GroupID)
	require.Equal(t, "audit-user", request.Username)
	require.Equal(t, "audit@example.com", request.UserEmail)
	require.Nil(t, cloneSecurityAuditGroupID(nil))
}

func TestRunSecurityAuditWithNoCoordinatorKeepsLegacyPathNilSafe(t *testing.T) {
	c, _ := newPromptAuditBlockedContext("/v1/chat/completions", `{"model":"gpt-test"}`)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	require.True(t, ok)
	decision := runSecurityAudit(c, nil, nil, nil, apiKey, middleware2.AuthSubject{UserID: 7}, "openai_chat_completions", "gpt-test", []byte(`{"model":"gpt-test"}`), "http")
	require.Nil(t, decision)
}

func TestPromptAuditNilReceiverAndLegacyCompatibilityBranches(t *testing.T) {
	var gateway *GatewayHandler
	var openAI *OpenAIGatewayHandler
	require.Nil(t, gateway.checkSecurityAudit(nil, nil, nil, middleware2.AuthSubject{}, "", "", nil))
	require.Nil(t, openAI.checkSecurityAudit(nil, nil, nil, middleware2.AuthSubject{}, "", "", nil))
	require.Nil(t, openAI.checkSecurityAuditStage(nil, nil, nil, middleware2.AuthSubject{}, "", "", nil, "subsequent_turn"))
	require.Nil(t, runSecurityAudit(nil, nil, nil, nil, nil, middleware2.AuthSubject{}, "", "", nil, "http"))

	legacyDecision := &securityaudit.Decision{
		Kind: securityaudit.DecisionBlock, HTTPStatus: http.StatusForbidden,
		ErrorCode: "content_policy_violation", ClientMessage: "legacy blocked",
		Legacy: &securityaudit.LegacyDecision{Blocked: true, Message: "legacy blocked", StatusCode: http.StatusForbidden},
	}
	for _, name := range []string{"openai", "responses", "gateway_anthropic", "openai_anthropic"} {
		t.Run(name, func(t *testing.T) {
			c, recorder := securityAuditErrorTestContext(t)
			switch name {
			case "openai":
				(&OpenAIGatewayHandler{}).openAISecurityAuditError(c, legacyDecision)
			case "responses":
				(&GatewayHandler{}).responsesSecurityAuditError(c, legacyDecision)
			case "gateway_anthropic":
				(&GatewayHandler{}).anthropicSecurityAuditError(c, legacyDecision)
			case "openai_anthropic":
				(&OpenAIGatewayHandler{}).anthropicSecurityAuditError(c, legacyDecision)
			}
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Body.String(), "legacy blocked")
		})
	}

	writeSecurityAuditWSError(context.TODO(), nil, nil)
	require.Equal(t, "Request blocked by content policy", securityAuditMessage(&securityaudit.Decision{}))
	require.Equal(t, int64(coderws.StatusInternalError), int64(securityAuditWSCloseStatus(nil)))
	require.Equal(t, securityaudit.ErrorCodeUnavailable, securityAuditWSCloseReason(nil))
	require.Equal(t, "content_policy_violation", securityAuditWSCloseReason(&securityaudit.Decision{}))
}

func TestWriteSecurityAuditWSErrorWritesPromptGuardEnvelope(t *testing.T) {
	decision := promptGuardDecision(securityaudit.DecisionBlock)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		writeSecurityAuditWSError(context.TODO(), conn, decision)
	}))
	defer wsServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientConn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	messageType, payload, err := clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.Contains(t, string(payload), securityaudit.ErrorCodeBlocked)
}
