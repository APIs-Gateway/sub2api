//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 端到端（handler 失败切换循环 × service 流式路径）：等待上游首事件期间网关已按
// stream_keepalive_interval 写过心跳（Responses / Chat 是注释行、Anthropic 入站是 ping），
// 随后上游首事件带错误 model。三个入口都必须：
//   - 心跳不算内容交付：仍拦截并切号（openAIForwardMayFailover 用扣除心跳后的 Size）；
//   - 响应头已被心跳提交为 200 SSE：openAIForwardWroteKeepaliveOnly 把 streamStarted 置位，
//     耗尽时在同一连接内以流内终止事件（response.failed / event: error）收尾，不能再写 JSON 错误体；
//   - 被拦截的尝试落一条零计费审计行，换号成功后的真实请求照常落行。
//
// 池模式同账号重试（waitPoolModeSameAccountRetry）的 retry / canceled 两个出口也在此覆盖。

const (
	keepaliveMismatchRequestedModel = "gpt-5.1"
	keepaliveMismatchWrongModel     = "gpt-4.1-mini"
	keepaliveMismatchSSEComment     = ":\n\n"
	keepaliveMismatchAnthropicPing  = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
)

// keepaliveMismatchResponsesSSE 是上游 Responses SSE：response.created 就带 model。
func keepaliveMismatchResponsesSSE(model, text string) string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ka","object":"response","model":"` + model + `","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_ka","role":"assistant","status":"in_progress","content":[]}}`,
		"",
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_ka","part":{"type":"output_text","text":""}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_ka","delta":"` + text + `"}`,
		"",
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_ka","text":"` + text + `"}`,
		"",
		`data: {"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_ka","part":{"type":"output_text","text":"` + text + `"}}`,
		"",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_ka","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ka","object":"response","model":"` + model + `","status":"completed","output":[{"type":"message","id":"msg_ka","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
		"",
	}, "\n")
}

// keepaliveMismatchGatedBody 在首次 Read 时阻塞，直到 gate 关闭（网关首次 Flush，即心跳真正写出）。
type keepaliveMismatchGatedBody struct {
	gate   <-chan struct{}
	reader io.Reader
	opened bool
}

func (b *keepaliveMismatchGatedBody) Read(p []byte) (int, error) {
	if !b.opened {
		select {
		case <-b.gate:
		case <-time.After(10 * time.Second):
		}
		b.opened = true
	}
	return b.reader.Read(p)
}

func (b *keepaliveMismatchGatedBody) Close() error { return nil }

// keepaliveMismatchFlushSignalWriter 在首次 Flush 时关闭 flushed（心跳写出的信号）。
type keepaliveMismatchFlushSignalWriter struct {
	gin.ResponseWriter
	flushed chan struct{}
	once    sync.Once
}

func (w *keepaliveMismatchFlushSignalWriter) Flush() {
	w.ResponseWriter.Flush()
	w.once.Do(func() { close(w.flushed) })
}

// keepaliveMismatchUpstream 按调用顺序出响应：bodies[i] 是第 i+1 次上游调用的 SSE；
// gateFirst=true 时第一次调用的 body 门控在网关首次 Flush 之后放出。
type keepaliveMismatchUpstream struct {
	mu        sync.Mutex
	calls     []int64
	bodies    []string
	gateFirst bool
	gate      chan struct{}
	onFirstDo func()
}

func (u *keepaliveMismatchUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	n := len(u.calls)
	u.mu.Unlock()
	if n > len(u.bodies) {
		return nil, io.ErrUnexpectedEOF
	}
	var body io.ReadCloser = io.NopCloser(strings.NewReader(u.bodies[n-1]))
	if n == 1 && u.gateFirst {
		body = &keepaliveMismatchGatedBody{gate: u.gate, reader: strings.NewReader(u.bodies[0])}
	}
	if n == 1 && u.onFirstDo != nil {
		u.onFirstDo()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}, nil
}

func (u *keepaliveMismatchUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *keepaliveMismatchUpstream) accountCalls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...)
}

type keepaliveMismatchEntry struct {
	name          string
	path          string
	body          string
	register      func(*gin.Engine, *OpenAIGatewayHandler)
	wantKeepalive string
	wantTerminal  string // 耗尽时的流内终止事件
}

func keepaliveMismatchEntries() []keepaliveMismatchEntry {
	return []keepaliveMismatchEntry{
		{
			name:          "responses",
			path:          "/openai/v1/responses",
			body:          `{"model":"gpt-5.1","stream":true,"input":"hello"}`,
			register:      func(r *gin.Engine, h *OpenAIGatewayHandler) { r.POST("/openai/v1/responses", h.Responses) },
			wantKeepalive: keepaliveMismatchSSEComment,
			wantTerminal:  "event: response.failed",
		},
		{
			name:          "chat_completions",
			path:          "/openai/v1/chat/completions",
			body:          `{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			register:      func(r *gin.Engine, h *OpenAIGatewayHandler) { r.POST("/openai/v1/chat/completions", h.ChatCompletions) },
			wantKeepalive: keepaliveMismatchSSEComment,
			wantTerminal:  "event: error",
		},
		{
			name:          "messages",
			path:          "/openai/v1/messages",
			body:          `{"model":"gpt-5.1","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			register:      func(r *gin.Engine, h *OpenAIGatewayHandler) { r.POST("/openai/v1/messages", h.Messages) },
			wantKeepalive: keepaliveMismatchAnthropicPing,
			wantTerminal:  "event: error",
		},
	}
}

func keepaliveMismatchAccount(id int64, priority int, credentials map[string]any) service.Account {
	creds := map[string]any{"api_key": fmt.Sprintf("sk-test-%d", id), "base_url": "https://api.openai.example"}
	for k, v := range credentials {
		creds[k] = v
	}
	return service.Account{
		ID:          id,
		Name:        "openai-keepalive-mismatch",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    priority,
		Credentials: creds,
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
	}
}

type keepaliveMismatchHarness struct {
	handler   *OpenAIGatewayHandler
	router    *gin.Engine
	usageLogs chan *service.UsageLog
	flushed   chan struct{}
	stop      func()
}

// newKeepaliveMismatchHarness 组装真实的 OpenAIGatewayService + handler（simple 模式，usage 落到 channel），
// 中间件把 c.Writer 换成首次 Flush 即发信号的包装，供上游 body 门控。
func newKeepaliveMismatchHarness(t *testing.T, accounts []service.Account, upstream service.HTTPUpstream) *keepaliveMismatchHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.StreamKeepaliveInterval = 1

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		upstream,
		&service.DeferredService{},
		nil, // grokTokenProvider
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
		nil, // stableStore
		nil, // groupRepo
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
	}
	h := &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		maxAccountSwitches:  3,
	}

	groupID := int64(4930)
	apiKey := &service.APIKey{
		ID:      1930,
		GroupID: &groupID,
		User:    &service.User{ID: 1730, Status: service.StatusActive},
		Group: &service.Group{
			ID:                    groupID,
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			AllowMessagesDispatch: true,
		},
	}
	flushed := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Writer = &keepaliveMismatchFlushSignalWriter{ResponseWriter: c.Writer, flushed: flushed}
		c.Next()
	})
	return &keepaliveMismatchHarness{handler: h, router: router, usageLogs: usageRepo.created, flushed: flushed, stop: billingCacheSvc.Stop}
}

func (hs *keepaliveMismatchHarness) serve(t *testing.T, entry keepaliveMismatchEntry, reqCtx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	entry.register(hs.router, hs.handler)
	req := httptest.NewRequest(http.MethodPost, entry.path, strings.NewReader(entry.body))
	if reqCtx != nil {
		req = req.WithContext(reqCtx)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.router.ServeHTTP(rec, req)
	return rec
}

// waitUsageLogs 收集 n 条异步落库的 usage 行（审计行与真实行的先后不定）。
func (hs *keepaliveMismatchHarness) waitUsageLogs(t *testing.T, n int) []*service.UsageLog {
	t.Helper()
	logs := make([]*service.UsageLog, 0, n)
	for len(logs) < n {
		select {
		case log := <-hs.usageLogs:
			logs = append(logs, log)
		case <-time.After(3 * time.Second):
			t.Fatalf("等待第 %d/%d 条 usage 行超时", len(logs)+1, n)
		}
	}
	return logs
}

func requireKeepaliveMismatchAuditRow(t *testing.T, logs []*service.UsageLog, accountID int64) {
	t.Helper()
	found := false
	for _, log := range logs {
		if !log.UpstreamModelMismatch {
			continue
		}
		if log.AccountID != accountID {
			continue
		}
		found = true
		require.NotNil(t, log.UpstreamResponseModel)
		require.Equal(t, keepaliveMismatchWrongModel, *log.UpstreamResponseModel)
		require.Zero(t, log.TotalCost, "被拦截的尝试零计费")
		require.Zero(t, log.ActualCost)
	}
	require.True(t, found, "账号 %d 被拦截的尝试必须落一条不一致审计行", accountID)
}

// 心跳后首事件带错 model → 切号；第二个账号正常 → 客户端只多收到一段心跳，内容来自第二个账号。
func TestOpenAIGateway_KeepaliveThenUpstreamModelMismatch_SwitchesAccount(t *testing.T) {
	for _, entry := range keepaliveMismatchEntries() {
		t.Run(entry.name, func(t *testing.T) {
			accounts := []service.Account{keepaliveMismatchAccount(9931, 1, nil), keepaliveMismatchAccount(9932, 2, nil)}
			upstream := &keepaliveMismatchUpstream{
				bodies:    []string{keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak"), keepaliveMismatchResponsesSSE(keepaliveMismatchRequestedModel, "served")},
				gateFirst: true,
			}
			hs := newKeepaliveMismatchHarness(t, accounts, upstream)
			defer hs.stop()
			upstream.gate = hs.flushed

			rec := hs.serve(t, entry, nil)

			calls := upstream.accountCalls()
			require.Len(t, calls, 2, "被拦截后必须切到第二个账号")
			require.NotEqual(t, calls[0], calls[1], "非池模式：切号而不是同账号重试")
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.True(t, strings.HasPrefix(body, entry.wantKeepalive), "客户端先收到心跳: %q", body)
			require.Contains(t, body, "served", "内容来自第二个账号")
			require.NotContains(t, body, "leak", "被拦截账号的事件不能泄漏")
			require.NotContains(t, body, keepaliveMismatchWrongModel, "被换掉的模型名不能出现在客户端响应里")
			require.NotContains(t, body, "event: error")
			require.NotContains(t, body, "response.failed")

			logs := hs.waitUsageLogs(t, 2)
			requireKeepaliveMismatchAuditRow(t, logs, calls[0])
			realRows := 0
			for _, log := range logs {
				if !log.UpstreamModelMismatch {
					realRows++
					require.Equal(t, calls[1], log.AccountID, "真实请求行归属第二个账号")
					require.Nil(t, log.UpstreamResponseModel)
				}
			}
			require.Equal(t, 1, realRows)
		})
	}
}

// 心跳后两个账号都返回错 model → 耗尽：响应头已被心跳提交为 200，必须以流内终止事件收尾，
// 且不能泄漏任何上游事件或模型名。
func TestOpenAIGateway_KeepaliveThenUpstreamModelMismatch_ExhaustedEndsInStream(t *testing.T) {
	for _, entry := range keepaliveMismatchEntries() {
		t.Run(entry.name, func(t *testing.T) {
			accounts := []service.Account{keepaliveMismatchAccount(9941, 1, nil), keepaliveMismatchAccount(9942, 2, nil)}
			wrong := keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak")
			upstream := &keepaliveMismatchUpstream{bodies: []string{wrong, wrong}, gateFirst: true}
			hs := newKeepaliveMismatchHarness(t, accounts, upstream)
			defer hs.stop()
			upstream.gate = hs.flushed

			rec := hs.serve(t, entry, nil)

			calls := upstream.accountCalls()
			require.Len(t, calls, 2)
			require.NotEqual(t, calls[0], calls[1])
			require.Equal(t, http.StatusOK, rec.Code, "心跳已提交 200，耗尽时不能再改状态码写 JSON")
			body := rec.Body.String()
			require.True(t, strings.HasPrefix(body, entry.wantKeepalive), "客户端先收到心跳: %q", body)
			require.Contains(t, body, entry.wantTerminal, "耗尽时在同一 SSE 连接内以流内终止事件收尾")
			require.NotContains(t, body, "leak")
			// 终止事件可回显客户端自己请求的模型名（Codex canonical response.failed 带 response.model），
			// 但绝不能出现上游偷换后的模型名。
			require.NotContains(t, body, keepaliveMismatchWrongModel)

			logs := hs.waitUsageLogs(t, 2)
			for _, log := range logs {
				require.True(t, log.UpstreamModelMismatch, "两次尝试都是被拦截的审计行")
			}
			requireKeepaliveMismatchAuditRow(t, logs, calls[0])
			requireKeepaliveMismatchAuditRow(t, logs, calls[1])
		})
	}
}

func keepaliveMismatchPoolAccount(id int64) service.Account {
	return keepaliveMismatchAccount(id, 1, map[string]any{"pool_mode": true, "pool_mode_retry_count": float64(1)})
}

// 池模式：被拦截后先同账号重试一次（waitPoolModeSameAccountRetry retry 出口），重试成功则正常返回。
func TestOpenAIGateway_UpstreamModelMismatch_PoolModeRetriesSameAccount(t *testing.T) {
	for _, entry := range keepaliveMismatchEntries() {
		t.Run(entry.name, func(t *testing.T) {
			upstream := &keepaliveMismatchUpstream{
				bodies: []string{keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak"), keepaliveMismatchResponsesSSE(keepaliveMismatchRequestedModel, "served")},
			}
			hs := newKeepaliveMismatchHarness(t, []service.Account{keepaliveMismatchPoolAccount(9951)}, upstream)
			defer hs.stop()

			rec := hs.serve(t, entry, nil)

			require.Equal(t, []int64{9951, 9951}, upstream.accountCalls(), "池模式：同账号重试而不是切号")
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "served")
			require.NotContains(t, body, "leak")
			require.NotContains(t, body, keepaliveMismatchWrongModel)

			logs := hs.waitUsageLogs(t, 2)
			requireKeepaliveMismatchAuditRow(t, logs, 9951)
		})
	}
}

// 池模式：同账号重试等待期间客户端断开（waitPoolModeSameAccountRetry canceled 出口）→ 直接返回，不再重试。
func TestOpenAIGateway_UpstreamModelMismatch_PoolModeRetryCanceledByClient(t *testing.T) {
	for _, entry := range keepaliveMismatchEntries() {
		t.Run(entry.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := &keepaliveMismatchUpstream{
				bodies: []string{keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak"), keepaliveMismatchResponsesSSE(keepaliveMismatchRequestedModel, "served")},
				// 首次上游调用后 200ms 断开：拦截在首事件即刻返回，此时 handler 已进入 500ms 的同账号重试等待。
				onFirstDo: func() { time.AfterFunc(200*time.Millisecond, cancel) },
			}
			hs := newKeepaliveMismatchHarness(t, []service.Account{keepaliveMismatchPoolAccount(9961)}, upstream)
			defer hs.stop()

			rec := hs.serve(t, entry, ctx)

			require.Equal(t, []int64{9961}, upstream.accountCalls(), "等待期间断开：不再发起重试")
			require.Empty(t, rec.Body.String(), "客户端已断开，不写任何字节")
			require.Never(t, func() bool { return len(upstream.accountCalls()) > 1 }, 800*time.Millisecond, 50*time.Millisecond)
			logs := hs.waitUsageLogs(t, 1)
			requireKeepaliveMismatchAuditRow(t, logs, 9961)
		})
	}
}
