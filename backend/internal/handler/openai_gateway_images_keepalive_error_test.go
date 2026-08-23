package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCommittedImagesJSONKeepaliveContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	stop := service.StartOpenAIImagesJSONKeepalive(c, time.Millisecond)
	require.Eventually(t, func() bool {
		return c.Writer.Written()
	}, time.Second, time.Millisecond)
	return c, rec, stop
}

// 心跳只写过填充空白（未产生真实输出）时，ensureForwardErrorResponse 必须仍然
// 补写兜底错误体，而不是被 c.Writer.Written() 误判为"流已开始"而跳过。
func TestOpenAIGatewayHandlerEnsureForwardErrorResponse_ImagesKeepalivePaddingOnlyStillWritesFallback(t *testing.T) {
	c, rec, stop := newCommittedImagesJSONKeepaliveContext(t)
	defer stop()

	h := &OpenAIGatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.True(t, wrote)
	// json.Unmarshal 会自动跳过前导空白，心跳打了几拍不影响解析。
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upstream_error", errorObj["type"])
}

// 心跳之后如果已经产生了真实输出，ensureForwardErrorResponse 不能再补写兜底
// 错误体（会污染已提交的合法响应），必须直接返回 false。
func TestOpenAIGatewayHandlerEnsureForwardErrorResponse_ImagesKeepaliveRealOutputSkipsFallback(t *testing.T) {
	c, rec, stop := newCommittedImagesJSONKeepaliveContext(t)
	defer stop()
	_, err := c.Writer.WriteString(`{"data":[{"url":"https://example.com/image.png"}]}`)
	require.NoError(t, err)
	bodyBeforeFallback := rec.Body.String()

	h := &OpenAIGatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.False(t, wrote)
	require.Equal(t, bodyBeforeFallback, rec.Body.String())
}
