package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayHandlerImages_DisabledGroupRejectsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(111)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      222,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: 333},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 333, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Images(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIGatewayHandlerResponses_PassiveNamespaceDoesNotRejectDisabledGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":"auto","input":"write code"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(112)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      223,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: 334},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 334, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Responses(c)

	require.NotEqual(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIGatewayHandlerResponses_ExplicitNamespaceRejectsDisabledGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":{"type":"namespace","name":"image_gen"},"input":"draw"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(113)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      224,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: 335},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 335, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}

	h.Responses(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}
