package service

// Upstream b8ed24508 later-turn quota recovery case. Upstream adds it to
// openai_ws_v2_passthrough_lifecycle_test.go; the fork keeps the staged
// passthrough helpers in openai_ws_v2_passthrough_cyber_lifecycle_test.go, so
// the case lives in its own file.

import (
	"context"
	"fmt"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func requireStagedPassthroughUpstreamWrite(t *testing.T, upstream *stagedPassthroughConn, timeout time.Duration) []byte {
	t.Helper()
	select {
	case payload := <-upstream.writes:
		return payload
	case <-time.After(timeout):
		t.Fatal("timed out waiting for passthrough upstream write")
		return nil
	}
}

func TestPassthroughLifecycle_LaterTurnPreOutputRateLimitRequestsReconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetUpstream429TrackerForTest()
	t.Cleanup(resetUpstream429TrackerForTest)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	account := passthroughLifecycleAccount()
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}}}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.accountRepo = repo
	svc.rateLimitService = &RateLimitService{accountRepo: repo}

	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, account)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	firstRequest := requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, "response.create", gjson.GetBytes(firstRequest, "type").String())

	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	completed, err := readPassthroughLifecycleFrame(t, clientConn, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)
	secondRequest := requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, "response.create", gjson.GetBytes(secondRequest, "type").String())

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	upstream.Send(fmt.Sprintf(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":%d}}`, resetAt))
	_, err = readPassthroughLifecycleFrame(t, clientConn, time.Second)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusTryAgainLater, websocketCloseErr.Code)
	require.Equal(t, "upstream rate limit exceeded; please reconnect", websocketCloseErr.Reason)
	require.Len(t, repo.rateLimitCalls, 1)
	require.WithinDuration(t, time.Unix(resetAt, 0), repo.rateLimitCalls[0], 2*time.Second)

	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	case <-time.After(time.Second):
		t.Fatal("later-turn rate limit did not terminate passthrough")
	}
	select {
	case replay := <-upstream.writes:
		t.Fatalf("later-turn reconnect must not replay the retained first request: %s", replay)
	default:
	}
}
