//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newInvalidJSONRequestContext(t *testing.T, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model": bad}`))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(10)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      100,
		UserID:  200,
		GroupID: &groupID,
		User:    &service.User{ID: 200},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 200, Concurrency: 1})

	return c, w
}

func newOpenAIHandlerWithParseDependencies() *OpenAIGatewayHandler {
	return &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}
}

func TestRequestBodyParseFailureHandlersReturnStableBadRequest(t *testing.T) {
	tests := []struct {
		name string
		path string
		run  func(*gin.Context)
	}{
		{
			name: "gateway messages",
			path: "/v1/messages",
			run:  (&GatewayHandler{}).Messages,
		},
		{
			name: "gateway count tokens",
			path: "/v1/messages/count_tokens",
			run:  (&GatewayHandler{}).CountTokens,
		},
		{
			name: "gateway chat completions",
			path: "/v1/chat/completions",
			run:  (&GatewayHandler{}).ChatCompletions,
		},
		{
			name: "gateway responses",
			path: "/v1/responses",
			run:  (&GatewayHandler{}).Responses,
		},
		{
			name: "openai chat completions",
			path: "/openai/v1/chat/completions",
			run:  newOpenAIHandlerWithParseDependencies().ChatCompletions,
		},
		{
			name: "openai responses",
			path: "/openai/v1/responses",
			run:  newOpenAIHandlerWithParseDependencies().Responses,
		},
		{
			name: "openai messages",
			path: "/openai/v1/messages",
			run:  newOpenAIHandlerWithParseDependencies().Messages,
		},
		{
			name: "openai embeddings",
			path: "/openai/v1/embeddings",
			run:  newOpenAIHandlerWithParseDependencies().Embeddings,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, w := newInvalidJSONRequestContext(t, tt.path)

			tt.run(c)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "Failed to parse request body")
		})
	}
}
