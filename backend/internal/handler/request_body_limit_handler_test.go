package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestGatewayHandlerMessages_RejectsBodyOverConfiguredLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &config.Config{
		Gateway: config.GatewayConfig{MaxBodySize: 8},
	}, nil)

	c, recorder := newBodyLimitTestContext(t, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))

	h.Messages(c)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status mismatch: got %d want %d, body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func TestOpenAIGatewayResponses_RejectsBodyOverConfiguredLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIGatewayHandler(
		&service.OpenAIGatewayService{},
		&service.ConcurrencyService{},
		&service.BillingCacheService{},
		&service.APIKeyService{},
		nil,
		nil,
		nil,
		nil,
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 8}},
	)

	c, recorder := newBodyLimitTestContext(t, []byte(`{"input":"hello"}`))

	h.Responses(c)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status mismatch: got %d want %d, body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func newBodyLimitTestContext(t *testing.T, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(30)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 10, UserID: 20, GroupID: &groupID})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 20, Concurrency: 1})

	return c, recorder
}
