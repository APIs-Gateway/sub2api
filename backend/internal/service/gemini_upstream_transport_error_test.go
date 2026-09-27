//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGeminiTransportErrorService(err error) (*GeminiMessagesCompatService, *geminiCompatHTTPUpstreamStub, *transportTempUnschedRepoStub) {
	upstream := &geminiCompatHTTPUpstreamStub{err: err}
	repo := &transportTempUnschedRepoStub{}
	return &GeminiMessagesCompatService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}, upstream, repo
}

func newGeminiTransportContext(t *testing.T, path string, body []byte) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	return c
}

func requireGeminiTransportFailover(t *testing.T, err error) {
	t.Helper()
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusBadGateway, failover.StatusCode)
	require.Equal(t, geminiTransportFailoverBody, failover.ResponseBody)
	require.False(t, failover.RetryableOnSameAccount)
	require.False(t, failover.RequestScopedTransient)
}

func TestGeminiTransportError_ThreeEntryPathsFailOverBeforeClientOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	claudeBody := []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	chatBody := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}]}`)
	cases := []struct {
		name    string
		path    string
		body    []byte
		forward func(*GeminiMessagesCompatService, context.Context, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{
			name: "native", path: "/v1beta/models/gemini-2.5-flash:generateContent", body: geminiSignalTestRequest(),
			forward: func(s *GeminiMessagesCompatService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return s.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, body)
			},
		},
		{
			name: "claude", path: "/v1/messages", body: claudeBody,
			forward: func(s *GeminiMessagesCompatService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return s.Forward(ctx, c, account, body)
			},
		},
		{
			name: "chat_completions", path: "/v1/chat/completions", body: chatBody,
			forward: func(s *GeminiMessagesCompatService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return s.ForwardAsChatCompletions(ctx, c, account, body)
			},
		},
	}

	for _, tc := range cases {
		for _, upstreamErr := range []struct {
			name string
			err  error
		}{
			{name: "EOF", err: errors.New("read tcp: EOF")},
			{name: "provider_canceled", err: context.Canceled},
			{name: "provider_deadline", err: context.DeadlineExceeded},
		} {
			t.Run(tc.name+"/"+upstreamErr.name, func(t *testing.T) {
				svc, upstream, repo := newGeminiTransportErrorService(upstreamErr.err)
				c := newGeminiTransportContext(t, tc.path, tc.body)
				account := geminiSignalTestAccount()

				result, err := tc.forward(svc, context.Background(), c, account, tc.body)

				require.Nil(t, result)
				requireGeminiTransportFailover(t, err)
				require.Equal(t, 1, upstream.calls, "transport failure must not retry the same account")
				require.Zero(t, repo.calls, "transient failure must not unschedule the account")
				require.False(t, c.Writer.Written(), "the handler owns the failover response")
				events := upstreamErrorEventsFromContext(t, c)
				require.Len(t, events, 1)
				require.Equal(t, "request_error", events[0].Kind)
				require.Equal(t, account.ID, events[0].AccountID)
				require.Zero(t, events[0].UpstreamStatusCode)
			})
		}
	}
}

func TestGeminiTransportError_PersistentFailureUnschedulesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, upstream, repo := newGeminiTransportErrorService(errors.New("dial tcp 1.2.3.4:443: connect: connection refused"))
	body := geminiSignalTestRequest()
	c := newGeminiTransportContext(t, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", body)
	account := geminiSignalTestAccount()
	before := time.Now()

	result, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "streamGenerateContent", true, body)

	require.Nil(t, result)
	requireGeminiTransportFailover(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, account.ID, repo.lastID)
	require.WithinDuration(t, before.Add(gatewayTransportErrorTempUnschedDuration), repo.lastUntil, 5*time.Second)
	require.False(t, c.Writer.Written())
}

func TestGeminiTransportError_ClientCancellationDoesNotFailOverOrLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, upstream, repo := newGeminiTransportErrorService(context.Canceled)
	body := geminiSignalTestRequest()
	c := newGeminiTransportContext(t, "/v1beta/models/gemini-2.5-flash:generateContent", body)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.ForwardNative(ctx, c, geminiSignalTestAccount(), "gemini-2.5-flash", "generateContent", false, body)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, repo.calls)
	require.Empty(t, upstreamErrorEventsFromContext(t, c))
	require.False(t, c.Writer.Written())
}

func TestGeminiTransportError_RequestDeadlineDoesNotFailOverOrLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, upstream, repo := newGeminiTransportErrorService(context.DeadlineExceeded)
	body := geminiSignalTestRequest()
	c := newGeminiTransportContext(t, "/v1beta/models/gemini-2.5-flash:generateContent", body)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result, err := svc.ForwardNative(ctx, c, geminiSignalTestAccount(), "gemini-2.5-flash", "generateContent", false, body)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, repo.calls)
	require.Empty(t, upstreamErrorEventsFromContext(t, c))
	require.False(t, c.Writer.Written())
}

func TestGeminiTransportError_CountTokensFallsBackWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, upstream, repo := newGeminiTransportErrorService(errors.New("EOF"))
	body := geminiSignalTestRequest()
	c := newGeminiTransportContext(t, "/v1beta/models/gemini-2.5-flash:countTokens", body)

	result, err := svc.ForwardNative(context.Background(), c, geminiSignalTestAccount(), "gemini-2.5-flash", "countTokens", false, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, repo.calls)
	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.True(t, c.Writer.Written())
}
