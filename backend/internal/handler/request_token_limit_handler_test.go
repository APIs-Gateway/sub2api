package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// tokenLimitTestConfig 打开 token 闸门但把请求体上限留在默认值，
// 这样测试断言的一定是 token 闸门而不是字节数闸门。
func tokenLimitTestConfig() *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			MaxBodySize:    256 * 1024 * 1024,
			MaxInputTokens: 100,
		},
	}
}

// oversizedPrompt 是 8000 个 ASCII 字符，按估算器的 4 字符/token 约合 2000 token，
// 稳稳越过测试里的 100 token 上限。
func oversizedPrompt() string {
	return strings.Repeat("a", 8000)
}

// /v1/messages 是 Claude Code 的主力路径，闸门必须在选号和转发之前就生效。
func TestGatewayHandlerMessages_RejectsOverInputTokenLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		tokenLimitTestConfig(), nil, nil)

	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + oversizedPrompt() + `"}]}`)
	c, recorder := newBodyLimitTestContext(t, body)

	h.Messages(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s", recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "too large")
}

// Gemini 原生路径走的是另一套错误响应格式，同样要拦住。
func TestGeminiV1BetaModels_RejectsOverInputTokenLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		tokenLimitTestConfig(), nil, nil)

	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"` + oversizedPrompt() + `"}]}]}`)
	c, recorder := newGeminiTokenLimitTestContext(t, "/gemini-2.5-pro:generateContent", body)

	h.GeminiV1BetaModels(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s", recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "too large")
}

// countTokens 是客户端用来自查长度的动作，闸门刻意放行——否则客户端连
// "我到底超了多少" 都问不出来。
func TestGeminiV1BetaModels_CountTokensSkipsInputTokenLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		tokenLimitTestConfig(), nil, nil)

	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"` + oversizedPrompt() + `"}]}]}`)
	c, recorder := newGeminiTokenLimitTestContext(t, "/gemini-2.5-pro:countTokens", body)

	// 越过闸门之后的流程依赖本测试没有注入的服务，怎么收场都无所谓；
	// 这里唯一要断言的是请求没有被闸门拦下。
	func() {
		defer func() { _ = recover() }()
		h.GeminiV1BetaModels(c)
	}()
	require.NotContains(t, recorder.Body.String(), "too large")
}

func newGeminiTokenLimitTestContext(t *testing.T, modelAction string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models"+modelAction, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "modelAction", Value: modelAction}}

	groupID := int64(30)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      10,
		UserID:  20,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 20, Concurrency: 1})

	return c, recorder
}
