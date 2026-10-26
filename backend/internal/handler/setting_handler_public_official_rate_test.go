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

// 前端用官方价汇率把模型官方美元价显示成人民币。handler 漏赋值时会序列化成 0，
// 前端就只能把官方价整个藏起来，这条测试钉住赋值和默认值。
func TestSettingHandler_GetPublicSettings_ExposesOfficialPriceCNYRate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for raw, want := range map[string]float64{"7.05": 7.05, "": service.DefaultOfficialPriceCNYRate} {
		values := map[string]string{}
		if raw != "" {
			values[service.SettingOfficialPriceCNYRate] = raw
		}
		h := NewSettingHandler(service.NewSettingService(&settingHandlerPublicRepoStub{values: values}, &config.Config{}), "test-version")

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)
		h.GetPublicSettings(c)
		require.Equal(t, http.StatusOK, recorder.Code)

		var resp struct {
			Data struct {
				OfficialPriceCNYRate float64 `json:"official_price_cny_rate"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
		require.InDelta(t, want, resp.Data.OfficialPriceCNYRate, 1e-9, "raw=%q", raw)
	}
}
