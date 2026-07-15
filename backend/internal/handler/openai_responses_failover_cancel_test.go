//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// openAIResponsesFailoverCancelUpstream 固定返回 HTTP 520，可在首次上游调用时
// 触发回调（用于模拟“上游在途期间客户端断开”）。
type openAIResponsesFailoverCancelUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	onFirstDo  func()
}

type cancelOnOpenAIAccountSelectionRepo struct {
	openAIImagesFailoverAccountRepo
	onCancel context.CancelFunc
	once     sync.Once
}

func (r *cancelOnOpenAIAccountSelectionRepo) cancelAndFail() ([]service.Account, error) {
	r.once.Do(r.onCancel)
	return nil, context.Canceled
}

func (r *cancelOnOpenAIAccountSelectionRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return r.cancelAndFail()
}

func (r *cancelOnOpenAIAccountSelectionRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return r.cancelAndFail()
}

func (r *cancelOnOpenAIAccountSelectionRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]service.Account, error) {
	return r.cancelAndFail()
}

func (u *openAIResponsesFailoverCancelUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	first := len(u.accountIDs) == 1
	u.mu.Unlock()
	if first && u.onFirstDo != nil {
		u.onFirstDo()
	}
	return &http.Response{
		StatusCode: 520,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(bytes.NewBufferString("<html>520: unknown error</html>")),
	}, nil
}

func (u *openAIResponsesFailoverCancelUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func newOpenAIResponsesFailoverTestHandler(t *testing.T, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{
		{
			ID:          1,
			Name:        "responses-account-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
			Credentials: map[string]any{"access_token": "token-1", "api_key": "sk-test-1", "openai_capabilities": []any{"chat_completions", "embeddings"}},
		},
		{
			ID:          2,
			Name:        "responses-account-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    1,
			Credentials: map[string]any{"access_token": "token-2", "api_key": "sk-test-2", "openai_capabilities": []any{"chat_completions", "embeddings"}},
		},
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	return newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, accountRepo)
}

func newOpenAIResponsesFailoverTestHandlerWithRepo(t *testing.T, upstream service.HTTPUpstream, accountRepo service.AccountRepository) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)
	concurrencyService := service.NewConcurrencyService(nil)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	handler.maxAccountSwitches = 10
	return handler
}

func newOpenAIResponsesFailoverTestContext(t *testing.T, ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	return newOpenAIFailoverTestContext(t, ctx, "/v1/responses", `{"model":"gpt-5.1","stream":false,"input":"hello"}`, false)
}

func newOpenAIFailoverTestContext(t *testing.T, ctx context.Context, path, body string, allowImageGeneration bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	groupID := int64(3131)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                    groupID,
			Platform:              service.PlatformOpenAI,
			AllowImageGeneration:  allowImageGeneration,
			AllowMessagesDispatch: true,
		},
		User: &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	return c, rec
}

func TestOpenAIGatewayHandlerChatCompletions_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/chat/completions", `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`, false)

	handler.ChatCompletions(c)

	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}

func TestOpenAIGatewayHandlerEmbeddings_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hello"}`, false)

	handler.Embeddings(c)
	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}

func TestOpenAIGatewayHandlerImages_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/images/generations", `{"model":"gpt-image-2","prompt":"draw a cat"}`, true)

	handler.Images(c)

	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}

func TestOpenAIGatewayHandlerMessages_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/messages", `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`, false)

	handler.Messages(c)

	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}

func TestOpenAIGatewayHandlerFailoverGuardsStopBeforeSelectingAnAccount(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		body   string
		invoke func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{
			name:   "responses",
			path:   "/v1/responses",
			body:   `{"model":"gpt-5.1","stream":false,"input":"hello"}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Responses(c) },
		},
		{
			name:   "chat_completions",
			path:   "/v1/chat/completions",
			body:   `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.ChatCompletions(c) },
		},
		{
			name:   "messages",
			path:   "/v1/messages",
			body:   `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Messages(c) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			upstream := &openAIResponsesFailoverCancelUpstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
			c, rec := newOpenAIFailoverTestContext(t, ctx, tc.path, tc.body, false)

			tc.invoke(handler, c)

			require.Empty(t, upstream.calls())
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
			require.Zero(t, rec.Body.Len())
		})
	}
}

func TestOpenAIGatewayHandlerFailoverGuardsStopAfterAccountSelectionError(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		body   string
		invoke func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{
			name:   "responses",
			path:   "/v1/responses",
			body:   `{"model":"gpt-5.1","stream":false,"input":"hello"}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Responses(c) },
		},
		{
			name:   "chat_completions",
			path:   "/v1/chat/completions",
			body:   `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.ChatCompletions(c) },
		},
		{
			name:   "embeddings",
			path:   "/v1/embeddings",
			body:   `{"model":"text-embedding-3-small","input":"hello"}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Embeddings(c) },
		},
		{
			name:   "images",
			path:   "/v1/images/generations",
			body:   `{"model":"gpt-image-2","prompt":"draw a cat"}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Images(c) },
		},
		{
			name:   "messages",
			path:   "/v1/messages",
			body:   `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`,
			invoke: func(h *OpenAIGatewayHandler, c *gin.Context) { h.Messages(c) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, cancel := context.WithCancel(context.Background())
			upstream := &openAIResponsesFailoverCancelUpstream{}
			repo := &cancelOnOpenAIAccountSelectionRepo{
				openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{{
					ID: 1, Name: "selection-account", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "sk-test", "openai_capabilities": []any{"chat_completions", "embeddings"}},
				}}},
				onCancel: cancel,
			}
			handler := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, repo)
			c, rec := newOpenAIFailoverTestContext(t, ctx, tc.path, tc.body, tc.name == "images")

			tc.invoke(handler, c)

			require.Empty(t, upstream.calls())
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
			require.Zero(t, rec.Body.Len())
		})
	}
}

func TestFailoverClientGoneStopsCommittedCompactKeepalive(t *testing.T) {
	c, _, stop := newCommittedCompactKeepaliveContext(t)
	defer stop()
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	cancel()

	require.True(t, failoverClientGone(c))
}

// TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected 复现
// #4257：客户端在上游请求在途期间断开，上游随后返回可 failover 的 520。
// 期望：不再用已取消的 context 重新选号（不触达账号 2）、不把取消误报成
// 502 账号耗尽、请求按 499 归类。
func TestOpenAIGatewayHandlerResponses_FailoverAbortsWhenClientDisconnected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)

	handler.Responses(c)

	require.Equal(t, []int64{1}, upstream.calls(), "客户端断开后不应再切换到账号 2")
	require.Equal(t, statusClientClosedRequest, c.Writer.Status(), "应按 499 归类")
	require.Zero(t, rec.Body.Len(), "不应写入 502 错误响应体")

	_, hasFinalUpstreamErr := c.Get(service.OpsUpstreamStatusCodeKey)
	require.False(t, hasFinalUpstreamErr, "不应记录 failover 耗尽的上游错误终态")

	// 真实发生过的 520 应保留 failover 事件（service 层在返回 failover 错误前记录）
	rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, 520, events[0].UpstreamStatusCode)
}

// TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient 回归
// 守卫：客户端在线时 failover 行为不变——切换到账号 2，两个账号都 520 后按
// 耗尽返回 502。
func TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &openAIResponsesFailoverCancelUpstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, nil)

	handler.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "在线客户端应正常切换账号")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}
