package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func readOpenAIWSRejectionStatus(t *testing.T, conn *coderws.Conn, status int, errType, message string) coderws.CloseError {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	messageType, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
	require.Equal(t, int64(status), gjson.GetBytes(payload, "status").Int(), string(payload))
	require.Equal(t, errType, gjson.GetBytes(payload, "error.type").String())
	require.Contains(t, gjson.GetBytes(payload, "error.message").String(), message)
	_, _, err = conn.Read(ctx)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	return closeErr
}

func TestOpenAIWSBillingRejectionUsesForkHTTPClassification(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		errType string
	}{
		{name: "forbidden", err: errors.New("billing denied"), status: http.StatusForbidden, errType: "billing_error"},
		{name: "rate limited", err: service.ErrAPIKeyRateLimit5hExceeded, status: http.StatusTooManyRequests, errType: "rate_limit_exceeded"},
		{name: "billing unavailable", err: service.ErrBillingServiceUnavailable, status: http.StatusServiceUnavailable, errType: "billing_service_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept websocket: %v", err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				writeOpenAIWSBillingRejection(r.Context(), conn, tc.err)
				closeOpenAIClientWS(conn, coderws.StatusPolicyViolation, "billing check failed")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			closeErr := readOpenAIWSRejectionStatus(t, conn, tc.status, tc.errType, "")
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
			require.Equal(t, "billing check failed", closeErr.Reason)
		})
	}
}

func TestOpenAIResponsesWebSocket_InvalidFirstFrameHasHTTPStatus(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		message     string
		closeReason string
	}{
		{name: "invalid JSON", payload: `{`, message: "Failed to parse request body", closeReason: "invalid JSON payload"},
		{name: "missing model", payload: `{"type":"response.create"}`, message: "model is required", closeReason: "model is required in first response.create payload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
			server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(tc.payload)))
			closeErr := readOpenAIWSRejectionStatus(t, conn, http.StatusBadRequest, "invalid_request_error", tc.message)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
			require.Equal(t, tc.closeReason, closeErr.Reason)
		})
	}
}

func TestOpenAIResponsesWebSocket_FirstUserSlotRejectionHasHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		acquireErr error
		acquired   bool
		status     int
		errType    string
		message    string
		closeCode  coderws.StatusCode
	}{
		{name: "limited", acquired: false, status: http.StatusTooManyRequests, errType: "rate_limit_error", message: "Concurrency limit exceeded for user", closeCode: coderws.StatusTryAgainLater},
		{name: "cache failure", acquireErr: errors.New("redis unavailable"), status: http.StatusServiceUnavailable, errType: "api_error", message: "Service temporarily unavailable", closeCode: coderws.StatusInternalError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
				return tc.acquired, tc.acquireErr
			}}
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
			server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5"}`)))
			closeErr := readOpenAIWSRejectionStatus(t, conn, tc.status, tc.errType, tc.message)
			require.Equal(t, tc.closeCode, closeErr.Code)
		})
	}
}

func TestOpenAIResponsesWebSocket_ImageGenerationDisabledHasHTTPStatus(t *testing.T) {
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.cfg = &config.Config{}
	h.cfg.Gateway.DisableOpenAIResponsesImageGeneration = true
	server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","tools":[{"type":"image_generation"}]}`)))
	closeErr := readOpenAIWSRejectionStatus(t, conn, http.StatusBadRequest, "invalid_request_error", service.OpenAIResponsesImageGenerationDisabledMessage())
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, service.OpenAIResponsesImageGenerationDisabledMessage(), closeErr.Reason)
}

func TestOpenAIResponsesWebSocket_GroupImagePermissionHasHTTPStatus(t *testing.T) {
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	groupID := int64(2)
	apiKey := &service.APIKey{ID: 101, GroupID: &groupID, Group: &service.Group{AllowImageGeneration: false}, User: &service.User{ID: 1}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","tools":[{"type":"image_generation"}]}`)))
	closeErr := readOpenAIWSRejectionStatus(t, conn, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, service.ImageGenerationPermissionMessage(), closeErr.Reason)
}

func TestOpenAIResponsesWebSocket_BillingEligibilityHasHTTPStatus(t *testing.T) {
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.billingCacheService = service.NewBillingCacheService(nil, &whamUsageUserRepoStub{user: &service.User{ID: 1, Balance: 0}}, nil, nil, nil, nil, &config.Config{}, nil, nil)
	t.Cleanup(h.billingCacheService.Stop)
	server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5"}`)))
	closeErr := readOpenAIWSRejectionStatus(t, conn, http.StatusForbidden, "billing_error", "insufficient balance")
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, "billing check failed", closeErr.Reason)
}

func TestOpenAIResponsesWebSocket_FirstAccountSlotFullHasHTTPStatus(t *testing.T) {
	reports := make(chan bool, 1)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return false, nil },
	}
	h := newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, nil, reports, true)
	server := newOpenAIResponsesWebSocketAttributionServer(t, h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4"}`)))
	closeErr := readOpenAIWSRejectionStatus(t, conn, http.StatusTooManyRequests, "rate_limit_error", "Concurrency limit exceeded for account")
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "account is busy, please retry later", closeErr.Reason)
	select {
	case <-reports:
		t.Fatal("local account capacity rejection must not lower scheduler health")
	default:
	}
}

func TestOpenAIWSLocalPolicyEventsIncludeHTTPStatus(t *testing.T) {
	tests := []struct {
		name    string
		write   func(context.Context, *coderws.Conn)
		status  int
		errType string
	}{
		{name: "content moderation", write: func(ctx context.Context, conn *coderws.Conn) {
			writeContentModerationWSError(ctx, conn, &service.ContentModerationDecision{StatusCode: http.StatusUnprocessableEntity, Message: "moderation blocked"})
		}, status: http.StatusUnprocessableEntity, errType: "invalid_request_error"},
		{name: "cyber session", write: writeCyberSessionBlockedWSError, status: http.StatusForbidden, errType: "permission_error"},
		{name: "Codex client restriction", write: func(ctx context.Context, conn *coderws.Conn) {
			writeCodexClientRestrictedWSError(ctx, conn, service.CodexOfficialClientsOnlyMessage)
		}, status: http.StatusForbidden, errType: "forbidden_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept websocket: %v", err)
					return
				}
				defer func() { _ = conn.CloseNow() }()
				tc.write(r.Context(), conn)
				closeOpenAIClientWS(conn, coderws.StatusPolicyViolation, "rejected")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			closeErr := readOpenAIWSRejectionStatus(t, conn, tc.status, tc.errType, "")
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
		})
	}
}
