//go:build unit

package middleware

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// writePublicBenefitCapError 必须输出协议标准 error 信封（429）而非裸文本：
// AI 编程客户端把 2xx 当成功响应、按 JSON/SSE 解析 body，纯文本会解析失败而
// 丢失提示文案；唯有结构化 error.message 才能被各客户端原样展示给用户。
func TestWritePublicBenefitCapError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const msg = "今日您的公益 API 额度已满，可购买套餐"

	t.Run("OpenAI 分组 → OpenAI error 信封", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}}

		writePublicBenefitCapError(c, apiKey, msg)

		require.Equal(t, 429, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.NotContains(t, body, "type", "OpenAI 信封顶层不应带 type 字段")
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "应有 error 对象")
		require.Equal(t, msg, errObj["message"])
		require.Equal(t, "insufficient_quota", errObj["type"])
		require.Equal(t, "public_benefit_daily_cap", errObj["code"])
	})

	t.Run("Anthropic 分组 → Anthropic error 信封", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformAnthropic}}

		writePublicBenefitCapError(c, apiKey, msg)

		require.Equal(t, 429, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "error", body["type"])
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "应有 error 对象")
		require.Equal(t, msg, errObj["message"])
		require.Equal(t, "rate_limit_error", errObj["type"])
	})

	t.Run("未分组(nil group) → 回退 Anthropic 信封", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		apiKey := &service.APIKey{}

		writePublicBenefitCapError(c, apiKey, msg)

		require.Equal(t, 429, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "error", body["type"])
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "应有 error 对象")
		require.Equal(t, msg, errObj["message"])
	})
}
