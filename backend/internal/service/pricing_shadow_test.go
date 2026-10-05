//go:build unit

package service

// W6 PR5：影子比对的计数、限速、采样与异步写入。测试名以 TestPricingShadow_ 开头，CI 的 -race job 会跑它们。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestPricingShadow_RateGateAllowsLimitPerSecond(t *testing.T) {
	g := shadowRateGate{limit: 3}
	t0 := time.Unix(1000, 0)
	allowed := 0
	for i := 0; i < 10; i++ {
		if g.allow(t0) {
			allowed++
		}
	}
	require.Equal(t, 3, allowed)
	require.True(t, g.allow(t0.Add(time.Second)), "a new second starts a new window")
}

func TestPricingShadow_SamplerSpacingAndHourlyCap(t *testing.T) {
	var s shadowSampler
	t0 := time.Unix(10_000, 0)
	key := shadowSampleKey{GroupID: 1, Kind: ShadowKindCost, Class: ShadowClassTranslation, Model: "m"}

	require.True(t, s.allow(key, t0))
	require.False(t, s.allow(key, t0.Add(time.Minute)), "the same key is spaced by the minimum interval")
	require.True(t, s.allow(key, t0.Add(shadowSampleMinInterval)))
	other := key
	other.Model = "n"
	require.True(t, s.allow(other, t0.Add(shadowSampleMinInterval)), "other keys are independent")

	// 每小时的全局上限。
	var capped shadowSampler
	admitted := 0
	for i := 0; i < shadowSampleMaxPerHour+50; i++ {
		k := shadowSampleKey{GroupID: int64(i), Kind: ShadowKindCost, Class: ShadowClassTranslation, Model: "m"}
		if capped.allow(k, t0) {
			admitted++
		}
	}
	require.Equal(t, shadowSampleMaxPerHour, admitted)
	require.True(t, capped.allow(shadowSampleKey{GroupID: 9999, Kind: ShadowKindCost, Model: "m"}, t0.Add(time.Hour)), "the next hour starts fresh")
}

func TestPricingShadow_SamplerKeyTableIsBounded(t *testing.T) {
	var s shadowSampler
	t0 := time.Unix(10_000, 0)
	s.last = make(map[shadowSampleKey]time.Time, shadowSampleKeyLimit)
	for i := 0; i < shadowSampleKeyLimit; i++ {
		s.last[shadowSampleKey{GroupID: int64(i), Model: "m"}] = t0
	}
	s.hourFrom = t0
	// 表满了而且没有过期项：拒绝新键。
	require.False(t, s.allow(shadowSampleKey{GroupID: -1, Model: "m"}, t0.Add(time.Second)))
	// 过期项被清掉之后可以继续。
	require.True(t, s.allow(shadowSampleKey{GroupID: -1, Model: "m"}, t0.Add(shadowSampleMinInterval+time.Second)))
	require.Less(t, len(s.last), shadowSampleKeyLimit)
}

func TestPricingShadow_HubCountsDiffsAndSamples(t *testing.T) {
	sink := &spSink{}
	h := newPricingShadowHub(sink)
	t0 := time.Unix(50_000, 0)
	h.now = func() time.Time { return t0 }

	h.noteCompared(7)
	h.noteCompared(7)
	h.noteCompared(3)
	h.noteSkipped(ShadowSkipRateLimited)
	h.noteDiff(7, ShadowKindCost, ShadowClassTranslation, "m", "req-1", map[string]int{"a": 1}, map[string]int{"a": 2})
	h.noteDiff(7, ShadowKindCost, ShadowClassTranslation, "m", "req-2", 1, 2) // 采样间隔内：只计数，不写样本
	h.noteDiff(7, ShadowKindAccess, ShadowClassExpected, "m", "", 1, 2)

	stats := h.Stats()
	require.Equal(t, []PricingShadowComparedCount{{GroupID: 3, Count: 1}, {GroupID: 7, Count: 2}}, stats.ComparedTotal)
	require.Equal(t, []PricingShadowDiffCount{
		{GroupID: 7, Kind: ShadowKindAccess, Class: ShadowClassExpected, Count: 1},
		{GroupID: 7, Kind: ShadowKindCost, Class: ShadowClassTranslation, Count: 2},
	}, stats.DiffTotal)
	require.Equal(t, map[string]int64{ShadowSkipRateLimited: 1}, stats.SkippedTotal)

	samples := sink.all()
	require.Len(t, samples, 2, "two distinct sample keys")
	require.Equal(t, "req-1", samples[0].UsageRef)
	require.JSONEq(t, `{"a":1}`, string(samples[0].LegacyView))
	require.Equal(t, t0, samples[0].CreatedAt)
}

func TestPricingShadow_HubDropsSamplesWhenTheQueueIsFullButStillCounts(t *testing.T) {
	sink := &spSink{full: true}
	h := newPricingShadowHub(sink)
	h.noteDiff(1, ShadowKindCost, ShadowClassTranslation, "m", "", 1, 2)
	require.EqualValues(t, 1, h.Stats().SamplesDropped)
	require.EqualValues(t, 1, h.Stats().DiffTotal[0].Count)

	// 没有 sink：只计数。
	noSink := newPricingShadowHub(nil)
	noSink.noteDiff(1, ShadowKindCost, ShadowClassTranslation, "m", "", 1, 2)
	require.EqualValues(t, 1, noSink.Stats().DiffTotal[0].Count)

	// 无法序列化的视图只丢样本，不影响计数。
	bad := newPricingShadowHub(&spSink{})
	bad.noteDiff(1, ShadowKindCost, ShadowClassTranslation, "m", "", make(chan int), 2)
	require.EqualValues(t, 1, bad.Stats().DiffTotal[0].Count)
}

func TestPricingShadow_ViewsAreTruncatedAndFieldsBounded(t *testing.T) {
	huge := map[string]string{"x": strings.Repeat("a", shadowViewMaxBytes*2)}
	raw, err := marshalShadowView(huge)
	require.NoError(t, err)
	require.JSONEq(t, `{"truncated":true}`, string(raw))
	require.Equal(t, "ab", truncateShadowString("abcdef", 2))
	require.Equal(t, "abc", truncateShadowString("abc", 10))
	// 多字节：不切半个字符；非法 UTF-8 被剔除。
	require.Equal(t, "你", truncateShadowString("你好", 4))
	require.Equal(t, "", truncateShadowString("你好", 2))
	require.Equal(t, "ab", truncateShadowString("a\xffb", 10))
	require.True(t, utf8.ValidString(truncateShadowString("模型模型模型", 7)))

	sink := &spSink{}
	h := newPricingShadowHub(sink)
	h.noteDiff(1, ShadowKindCost, ShadowClassTranslation, strings.Repeat("m", 500), strings.Repeat("r", 200), 1, 2)
	require.Len(t, sink.all()[0].Model, 200)
	require.Len(t, sink.all()[0].UsageRef, 64)
}

func TestPricingShadow_CounterCardinalityIsBounded(t *testing.T) {
	h := newPricingShadowHub(nil)
	for i := 0; i < shadowCounterMaxKeys+200; i++ {
		h.noteCompared(int64(i + 1))
	}
	stats := h.Stats()
	require.LessOrEqual(t, len(stats.ComparedTotal), shadowCounterMaxKeys+1, "overflow groups are merged into one bucket")
	var total int64
	for _, c := range stats.ComparedTotal {
		total += c.Count
	}
	require.EqualValues(t, shadowCounterMaxKeys+200, total, "nothing is lost, only merged")
}

func TestPricingShadow_GuardSwallowsPanics(t *testing.T) {
	h := newPricingShadowHub(nil)
	require.True(t, h.guard("ok", func() {}))
	require.False(t, h.guard("boom", func() { panic("x") }))
	require.EqualValues(t, 1, h.Stats().PanicsTotal)
	for i := 0; i < 1002; i++ { // 越过日志限频的分支
		h.guard("boom", func() { panic("x") })
	}
	require.EqualValues(t, 1003, h.Stats().PanicsTotal)
}

// ---------------------------------------------------------------------------
// 成本视图
// ---------------------------------------------------------------------------

func TestPricingShadow_CostViewComparison(t *testing.T) {
	base := &CostBreakdown{InputCost: 1, OutputCost: 2, TotalCost: 3, ActualCost: 4.5, BillingMode: "token"}
	a := newShadowCostView("m", base, nil)

	same := *base
	require.True(t, a.equal(newShadowCostView("m", &same, nil)))

	for name, mutate := range map[string]func(*CostBreakdown){
		"input":       func(c *CostBreakdown) { c.InputCost = 1.0000000000000002 },
		"image input": func(c *CostBreakdown) { c.ImageInputCost = 1 },
		"output":      func(c *CostBreakdown) { c.OutputCost = 3 },
		"image out":   func(c *CostBreakdown) { c.ImageOutputCost = 1 },
		"cache write": func(c *CostBreakdown) { c.CacheCreationCost = 1 },
		"cache read":  func(c *CostBreakdown) { c.CacheReadCost = 1 },
		"total":       func(c *CostBreakdown) { c.TotalCost = 3.5 },
		"actual":      func(c *CostBreakdown) { c.ActualCost = 4.6 },
		"mode":        func(c *CostBreakdown) { c.BillingMode = "image" },
		"extra":       func(c *CostBreakdown) { c.extraMultiplier = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			other := *base
			mutate(&other)
			require.False(t, a.equal(newShadowCostView("m", &other, nil)), "bitwise comparison catches the smallest difference")
		})
	}
	require.False(t, a.equal(newShadowCostView("other", &same, nil)), "the billing model is part of the view")

	// 错误只分两类，不带错误文本。
	pricingErr := newShadowCostView("m", nil, ErrModelPricingUnavailable)
	require.Equal(t, "pricing_unavailable", pricingErr.Error)
	calcErr := newShadowCostView("m", nil, errors.New("boom with details"))
	require.Equal(t, "calc_error", calcErr.Error)
	require.False(t, pricingErr.equal(calcErr))
	require.True(t, pricingErr.equal(newShadowCostView("m", nil, errors.New("pricing not found: other text"))))
	require.Equal(t, "nil_cost", newShadowCostView("m", nil, nil).Error)

	// +0 与 -0 位模式不同。
	require.True(t, sameBits(0, 0))
	require.False(t, sameBits(0, negZeroValue()))
}

func negZeroValue() float64 {
	z := 0.0
	return -z
}

func TestPricingShadow_AccountCostViewComparison(t *testing.T) {
	one, two := 1.0, 2.0
	require.True(t, newShadowAccountCostView(nil).equal(newShadowAccountCostView(nil)))
	require.True(t, newShadowAccountCostView(&one).equal(newShadowAccountCostView(&one)))
	require.False(t, newShadowAccountCostView(&one).equal(newShadowAccountCostView(&two)))
	require.False(t, newShadowAccountCostView(nil).equal(newShadowAccountCostView(&one)))
	zero := 0.0
	require.False(t, newShadowAccountCostView(nil).equal(newShadowAccountCostView(&zero)), "unset (default formula) differs from an explicit zero")
}

// ---------------------------------------------------------------------------
// 异步写入
// ---------------------------------------------------------------------------

type shadowStoreFake struct {
	mu       sync.Mutex
	inserted []PricingShadowSample
	purged   []time.Time
	insertEr error
	listed   []PricingShadowSample
	lastList [2]int64
}

func (s *shadowStoreFake) InsertDiffs(_ context.Context, samples []PricingShadowSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertEr != nil {
		return s.insertEr
	}
	s.inserted = append(s.inserted, samples...)
	return nil
}

func (s *shadowStoreFake) PurgeDiffsBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purged = append(s.purged, before)
	return 0, nil
}

func (s *shadowStoreFake) ListDiffs(_ context.Context, groupID int64, limit int) ([]PricingShadowSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastList = [2]int64{groupID, int64(limit)}
	return s.listed, nil
}

func (s *shadowStoreFake) insertedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inserted)
}

func shadowTestSample(i int) PricingShadowSample {
	return PricingShadowSample{
		CreatedAt: time.Unix(int64(i), 0), GroupID: int64(i), Model: "m", Kind: ShadowKindCost, Class: ShadowClassTranslation,
		LegacyView: json.RawMessage(`{}`), V2View: json.RawMessage(`{}`),
	}
}

func TestPricingShadow_RecorderWritesQueuedSamplesOnClose(t *testing.T) {
	store := &shadowStoreFake{}
	r := NewPricingShadowRecorder(store)
	for i := 1; i <= 5; i++ {
		require.True(t, r.Enqueue(shadowTestSample(i)))
	}
	r.Close()
	r.Close() // 可重复调用
	require.Equal(t, 5, store.insertedCount())
	require.NotEmpty(t, store.purged, "old samples are purged at start-up")
	require.WithinDuration(t, time.Now().Add(-PricingShadowRetention), store.purged[0], time.Minute)
	require.Zero(t, r.WriteFailures())
}

func TestPricingShadow_RecorderCountsWriteFailuresAndKeepsRunning(t *testing.T) {
	store := &shadowStoreFake{insertEr: errors.New("db down")}
	r := NewPricingShadowRecorder(store)
	require.True(t, r.Enqueue(shadowTestSample(1)))
	r.Close()
	require.EqualValues(t, 1, r.WriteFailures())
}

func TestPricingShadow_RecorderEnqueueDropsWhenQueueIsFull(t *testing.T) {
	// 不启动后台协程：直接造一个小队列。
	r := &PricingShadowRecorder{queue: make(chan PricingShadowSample, 1)}
	require.True(t, r.Enqueue(shadowTestSample(1)))
	require.False(t, r.Enqueue(shadowTestSample(2)))
}

func TestPricingShadow_RecorderListClampsTheLimit(t *testing.T) {
	store := &shadowStoreFake{listed: []PricingShadowSample{shadowTestSample(1)}}
	r := NewPricingShadowRecorder(store)
	defer r.Close()

	got, err := r.List(context.Background(), 9, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, [2]int64{9, shadowListMaxLimit}, store.lastList)
	_, err = r.List(context.Background(), 0, 100000)
	require.NoError(t, err)
	require.Equal(t, [2]int64{0, shadowListMaxLimit}, store.lastList)
	_, err = r.List(context.Background(), 0, 7)
	require.NoError(t, err)
	require.Equal(t, [2]int64{0, 7}, store.lastList)
}
