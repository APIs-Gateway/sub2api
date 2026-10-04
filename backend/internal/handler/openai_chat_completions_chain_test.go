//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// /v1/chat/completions 接入 Key 级分组回退链的端到端测试：与 openai_responses_chain_test.go 共用同一套装置
// （真实的 OpenAIGatewayService + OpenAIGatewayHandler，上游、账号仓储、链解析、熔断器、设置都是假实现），
// 只是请求打到 chat/completions 入口。
//
// 覆盖：开关关闭 / Key 没链 / 链长 < 2 / 解析出错 / 运行时未接入时与无链路径逐字节一致；
// 无号 / failover 耗尽 / 弱 429 / 繁忙时回退，served 计费列；分组层 RPM；熔断计数；
// 只写过心跳可以回退并按 chat 的流式格式收尾，真实内容已输出不回退（含部分用量照常计费）；
// 静态资格；forced 路由与管理员链；以及旧「稳定优先」在链生效时不会被触发（两者不叠加）。

const (
	chainChatBodyJSON      = `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	chainChatBodyNonStream = `{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`
)

var (
	chainChatIDPattern      = regexp.MustCompile(`"id":"chatcmpl-[0-9a-f]+"`)
	chainChatCreatedPattern = regexp.MustCompile(`"created":[0-9]+`)
)

// normalizeChainChatBody 抹掉 chat 响应里每次都会变的字段（随机 chatcmpl id、创建时间），其余逐字节比较。
func normalizeChainChatBody(body string) string {
	body = chainChatIDPattern.ReplaceAllString(body, `"id":"chatcmpl-X"`)
	return chainChatCreatedPattern.ReplaceAllString(body, `"created":0`)
}

func (hs *chainRespHarness) serveChat(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.router.ServeHTTP(rec, req)
	return rec
}

// runChainChatCase 发一次 chat/completions 请求；服务成功时等到那条 usage 行。
func runChainChatCase(t *testing.T, o chainRespOptions, body string) (chainRespOutcome, *chainRespHarness) {
	t.Helper()
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(body)
	out := chainRespOutcome{status: rec.Code, body: rec.Body.String(), calls: hs.upstream.accountCalls()}
	if rec.Code == http.StatusOK && len(out.calls) > 0 {
		out.usage = hs.waitUsage(t, 1)[0]
	}
	return out, hs
}

// chainChatStableStore 是旧「稳定优先」状态存储的假实现：只记录有没有被碰过。
// 状态始终是 normal，所以即使被调用也不会真的把请求导到别的分组。
type chainChatStableStore struct {
	mu     sync.Mutex
	gets   int
	enters int
}

func (s *chainChatStableStore) Get(context.Context, int64) (service.StablePriorityState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	return service.StablePriorityState{Mode: service.StablePriorityModeNormal}, nil
}

func (s *chainChatStableStore) TryEnterFallback(context.Context, int64, int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enters++
	return true, nil
}

func (s *chainChatStableStore) ObserveHomeHealth(context.Context, int64, bool, int64) (int64, error) {
	return 0, nil
}

func (s *chainChatStableStore) TryRevert(context.Context, int64, int64, int64, int64) (bool, error) {
	return false, nil
}

func (s *chainChatStableStore) touched() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets + s.enters
}

// ---- 无链路径逐字节一致 ----

func TestChatChain_GatedRequestsBehaveLikeLegacy(t *testing.T) {
	scenarios := []struct {
		name   string
		mutate func(*chainRespOptions)
	}{
		{"primary group serves", func(*chainRespOptions) {}},
		{"primary group has no accounts", func(o *chainRespOptions) { o.schedulable[1] = nil }},
		{"primary group upstream failure", func(o *chainRespOptions) { o.replies = map[int64]chainRespReply{11: chainRespUpstreamError()} }},
	}
	gates := []struct {
		name         string
		mutate       func(*chainRespOptions)
		wantResolves int32
	}{
		{"switch off", func(o *chainRespOptions) { o.switchOn = false }, 0},
		{"key without a chain", func(o *chainRespOptions) { o.hasRoutes = false }, 0},
		{"chain shorter than two hops", func(o *chainRespOptions) { o.hops = o.hops[:1] }, 1},
		{"chain resolution error", func(o *chainRespOptions) { o.resolveErr = fmt.Errorf("resolve failed") }, 1},
		{"fallback runtime not wired", func(o *chainRespOptions) { o.noRuntime = true }, 0},
	}
	bodies := []struct {
		name string
		body string
	}{
		{"stream", chainChatBodyJSON},
		{"non-stream", chainChatBodyNonStream},
	}
	for _, sc := range scenarios {
		for _, gate := range gates {
			for _, b := range bodies {
				t.Run(sc.name+"/"+gate.name+"/"+b.name, func(t *testing.T) {
					baselineOpts := chainRespBase()
					sc.mutate(&baselineOpts)
					baselineOpts.noRuntime = true
					baseline, _ := runChainChatCase(t, baselineOpts, b.body)

					opts := chainRespBase()
					sc.mutate(&opts)
					gate.mutate(&opts)
					got, hs := runChainChatCase(t, opts, b.body)

					require.Equal(t, baseline.status, got.status)
					require.Equal(t, normalizeChainChatBody(baseline.body), normalizeChainChatBody(got.body), "响应体与无链路径完全一致")
					require.Equal(t, baseline.calls, got.calls, "上游调用序列一致，兜底分组的账号 21 从未被碰")
					require.EqualValues(t, gate.wantResolves, hs.routes.calls.Load())
					require.Empty(t, opts.breaker.admitted(), "不进熔断")
					require.Empty(t, opts.breaker.failedGroups())
					if baseline.usage == nil {
						require.Nil(t, got.usage)
						return
					}
					require.NotNil(t, got.usage)
					require.Equal(t, baseline.usage.AccountID, got.usage.AccountID)
					require.InDelta(t, baseline.usage.RateMultiplier, got.usage.RateMultiplier, 1e-12)
					requireNoServedColumns(t, got.usage, 1)
				})
			}
		}
	}
}

func TestChatChain_LegacyPathServesFromPrimaryGroupOnly(t *testing.T) {
	// 对照基线自身的健全性：主分组有号时就是账号 11 服务，计费倍率是主分组的 1。
	o := chainRespBase()
	o.noRuntime = true
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)
	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{11}, got.calls)
	require.Contains(t, got.body, "served-by-11")
	require.InDelta(t, 1.0, got.usage.RateMultiplier, 1e-12)
	requireNoServedColumns(t, got.usage, 1)
}

// 旧「稳定优先」与回退链不叠加：链生效时每一跳都不碰稳定优先状态存储（调度器走原始选号）；
// 链没生效（开关关闭）时旧机制照旧参与。删除旧机制属于 PR5。
func TestChatChain_LegacyStablePriorityIsNotConsultedWhenTheChainRuns(t *testing.T) {
	newOpts := func(store *chainChatStableStore) chainRespOptions {
		o := chainRespBase()
		fallbackID := int64(2)
		o.primary.StablePriorityFallbackGroupID = &fallbackID
		o.stableStore = store
		o.stableKey = true
		return o
	}

	t.Run("chain active: primary serves and the old mechanism is bypassed", func(t *testing.T) {
		store := &chainChatStableStore{}
		got, _ := runChainChatCase(t, newOpts(store), chainChatBodyJSON)
		require.Equal(t, http.StatusOK, got.status)
		require.Equal(t, []int64{11}, got.calls)
		require.Zero(t, store.touched(), "有链：不读也不写稳定优先状态")
	})

	t.Run("chain active: fallback hop serves and the old mechanism is bypassed", func(t *testing.T) {
		store := &chainChatStableStore{}
		o := newOpts(store)
		o.schedulable[1] = nil
		got, _ := runChainChatCase(t, o, chainChatBodyJSON)
		require.Equal(t, http.StatusOK, got.status)
		require.Equal(t, []int64{21}, got.calls, "回退由链决定")
		require.EqualValues(t, 2, *got.usage.ServedGroupID)
		require.EqualValues(t, 1, *got.usage.GroupID)
		require.Zero(t, store.touched(), "有链：每一跳都不参与旧机制，不会既按链又按分组指针回退")
	})

	t.Run("switch off: the old mechanism still runs", func(t *testing.T) {
		store := &chainChatStableStore{}
		o := newOpts(store)
		o.switchOn = false
		got, _ := runChainChatCase(t, o, chainChatBodyJSON)
		require.Equal(t, http.StatusOK, got.status)
		require.Equal(t, []int64{11}, got.calls)
		require.Positive(t, store.touched(), "无链：稳定优先照旧参与")
		requireNoServedColumns(t, got.usage, 1)
	})
}

// ---- 兜底服务与 served 计费 ----

func TestChatChain_NoAccountFallsBackAndBillsHomeGroupWithServedColumns(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	o.audit = []int64{9999} // 审计范围不含链上任何分组：放行，但要看到审计时用的分组集合
	got, hs := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls)
	require.Contains(t, got.body, "served-by-21")
	require.NotContains(t, got.body, "event: error")

	// served 计费：group_id 是主分组，served_group_id / served_route_source 记实际服务的分组与来源，倍率按服务分组。
	require.NotNil(t, got.usage.GroupID)
	require.EqualValues(t, 1, *got.usage.GroupID)
	require.NotNil(t, got.usage.ServedGroupID)
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.NotNil(t, got.usage.ServedRouteSource)
	require.EqualValues(t, service.ServedRouteSourceUserChain, *got.usage.ServedRouteSource)
	require.InDelta(t, 2.0, got.usage.RateMultiplier, 1e-9, "定价按服务分组（倍率 2）")
	require.EqualValues(t, 21, got.usage.AccountID)

	// B1：链只解析一次；审计看到的是链上全部分组的并集，服务分组属于其中。
	require.EqualValues(t, 1, hs.routes.calls.Load())
	hs.audit.mu.Lock()
	seen := append([]securityaudit.Request(nil), hs.audit.seen...)
	hs.audit.mu.Unlock()
	require.Len(t, seen, 1)
	audited := map[int64]bool{}
	for _, g := range seen[0].ChainGroups {
		audited[g.ID] = true
	}
	require.True(t, audited[1] && audited[2])
	require.True(t, audited[*got.usage.ServedGroupID], "服务分组必须属于审计时使用的集合")
	require.EqualValues(t, 1, *seen[0].GroupID, "用户可见的分组仍是主分组")

	// 审计事件补记服务分组；ops 事件记录服务分组；池里没有账号不计熔断，只有非末跳进熔断。
	require.Equal(t, 1, hs.served.calls)
	require.EqualValues(t, 2, hs.served.groupID)
	require.EqualValues(t, 2, hs.keys[service.OpsServedGroupIDKey])
	require.Empty(t, o.breaker.failedGroups())
	require.Equal(t, []int64{1}, o.breaker.admitted())
}

func TestChatChain_NonStreamNoAccountFallsBackWithJSONResponse(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	got, _ := runChainChatCase(t, o, chainChatBodyNonStream)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls)
	require.True(t, strings.HasPrefix(strings.TrimSpace(got.body), "{"), "非流式请求收到的是 JSON 而不是 SSE: %q", got.body)
	require.Contains(t, got.body, "served-by-21")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.EqualValues(t, 1, *got.usage.GroupID)
}

// #1535：入口先去掉 model 首尾空白，链上取模型名（静态资格、取价、熔断模型族）用的是去过空白的名字。
func TestChatChain_ModelWhitespaceIsTrimmedBeforeTheChain(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	got, _ := runChainChatCase(t, o, `{"model":"  gpt-5.4 ","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "带空白的名字也能通过兜底跳的开放 / 有价检查")
	require.Equal(t, "gpt-5.4", got.usage.Model, "用量行记录的是去过空白的模型名")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
}

func TestChatChain_FailoverExhaustedFallsBackAndConfirmsBreakerFailure(t *testing.T) {
	o := chainRespBase()
	o.replies = map[int64]chainRespReply{11: chainRespUpstreamError()}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{11, 21}, got.calls)
	require.Contains(t, got.body, "served-by-21")
	require.NotContains(t, got.body, "unknown error", "主分组的上游错误不能泄漏给兜底成功的请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.Equal(t, []int64{1}, o.breaker.failedGroups(), "5xx 暂记，下一跳成功后才计入主分组的熔断")
}

// 弱 429：账号近 30 秒的 429 占比不够，429 闸门故意不换号，组内其它账号一个都没试过。
// 这不是整组耗尽的证据：请求照常回退到下一个分组，但熔断器不能有任何记录（BK-1，chat 的 failover 循环里同样有这个出口）。
func TestChatChain_Weak429OnFirstHopFallsBackWithoutBreakerCount(t *testing.T) {
	o := chainRespBase()
	// 429 闸门是进程级状态，账号号段与其它用例错开，避免互相影响。
	o.schedulable = map[int64][]service.Account{
		1: {chainRespAccount(1201)},
		2: {chainRespAccount(2201)},
	}
	o.replies = map[int64]chainRespReply{
		1201: {status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error","message":"slow down"}}`, contentType: "application/json"},
	}
	require.False(t, service.ShouldSwitchAccountOn429(1201), "前提：这个账号没有任何放行换号的 429 判定")
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{1201, 2201}, got.calls, "主分组只试了一个号就被 429 闸门停下，回退到下一个分组")
	require.Contains(t, got.body, "served-by-2201")
	require.NotContains(t, got.body, "slow down", "主分组的上游错误不能泄漏给兜底成功的请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID, "计费 served 列是下一跳")
	require.EqualValues(t, 1, *got.usage.GroupID, "group_id 仍是主分组")
	require.Empty(t, o.breaker.failedGroups(), "弱 429 不是整组耗尽的证据：不计熔断")
}

func TestChatChain_AllHopsFailEndsWithLastHopOriginalError(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"stream request", chainChatBodyJSON},
		{"non-stream request", chainChatBodyNonStream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := chainRespBase()
			o.replies = map[int64]chainRespReply{11: chainRespUpstreamError(), 21: chainRespUpstreamError()}
			got, hs := runChainChatCase(t, o, tc.body)

			require.Equal(t, []int64{11, 21}, got.calls)
			require.GreaterOrEqual(t, got.status, 500)
			require.Nil(t, got.usage)
			require.Empty(t, o.breaker.failedGroups(), "下一跳也失败：主分组暂记的 5xx 作废")
			require.EqualValues(t, 1, hs.routes.calls.Load())

			// 最终错误与「只有最后一跳那个分组、没有回退链」时一模一样，不暴露路由细节。
			baselineOpts := chainRespBase()
			baselineOpts.noRuntime = true
			baselineOpts.schedulable = map[int64][]service.Account{1: {chainRespAccount(21)}}
			baselineOpts.replies = map[int64]chainRespReply{21: chainRespUpstreamError()}
			baseline, _ := runChainChatCase(t, baselineOpts, tc.body)
			require.Equal(t, baseline.status, got.status)
			require.Equal(t, baseline.body, got.body)
		})
	}
}

func TestChatChain_AuditHitOnFallbackGroupBlocksBeforeAnyAttempt(t *testing.T) {
	o := chainRespBase()
	o.audit = []int64{2} // 只有兜底分组在审计范围内
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_error")
	require.Empty(t, hs.upstream.accountCalls(), "审计拦截即终止，不会去试任何一跳")
	require.EqualValues(t, 1, hs.routes.calls.Load(), "链在审计之前只解析一次")
	require.Equal(t, 0, hs.served.calls)
}

// ---- 分组层 RPM ----

func TestChatChain_FirstHopRPMExceededReturns429WithoutFallback(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 1), chainRespGroup(2, 2, 0))
	o.schedulable = map[int64][]service.Account{1: {chainRespAccount(11)}, 2: {chainRespAccount(21)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Empty(t, hs.upstream.accountCalls(), "首跳超限：429，不回退")
	require.EqualValues(t, 0, hs.rpm.decr, "首跳超限保持计数，与无链现状一致")
	require.Empty(t, o.breaker.admitted())
}

func TestChatChain_NonFirstHopRPMExceededIsSkipped(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 0), chainRespGroup(2, 2, 1), chainRespGroup(3, 3, 0))
	o.schedulable = map[int64][]service.Account{2: {chainRespAccount(21)}, 3: {chainRespAccount(31)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	got, hs := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{31}, got.calls, "第 2 个分组超限被跳过，由第 3 个分组服务")
	require.EqualValues(t, 3, *got.usage.ServedGroupID)
	require.EqualValues(t, 1, hs.rpm.decr, "被跳过的这一跳不占用 RPM 额度")
}

func TestChatChain_LastHopRPMExceededWritesRateLimitError(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 0), chainRespGroup(2, 2, 1))
	o.schedulable = map[int64][]service.Account{2: {chainRespAccount(21)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, http.StatusTooManyRequests, rec.Code, "末跳超限：429 覆盖主分组暂存的没号错误")
	require.Empty(t, hs.upstream.accountCalls())
	require.EqualValues(t, 1, hs.rpm.decr)
}

// ---- 熔断计数 ----

func TestChatChain_BusyHopFallsBackWithoutBreakerCount(t *testing.T) {
	o := chainRespBase()
	o.busy = map[int64]bool{11: true}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "主分组的号一直拿不到槽：短等后换组，从未向上游发请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.Empty(t, o.breaker.failedGroups(), "繁忙不计熔断")
}

func TestChatChain_NoAccountCountsTowardBreakerOnlyWhenPoolHasSchedulableAccounts(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	// 池里配置了可用账号（诊断看得到），但此刻选不出号：这是容量问题，计入熔断。
	o.configured = map[int64][]service.Account{1: {chainRespAccount(11)}}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls)
	require.Equal(t, []int64{1}, o.breaker.failedGroups())
}

// ---- 输出状态：心跳 / 真实内容 ----

func TestChatChain_HeartbeatOnlyThenFailureFallsBackToNextGroup(t *testing.T) {
	o := chainRespBase()
	o.keepalive = 1
	o.replies = map[int64]chainRespReply{
		11: {body: keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak"), gated: true},
	}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, []int64{11, 21}, hs.upstream.accountCalls())
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, keepaliveMismatchSSEComment), "客户端先收到心跳: %q", body)
	require.Contains(t, body, "served-by-21", "只写过心跳不算内容交付：可以换组")
	require.NotContains(t, body, "leak")
	require.NotContains(t, body, keepaliveMismatchWrongModel)
	require.NotContains(t, body, "event: error")

	logs := hs.waitUsage(t, 2)
	served := 0
	for _, log := range logs {
		if log.UpstreamModelMismatch {
			require.EqualValues(t, 11, log.AccountID, "被拦截的尝试落一条零计费审计行")
			continue
		}
		served++
		require.EqualValues(t, 21, log.AccountID)
		require.NotNil(t, log.ServedGroupID)
		require.EqualValues(t, 2, *log.ServedGroupID)
	}
	require.Equal(t, 1, served)
}

// chat 的流式错误收尾是 `event: error`（Responses 是 `response.failed`），以 chat 现有的写法为准。
func TestChatChain_HeartbeatOnlyAllGroupsFailEndsInStreamFormat(t *testing.T) {
	o := chainRespBase()
	o.keepalive = 1
	wrong := keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak")
	o.replies = map[int64]chainRespReply{11: {body: wrong, gated: true}, 21: {body: wrong, gated: true}}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, []int64{11, 21}, hs.upstream.accountCalls())
	require.Equal(t, http.StatusOK, rec.Code, "心跳已提交 200，最终错误不能再改状态码写 JSON")
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, keepaliveMismatchSSEComment), "客户端先收到心跳: %q", body)
	require.Contains(t, body, "event: error", "耗尽时在同一 SSE 连接内以流内终止事件收尾")
	require.NotContains(t, body, "leak")
	require.NotContains(t, body, keepaliveMismatchWrongModel)
	hs.waitUsage(t, 2)
}

// 主分组的号已经把真实内容写给客户端、随后流中断：不换号也不换组，已输出内容的用量照常计费到主分组。
func TestChatChain_RealContentAlreadyWrittenDoesNotFallBack(t *testing.T) {
	o := chainRespBase()
	// 走 chat 直转（不转 Responses）的账号：上游吐出一段内容后以 error 帧结束，帧里带已计量的用量。
	rawAccount := chainRespAccount(11)
	rawAccount.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
	o.schedulable[1] = []service.Account{rawAccount}
	o.replies = map[int64]chainRespReply{11: {body: "data: {\"id\":\"chatcmpl_partial\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial output\"}}]}\n\n" +
		"event: error\n" +
		"data: {\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream interrupted\"},\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n"}}
	hs := newChainRespHarness(t, o)
	rec := hs.serveChat(chainChatBodyJSON)

	require.Equal(t, []int64{11}, hs.upstream.accountCalls(), "真实内容已写给客户端：不换组，账号 21 不会被碰")
	body := rec.Body.String()
	require.Contains(t, body, "partial output")
	require.NotContains(t, body, "served-by-21")
	require.Empty(t, o.breaker.failedGroups())

	usage := hs.waitUsage(t, 1)[0]
	require.EqualValues(t, 11, usage.AccountID, "已输出内容的部分用量计到服务它的账号")
	requireNoServedColumns(t, usage, 1)
}

// ---- 静态资格 ----

func TestChatChain_UnpricedModelSkipsFallbackHop(t *testing.T) {
	const unpriced = `{"model":"pricing-missing-test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`

	o := chainRespBase()
	o.schedulable[1] = nil
	got, hs := runChainChatCase(t, o, unpriced)

	require.NotEqual(t, http.StatusOK, got.status)
	require.Empty(t, got.calls, "没配价格的模型会按零成本放行，兜底分组不能白送：这一跳被跳过")
	require.EqualValues(t, 1, hs.routes.calls.Load())

	// 被跳过之后客户端收到的就是主分组原本的错误。
	baselineOpts := chainRespBase()
	baselineOpts.noRuntime = true
	baselineOpts.schedulable[1] = nil
	baseline, _ := runChainChatCase(t, baselineOpts, unpriced)
	require.Equal(t, baseline.status, got.status)
	require.Equal(t, baseline.body, got.body)
}

// 资格检查里的取价不算结算：对无价模型反复检查，无价计数（只属于真正写用量行的结算）不能增加。
func TestChatChain_EligibilityPricingCheckDoesNotCountUnpricedBilling(t *testing.T) {
	const unpriced = `{"model":"pricing-missing-test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	total := func() int64 {
		var sum int64
		for _, counter := range service.UnpricedBillingCounterSnapshot() {
			if counter.Model == "pricing-missing-test-model" {
				sum += counter.Count
			}
		}
		return sum
	}

	o := chainRespBase()
	o.schedulable[1] = nil
	before := total()
	got, _ := runChainChatCase(t, o, unpriced)
	require.NotEqual(t, http.StatusOK, got.status)
	require.Empty(t, got.calls, "兜底跳因未定价被跳过，没有任何结算")
	require.Equal(t, before, total(), "资格检查的取价调用带非结算标记，不产生无价计数")
}

// ---- forced 账号路由（设计 6.3） ----

func TestChatChain_ForcedUserWithoutHiddenChainKeepsForcedRouting(t *testing.T) {
	o := chainRespBase()
	o.forced = []config.OpenAIForcedAccountRoute{{UserID: chainRespUserID, AccountID: 21}}
	got, hs := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "forced 路由照旧生效")
	requireNoServedColumns(t, got.usage, 1)
	require.Empty(t, o.breaker.admitted(), "链层对 forced 用户短路：不进逐跳循环")
	require.EqualValues(t, 1, hs.routes.calls.Load())
}

func TestChatChain_ForcedUserWithHiddenHeadYieldsToTheChain(t *testing.T) {
	g1 := chainRespGroup(1, 1, 0)
	g9 := chainRespGroup(9, 3, 0)
	o := chainRespBase()
	o.primary = g1
	o.hops = []service.ChainHop{
		{GroupID: 9, Group: g9, RouteSource: service.RouteSourceAdmin},
		{GroupID: 1, Group: g1, RouteSource: service.RouteSourcePrimary},
	}
	o.schedulable = map[int64][]service.Account{9: {chainRespAccount(91)}, 1: {chainRespAccount(11)}}
	o.forced = []config.OpenAIForcedAccountRoute{{UserID: chainRespUserID, AccountID: 11}}
	got, _ := runChainChatCase(t, o, chainChatBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{91}, got.calls, "有管理员隐藏链：forced 路由让位，第 0 跳走标准调度")
	require.EqualValues(t, 1, *got.usage.GroupID, "group_id 仍是用户 Key 的主分组")
	require.NotNil(t, got.usage.ServedGroupID)
	require.EqualValues(t, 9, *got.usage.ServedGroupID)
	require.NotNil(t, got.usage.ServedRouteSource)
	require.EqualValues(t, service.ServedRouteSourceAdminChain, *got.usage.ServedRouteSource)
	require.InDelta(t, 3.0, got.usage.RateMultiplier, 1e-9)
}
