//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

func runnerTestChain(ids ...int64) []ChainHop {
	hops := make([]ChainHop, 0, len(ids))
	for i, id := range ids {
		source := RouteSourceUser
		if i == 0 {
			source = RouteSourcePrimary
		}
		hops = append(hops, ChainHop{
			GroupID:     id,
			Group:       &Group{ID: id, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true},
			RouteSource: source,
		})
	}
	return hops
}

type fakeBreakerFailure struct {
	key   BreakerKey
	user  int64
	probe string
}

type fakeBreakerGate struct {
	admissions map[int64]BreakerAdmission
	admitted   []BreakerKey
	failures   []fakeBreakerFailure
	successes  []string
	releases   []string
}

func (g *fakeBreakerGate) Admit(_ context.Context, key BreakerKey, _ BreakerConfig) BreakerAdmission {
	g.admitted = append(g.admitted, key)
	if adm, ok := g.admissions[key.GroupID]; ok {
		return adm
	}
	return BreakerAdmission{Allowed: true, State: BreakerStateClosed}
}

func (g *fakeBreakerGate) RecordFailure(_ context.Context, key BreakerKey, _ BreakerConfig, userID int64, probeToken string) {
	g.failures = append(g.failures, fakeBreakerFailure{key: key, user: userID, probe: probeToken})
}

func (g *fakeBreakerGate) RecordProbeSuccess(_ context.Context, _ BreakerKey, _ BreakerConfig, probeToken string) {
	g.successes = append(g.successes, probeToken)
}

func (g *fakeBreakerGate) ReleaseProbe(_ context.Context, _ BreakerKey, probeToken string) {
	g.releases = append(g.releases, probeToken)
}

func (g *fakeBreakerGate) failedGroups() []int64 {
	out := make([]int64, 0, len(g.failures))
	for _, f := range g.failures {
		out = append(out, f.key.GroupID)
	}
	return out
}

// hopScript 描述 fake attempt 在某个分组上的行为。
type hopScript struct {
	done     bool       // 成功
	failure  HopFailure // 否则用 ClassifyHopFailure 分类
	attempts int        // 本跳的上游尝试次数
	skip     bool       // 返回 Skipped
	skipErr  bool       // Skipped 时带 WriteFinalError
	onCall   func()     // 调用时的副作用（推进时钟、写心跳等）
	override *HopResult // 直接返回该结果（用于构造协议边界）
}

type fakeAttempt struct {
	scripts map[int64]hopScript
	calls   []HopInfo
	written []string
}

func (f *fakeAttempt) fn() ChainHopAttempt {
	return func(_ context.Context, info HopInfo) HopResult {
		f.calls = append(f.calls, info)
		gid := info.Hop.GroupID
		s := f.scripts[gid]
		if s.onCall != nil {
			s.onCall()
		}
		if s.override != nil {
			return *s.override
		}
		if s.skip {
			res := HopResult{Outcome: HopOutcomeSkipped}
			if s.skipErr {
				res.WriteFinalError = func() { f.written = append(f.written, fmt.Sprintf("skip-err:%d", gid)) }
			}
			return res
		}
		if s.done {
			f.written = append(f.written, fmt.Sprintf("ok:%d", gid))
			return HopResult{Outcome: HopOutcomeDone, Attempts: s.attempts}
		}
		res := ClassifyHopFailure(s.failure)
		res.Attempts = s.attempts
		switch res.Outcome {
		case HopOutcomeTerminal:
			f.written = append(f.written, fmt.Sprintf("terminal:%d", gid))
		case HopOutcomeFallbackWorthy:
			if info.DeferFinalError {
				res.WriteFinalError = func() { f.written = append(f.written, fmt.Sprintf("err:%d", gid)) }
			} else {
				f.written = append(f.written, fmt.Sprintf("err:%d", gid))
				res.ErrorWritten = true
			}
		}
		return res
	}
}

func (f *fakeAttempt) calledGroups() []int64 {
	out := make([]int64, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Hop.GroupID)
	}
	return out
}

func noAccount() HopFailure {
	return HopFailure{Kind: HopFailureNoAccount, PoolHasAccounts: true, ModelSupported: true}
}

func exhausted(status int) HopFailure {
	return HopFailure{Kind: HopFailureFailoverExhausted, LastStatus: status}
}

func newTestRunner(gate GroupChainBreakerGate) *GroupChainRunner {
	r := &GroupChainRunner{Settings: DefaultGroupFallbackSettings()}
	if gate != nil {
		r.Breaker = gate
	}
	return r
}

const runnerTestModel = "gpt-6-sol"

func runnerInput(ids ...int64) ChainRunInput {
	return ChainRunInput{Chain: runnerTestChain(ids...), Model: runnerTestModel, UserID: 42}
}

// ---------------------------------------------------------------------------
// 回退条件：每条一个用例（设计 3.3）
// ---------------------------------------------------------------------------

func TestRunner_Fallback_NoAccount(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: noAccount()}, 2: {done: true, attempts: 1}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, 1, res.ServedIndex)
	require.Equal(t, []int64{1, 2}, fa.calledGroups())
	require.Equal(t, []string{"ok:2"}, fa.written, "被回退的那一跳不得写任何响应")
}

func TestRunner_Fallback_BusyTimeout(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: HopFailure{Kind: HopFailureBusyTimeout}}, 2: {done: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, FallbackReasonBusy, res.Trace[0].Reason)
	require.Equal(t, []string{"ok:2"}, fa.written)
}

func TestRunner_Fallback_QueueFull(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: HopFailure{Kind: HopFailureQueueFull}}, 2: {done: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, FallbackReasonBusy, res.Trace[0].Reason)
	require.Equal(t, []string{"ok:2"}, fa.written)
}

func TestRunner_Fallback_FailoverExhaustedCapacityStatuses(t *testing.T) {
	for _, status := range []int{401, 402, 403, 429, 500, 502, 503, 529} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(status), attempts: 3}, 2: {done: true}}}
			res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
			require.Equal(t, ChainRunServed, res.Status)
			require.Equal(t, FallbackReasonFailoverExhausted, res.Trace[0].Reason)
			require.Equal(t, []string{"ok:2"}, fa.written)
		})
	}
}

// ---------------------------------------------------------------------------
// 不回退：每条一个用例
// ---------------------------------------------------------------------------

func TestRunner_NoFallback_TerminalFailures(t *testing.T) {
	cases := map[string]HopFailure{
		"invalid_request":           {Kind: HopFailureInvalidRequest},
		"context_too_long":          {Kind: HopFailureContextTooLong},
		"moderation_blocked":        {Kind: HopFailureModerationBlocked},
		"insufficient_balance":      {Kind: HopFailureInsufficientBalance},
		"client_disconnected":       {Kind: HopFailureClientDisconnected},
		"other_error":               {Kind: HopFailureOther},
		"failover_non_capacity_400": exhausted(400),
		"failover_non_capacity_404": exhausted(404),
		"failover_non_capacity_422": exhausted(422),
		// 真实内容已经输出：即使事实上是没号 / 容量类也不能换组
		"output_committed_no_account": {Kind: HopFailureNoAccount, PoolHasAccounts: true, ModelSupported: true, OutputCommitted: true},
		"output_committed_failover":   {Kind: HopFailureFailoverExhausted, LastStatus: 503, OutputCommitted: true},
		"output_committed_busy":       {Kind: HopFailureBusyTimeout, OutputCommitted: true},
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			gate := &fakeBreakerGate{}
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: failure, attempts: 1}, 2: {done: true}}}
			res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
			require.Equal(t, ChainRunTerminal, res.Status)
			require.Equal(t, []int64{1}, fa.calledGroups(), "不得尝试下一跳")
			require.Equal(t, []string{"terminal:1"}, fa.written)
			require.Empty(t, gate.failures, "不可回退的结果不计入熔断")
		})
	}
}

func TestRunner_NoFallback_ClientGoneBeforeNextHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount(), onCall: cancel}, // 客户端在第一跳期间断开
		2: {done: true},
	}}
	gate := &fakeBreakerGate{}
	res := newTestRunner(gate).Run(ctx, runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunClientGone, res.Status)
	require.Equal(t, []int64{1}, fa.calledGroups())
	require.Empty(t, fa.written, "客户端已断开：不写任何响应")
	require.Empty(t, gate.failures, "客户端断开不记熔断")
}

func TestRunner_NoFallback_CommittedOutputGuardEvenIfAttemptSaysWorthy(t *testing.T) {
	// attempt 误报 FallbackWorthy，但 tracker 显示真实内容已写出：runner 必须拦住并把原始错误写出。
	tracker := &OutputTracker{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {onCall: func() { tracker.Observe(100, 10, false) }, failure: noAccount()},
		2: {done: true},
	}}
	in := runnerInput(1, 2)
	in.Output = tracker
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunTerminal, res.Status)
	require.True(t, res.ErrorFlushed)
	require.Equal(t, []int64{1}, fa.calledGroups())
	require.Equal(t, []string{"err:1"}, fa.written)
}

// ---------------------------------------------------------------------------
// 心跳：只写过心跳可回退，写过真实内容不行
// ---------------------------------------------------------------------------

func TestRunner_HeartbeatOnlyAllowsFallback(t *testing.T) {
	tracker := &OutputTracker{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {onCall: func() { tracker.Observe(12, 12, false) }, failure: noAccount()},
		2: {done: true},
	}}
	in := runnerInput(1, 2)
	in.Output = tracker
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())

	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{1, 2}, fa.calledGroups())
	require.False(t, fa.calls[0].HeartbeatOnly)
	require.True(t, fa.calls[1].HeartbeatOnly, "下一跳要知道此前只写过心跳，错误需按流式格式收尾")
	require.False(t, tracker.Committed())
	require.True(t, tracker.HeartbeatOnly())
}

func TestOutputCommitted_SubtractsKeepalive(t *testing.T) {
	require.False(t, OutputCommitted(0, 0, false))
	require.False(t, OutputCommitted(30, 30, false), "只有心跳")
	require.True(t, OutputCommitted(31, 30, false), "心跳之外还有字节")
	require.True(t, OutputCommitted(0, 0, true), "contentStarted 跨跳传递")

	var nilTracker *OutputTracker
	require.False(t, nilTracker.Committed())
	require.False(t, nilTracker.HeartbeatOnly())
	nilTracker.Observe(1, 0, true) // 不 panic

	tr := &OutputTracker{}
	require.False(t, tr.Committed())
	require.False(t, tr.HeartbeatOnly())
	tr.Observe(5, 5, false)
	require.True(t, tr.HeartbeatOnly())
	tr.Observe(3, 0, false) // 单调不减
	require.False(t, tr.Committed())
	tr.Observe(0, 0, true)
	require.True(t, tr.Committed())
	require.False(t, tr.HeartbeatOnly())
}

func TestOutputTracker_StreamCommittedWithoutContentIsHeartbeatOnly(t *testing.T) {
	// 响应头已按流式提交（例如 handler 的 streamStarted），但没有任何真实内容：可以回退。
	tr := &OutputTracker{}
	tr.MarkStreamCommitted()
	require.False(t, tr.Committed())
	require.True(t, tr.HeartbeatOnly())

	// 写过真实内容之后不可回退，也不再是「只有心跳」。
	tr.MarkContentStarted()
	require.True(t, tr.Committed())
	require.False(t, tr.HeartbeatOnly())

	var nilTracker *OutputTracker
	nilTracker.MarkStreamCommitted()
	nilTracker.MarkContentStarted()
}

func TestRunner_StreamCommittedHeartbeatOnlyFallsBackButContentDoesNot(t *testing.T) {
	// 只写过心跳（流式已提交、没有真实内容）：可以回退。
	hb := &OutputTracker{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {onCall: func() { hb.MarkStreamCommitted(); hb.Observe(12, 12, false) }, failure: noAccount()},
		2: {done: true},
	}}
	in := runnerInput(1, 2)
	in.Output = hb
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{1, 2}, fa.calledGroups())
	require.True(t, fa.calls[1].HeartbeatOnly)

	// 写过真实内容：不可回退。
	content := &OutputTracker{}
	fb := &fakeAttempt{scripts: map[int64]hopScript{
		1: {onCall: func() { content.MarkStreamCommitted(); content.MarkContentStarted() }, failure: noAccount()},
		2: {done: true},
	}}
	in2 := runnerInput(1, 2)
	in2.Output = content
	res2 := newTestRunner(nil).Run(context.Background(), in2, fb.fn())
	require.Equal(t, ChainRunTerminal, res2.Status)
	require.Equal(t, []int64{1}, fb.calledGroups())
}

// ---------------------------------------------------------------------------
// deferFinalError：最后一跳原样输出原始错误
// ---------------------------------------------------------------------------

func TestRunner_LastHopWritesOriginalError(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount()},
		2: {failure: exhausted(503), attempts: 3},
	}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())

	require.Equal(t, ChainRunExhausted, res.Status)
	require.False(t, res.ErrorFlushed, "最后一跳自己写了错误，runner 不能再写第二次")
	require.Equal(t, []string{"err:2"}, fa.written, "只有最后一跳的原始错误写给客户端，不暴露已回退")
	require.True(t, fa.calls[0].DeferFinalError)
	require.False(t, fa.calls[0].IsLast)
	require.False(t, fa.calls[1].DeferFinalError)
	require.True(t, fa.calls[1].IsLast)
}

func TestRunner_SingleHopChainKeepsOriginalBehavior(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503), attempts: 3}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1), fa.fn())

	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []string{"err:1"}, fa.written)
	require.False(t, fa.calls[0].HasChain)
	require.False(t, fa.calls[0].DeferFinalError)
	require.Zero(t, fa.calls[0].MaxSwitches)
	require.Empty(t, gate.admitted, "单跳链不涉及熔断")
	require.Empty(t, gate.failures)
}

func TestRunner_HopInfoForChains(t *testing.T) {
	fixed := time.Unix(1_800_000_000, 0)
	r := newTestRunner(nil)
	r.Now = func() time.Time { return fixed }
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: noAccount()}, 2: {done: true}}}
	_ = r.Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.True(t, fa.calls[0].HasChain)
	require.Equal(t, 3, fa.calls[0].MaxSwitches, "有链时每跳换号预算取 max_per_hop_switches")
	require.Equal(t, 12, fa.calls[0].AttemptsRemaining)
	require.Equal(t, 25*time.Second, fa.calls[0].TimeRemaining)
}

func TestRunner_DeferredErrorFlushedWhenNothingElseRuns(t *testing.T) {
	// 第二跳（最后一跳）被 attempt 跳过（例如分组 RPM 超限且没有更多跳）：前一跳暂存的原始错误必须写出。
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: noAccount()}, 2: {skip: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.True(t, res.ErrorFlushed)
	require.Equal(t, []string{"err:1"}, fa.written)
}

func TestRunner_SkippedHopCanOverrideFinalError(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: noAccount()}, 2: {skip: true, skipErr: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []string{"skip-err:2"}, fa.written)
}

func TestRunner_WorthyWithoutWriterKeepsEarlierStagedError(t *testing.T) {
	// 后一跳返回 FallbackWorthy 但没有 WriteFinalError：不能用 nil 覆盖前面跳已暂存的错误。
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount()},
		2: {override: &HopResult{Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonBusy}},
	}}
	in := runnerInput(1, 2, 3)
	fa.scripts[3] = hopScript{skip: true}
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []string{"err:1"}, fa.written)
}

func TestRunner_CommittedGuardDoesNotFlushEarlierHopError(t *testing.T) {
	// 本跳已写出真实内容且没有自己的 WriteFinalError：不能执行前面跳暂存的旧错误。
	tracker := &OutputTracker{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount()},
		2: {override: &HopResult{Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonBusy}, onCall: func() { tracker.MarkContentStarted() }},
	}}
	in := runnerInput(1, 2, 3)
	in.Output = tracker
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunTerminal, res.Status)
	require.Empty(t, fa.written)
}

func TestRunner_UnresolvedWhenNothingRanAndNothingToWrite(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {skip: true}, 2: {skip: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunUnresolved, res.Status)
	require.Empty(t, fa.written)

	res = newTestRunner(nil).Run(context.Background(), ChainRunInput{}, fa.fn())
	require.Equal(t, ChainRunUnresolved, res.Status)
}

func TestRunner_SkippedHopFallsThroughToNext(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {skip: true}, 2: {done: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, 1, res.ServedIndex)
	require.Equal(t, "attempt_skipped", res.Trace[0].SkippedBy)
}

func TestRunner_AttemptThatWroteErrorEarlyStopsRunner(t *testing.T) {
	// 协议违规保护：非最后一跳已经把错误写出（ErrorWritten=true），runner 不得再尝试后续跳或再写一次。
	written := 0
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {override: &HopResult{Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonBusy, ErrorWritten: true,
			WriteFinalError: func() { written++ }}},
		2: {done: true},
	}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []int64{1}, fa.calledGroups())
	require.Zero(t, written)
}

// ---------------------------------------------------------------------------
// 总尝试次数与时间预算（设计 3.7）
// ---------------------------------------------------------------------------

func TestRunner_TotalAttemptsCap(t *testing.T) {
	r := newTestRunner(nil)
	r.Settings.MaxTotalAttempts = 5
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: exhausted(503), attempts: 3},
		2: {failure: exhausted(503), attempts: 3},
		3: {done: true},
		4: {done: true},
	}}
	res := r.Run(context.Background(), runnerInput(1, 2, 3, 4), fa.fn())

	require.Equal(t, "total_attempts", res.StoppedBy)
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []int64{1, 2}, fa.calledGroups(), "上限用尽后不再换组")
	require.Equal(t, 5, fa.calls[0].AttemptsRemaining)
	require.Equal(t, 2, fa.calls[1].AttemptsRemaining)
	require.Equal(t, []string{"err:2"}, fa.written, "停止时写出最后一个原始错误，且只写一次")
	require.True(t, res.ErrorFlushed)
}

func TestRunner_DefaultMaxTotalAttemptsIsTwelve(t *testing.T) {
	r := &GroupChainRunner{} // 零值设置回落默认
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: exhausted(503), attempts: 12},
		2: {done: true},
	}}
	res := r.Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, "total_attempts", res.StoppedBy)
	require.Equal(t, []int64{1}, fa.calledGroups())
}

func TestRunner_TimeBudgetNonStream(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRunner(nil)
	r.Now = func() time.Time { return now }
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount(), onCall: func() { now = now.Add(26 * time.Second) }},
		2: {done: true},
	}}
	res := r.Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, "time_budget", res.StoppedBy)
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []int64{1}, fa.calledGroups())
	require.Equal(t, []string{"err:1"}, fa.written)
}

func TestRunner_TimeBudgetStreamIsLonger(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRunner(nil)
	r.Now = func() time.Time { return now }
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: noAccount(), onCall: func() { now = now.Add(26 * time.Second) }},
		2: {done: true},
	}}
	in := runnerInput(1, 2)
	in.Stream = true
	res := r.Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunServed, res.Status, "流式预算 60 秒，26 秒时仍可回退")
	require.Equal(t, []int64{1, 2}, fa.calledGroups())
	require.Equal(t, 34*time.Second, fa.calls[1].TimeRemaining)
}

func TestRunner_FirstHopIgnoresBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRunner(nil)
	r.Now = func() time.Time { return now }
	r.Settings.TotalBudgetNonStreamMS = 1
	r.Settings.MaxTotalAttempts = 1
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {done: true, attempts: 1}}}
	res := r.Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, 0, res.ServedIndex)
}

// ---------------------------------------------------------------------------
// B1 不变式：原样迭代入口传入的链
// ---------------------------------------------------------------------------

func TestRunner_IteratesGivenChainVerbatim(t *testing.T) {
	in := runnerInput(11, 22, 33)
	before := make([]ChainHop, len(in.Chain))
	copy(before, in.Chain)

	fa := &fakeAttempt{scripts: map[int64]hopScript{
		11: {failure: noAccount()},
		22: {failure: noAccount()},
		33: {done: true},
	}}
	res := newTestRunner(nil).Run(context.Background(), in, fa.fn())

	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{11, 22, 33}, fa.calledGroups())
	reviewed := map[int64]bool{11: true, 22: true, 33: true}
	for i, call := range fa.calls {
		require.True(t, reviewed[call.Hop.GroupID], "served 分组必须属于审核时用的集合")
		require.Equal(t, before[i], call.Hop, "传给 attempt 的跳与入口给的链逐项相同")
	}
	require.Equal(t, before, in.Chain, "runner 不得修改入口给的链")
}

func TestRunner_ChainLongerThanHardCapIsTruncated(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	scripts := map[int64]hopScript{}
	for _, id := range ids {
		scripts[id] = hopScript{failure: noAccount()}
	}
	fa := &fakeAttempt{scripts: scripts}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(ids...), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6}, fa.calledGroups(), "有效链总长上限 6")
	require.True(t, fa.calls[5].IsLast)
	require.Equal(t, []string{"err:6"}, fa.written)
}

// ---------------------------------------------------------------------------
// 熔断集成
// ---------------------------------------------------------------------------

func TestRunner_BreakerOpenHopIsSkipped(t *testing.T) {
	gate := &fakeBreakerGate{admissions: map[int64]BreakerAdmission{
		1: {Allowed: false, State: BreakerStateOpen},
	}}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {done: true}, 2: {done: true}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())

	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, 1, res.ServedIndex)
	require.Equal(t, []int64{2}, fa.calledGroups(), "熔断打开的非最后一跳直接跳过，不白等")
	require.Equal(t, "breaker_open", res.Trace[0].SkippedBy)
}

func TestRunner_BreakerHalfOpenBusyProbeIsSkipped(t *testing.T) {
	gate := &fakeBreakerGate{admissions: map[int64]BreakerAdmission{
		1: {Allowed: false, State: BreakerStateHalfOpen},
	}}
	fa := &fakeAttempt{scripts: map[int64]hopScript{2: {done: true}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, "breaker_probe_busy", res.Trace[0].SkippedBy)
	require.Equal(t, []int64{2}, fa.calledGroups())
}

func TestRunner_LastHopIsNotSubjectToBreaker(t *testing.T) {
	gate := &fakeBreakerGate{admissions: map[int64]BreakerAdmission{
		1: {Allowed: false, State: BreakerStateOpen},
		2: {Allowed: false, State: BreakerStateOpen}, // 即使最后一跳在熔断里也必须尝试
	}}
	fa := &fakeAttempt{scripts: map[int64]hopScript{2: {done: true}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())

	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{2}, fa.calledGroups())
	require.Len(t, gate.admitted, 1, "只对非最后一跳查询熔断")
	require.EqualValues(t, 1, gate.admitted[0].GroupID)
}

func TestRunner_AllNonLastSkippedByBreakerStillRunsLast(t *testing.T) {
	gate := &fakeBreakerGate{admissions: map[int64]BreakerAdmission{
		1: {Allowed: false, State: BreakerStateOpen},
		2: {Allowed: false, State: BreakerStateOpen},
	}}
	fa := &fakeAttempt{scripts: map[int64]hopScript{3: {failure: exhausted(503)}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2, 3), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Equal(t, []int64{3}, fa.calledGroups())
	require.Equal(t, []string{"err:3"}, fa.written, "最后一跳输出原始错误")
}

func TestRunner_ModelFamilyMissSkipsBreaker(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(429)}, 2: {done: true}}}
	in := runnerInput(1, 2)
	in.Model = "gpt-5.x-随便写"
	res := newTestRunner(gate).Run(context.Background(), in, fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Empty(t, gate.admitted)
	require.Empty(t, gate.failures, "模型族没命中的不进熔断")
}

func TestRunner_HopModelOverridesFamily(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(429)}, 2: {done: true}}}
	in := runnerInput(1, 2)
	in.Model = "alias-without-family"
	in.HopModel = func(hop ChainHop) string { return "gpt-5.4" }
	_ = newTestRunner(gate).Run(context.Background(), in, fa.fn())
	require.Equal(t, []BreakerKey{{Platform: PlatformOpenAI, GroupID: 1, Family: "gpt-5.4"}}, gate.admitted)
	require.Equal(t, []int64{1}, gate.failedGroups())
}

func TestRunner_NilBreakerIsFine(t *testing.T) {
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(429)}, 2: {done: true}}}
	res := newTestRunner(nil).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
}

// ---------------------------------------------------------------------------
// 熔断计数规则（设计 3.6 / B3）
// ---------------------------------------------------------------------------

func TestRunner_Breaker_CountsCapacityFailureImmediately(t *testing.T) {
	for _, status := range []int{429, 529} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			gate := &fakeBreakerGate{}
			// 下一跳也失败：429/529 是立即计入的，不依赖下一跳成功
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(status)}, 2: {failure: noAccount()}}}
			_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
			require.Equal(t, []int64{1, 2}, gate.failedGroups(), "组 1 立即计入；组 2 是没号且组内有支持账号，也计入")
			require.EqualValues(t, 42, gate.failures[0].user)
		})
	}
}

func TestRunner_Breaker_NoAccountCountsOnlyWhenPoolSupportsModel(t *testing.T) {
	cases := map[string]struct {
		failure HopFailure
		counted bool
	}{
		"pool_supports_model": {noAccount(), true},
		"model_not_found":     {HopFailure{Kind: HopFailureNoAccount, PoolHasAccounts: true, ModelSupported: false}, false},
		"capability_blocked":  {HopFailure{Kind: HopFailureNoAccount, PoolHasAccounts: true, ModelSupported: true, CapabilityBlocked: true}, false},
		"empty_pool":          {HopFailure{Kind: HopFailureNoAccount}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate := &fakeBreakerGate{}
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: tc.failure}, 2: {done: true}}}
			_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
			if tc.counted {
				require.Equal(t, []int64{1}, gate.failedGroups())
			} else {
				require.Empty(t, gate.failures)
			}
		})
	}
}

func TestRunner_Breaker_BusyIsNotCounted(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: HopFailure{Kind: HopFailureBusyTimeout}},
		2: {failure: HopFailure{Kind: HopFailureQueueFull}},
		3: {done: true},
	}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2, 3), fa.fn())
	require.Empty(t, gate.failures, "繁忙是负载不是故障")
}

func TestRunner_Breaker_401And403AreNotCounted(t *testing.T) {
	for _, status := range []int{401, 402, 403} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			gate := &fakeBreakerGate{}
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(status)}, 2: {done: true}}}
			res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
			require.Equal(t, ChainRunServed, res.Status, "仍然可以回退")
			require.Empty(t, gate.failures, "账号鉴权问题不计入熔断，下一跳成功也不补记")
		})
	}
}

func TestRunner_Breaker_RequestScopedTransientIsNotCounted(t *testing.T) {
	gate := &fakeBreakerGate{}
	f := exhausted(503)
	f.RequestScopedTransient = true
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: f}, 2: {done: true}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Empty(t, gate.failures)
}

func TestRunner_Breaker_5xxCountedOnlyIfNextHopSucceeds(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503)}, 2: {done: true}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{1}, gate.failedGroups(), "下一跳成功：暂记的 5xx 正式计入")
	require.EqualValues(t, 42, gate.failures[0].user)
}

func TestRunner_Breaker_5xxDiscardedIfNextHopAlsoFails(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: exhausted(503)},
		2: {failure: exhausted(503)},
		3: {done: true},
	}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2, 3), fa.fn())
	require.Equal(t, ChainRunServed, res.Status)
	require.Equal(t, []int64{2}, gate.failedGroups(), "组 1 的下一跳失败，作废；组 2 的下一跳成功，计入")
}

func TestRunner_Breaker_5xxOnLastHopNeverCounted(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503)}, 2: {failure: exhausted(503)}}}
	res := newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, ChainRunExhausted, res.Status)
	require.Empty(t, gate.failures, "请求整体失败：载荷造成的可能性大，不计入")
}

func TestRunner_Breaker_5xxDiscardedWhenNextHopTerminal(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{
		1: {failure: exhausted(503)},
		2: {failure: HopFailure{Kind: HopFailureInvalidRequest}},
	}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Empty(t, gate.failures)
}

func TestRunner_Breaker_5xxPendingSurvivesSkippedHops(t *testing.T) {
	gate := &fakeBreakerGate{admissions: map[int64]BreakerAdmission{2: {Allowed: false, State: BreakerStateOpen}}}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503)}, 3: {done: true}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2, 3), fa.fn())
	require.Equal(t, []int64{1}, gate.failedGroups(), "被跳过的跳不是「下一跳」，暂记保持到真正尝试的下一跳")
}

func TestRunner_Breaker_FailuresRecordedOnLastHopToo(t *testing.T) {
	gate := &fakeBreakerGate{}
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: HopFailure{Kind: HopFailureBusyTimeout}}, 2: {failure: exhausted(429)}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, []int64{2}, gate.failedGroups(), "最后一跳不受熔断影响，但它的容量失败仍然是证据")
}

// ---------------------------------------------------------------------------
// half-open 探测令牌在 runner 里的流转
// ---------------------------------------------------------------------------

func probeGate(token string) *fakeBreakerGate {
	return &fakeBreakerGate{admissions: map[int64]BreakerAdmission{
		1: {Allowed: true, ProbeToken: token, State: BreakerStateHalfOpen},
	}}
}

func TestRunner_Probe_SuccessClosesBreaker(t *testing.T) {
	gate := probeGate("tok")
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {done: true}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Equal(t, []string{"tok"}, gate.successes)
	require.Empty(t, gate.releases)
}

func TestRunner_Probe_CapacityFailureReopens(t *testing.T) {
	gate := probeGate("tok")
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(429)}, 2: {done: true}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Len(t, gate.failures, 1)
	require.Equal(t, "tok", gate.failures[0].probe)
	require.Empty(t, gate.releases)
}

func TestRunner_Probe_PendingConfirmedByNextHopSuccess(t *testing.T) {
	gate := probeGate("tok")
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503)}, 2: {done: true}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Len(t, gate.failures, 1)
	require.Equal(t, "tok", gate.failures[0].probe)
}

func TestRunner_Probe_PendingDiscardedReleasesToken(t *testing.T) {
	gate := probeGate("tok")
	fa := &fakeAttempt{scripts: map[int64]hopScript{1: {failure: exhausted(503)}, 2: {failure: HopFailure{Kind: HopFailureBusyTimeout}}}}
	_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
	require.Empty(t, gate.failures)
	require.Equal(t, []string{"tok"}, gate.releases)
}

func TestRunner_Probe_InconclusiveOutcomesReleaseToken(t *testing.T) {
	cases := map[string]hopScript{
		"busy":             {failure: HopFailure{Kind: HopFailureBusyTimeout}},
		"terminal":         {failure: HopFailure{Kind: HopFailureInvalidRequest}},
		"skipped":          {skip: true},
		"unauthorized_401": {failure: exhausted(401)},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			gate := probeGate("tok")
			fa := &fakeAttempt{scripts: map[int64]hopScript{1: script, 2: {done: true}}}
			_ = newTestRunner(gate).Run(context.Background(), runnerInput(1, 2), fa.fn())
			require.Equal(t, []string{"tok"}, gate.releases)
			require.Empty(t, gate.successes)
			require.Empty(t, gate.failures)
		})
	}
}

// ---------------------------------------------------------------------------
// 分类函数
// ---------------------------------------------------------------------------

func TestClassifyHopFailure_Table(t *testing.T) {
	type want struct {
		outcome HopOutcome
		reason  FallbackReason
		breaker BreakerSignal
	}
	cases := []struct {
		name string
		in   HopFailure
		want want
	}{
		{"no_account_supported", noAccount(), want{HopOutcomeFallbackWorthy, FallbackReasonNoAccount, BreakerSignalCount}},
		{"no_account_model_unsupported", HopFailure{Kind: HopFailureNoAccount, PoolHasAccounts: true}, want{HopOutcomeFallbackWorthy, FallbackReasonNoAccount, BreakerSignalNone}},
		{"busy", HopFailure{Kind: HopFailureBusyTimeout}, want{HopOutcomeFallbackWorthy, FallbackReasonBusy, BreakerSignalNone}},
		{"queue_full", HopFailure{Kind: HopFailureQueueFull}, want{HopOutcomeFallbackWorthy, FallbackReasonBusy, BreakerSignalNone}},
		{"429", exhausted(429), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalCount}},
		{"529", exhausted(529), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalCount}},
		{"500", exhausted(500), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalPending}},
		{"503", exhausted(503), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalPending}},
		{"401", exhausted(401), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalNone}},
		{"402", exhausted(402), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalNone}},
		{"403", exhausted(403), want{HopOutcomeFallbackWorthy, FallbackReasonFailoverExhausted, BreakerSignalNone}},
		{"400", exhausted(400), want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"0", exhausted(0), want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"invalid", HopFailure{Kind: HopFailureInvalidRequest}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"ctx_too_long", HopFailure{Kind: HopFailureContextTooLong}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"moderation", HopFailure{Kind: HopFailureModerationBlocked}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"balance", HopFailure{Kind: HopFailureInsufficientBalance}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"client_gone", HopFailure{Kind: HopFailureClientDisconnected}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"other", HopFailure{Kind: HopFailureOther}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
		{"unknown_kind", HopFailure{}, want{HopOutcomeTerminal, "", BreakerSignalNone}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyHopFailure(tc.in)
			require.Equal(t, tc.want.outcome, got.Outcome)
			require.Equal(t, tc.want.reason, got.Reason)
			require.Equal(t, tc.want.breaker, got.Breaker)
		})
	}
}
