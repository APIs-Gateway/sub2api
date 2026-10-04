//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// /v1/chat/completions 回退链的边界用例：客户端断开、换号 / 尝试次数上限、池模式同账号重试、
// 上游 4xx 的终止语义，以及旧「稳定优先」在没有链时仍按原样沿分组指针兜底。
// 与 openai_chat_completions_chain_test.go 共用同一套装置。

// serveChatCtx 与 serveChat 相同，但请求带调用方给的 ctx（用来模拟客户端断开）。
func (hs *chainRespHarness) serveChatCtx(ctx context.Context, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.router.ServeHTTP(rec, req)
	return rec
}

func (s *chainChatStableStore) entered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enters
}

// chainChatGroupRepo 只实现旧稳定优先沿分组指针解析兜底档位用到的 GetByIDLite。
type chainChatGroupRepo struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r *chainChatGroupRepo) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	group, ok := r.groups[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	cp := *group
	return &cp, nil
}

// chainChatPoolAccount 是池模式账号：命中可重试状态码时先在同一账号上重试 1 次，再谈换号。
func chainChatPoolAccount(id int64) service.Account {
	return keepaliveMismatchAccount(id, 1, map[string]any{"pool_mode": true, "pool_mode_retry_count": 1})
}

func chainChatRateLimited() chainRespReply {
	return chainRespReply{status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error","message":"slow down"}}`, contentType: "application/json"}
}

// 上游处理期间客户端断开：不换号、不换组、不记熔断，也不会再向兜底分组发请求。
func TestChatChain_ClientGoneAfterUpstreamFailureStopsWithoutFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := chainRespBase()
	reply := chainRespUpstreamError()
	reply.onDo = cancel
	o.replies = map[int64]chainRespReply{11: reply}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChatCtx(ctx, chainChatBodyJSON)

	require.Equal(t, []int64{11}, hs.upstream.accountCalls(), "客户端已断开：不换号，也不换组")
	require.NotContains(t, rec.Body.String(), "served-by-21")
	require.Empty(t, o.breaker.failedGroups(), "客户端断开不是分组故障的证据")
}

// 单跳换号上限：主分组两个号都失败、上限为 1 次换号时，这一跳在第二个号失败后结束，回退到下一个分组。
func TestChatChain_PerHopSwitchLimitFallsBackToNextGroup(t *testing.T) {
	o := chainRespBase()
	o.maxSwitches = 1
	o.schedulable[1] = []service.Account{chainRespAccount(11), chainRespAccount(12)}
	o.replies = map[int64]chainRespReply{11: chainRespUpstreamError(), 12: chainRespUpstreamError()}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Len(t, got.calls, 3)
	require.ElementsMatch(t, []int64{11, 12}, got.calls[:2], "主分组的两个号各试一次，换号上限用完")
	require.EqualValues(t, 21, got.calls[2], "之后由兜底分组服务")
	require.Contains(t, got.body, "served-by-21")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.Equal(t, []int64{1}, o.breaker.failedGroups(), "换号用尽是整组耗尽的证据：计入主分组熔断")
}

// 每请求上游尝试总次数用完：本跳不再换号，也不会进入下一个分组，客户端收到主分组原样的失败。
func TestChatChain_TotalAttemptBudgetStopsTheChain(t *testing.T) {
	o := chainRespBase()
	o.settings = func(s *service.GroupFallbackSettings) { s.MaxTotalAttempts = 1 }
	o.schedulable[1] = []service.Account{chainRespAccount(11), chainRespAccount(12)}
	o.replies = map[int64]chainRespReply{11: chainRespUpstreamError(), 12: chainRespUpstreamError()}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Len(t, hs.upstream.accountCalls(), 1, "总尝试次数只有 1：既不换号，也不去下一个分组")
	require.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
	require.NotContains(t, rec.Body.String(), "served-by-21")
}

// 池模式账号命中可重试状态码：先在同一账号上重试，重试用完后弱 429 / 耗尽照常回退到下一个分组。
func TestChatChain_PoolModeRetriesSameAccountBeforeFallingBack(t *testing.T) {
	o := chainRespBase()
	// 429 闸门是进程级状态，账号号段与其它用例错开。
	o.schedulable = map[int64][]service.Account{
		1: {chainChatPoolAccount(1301)},
		2: {chainRespAccount(2301)},
	}
	o.replies = map[int64]chainRespReply{1301: chainChatRateLimited()}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{1301, 1301, 2301}, got.calls, "同一账号重试 1 次之后才回退")
	require.Contains(t, got.body, "served-by-2301")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
}

func TestChatChain_PoolModeRetryRespectsTheTotalAttemptBudget(t *testing.T) {
	o := chainRespBase()
	o.settings = func(s *service.GroupFallbackSettings) { s.MaxTotalAttempts = 1 }
	o.schedulable = map[int64][]service.Account{
		1: {chainChatPoolAccount(1302)},
		2: {chainRespAccount(2302)},
	}
	o.replies = map[int64]chainRespReply{1302: chainChatRateLimited()}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, []int64{1302}, hs.upstream.accountCalls(), "总尝试次数用完：不再重试，也不回退")
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "served-by-2302")
}

// 上游 4xx（非 failover 类）：这一跳终止，不换号也不换组，客户端收到的错误与无链路径完全一致。
func TestChatChain_UpstreamClientErrorIsTerminalAndMatchesLegacy(t *testing.T) {
	replies := map[int64]chainRespReply{11: {
		status:      http.StatusBadRequest,
		body:        `{"error":{"message":"bad request","type":"invalid_request_error"}}`,
		contentType: "application/json",
	}}
	o := chainRespBase()
	o.replies = replies
	got, hs := runChainChatCase(t, o, chainChatBodyJSON)

	baselineOpts := chainRespBase()
	baselineOpts.noRuntime = true
	baselineOpts.replies = replies
	baseline, _ := runChainChatCase(t, baselineOpts, chainChatBodyJSON)

	require.Equal(t, []int64{11}, got.calls, "非 failover 类错误不触发回退，账号 21 不会被碰")
	require.NotEqual(t, http.StatusOK, got.status)
	require.Nil(t, got.usage)
	require.Equal(t, baseline.status, got.status)
	require.Equal(t, baseline.body, got.body)
	require.Empty(t, o.breaker.failedGroups())
	require.EqualValues(t, 1, hs.routes.calls.Load())
}

// 没有链（开关关闭）时旧「稳定优先」照旧：主分组没号，沿分组指针兜底到下一档并进入兜底态；
// 用量行只写旧机制的列（group_id 为主分组、没有 served 列）。
func TestChatChain_LegacyStablePriorityStillFallsBackWhenTheChainIsOff(t *testing.T) {
	store := &chainChatStableStore{}
	o := chainRespBase()
	o.switchOn = false
	fallbackID := int64(2)
	o.primary.StablePriorityFallbackGroupID = &fallbackID
	o.stableStore = store
	o.stableKey = true
	o.groupRepo = &chainChatGroupRepo{groups: map[int64]*service.Group{2: chainRespGroup(2, 2, 0)}}
	o.schedulable[1] = nil
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "旧机制：沿分组指针兜底到分组 2 的账号")
	require.Equal(t, 1, store.entered(), "进入兜底态")
	requireNoServedColumns(t, got.usage, 1)
}
