//go:build unit

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 领码 handler 的单测。这里刻意只拼一个「功能关闭」的 service：
// 关闭态是线上最常见的状态（单站部署压根不配旧站库），也是最容易被写漏的分支——
// 它必须给出明确的「未开放」而不是空指针崩溃。
// 达标判定、验证码校验等业务分支由 service 层的单测覆盖，不在这里重复。

func newDisabledLegacyInviteHandler() *LegacyInviteHandler {
	svc := service.NewLegacyInviteService(nil, nil, nil, nil, nil, service.LegacyInviteOptions{})
	// turnstileService 传 nil：下面用到的路径都在人机校验之前或之后，不会触碰它。
	return NewLegacyInviteHandler(svc, nil)
}

func performLegacyInviteRequest(t *testing.T, handle gin.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")

	handle(c)
	return recorder
}

// TestLegacyInviteHandler_GetStatus_Disabled 验证关闭时状态接口的形状。
// 关闭态不外泄门槛金额——前端此时也用不上它。
func TestLegacyInviteHandler_GetStatus_Disabled(t *testing.T) {
	t.Parallel()

	h := newDisabledLegacyInviteHandler()
	recorder := performLegacyInviteRequest(t, h.GetStatus, http.MethodGet, "")
	require.Equal(t, http.StatusOK, recorder.Code)

	var payload struct {
		Code int `json:"code"`
		Data struct {
			Enabled       bool    `json:"enabled"`
			MinPaidAmount float64 `json:"min_paid_amount"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.False(t, payload.Data.Enabled)
	require.Zero(t, payload.Data.MinPaidAmount)
}

// TestLegacyInviteHandler_RejectsMalformedBody 验证请求体校验：
// 缺字段或不是合法 JSON 都应在进入业务逻辑之前就被挡下。
func TestLegacyInviteHandler_RejectsMalformedBody(t *testing.T) {
	t.Parallel()

	h := newDisabledLegacyInviteHandler()

	cases := []struct {
		name   string
		handle gin.HandlerFunc
		body   string
	}{
		{"send-code invalid json", h.SendCode, "{"},
		{"send-code missing email", h.SendCode, `{}`},
		{"claim invalid json", h.Claim, "{"},
		{"claim missing code", h.Claim, `{"email":"a@b.com"}`},
		{"claim missing email", h.Claim, `{"code":"123456"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := performLegacyInviteRequest(t, tc.handle, http.MethodPost, tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

// TestLegacyInviteHandler_Claim_Disabled 验证功能关闭时领取请求得到明确拒绝。
// 走的是 service 的 ErrLegacyInviteDisabled，映射成 404 而不是 500。
func TestLegacyInviteHandler_Claim_Disabled(t *testing.T) {
	t.Parallel()

	h := newDisabledLegacyInviteHandler()
	recorder := performLegacyInviteRequest(t, h.Claim, http.MethodPost, `{"email":"a@b.com","code":"123456"}`)
	require.Equal(t, http.StatusNotFound, recorder.Code)

	var payload struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "LEGACY_INVITE_DISABLED", payload.Reason)
}
