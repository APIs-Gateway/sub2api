//go:build unit

package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type alphaSearchHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	responses  []*http.Response
}

type alphaSearchReadErrorBody struct{}

func (alphaSearchReadErrorBody) Read([]byte) (int, error) {
	return 0, errors.New("request body read failed")
}

func (alphaSearchReadErrorBody) Close() error { return nil }

func (u *alphaSearchHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.accountIDs = append(u.accountIDs, accountID)
	if len(u.responses) == 0 {
		return nil, io.EOF
	}
	resp := u.responses[0]
	u.responses = u.responses[1:]
	return resp, nil
}

func (u *alphaSearchHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func newAlphaSearchContext(body string, apiKey *service.APIKey, subject *middleware2.AuthSubject) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search?feature=standalone", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if apiKey != nil {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	}
	if subject != nil {
		c.Set(string(middleware2.ContextKeyUser), *subject)
	}
	return c, recorder
}

func newReadyAlphaSearchHandler(t *testing.T) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)
	return NewOpenAIGatewayHandler(
		&service.OpenAIGatewayService{},
		service.NewConcurrencyService(nil),
		billingService,
		&service.APIKeyService{},
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
}

func alphaSearchAPIKey(platform string, groupID int64) *service.APIKey {
	return &service.APIKey{
		ID:      99,
		UserID:  100,
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: platform,
		},
		User: &service.User{ID: 100},
	}
}

func TestOpenAIGatewayHandlerAlphaSearchValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	validKey := alphaSearchAPIKey(service.PlatformOpenAI, 5100)
	validSubject := &middleware2.AuthSubject{UserID: 100}

	tests := []struct {
		name      string
		handler   func(*testing.T) *OpenAIGatewayHandler
		body      string
		apiKey    *service.APIKey
		subject   *middleware2.AuthSubject
		status    int
		errorType string
		message   string
	}{
		{
			name:      "missing_api_key",
			handler:   func(*testing.T) *OpenAIGatewayHandler { return &OpenAIGatewayHandler{} },
			status:    http.StatusUnauthorized,
			errorType: "authentication_error",
			message:   "Invalid API key",
		},
		{
			name:      "missing_group",
			handler:   func(*testing.T) *OpenAIGatewayHandler { return &OpenAIGatewayHandler{} },
			apiKey:    &service.APIKey{},
			status:    http.StatusUnauthorized,
			errorType: "authentication_error",
			message:   "Invalid API key",
		},
		{
			name:      "non_openai_group",
			handler:   func(*testing.T) *OpenAIGatewayHandler { return &OpenAIGatewayHandler{} },
			apiKey:    alphaSearchAPIKey(service.PlatformAnthropic, 5101),
			status:    http.StatusNotFound,
			errorType: "not_found_error",
			message:   "Codex alpha search is only available for OpenAI groups",
		},
		{
			name:      "missing_auth_subject",
			handler:   func(*testing.T) *OpenAIGatewayHandler { return &OpenAIGatewayHandler{} },
			apiKey:    validKey,
			status:    http.StatusInternalServerError,
			errorType: "api_error",
			message:   "User context not found",
		},
		{
			name:      "missing_dependencies",
			handler:   func(*testing.T) *OpenAIGatewayHandler { return &OpenAIGatewayHandler{} },
			apiKey:    validKey,
			subject:   validSubject,
			body:      `{"model":"gpt-5.6-sol"}`,
			status:    http.StatusServiceUnavailable,
			errorType: "api_error",
			message:   "Service temporarily unavailable",
		},
		{
			name:      "empty_body",
			handler:   newReadyAlphaSearchHandler,
			apiKey:    validKey,
			subject:   validSubject,
			status:    http.StatusBadRequest,
			errorType: "invalid_request_error",
			message:   "Request body is empty",
		},
		{
			name:      "invalid_json",
			handler:   newReadyAlphaSearchHandler,
			body:      `{not-json`,
			apiKey:    validKey,
			subject:   validSubject,
			status:    http.StatusBadRequest,
			errorType: "invalid_request_error",
			message:   "Failed to parse request body",
		},
		{
			name:      "missing_model",
			handler:   newReadyAlphaSearchHandler,
			body:      `{}`,
			apiKey:    validKey,
			subject:   validSubject,
			status:    http.StatusBadRequest,
			errorType: "invalid_request_error",
			message:   "model is required",
		},
		{
			name:      "non_string_model",
			handler:   newReadyAlphaSearchHandler,
			body:      `{"model":123}`,
			apiKey:    validKey,
			subject:   validSubject,
			status:    http.StatusBadRequest,
			errorType: "invalid_request_error",
			message:   "model is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := tt.handler(t)
			c, recorder := newAlphaSearchContext(tt.body, tt.apiKey, tt.subject)
			handler.AlphaSearch(c)

			require.Equal(t, tt.status, recorder.Code)
			require.Equal(t, tt.errorType, gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
			require.Equal(t, tt.message, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String())
		})
	}
}

func newAlphaSearchGatewayHandler(t *testing.T, accounts []service.Account, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	handler.maxAccountSwitches = 2
	return handler
}

func TestOpenAIGatewayHandlerAlphaSearchForwardsSuccessfully(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &alphaSearchHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"alpha-ok"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"encrypted_output":"result"}`)),
	}}}
	account := service.Account{
		ID:          1,
		Name:        "alpha-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "token"},
	}
	handler := newAlphaSearchGatewayHandler(t, []service.Account{account}, upstream)
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"id":"session-1","model":"gpt-5.6-sol","commands":{}}`, apiKey, &middleware2.AuthSubject{UserID: 100})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"encrypted_output":"result"}`, recorder.Body.String())
	require.Equal(t, []int64{1}, upstream.calls())
}

func TestOpenAIGatewayHandlerAlphaSearchRequestBodyReadError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newReadyAlphaSearchHandler(t)
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"model":"gpt-5.6-sol"}`, apiKey, &middleware2.AuthSubject{UserID: 100})
	c.Request.Body = alphaSearchReadErrorBody{}

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
	require.Equal(t, "Failed to read request body", gjson.GetBytes(recorder.Body.Bytes(), "error.message").String())
}

func TestOpenAIGatewayHandlerAlphaSearchNoAvailableAccountReturnsServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newAlphaSearchGatewayHandler(t, nil, &alphaSearchHTTPUpstream{})
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"id":"no-account","model":"gpt-5.6-sol"}`, apiKey, &middleware2.AuthSubject{UserID: 100})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "api_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
	require.Equal(t, "Service temporarily unavailable", gjson.GetBytes(recorder.Body.Bytes(), "error.message").String())
}

func TestOpenAIGatewayHandlerAlphaSearchForwardErrorReturnsBadGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &alphaSearchHTTPUpstream{}
	account := service.Account{
		ID:          31,
		Name:        "alpha-missing-token",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
	}
	handler := newAlphaSearchGatewayHandler(t, []service.Account{account}, upstream)
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"id":"token-error","model":"gpt-5.6-sol"}`, apiKey, &middleware2.AuthSubject{UserID: 100})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
	require.Equal(t, []int64(nil), upstream.calls())
}

func TestOpenAIGatewayHandlerAlphaSearchFailsOverToNextAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &alphaSearchHTTPUpstream{responses: []*http.Response{
		{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"temporary failure"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"encrypted_output":"recovered"}`)),
		},
	}}
	accounts := []service.Account{
		{ID: 11, Name: "alpha-primary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "token-1"}},
		{ID: 12, Name: "alpha-secondary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "token-2"}},
	}
	handler := newAlphaSearchGatewayHandler(t, accounts, upstream)
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"id":"session-2","model":"gpt-5.6-sol"}`, apiKey, &middleware2.AuthSubject{UserID: 100})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"encrypted_output":"recovered"}`, recorder.Body.String())
	require.Equal(t, []int64{11, 12}, upstream.calls())
}

func TestOpenAIGatewayHandlerAlphaSearchExhaustedFailoverReturnsGatewayError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &alphaSearchHTTPUpstream{responses: []*http.Response{
		{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"temporary failure"}}`)),
		},
		{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"still unavailable"}}`)),
		},
	}}
	accounts := []service.Account{
		{ID: 21, Name: "alpha-primary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "token-1"}},
		{ID: 22, Name: "alpha-secondary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "token-2"}},
	}
	handler := newAlphaSearchGatewayHandler(t, accounts, upstream)
	apiKey := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
	c, recorder := newAlphaSearchContext(`{"id":"session-3","model":"gpt-5.6-sol"}`, apiKey, &middleware2.AuthSubject{UserID: 100})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
	require.Equal(t, []int64{21, 22}, upstream.calls())
}
