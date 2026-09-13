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

// 未打标：不调用 gatewayService，也不改 context。
func TestRecordUpstreamModelMismatchIfMarked_NoMarkNoop(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", true, service.ChannelUsageFields{}, "hash-1")

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	require.Never(t, func() bool { return rec.callCount() > 0 }, 100*time.Millisecond, 10*time.Millisecond,
		"gatewayService must not be called when no mark is present")
}

// 打标 + forward 报错（被拦截走 failover）：异步调用一次，入参 Mark/元数据正确，随后标记被清。
func TestRecordUpstreamModelMismatchIfMarked_RecordsAndClears(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	mark := service.UpstreamModelMismatchMark{
		SentModel:     "gpt-5",
		ResponseModel: "gpt-4.1-mini",
		AccountID:     account.ID,
		Stream:        true,
		Usage:         service.OpenAIUsage{InputTokens: 11, OutputTokens: 2},
	}
	service.MarkOpsUpstreamModelMismatch(c, mark)
	channelFields := service.ChannelUsageFields{}

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", true, channelFields, "hash-2")

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
	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", true, channelFields, "hash-2")
	require.Never(t, func() bool { return rec.callCount() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
}

// 打标但 forward 成功（观察模式放行 / 客户端已收到输出无法拦截）：
// B 由成功路径的 RecordUsage 落库，这里不记审计行，只清标。
func TestRecordUpstreamModelMismatchIfMarked_ObserveModeSuccessDoesNotRecord(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{
		SentModel: "gpt-5", ResponseModel: "gpt-4.1-mini", AccountID: account.ID,
	})

	recordUpstreamModelMismatchIfMarked(c, rec, nil, apiKey, account, sub, "gpt-5", false, service.ChannelUsageFields{}, "hash-3")

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	require.Never(t, func() bool { return rec.callCount() > 0 }, 100*time.Millisecond, 10*time.Millisecond,
		"observe-mode success must not produce an audit row")
}

// 打标 + forward 报错但缺 apiKey/account（无法归属计费主体）：只清标，不记。
func TestRecordUpstreamModelMismatchIfMarked_MissingSubjectClearsOnly(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	_, account, _ := upstreamModelMismatchTestFixtures()
	rec := newFakeUpstreamModelMismatchRecorder()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b"})

	recordUpstreamModelMismatchIfMarked(c, rec, nil, nil, account, nil, "gpt-5", true, service.ChannelUsageFields{}, "")

	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
	require.Never(t, func() bool { return rec.callCount() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
}

// handler 方法包装：gatewayService 为 nil 时不 panic，且仍然清标（照 cyber 测试的 nil services 约定）。
func TestOpenAIGatewayHandler_RecordUpstreamModelMismatchIfMarked_NilServicesClearMark(t *testing.T) {
	c := newUpstreamModelMismatchTestContext(t)
	apiKey, account, sub := upstreamModelMismatchTestFixtures()
	service.MarkOpsUpstreamModelMismatch(c, service.UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b"})

	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.recordUpstreamModelMismatchIfMarked(c, apiKey, account, sub, "gpt-5", true, service.ChannelUsageFields{}, "")
	})
	require.Nil(t, service.GetOpsUpstreamModelMismatch(c))
}
