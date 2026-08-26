//go:build unit

package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestValidateAPIKeyCreateRequest(t *testing.T) {
	zero, large, negative, nan, inf := 0.0, 1e100, -1.0, math.NaN(), math.Inf(1)
	positiveDays, zeroDays, negativeDays := 1, 0, -1
	require.NoError(t, validateAPIKeyCreateRequest(CreateAPIKeyRequest{}))
	require.NoError(t, validateAPIKeyCreateRequest(CreateAPIKeyRequest{Quota: &zero, RateLimit5h: &large, ExpiresInDays: &positiveDays}))

	for _, req := range []CreateAPIKeyRequest{
		{Quota: &negative},
		{Quota: &nan},
		{RateLimit5h: &inf},
		{RateLimit1d: &negative},
		{RateLimit7d: &negative},
		{ExpiresInDays: &zeroDays},
		{ExpiresInDays: &negativeDays},
	} {
		require.Error(t, validateAPIKeyCreateRequest(req))
	}
}

func TestValidateAPIKeyUpdateRequest(t *testing.T) {
	zero, large, negative, nan, inf := 0.0, 1e100, -1.0, math.NaN(), math.Inf(-1)
	require.NoError(t, validateAPIKeyUpdateRequest(UpdateAPIKeyRequest{Quota: &zero, RateLimit7d: &large}))

	for _, req := range []UpdateAPIKeyRequest{
		{Quota: &negative},
		{RateLimit5h: &nan},
		{RateLimit1d: &inf},
		{RateLimit7d: &negative},
	} {
		require.Error(t, validateAPIKeyUpdateRequest(req))
	}
}

// TestAPIKeyHandlerCreate_InvalidExpiresInDaysReturns400 覆盖 Create handler 内部
// 调用 validateAPIKeyCreateRequest 拿到 error 后走 response.BadRequest 返回 400
// 的分支（校验函数本身已有单测，这里补的是 HTTP handler 层的调用链路）。
func TestAPIKeyHandlerCreate_InvalidExpiresInDaysReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"test-key","expires_in_days":0}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &APIKeyHandler{}
	h.Create(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	message, ok := resp["message"].(string)
	require.True(t, ok)
	require.Contains(t, message, "expires_in_days")
}

// TestAPIKeyHandlerUpdate_InvalidRateLimitReturns400 覆盖 Update handler 内部
// 调用 validateAPIKeyUpdateRequest 拿到 error 后走 response.BadRequest 返回 400
// 的分支。
func TestAPIKeyHandlerUpdate_InvalidRateLimitReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/api-keys/1", strings.NewReader(`{"rate_limit_5h":-1}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &APIKeyHandler{}
	h.Update(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	message, ok := resp["message"].(string)
	require.True(t, ok)
	require.Contains(t, message, "numeric limits must be finite and non-negative")
}
