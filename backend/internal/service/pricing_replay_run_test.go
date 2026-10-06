//go:build unit

package service

// W6 PR6：回放的调度、聚合与输出（pricing_replay_run.go、pricing_replay_source.go）的测试。
// 数据源与派生器都是内存里的假实现，不碰数据库。

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type prFakeSource struct {
	rows   []PricingReplayRow // 按 id 升序
	counts map[int64]int64
	groups map[int64]*Group
	rates  map[PricingReplayRateKey]float64

	fpBefore, fpAfter *PricingReplayFingerprint
	fpCalls           int

	batchCalls int
	rateCalls  [][]int64
	queries    []PricingReplayBatchQuery

	countsErr, groupsErr, boundsErr, batchErr, ratesErr, fpErr error
}

func (f *prFakeSource) UsageGroupCounts(context.Context, time.Time, time.Time) (map[int64]int64, error) {
	return f.counts, f.countsErr
}

func (f *prFakeSource) LoadGroups(_ context.Context, ids []int64) (map[int64]*Group, error) {
	if f.groupsErr != nil {
		return nil, f.groupsErr
	}
	out := map[int64]*Group{}
	for _, id := range ids {
		if g, ok := f.groups[id]; ok {
			cp := *g
			out[id] = &cp
		}
	}
	return out, nil
}

func (f *prFakeSource) IDBounds(context.Context, time.Time, time.Time) (int64, int64, error) {
	if f.boundsErr != nil {
		return 0, 0, f.boundsErr
	}
	var lo, hi int64
	for _, r := range f.rows {
		if lo == 0 || r.ID < lo {
			lo = r.ID
		}
		if r.ID > hi {
			hi = r.ID
		}
	}
	return lo, hi, nil
}

func (f *prFakeSource) Batch(_ context.Context, q PricingReplayBatchQuery) ([]PricingReplayRow, error) {
	f.batchCalls++
	f.queries = append(f.queries, q)
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	want := map[int64]bool{}
	for _, id := range q.GroupIDs {
		want[id] = true
	}
	var out []PricingReplayRow
	for _, r := range f.rows {
		if r.ID > q.AfterID && r.ID <= q.MaxID && want[r.GroupID] {
			cp := r
			cp.Group = nil // 数据源不带分组，由调度补上
			out = append(out, cp)
			if len(out) == q.Limit {
				break
			}
		}
	}
	return out, nil
}

func (f *prFakeSource) UserGroupRates(_ context.Context, ids []int64) (map[PricingReplayRateKey]float64, error) {
	f.rateCalls = append(f.rateCalls, append([]int64(nil), ids...))
	if f.ratesErr != nil {
		return nil, f.ratesErr
	}
	return f.rates, nil
}

func (f *prFakeSource) Fingerprint(context.Context, []int64) (*PricingReplayFingerprint, error) {
	f.fpCalls++
	if f.fpErr != nil {
		return nil, f.fpErr
	}
	if f.fpCalls == 1 || f.fpAfter == nil {
		return f.fpBefore, nil
	}
	return f.fpAfter, nil
}

type prFakeDeriver struct{ err error }

func (d prFakeDeriver) ViewGroup(_ context.Context, id int64) (*GroupDeriveView, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &GroupDeriveView{
		GroupID: id, Derived: DerivedGroupState{GroupID: id, ChannelID: 9, Revision: "rev"},
		StoredConfig: &StoredGroupConfig{PricingStage: PricingStageShadow, Revision: 4}, InSync: true,
	}, nil
}

func prFp(hash string) *PricingReplayFingerprint {
	return &PricingReplayFingerprint{ChannelConfigHash: hash, GroupMatrixHash: map[int64]string{1: "m1", 2: "m2"}}
}

// prSource 造一个有 7 行（分组 1 与 2 交替，账号 3、4 交替）的数据源。分组 5 已软删，分组 6 有用量但没被选。
func prSource() *prFakeSource {
	f := &prFakeSource{
		counts: map[int64]int64{1: 4, 2: 3, 5: 10, 6: 8},
		groups: map[int64]*Group{
			1: {ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1},
			2: {ID: 2, Platform: PlatformOpenAI, RateMultiplier: 2},
			6: {ID: 6, Platform: PlatformOpenAI, RateMultiplier: 1},
		},
		rates:    map[PricingReplayRateKey]float64{{UserID: 8, GroupID: 2}: 5},
		fpBefore: prFp("cfg"),
	}
	for i := int64(1); i <= 7; i++ {
		row := prRow(i, prOpenAIModel)
		row.GroupID = 2 - i%2 // 1, 2, 1, 2, ...
		row.AccountID = 3 + i%2
		row.UserID = 7 + i%2
		row.Group = nil
		f.rows = append(f.rows, row)
	}
	return f
}

func prOptions(csvBuf *bytes.Buffer) PricingReplayOptions {
	opts := PricingReplayOptions{
		From: mpT0.Add(-48 * time.Hour), To: mpT0, BatchSize: 2, Workers: 3, SampleSize: 3,
		Now: func() time.Time { return mpT0 },
	}
	if csvBuf != nil {
		opts.DiffCSV = csvBuf
	}
	return opts
}

func TestPricingReplayRun_AggregatesPagesGroupsAndAccounts(t *testing.T) {
	// 分组 1 的 v2 一侧有额外倍率，分组 2 一致。
	v2 := map[int64]GroupStateSnapshot{1: prSnap(nil, mpExtra("gpt-5.4", 2)), 2: prSnap(nil)}
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), v2)
	src := prSource()
	var csvBuf bytes.Buffer
	var progress bytes.Buffer
	opts := prOptions(&csvBuf)
	opts.GroupIDs = []int64{1, 2, 5, 2}
	opts.Progress = &progress

	sum, err := r.Run(context.Background(), src, prFakeDeriver{}, opts)
	require.NoError(t, err)

	// keyset 分页：7 行、每批 2 行，下一批从上一批最后一个 id 之后开始，最后一批不满就停。
	require.Equal(t, 4, src.batchCalls)
	require.Equal(t, int64(0), src.queries[0].AfterID)
	require.Equal(t, int64(2), src.queries[1].AfterID)
	require.Equal(t, int64(6), src.queries[3].AfterID)
	require.Equal(t, []int64{1, 2}, src.queries[0].GroupIDs, "soft-deleted group 5 is not read, duplicates are dropped")
	// 每个用户只查一次专属倍率。
	seen := map[int64]int{}
	for _, call := range src.rateCalls {
		for _, id := range call {
			seen[id]++
		}
	}
	require.Equal(t, map[int64]int{7: 1, 8: 1}, seen)

	require.Equal(t, int64(7), sum.RowsSelected)
	require.Equal(t, int64(7), sum.RowsReplayed)
	require.Equal(t, int64(25), sum.RowsInWindow)
	require.Zero(t, sum.RowsUncovered+sum.RowsErrored)
	require.InDelta(t, 1.0, sum.Coverage, 1e-12)

	// 分组 1 有 4 行、每行一条成本翻译差异；分组 2 没有。
	require.Len(t, sum.Groups, 2)
	require.Equal(t, int64(4), sum.Groups[0].RowsReplayed)
	require.Equal(t, []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassTranslation, Count: 4}}, sum.Groups[0].Diffs)
	require.Empty(t, sum.Groups[1].Diffs)
	require.InDelta(t, sum.Groups[0].LegacyActualSum*2, sum.Groups[0].V2ActualSum, 1e-12)
	require.Equal(t, 1, sum.Groups[0].ModelsTotal)
	require.Equal(t, []PricingReplayModelSummary{{Model: prOpenAIModel, Rows: 4, Translation: 4}}, sum.Groups[0].ModelsWithDiffs)
	require.Equal(t, []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassTranslation, Count: 4}}, sum.Diffs)

	// 专属倍率：用户 8 在分组 2 的倍率是 5（用户 8 对应奇数 id，分组 1；分组 2 的行是偶数 id、用户 7 ，走分组倍率 2）。
	// 两侧用同一个倍率，所以分组 2 的两侧金额相等。
	require.Equal(t, sum.Groups[1].LegacyActualSum, sum.Groups[1].V2ActualSum)

	// 按账号拆开：账号 3 与 4 各自的行数与差异。
	require.Len(t, sum.Accounts, 2)
	require.Equal(t, int64(3), sum.Accounts[0].AccountID)
	var total int64
	for _, a := range sum.Accounts {
		total += a.RowsReplayed
		for _, d := range a.Diffs {
			require.Equal(t, ShadowKindCost, d.Kind)
		}
	}
	require.Equal(t, int64(7), total)

	// 没参与的分组：已软删的 5、有用量但没被选的 6。
	require.ElementsMatch(t, []PricingReplaySkippedGroup{
		{GroupID: 5, Rows: 10, Reason: "soft_deleted_or_missing"},
		{GroupID: 6, Rows: 8, Reason: "not_selected"},
	}, sum.SkippedGroups)

	// 前 3 条翻译差异做样本，CSV 有表头加 4 行。
	require.Len(t, sum.FirstTranslationDiffs, 3)
	require.Equal(t, int64(1), sum.FirstTranslationDiffs[0].UsageLogID)
	require.Equal(t, prOpenAIModel, sum.FirstTranslationDiffs[0].RequestedModel)
	var view shadowCostView
	require.NoError(t, json.Unmarshal(sum.FirstTranslationDiffs[2].V2, &view))
	require.Equal(t, 2.0, view.ExtraMultiplier)
	records, err := csv.NewReader(&csvBuf).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 5)
	require.Equal(t, "usage_log_id", records[0][0])
	require.Equal(t, []string{"1", "cost", "translation"}, []string{records[1][0], records[1][4], records[1][5]})
	require.Equal(t, int64(4), sum.DiffCSVRows)
	require.False(t, sum.DiffCSVTruncated)

	// 绑定：派生 revision 与指纹都记下来，没变化就是稳定的。
	require.True(t, sum.Binding.Stable)
	require.Equal(t, "cfg", sum.Binding.ChannelConfigHashBefore)
	require.Len(t, sum.Binding.Groups, 2)
	require.Equal(t, PricingReplayGroupBinding{
		GroupID: 1, Platform: PlatformOpenAI, ChannelID: 9, DeriveRevision: "rev", StoredInSync: true,
		StoredStage: "shadow", StoredConfigRevision: 4, MatrixHashBefore: "m1", MatrixHashAfter: "m1",
	}, sum.Binding.Groups[0])

	// 有翻译差异：不通过。
	require.False(t, sum.Verdict.Pass)
	require.EqualValues(t, 4, sum.Verdict.TranslationDiffs)
	require.NotEmpty(t, progress.String()+"x")

	// 汇总可以序列化。
	raw, err := json.Marshal(sum)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"tool":"pricing-replay"`)
}

func TestPricingReplayRun_PassesWhenConfigurationsMatch(t *testing.T) {
	snap := prSnap(nil, mpExtra("gpt-5.4", 2))
	r := prReplayer(PlatformOpenAI, prSame(snap), prSame(snap))
	src := prSource()
	// 最近 24 小时里的行：历史 actual_cost 与重算一致的算吻合；窗口末尾 24 小时之前的行不参与校准。
	probe := src.rows[0]
	probe.Group = &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}
	cost := r.Replay(context.Background(), &probe).LegacyActualCost
	for i := range src.rows {
		src.rows[i].StoredActualCost = cost
	}
	src.rows[0].StoredActualCost = cost * 3 // 一行不吻合
	src.rows[1].CreatedAt = mpT0.Add(-30 * time.Hour)

	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}
	sum, err := r.Run(context.Background(), src, prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.True(t, sum.Verdict.Pass, "%v", sum.Verdict.Reasons)
	require.Empty(t, sum.Diffs)
	require.Equal(t, int64(6), sum.Calibration.Rows)
	// 分组 2 的倍率是 2，探针按分组 1 算出的历史成本对它不吻合；分组 1 里被改动的那行也不吻合。
	require.Equal(t, int64(3), sum.Calibration.Matched)
	require.InDelta(t, 0.5, sum.Calibration.MatchRate, 1e-12)
	require.Equal(t, 24.0, sum.Calibration.WindowHours)
	require.NotNil(t, sum.FirstTranslationDiffs, "serializes as [] rather than null")
}

func TestPricingReplayRun_AutoSelectsLiveGroupsWithUsage(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	src := prSource()
	sum, err := r.Run(context.Background(), src, prFakeDeriver{}, prOptions(nil))
	require.NoError(t, err)
	// 没指定分组：窗口内有用量的未软删分组（1、2、6），已软删的 5 单独列出。
	require.Len(t, sum.Groups, 3)
	require.Equal(t, []PricingReplaySkippedGroup{{GroupID: 5, Rows: 10, Reason: "soft_deleted_or_missing"}}, sum.SkippedGroups)
	require.Equal(t, int64(7), sum.RowsReplayed)
	require.Zero(t, sum.Groups[2].RowsReplayed, "group 6 has no rows in the fake source")
}

func TestPricingReplayRun_ConfigurationChangeDuringTheRunInvalidatesIt(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}

	src := prSource()
	src.fpAfter = prFp("changed")
	sum, err := r.Run(context.Background(), src, prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.False(t, sum.Binding.Stable)
	require.False(t, sum.Verdict.Pass)
	require.Contains(t, strings.Join(sum.Verdict.Reasons, ";"), "changed during the replay")

	// 只有某个分组的矩阵行变了，同样作废。
	src = prSource()
	src.fpAfter = prFp("cfg")
	src.fpAfter.GroupMatrixHash[2] = "m2-changed"
	sum, err = r.Run(context.Background(), src, prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.False(t, sum.Binding.Stable)
	require.Equal(t, "m2-changed", sum.Binding.Groups[1].MatrixHashAfter)
}

func TestPricingReplayRun_MissingDeriveRevisionFailsTheVerdict(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1}

	sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{err: errors.New("derive boom")}, opts)
	require.NoError(t, err)
	require.False(t, sum.Verdict.Pass)
	require.Equal(t, "derive boom", sum.Binding.Groups[0].Error)

	sum, err = r.Run(context.Background(), prSource(), nil, opts)
	require.NoError(t, err)
	require.False(t, sum.Verdict.Pass)
	require.Equal(t, "no deriver", sum.Binding.Groups[0].Error)
}

func TestPricingReplayRun_DiffCSVIsCappedButEveryDiffIsCounted(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil, mpExtra("gpt-5.4", 2))))
	var csvBuf bytes.Buffer
	opts := prOptions(&csvBuf)
	opts.GroupIDs = []int64{1, 2}
	opts.MaxDiffRows = 2

	sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.Equal(t, int64(2), sum.DiffCSVRows)
	require.True(t, sum.DiffCSVTruncated)
	require.EqualValues(t, 7, sum.Verdict.TranslationDiffs, "diffs beyond the csv cap are still counted")
	records, err := csv.NewReader(&csvBuf).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3)
}

func TestPricingReplayRun_ExpectedDifferencesDoNotFailTheVerdict(t *testing.T) {
	closed := prSnap(nil, mpClosed(mpInherit(prOpenAIModel)))
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(closed))
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}

	sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.True(t, sum.Verdict.Pass, "%v", sum.Verdict.Reasons)
	require.EqualValues(t, 7, sum.Verdict.ExpectedDiffs)
	require.Zero(t, sum.Verdict.TranslationDiffs)
	require.Equal(t, []PricingReplayDiffCount{{Kind: ShadowKindAccess, Class: ShadowClassExpected, Reason: PricingReplayReasonClosedInGroup, Count: 7}}, sum.Diffs)
	require.Empty(t, sum.FirstTranslationDiffs)
}

func TestPricingReplayRun_GroupChecksAreCountedOncePerGroup(t *testing.T) {
	legacy := prSnap(func(c *MatrixGroupConfig) { c.Features = map[string]any{featureKeyBedrockCCCompat: true} })
	r := prReplayer(PlatformOpenAI, prSame(legacy), prSame(prSnap(nil)))
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}

	sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.Len(t, sum.GroupChecks, 2)
	require.Equal(t, []PricingReplayDiffCount{{Kind: ShadowKindFeature, Class: ShadowClassTranslation, Count: 2}}, sum.Diffs)
	require.False(t, sum.Verdict.Pass)
}

func TestPricingReplayRun_NoRowsAndErrors(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	opts := prOptions(nil)

	// 空窗口。
	bad := opts
	bad.To = bad.From
	_, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, bad)
	require.Error(t, err)

	// 没有任何行：不通过（没回放东西不能算零差异）。
	empty := &prFakeSource{counts: map[int64]int64{}, fpBefore: prFp("cfg")}
	sum, err := r.Run(context.Background(), empty, prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.False(t, sum.Verdict.Pass)
	require.Contains(t, strings.Join(sum.Verdict.Reasons, ";"), "no rows were replayed")
	require.Zero(t, empty.batchCalls)

	// 数据源各环节的错误都向上传递。
	for name, mutate := range map[string]func(*prFakeSource){
		"counts":      func(f *prFakeSource) { f.countsErr = errors.New("boom") },
		"groups":      func(f *prFakeSource) { f.groupsErr = errors.New("boom") },
		"fingerprint": func(f *prFakeSource) { f.fpErr = errors.New("boom") },
		"bounds":      func(f *prFakeSource) { f.boundsErr = errors.New("boom") },
		"batch":       func(f *prFakeSource) { f.batchErr = errors.New("boom") },
		"rates":       func(f *prFakeSource) { f.ratesErr = errors.New("boom") },
	} {
		src := prSource()
		mutate(src)
		o := opts
		o.GroupIDs = []int64{1, 2}
		_, err := r.Run(context.Background(), src, prFakeDeriver{}, o)
		require.ErrorContains(t, err, "boom", name)
	}

	// 取消。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := opts
	o.GroupIDs = []int64{1, 2}
	_, err = r.Run(ctx, prSource(), prFakeDeriver{}, o)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPricingReplayRun_PanickingRowsAreCountedAsErrors(t *testing.T) {
	ok, _ := newMPForTest(newMPSource(PlatformOpenAI, prSame(prSnap(nil))), nil)
	r := newPricingReplayer(newTestBillingService(), panicPolicy{}, ok)
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}
	opts.Workers = 1

	sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, opts)
	require.NoError(t, err)
	// 7 行出错，加上 2 个分组的分组级检查也各因 panic 记 1 次错误（不记成翻译差异）。
	require.EqualValues(t, 9, sum.RowsErrored)
	require.Len(t, sum.ErrorSamples, 9)
	require.Zero(t, sum.Verdict.TranslationDiffs)
	require.False(t, sum.Verdict.Pass)
}

func TestPricingReplayRun_UncoveredRows(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	src := prSource()
	src.rows[0].Model, src.rows[0].RequestedModel = "", ""
	opts := prOptions(nil)
	opts.GroupIDs = []int64{1, 2}

	sum, err := r.Run(context.Background(), src, prFakeDeriver{}, opts)
	require.NoError(t, err)
	require.EqualValues(t, 1, sum.RowsUncovered)
	require.EqualValues(t, 6, sum.RowsReplayed)
	require.InDelta(t, 6.0/7.0, sum.Coverage, 1e-12)
}

func TestPricingReplayRun_ParallelismDoesNotChangeTheResult(t *testing.T) {
	v2 := map[int64]GroupStateSnapshot{1: prSnap(nil, mpExtra("gpt-5.4", 2)), 2: prSnap(nil)}
	run := func(workers, batch int) *PricingReplaySummary {
		r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), v2)
		opts := prOptions(nil)
		opts.GroupIDs = []int64{1, 2}
		opts.Workers, opts.BatchSize = workers, batch
		sum, err := r.Run(context.Background(), prSource(), prFakeDeriver{}, opts)
		require.NoError(t, err)
		return sum
	}
	one, many, big := run(1, 2), run(4, 2), run(8, 100)
	for _, other := range []*PricingReplaySummary{many, big} {
		require.Equal(t, one.Diffs, other.Diffs)
		require.Equal(t, one.Groups[0].Diffs, other.Groups[0].Diffs)
		require.Equal(t, one.Accounts, other.Accounts)
		require.Equal(t, one.FirstTranslationDiffs, other.FirstTranslationDiffs)
	}
}

func TestPricingReplayRun_ModelAggregationIsBounded(t *testing.T) {
	agg := &replayGroupAgg{models: map[string]*replayModelAgg{}}
	for i := 0; i < pricingReplayMaxModelsPerGroup+10; i++ {
		agg.modelAgg("model-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26)).rows++
	}
	require.LessOrEqual(t, len(agg.models), pricingReplayMaxModelsPerGroup+1)
	agg.models = map[string]*replayModelAgg{}
	for i := 0; i < pricingReplayMaxModelsPerGroup; i++ {
		agg.models[strings.Repeat("m", i+1)] = &replayModelAgg{}
	}
	agg.modelAgg("brand-new").rows++
	agg.modelAgg("another-new").rows++
	require.EqualValues(t, 2, agg.models[replayOverflowModel].rows)
}

func TestAttachReplayInputs(t *testing.T) {
	groups := map[int64]*Group{1: {ID: 1, RateMultiplier: 1.5}, 2: {ID: 2, RateMultiplier: 2}}
	rates := map[PricingReplayRateKey]float64{{UserID: 7, GroupID: 2}: 9}
	rows := []PricingReplayRow{
		{ID: 1, GroupID: 1, UserID: 7},                   // 分组倍率
		{ID: 2, GroupID: 1, ServedGroupID: 2, UserID: 7}, // 服务分组 2 上的专属倍率
		{ID: 3, GroupID: 1, ServedGroupID: 8, UserID: 7}, // 服务分组不在集合里：退回主分组
		{ID: 4, GroupID: 9, UserID: 7},                   // 补不上分组
	}
	attachReplayInputs(rows, groups, rates)
	require.Equal(t, 1.5, rows[0].Multiplier)
	require.Equal(t, int64(2), rows[1].Group.ID)
	require.Equal(t, 9.0, rows[1].Multiplier)
	require.Equal(t, int64(1), rows[2].Group.ID)
	require.Nil(t, rows[3].Group)
}

func TestReplayCostMatches(t *testing.T) {
	require.True(t, replayCostMatches(1.23456789012, 1.2345678901))
	require.True(t, replayCostMatches(0, 0))
	require.True(t, replayCostMatches(100.0000001, 100))
	require.False(t, replayCostMatches(1, 1.1))
	require.False(t, replayCostMatches(0, 1e-6))
}

func TestPricingReplayOptionsNormalize(t *testing.T) {
	o := PricingReplayOptions{From: mpT0.Add(-time.Hour), To: mpT0}
	require.NoError(t, o.normalize())
	require.Equal(t, DefaultPricingReplayBatchSize, o.BatchSize)
	require.Equal(t, 1, o.Workers)
	require.Equal(t, DefaultPricingReplayMaxDiffRows, o.MaxDiffRows)
	require.Equal(t, DefaultPricingReplaySampleSize, o.SampleSize)
	require.Equal(t, DefaultPricingReplayCalibration, o.CalibrationWindow)
	require.NotNil(t, o.Now)
	require.Error(t, (&PricingReplayOptions{}).normalize())
}

func TestSortedDiffCounts(t *testing.T) {
	got := sortedDiffCounts(map[replayDiffKey]int64{
		{Kind: ShadowKindMapping, Class: ShadowClassTranslation}:                           1,
		{Kind: ShadowKindCost, Class: ShadowClassTranslation}:                              2,
		{Kind: ShadowKindAccess, Class: ShadowClassExpected, Reason: "b"}:                  3,
		{Kind: ShadowKindAccess, Class: ShadowClassExpected, Reason: "a"}:                  4,
		{Kind: ShadowKindAccountCost, Class: ShadowClassTranslation, Reason: "irrelevant"}: 5,
	})
	var order []string
	for _, d := range got {
		order = append(order, d.Class+"/"+d.Kind+"/"+d.Reason)
	}
	require.True(t, sort.StringsAreSorted(order), "%v", order)
	require.Equal(t, "expected/access/a", order[0])
}

// ---------------------------------------------------------------------------
// 派生来源与离线价格
// ---------------------------------------------------------------------------

type prViewDeriver struct {
	states map[int64]DerivedGroupState
	err    error
}

func (d prViewDeriver) ViewGroup(_ context.Context, id int64) (*GroupDeriveView, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &GroupDeriveView{GroupID: id, Derived: d.states[id]}, nil
}

func TestDerivedMatrixSourceFeedsTheV2Policy(t *testing.T) {
	ctx := context.Background()
	src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: {}}) // 只用它的分组元信息
	derived := DerivedGroupState{
		GroupID: 1, ChannelID: 9, Revision: "rev",
		Config: MatrixGroupConfig{
			AccessMode:         MatrixAccessAllowlist,
			BillingModelSource: mpS(BillingModelSourceChannelMapped),
			ModelMapping:       []MatrixMappingEntry{{Src: "alias", Dst: "gpt-5.4"}},
			CostMode:           MatrixCostFollowBilling,
		},
		Cells: []MatrixCell{
			{ModelKey: "gpt-5.4", Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: mpF(2), Source: MatrixSourceLegacyDerived},
		},
		CostRules: []MatrixCostRule{{Name: "r", SourceChannelID: 9, SourceOrdinal: 1, GroupIDs: []int64{1}, Enabled: true, SortOrder: 1}},
	}
	source := NewDerivedMatrixSource(src, prViewDeriver{states: map[int64]DerivedGroupState{1: derived}})
	policy := NewMatrixGroupPolicy(source, nil)

	require.True(t, policy.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, policy.ModelAccess(ctx, 1, "other").OK, "allowlist from the derived config")
	require.Equal(t, 2.0, policy.ExtraMultiplier(ctx, 1, "gpt-5.4", time.Time{}))
	mapping := policy.Mapping(ctx, 1, "alias")
	require.True(t, mapping.Mapped)
	require.Equal(t, "gpt-5.4", mapping.MappedModel)
	require.Equal(t, MatrixCostFollowBilling, policy.CostMode(ctx, 1))
	rules, _ := policy.CostRules(ctx, 1)
	require.Len(t, rules, 1)
	require.Equal(t, PricingStageV2, policy.Stage(ctx, 1))

	// 派生失败时快照加载失败：matrixPolicy 退回兜底快照并标记，回放里会表现为差异而不是静默通过。
	failing := NewMatrixGroupPolicy(NewDerivedMatrixSource(src, prViewDeriver{err: errors.New("derive boom")}), nil)
	require.True(t, failing.SnapshotDegraded(ctx, 1))

	snaps, err := NewDerivedMatrixSource(src, prViewDeriver{err: errors.New("derive boom")}).LoadGroupSnapshots(ctx, []int64{1})
	require.ErrorContains(t, err, "derive boom")
	require.Nil(t, snaps)
}

func TestLoadOfflinePricing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_pricing.json")
	body := `{"sample_spec": {}, "replay-test-model": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "openai", "mode": "chat"}}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	svc := NewPricingService(&config.Config{}, nil)
	info, err := svc.LoadOfflinePricing(path)
	require.NoError(t, err)
	require.Equal(t, path, info.Path)
	require.Equal(t, 1, info.Models)
	require.Len(t, info.SHA256, 64)
	require.NotNil(t, svc.GetModelPricing("replay-test-model"))

	_, err = svc.LoadOfflinePricing(filepath.Join(dir, "missing.json"))
	require.Error(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{"only_garbage": 1}`), 0o600))
	_, err = svc.LoadOfflinePricing(path)
	require.Error(t, err)
}
