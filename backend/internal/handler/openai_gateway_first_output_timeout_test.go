package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIForwardMayFailoverAfterKeepaliveOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)

	_, err := fmt.Fprint(c.Writer, ":\n\n")
	require.NoError(t, err)
	c.Writer.Flush()

	require.True(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{
		SafeToFailoverAfterWrite: true,
	}))
	require.False(t, openAIForwardMayFailover(c, before, &service.UpstreamFailoverError{}))
}

func TestOpenAIForwardFailoverGuardsNilAndUncommittedInputs(t *testing.T) {
	require.False(t, openAIForwardMayFailover(nil, 0, nil))
	require.False(t, openAIRequestAllowsFailoverReplay(nil))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	require.False(t, openAIForwardMayFailover(c, 1, nil))
	require.False(t, openAIRequestAllowsFailoverReplay(c))
}

func TestOpenAIFirstOutputFailoverSwitchesOnlyOnce(t *testing.T) {
	failoverErr := &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}
	count := 0

	require.False(t, openAIFirstOutputFailoverExhausted(failoverErr, &count))
	require.Equal(t, 1, count)
	require.True(t, openAIFirstOutputFailoverExhausted(failoverErr, &count))
	require.Equal(t, 1, count)
}

func TestOpenAIFirstOutputFailoverExhaustionRequiresSafeErrorAndCounter(t *testing.T) {
	count := 0
	require.False(t, openAIFirstOutputFailoverExhausted(nil, &count))
	require.False(t, openAIFirstOutputFailoverExhausted(&service.UpstreamFailoverError{}, &count))
	require.False(t, openAIFirstOutputFailoverExhausted(&service.UpstreamFailoverError{
		SafeToFailoverAfterWrite: true,
	}, nil))

	count = 1
	require.True(t, openAIFirstOutputFailoverExhausted(&service.UpstreamFailoverError{
		SafeToFailoverAfterWrite: true,
	}, &count))
	require.Equal(t, 1, count)
}

func TestOpenAIRequestDisallowsFailoverAfterClientCancellation(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)

	require.True(t, openAIRequestAllowsFailoverReplay(c))
	cancel()
	require.False(t, openAIRequestAllowsFailoverReplay(c))
}
