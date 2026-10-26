//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAccountTestLogging_NoRequestMetadata(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, c.Request)
	err := (&AccountTestService{}).sendErrorAndEnd(c, "private-error-body access_token=private-token")
	require.EqualError(t, err, "private-error-body access_token=private-token")
	require.True(t, sink.ContainsMessageAtLevel("account_test.failed", "error"))
	require.False(t, sink.ContainsMessage("private-error-body"))
	require.False(t, sink.ContainsField("account_id"))
	require.False(t, sink.ContainsField("platform"))
	require.False(t, sink.ContainsField("model"))
	require.False(t, sink.ContainsField("test_source"))
}

func TestAccountTestLogging_ReusedContextDoesNotInheritPriorIdentity(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/test", nil)
	c.Request = c.Request.WithContext(logger.IntoContext(context.Background(), logger.L().With(zap.String("request_id", "manual-request"))))
	svc := &AccountTestService{}
	beginAccountTestLogging(c, 12, "alias")
	setAccountTestLogPlatform(c, PlatformOpenAI)
	svc.sendEvent(c, TestEvent{Type: "test_start", Model: "selected-model"})
	require.Error(t, svc.sendErrorAndEnd(c, "first private error"))
	beginAccountTestLogging(c, 13, "")
	require.Error(t, svc.sendErrorAndEnd(c, "second private error"))
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var events int
	for _, event := range sink.events {
		if event.Message != "account_test.failed" {
			continue
		}
		events++
		require.Equal(t, "error", event.Level)
		require.Equal(t, "service.account_test", event.Fields["component"])
		require.Equal(t, "manual-request", event.Fields["request_id"])
		if events == 1 {
			require.EqualValues(t, 12, event.Fields["account_id"])
			require.Equal(t, "selected-model", event.Fields["model"])
			require.Equal(t, "alias", event.Fields["requested_model"])
		} else {
			require.EqualValues(t, 13, event.Fields["account_id"])
			require.NotContains(t, event.Fields, "platform")
			require.NotContains(t, event.Fields, "model")
			require.NotContains(t, event.Fields, "requested_model")
		}
		fields, err := json.Marshal(event.Fields)
		require.NoError(t, err)
		require.NotContains(t, string(fields), "private error")
	}
	require.Equal(t, 2, events)
}
