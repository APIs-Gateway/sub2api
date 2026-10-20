//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 前端用这个倍率把站内额度折算成法币（额度 ÷ 倍率）。DTO 里有字段但 handler
// 忘了赋值时，响应会安静地序列化成 0——不是报错，而是前端把它归一化成 1、
// 双口径切换器整个消失，功能看起来"没上线"。这条测试就是钉住这个赋值。
func TestSettingHandler_GetPublicSettings_ExposesBalanceRechargeMultiplier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &settingHandlerPublicRepoStub{
		values: map[string]string{
			service.SettingBalanceRechargeMult: "13",
		},
	}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), "test-version")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			BalanceRechargeMultiplier float64 `json:"balance_recharge_multiplier"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.InDelta(t, 13.0, resp.Data.BalanceRechargeMultiplier, 1e-9)
}

// 没配倍率时回落到 1（不折算），而不是 0——0 会让前端算出 Inf。
func TestSettingHandler_GetPublicSettings_MultiplierDefaultsToOne(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewSettingHandler(
		service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{}}, &config.Config{}),
		"test-version",
	)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)

	h.GetPublicSettings(c)

	var resp struct {
		Data struct {
			BalanceRechargeMultiplier float64 `json:"balance_recharge_multiplier"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.InDelta(t, 1.0, resp.Data.BalanceRechargeMultiplier, 1e-9)
}
