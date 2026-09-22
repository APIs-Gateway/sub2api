package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIWSExecutionScopeContext(headers map[string]string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for key, value := range headers {
		c.Request.Header.Set(key, value)
	}
	return c
}

func TestResolveOpenAIWSExecutionScopeIsolatesCodexThreadsAndLanes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"type":"response.create","input":"hello"}`)
	rootHeaders := map[string]string{
		"session-id": "root-session",
		openAIWSTurnMetadataHeader: `{"session_id":"root-session","thread_id":"root","request_kind":"turn"}`,
	}
	rootScope, rootThread := resolveOpenAIWSExecutionScope(newOpenAIWSExecutionScopeContext(rootHeaders), body, 9)
	require.Equal(t, "root", rootThread)
	require.Len(t, rootScope, 16)

	childHeaders := map[string]string{
		"session-id": "root-session",
		openAIWSTurnMetadataHeader: `{"session_id":"root-session","thread_id":"child","request_kind":"turn"}`,
	}
	childScope, childThread := resolveOpenAIWSExecutionScope(newOpenAIWSExecutionScopeContext(childHeaders), body, 9)
	require.Equal(t, "child", childThread)
	require.NotEqual(t, rootScope, childScope, "shared session-id must not join parent and child executions")

	memoryHeaders := map[string]string{
		"session-id": "root-session",
		openAIWSTurnMetadataHeader: `{"session_id":"root-session","thread_id":"root","request_kind":"memory"}`,
	}
	memoryScope, _ := resolveOpenAIWSExecutionScope(newOpenAIWSExecutionScopeContext(memoryHeaders), body, 9)
	require.NotEqual(t, rootScope, memoryScope, "background memory work must not preempt a user turn")
	require.Equal(t, rootScope, executionScopeForTest(rootHeaders, body, 9), "a reconnect stays in the same execution scope")
	require.NotEqual(t, rootScope, executionScopeForTest(rootHeaders, body, 10), "API keys are part of the execution isolation key")
}

func executionScopeForTest(headers map[string]string, body []byte, apiKeyID int64) string {
	scope, _ := resolveOpenAIWSExecutionScope(newOpenAIWSExecutionScopeContext(headers), body, apiKeyID)
	return scope
}

func TestOpenAIWSIngressSessionPreemptionOnlyReplacesSameExecutionScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	newContext := func(threadID string) *gin.Context {
		c := newOpenAIWSExecutionScopeContext(map[string]string{
			"session-id": "root-session",
			openAIWSTurnMetadataHeader: `{"session_id":"root-session","thread_id":"` + threadID + `"}`,
		})
		c.Set("api_key", &APIKey{ID: 11, GroupID: &groupID})
		return c
	}
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"type":"response.create","input":"hello"}`)

	rootCtx, rootCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newContext("root"), account, body)
	require.True(t, armed)
	defer rootCleanup()
	childCtx, childCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newContext("child"), account, body)
	require.True(t, armed)
	defer childCleanup()
	require.NoError(t, rootCtx.Err())
	require.NoError(t, childCtx.Err())

	_, replacementCleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newContext("root"), account, body)
	require.True(t, armed)
	defer replacementCleanup()
	require.True(t, IsOpenAIWSSessionPreemptedError(context.Cause(rootCtx)))
	require.NoError(t, childCtx.Err(), "a same-session sibling must not be cancelled")
}
