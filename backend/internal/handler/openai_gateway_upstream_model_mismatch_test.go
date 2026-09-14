package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fakeUpstreamModelMismatchRecorder 替代 *service.OpenAIGatewayService 接收审计行调用，
// 每次调用往 done 推一个信号，测试据此等待异步 goroutine。
type fakeUpstreamModelMismatchRecorder struct {
	mu    sync.Mutex
	calls []service.UpstreamModelMismatchUsageInput
	done  chan struct{}
}

func newFakeUpstreamModelMismatchRecorder() *fakeUpstreamModelMismatchRecorder {
	return &fakeUpstreamModelMismatchRecorder{done: make(chan struct{}, 8)}
}

func (f *fakeUpstreamModelMismatchRecorder) RecordUpstreamModelMismatchUsageLog(_ context.Context, in service.UpstreamModelMismatchUsageInput) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	f.done <- struct{}{}
}

func (f *fakeUpstreamModelMismatchRecorder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeUpstreamModelMismatchRecorder) waitOne(t *testing.T) service.UpstreamModelMismatchUsageInput {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordUpstreamModelMismatchUsageLog was not called within 2s")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.calls, 1)
	return f.calls[0]
}

// waitNth 等待第 n 次（从 1 起）异步调用完成并返回其入参；前 n-1 次必须已经由之前的 waitNth 消费。
func (f *fakeUpstreamModelMismatchRecorder) waitNth(t *testing.T, n int) service.UpstreamModelMismatchUsageInput {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("RecordUpstreamModelMismatchUsageLog call #%d was not observed within 2s", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.calls, n)
	return f.calls[n-1]
}

func newUpstreamModelMismatchTestContext(t *testing.T) *gin.Context {
	t.Helper()
	c := newTestGinContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{}`))
	c.Request.Header.Set("User-Agent", "codex-cli/1.0")
	c.Writer.Header().Set("X-Request-Id", "req-mismatch-1")
	return c
}

func upstreamModelMismatchTestFixtures() (*service.APIKey, *service.Account, *service.UserSubscription) {
	apiKey := &service.APIKey{ID: 7, GroupID: nil, User: &service.User{ID: 3}}
	account := &service.Account{ID: 12, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	sub := &service.UserSubscription{ID: 99}
	return apiKey, account, sub
}

func requireNeverRecorded(t *testing.T, rec *fakeUpstreamModelMismatchRecorder, above int, msg string) {
	t.Helper()
	require.Never(t, func() bool { return rec.callCount() > above }, 100*time.Millisecond, 10*time.Millisecond, msg)
}

// 未打标：不调用 gatewayService，也不改 context。
func TestRecordUpstreamModelMismatchIfMarked_NoMarkNoop(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "hash-1", nil)

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	requireNeverRecorded(t, rec, 0, "gatewayService must not be called when no mark is present")
}

// 打标且 Blocked=true（本次尝试被拦截走 failover）：异步调用一次，入参 Mark/元数据正确，随后标记被清。
func TestRecordUpstreamModelMismatchIfMarked_RecordsAndClears(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	mark := service.UpstreamModelMismatchMark{
		SentModel:     "gpt-5",
		ResponseModel: "gpt-4.1-mini",
		AccountID:     account.ID,
		Stream:        true,
		Blocked:       true,
		Usage:         service.OpenAIUsage{InputTokens: 11, OutputTokens: 2},
	}
	service.MarkOpsUpstreamModelMismatch(c, mark)
	channelFields := service.ChannelUsageFields{}

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", channelFields, "hash-2", nil)

	// 清标必须是同步的：failover 换号后的下一次 Forward 要能重新打标。
	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))

	in := rec.waitOne(t)
	require.Equal(t, mark, in.Mark)
	require.Same(t, apiKey, in.APIKey)
	require.Same(t, account, in.Account)
	require.Same(t, sub, in.Subscription)
	require.Equal(t, "gpt-5", in.Model)
	require.Equal(t, "req-mismatch-1", in.RequestID)
	require.Equal(t, "hash-2", in.RequestPayloadHash)
	require.Equal(t, "codex-cli/1.0", in.UserAgent)
	require.Equal(t, EndpointResponses, in.InboundEndpoint)
	require.Equal(t, resolveOpenAIUpstreamEndpoint(c, account), in.UpstreamEndpoint)
	require.Equal(t, channelFields, in.ChannelUsageFields)

	// 标记已清：再调一次不会再记（每次拦截只落一行）。
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", channelFields, "hash-2", nil)
	requireNeverRecorded(t, rec, 1, "cleared mark must not be recorded again")
}

// 打标但 Blocked=false（观察模式放行 / 客户端已收到输出无法拦截）：
// 即使随后 forward 因其他原因报错，也不记审计行，只清标；B 由成功/部分结果路径的 RecordUsage 落库。
func TestRecordUpstreamModelMismatchIfMarked_UnblockedMarkDoesNotRecord(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{
		SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: account.ID, Blocked: false,
	})

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "hash-3", nil)

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	requireNeverRecorded(t, rec, 0, "unblocked (observe-mode) mark must not produce an audit row")
}

// Blocked=true 但缺 apiKey/account（无法归属计费主体）：只清标，不记。
func TestRecordUpstreamModelMismatchIfMarked_MissingSubjectClearsOnly(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	_, account, _ := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", Blocked: true})

	recordUpstreamModelMismatchIfMarked(c, rec, nil, nil, account, nil, "gpt-5", service.ChannelUsageFields{}, "", nil)

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	requireNeverRecorded(t, rec, 0, "missing apiKey must not record")
}

// handler 方法包装：gatewayService 为 nil 时不 panic，且仍然清标（照 cyber 测试的 nil services 约定）。
func TestOpenAIGatewayHandler_RecordUpstreamModelMismatchIfMarked_NilServicesClearMark(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", Blocked: true})

	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.recordUpstreamModelMismatchIfMarked(c, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "", nil)
	})
	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
}

// 池模式同账号重试：同一请求内同一账号连续两次被拦截，两行审计 Attempt 依次为 0、1
// （service 侧据此给 request_id 加 ":<n>" 后缀，避免撞唯一索引被静默丢弃）；换到另一账号后从 0 重新计数。
func TestRecordUpstreamModelMismatchIfMarked_SameAccountRetriesCountAttempts(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	mark := func(acc *service.Account) service.UpstreamModelMismatchMark {
		return service.UpstreamModelMismatchMark{SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: acc.ID, Blocked: true}
	}

	service.MarkOpsUpstreamModelMismatch(c, mark(account))
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	first := rec.waitNth(t, 1)
	require.Equal(t, 0, first.Attempt, "首次拦截 Attempt=0，request_id 保持原格式")
	require.Same(t, account, first.Account)

	// 同账号重试后再次被拦截（handler 的 pool-mode continue 分支不换号）。
	service.MarkOpsUpstreamModelMismatch(c, mark(account))
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	second := rec.waitNth(t, 2)
	require.Equal(t, 1, second.Attempt, "同账号第二次被拦截 Attempt=1")
	require.Same(t, account, second.Account)
	require.Equal(t, first.RequestID, second.RequestID, "同一请求：基础 request_id 相同，靠 Attempt 区分")

	// 切到另一账号：计数按账号隔离，从 0 开始。
	other := &service.Account{ID: 13, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	service.MarkOpsUpstreamModelMismatch(c, mark(other))
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, other, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	third := rec.waitNth(t, 3)
	require.Equal(t, 0, third.Attempt, "换账号后从 0 重新计数")
	require.Same(t, other, third.Account)

	// 再回到原账号（理论上不会发生，但计数必须继续而不是重置，保证键不撞）。
	service.MarkOpsUpstreamModelMismatch(c, mark(account))
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	fourth := rec.waitNth(t, 4)
	require.Equal(t, 2, fourth.Attempt)
}

// 未落审计行的调用（无标记 / Blocked=false / 缺主体）不消耗计数：下一次真正落行仍是 Attempt=0。
func TestRecordUpstreamModelMismatchIfMarked_AttemptCountsOnlyRecordedRows(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", AccountID: account.ID, Blocked: false})
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", AccountID: account.ID, Blocked: true})
	recordUpstreamModelMismatchIfMarked(c, rec, nil, nil, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	requireNeverRecorded(t, rec, 0, "none of the above should record")

	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", AccountID: account.ID, Blocked: true})
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
	require.Equal(t, 0, rec.waitNth(t, 1).Attempt)
}

// 流式拦截发生在 response.created（不带 usage）时 Mark.Usage 全零：按请求体估算 input_tokens，
// 让审计行 token 列反映已发出的 prompt；上游已回报 usage 时原样保留，不覆盖。
func TestRecordUpstreamModelMismatchIfMarked_EstimatesInputTokensWhenUsageMissing(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("abcd", 50) + `"}]}`)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()

	t.Run("zero usage -> estimated from body", func(t *testing.T) {
		c := newUpstreamModelMismatchTestContext(t)
		rec := newFakeUpstreamModelMismatchRecorder()
		service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: account.ID, Blocked: true})
		recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", body)
		in := rec.waitNth(t, 1)
		require.Equal(t, service.EstimateOpenAIRequestInputTokens(body), in.Mark.Usage.InputTokens)
		require.Greater(t, in.Mark.Usage.InputTokens, 0)
		require.Zero(t, in.Mark.Usage.OutputTokens, "只估输入侧")
	})

	t.Run("upstream usage present -> kept", func(t *testing.T) {
		c := newUpstreamModelMismatchTestContext(t)
		rec := newFakeUpstreamModelMismatchRecorder()
		service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{
			SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: account.ID, Blocked: true,
			Usage: service.OpenAIUsage{InputTokens: 596, OutputTokens: 5},
		})
		recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", body)
		in := rec.waitNth(t, 1)
		require.Equal(t, 596, in.Mark.Usage.InputTokens)
		require.Equal(t, 5, in.Mark.Usage.OutputTokens)
	})

	t.Run("zero usage and empty body -> stays zero", func(t *testing.T) {
		c := newUpstreamModelMismatchTestContext(t)
		rec := newFakeUpstreamModelMismatchRecorder()
		service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: account.ID, Blocked: true})
		recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", service.ChannelUsageFields{}, "h", nil)
		require.Zero(t, rec.waitNth(t, 1).Mark.Usage.InputTokens)
	})
}

// upstreamModelMismatchFailoverErr 按 service.checkUpstreamModelMismatch 的形态构造被拦截后的 failover 错误
// （service 侧单测已断言 body 结构；这里复用同一常量保证口径一致）。
func upstreamModelMismatchFailoverErr() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    []byte(`{"error":{"type":"upstream_error","code":"upstream_model_mismatch","message":"` + service.UpstreamModelMismatchClientMessage + `"}}`),
		ResponseHeaders: http.Header{"X-Request-Id": []string{"one-9"}},
	}
}

func requireNoUpstreamModelMismatchInternals(t *testing.T, body string) {
	t.Helper()
	require.NotContains(t, body, "gpt-6-astra", "不能泄露发给上游的模型名")
	require.NotContains(t, body, "gpt-5.6-terra", "不能泄露上游偷换后的模型名")
	require.NotContains(t, body, "sent=")
	require.NotContains(t, body, "got=")
	require.NotContains(t, body, "different model")
	require.NotContains(t, body, "one-9", "不能泄露上游 request id")
}

// 端到端出口：被拦截且无可切账号 → handleFailoverExhausted。三种入站的最终响应都不含内部名词；
// ops 上游错误记录（内部）仍保留。
func TestOpenAIHandleFailoverExhausted_UpstreamModelMismatchClientMessageGeneric(t *testing.T) {
	t.Run("responses route (codex canonical) 502", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, upstreamModelMismatchFailoverErr(), false)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		body := rec.Body.String()
		requireNoUpstreamModelMismatchInternals(t, body)
		msg := gjson.Get(body, "error.message").String()
		require.NotContains(t, strings.ToLower(msg), "upstream")
		require.NotContains(t, strings.ToLower(msg), "model")
	})

	t.Run("chat completions route 502 default mapping", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, upstreamModelMismatchFailoverErr(), false)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		body := rec.Body.String()
		requireNoUpstreamModelMismatchInternals(t, body)
		require.Equal(t, "Upstream service temporarily unavailable", gjson.Get(body, "error.message").String(), "沿用产品既有 502 默认文案")
	})

	t.Run("anthropic messages route 502", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/messages")
		(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, upstreamModelMismatchFailoverErr(), false)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		requireNoUpstreamModelMismatchInternals(t, rec.Body.String())
	})

	t.Run("responses stream started -> response.failed without model names", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/responses")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, upstreamModelMismatchFailoverErr(), true)
		body := rec.Body.String()
		require.Contains(t, body, "event: response.failed")
		requireNoUpstreamModelMismatchInternals(t, body)
		data := body[len("event: response.failed\ndata: "):]
		require.Equal(t, "response.failed", gjson.Get(data, "type").String())
		require.Equal(t, "failed", gjson.Get(data, "response.status").String())
		require.NotContains(t, strings.ToLower(gjson.Get(data, "response.error.message").String()), "upstream")
	})

	t.Run("chat stream started -> event: error without model names", func(t *testing.T) {
		c, rec := newCodexTestCtx("/v1/chat/completions")
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, upstreamModelMismatchFailoverErr(), true)
		body := rec.Body.String()
		require.Contains(t, body, "event: error")
		requireNoUpstreamModelMismatchInternals(t, body)
	})

	t.Run("passthrough-body rule exposes only the generic message", func(t *testing.T) {
		// 管理员配「透传 body」规则时，客户端拿到的就是 ResponseBody.message 原文——必须已是笼统文案。
		c, rec := newCodexTestCtx("/v1/chat/completions")
		status, errType, msg := service.ResolveUpstreamErrorResponse(c, service.PlatformOpenAI, http.StatusBadGateway, upstreamModelMismatchFailoverErr().ResponseBody)
		require.Equal(t, http.StatusBadGateway, status)
		require.Equal(t, "upstream_error", errType)
		require.Equal(t, "Upstream service temporarily unavailable", msg)
		require.Equal(t, service.UpstreamModelMismatchClientMessage, service.ExtractUpstreamErrorMessage(upstreamModelMismatchFailoverErr().ResponseBody))
		_ = rec
	})
}
