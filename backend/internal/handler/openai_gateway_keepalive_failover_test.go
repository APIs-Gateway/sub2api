//go:build unit

package handler

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 只写过心跳字节（compact keepalive 已把 200 SSE 提交，但 OpenAICompactKeepaliveAdjustedWrittenSize
// 扣除后仍等于转发前）时：允许切号（无论 SafeToFailoverAfterWrite），且 handler 必须把
// streamStarted 置位，耗尽时走流内错误。流式心跳（addOpenAIStreamKeepaliveBytes）与 compact
// keepalive 走同一个扣除函数，service 侧单测已覆盖其计数语义。
func TestOpenAIForwardMayFailover_KeepaliveOnlyBytesAllowFailover(t *testing.T) {
	c, rec, stop := newCommittedCompactKeepaliveContext(t)
	defer stop()
	before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
	require.Equal(t, -1, before, "只写过心跳等价于未写")
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), ": keepalive")

	require.True(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}))
	require.True(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{}),
		"扣除心跳后 Size 未变：即使没有 SafeToFailoverAfterWrite 也允许切号")
	require.True(t, openAIForwardWroteKeepaliveOnly(c, before), "已提交响应且只有心跳字节 → 耗尽时必须走流内错误")
}

func TestOpenAIForwardWroteKeepaliveOnly_FalseWhenUnwrittenOrApplicationBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.False(t, openAIForwardWroteKeepaliveOnly(nil, -1))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
	require.False(t, openAIForwardWroteKeepaliveOnly(c, before), "未写过任何字节")

	// 未计入心跳计数的字节视为业务输出：不允许无标记切号，也不是「只写过心跳」
	_, err := fmt.Fprint(c.Writer, ":\n\n")
	require.NoError(t, err)
	c.Writer.Flush()
	require.False(t, openAIForwardWroteKeepaliveOnly(c, before))
	require.False(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{}))
	require.True(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}))
}
