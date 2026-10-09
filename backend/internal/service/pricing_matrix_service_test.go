//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 假的矩阵仓库：把「库」放在内存里，执行器用 mxApplyPlan 模拟。
// ---------------------------------------------------------------------------

type mxFakeMatrixRepo struct {
	mu     sync.Mutex
	groups map[int64]DeriveGroup
	state  map[int64]GroupStateSnapshot

	applyCalls [][]int64
	ctxErrSeen error

	summaries    []GroupPricingSummary // LoadGroupSummaries 的全部可见分组（按 id 升序）
	summaryErr   error
	summaryCalls []summaryCall

	metaErr    error
	ruleErr    error
	applyErr   error
	applyPanic bool
	block      bool   // ApplyPlans 一直阻塞到 ctx 结束
	beforeRead func() // 在 ApplyPlans 读取快照之前执行（模拟并发的阶段切换）

	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func (r *mxFakeMatrixRepo) GetGroupMeta(_ context.Context, ids []int64) (map[int64]DeriveGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.metaErr != nil {
		return nil, r.metaErr
	}
	out := map[int64]DeriveGroup{}
	for _, id := range ids {
		if g, ok := r.groups[id]; ok {
			out[id] = g
		}
	}
	return out, nil
}

func (r *mxFakeMatrixRepo) ListDerivedRuleGroupIDs(_ context.Context, channelID int64) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ruleErr != nil {
		return nil, r.ruleErr
	}
	var out []int64
	for gid, snap := range r.state {
		for _, rule := range snap.Rules {
			if rule.Source == MatrixSourceLegacyDerived && rule.SourceChannelID == channelID {
				out = append(out, gid)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (r *mxFakeMatrixRepo) ListDerivedRuleChannels(context.Context) (map[int64][]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ruleErr != nil {
		return nil, r.ruleErr
	}
	seen := map[[2]int64]bool{}
	out := map[int64][]int64{}
	for gid, snap := range r.state {
		for _, rule := range snap.Rules {
			k := [2]int64{rule.SourceChannelID, gid}
			if rule.Source == MatrixSourceLegacyDerived && !seen[k] {
				seen[k] = true
				out[rule.SourceChannelID] = append(out[rule.SourceChannelID], gid)
			}
		}
	}
	for _, ids := range out {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	return out, nil
}

func (r *mxFakeMatrixRepo) LoadGroupSnapshots(_ context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]GroupStateSnapshot{}
	for _, id := range ids {
		out[id] = r.state[id]
	}
	return out, nil
}

type summaryCall struct {
	ids   []int64
	limit int
}

func (r *mxFakeMatrixRepo) LoadGroupSummaries(_ context.Context, ids []int64, limit int) ([]GroupPricingSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaryCalls = append(r.summaryCalls, summaryCall{ids: append([]int64(nil), ids...), limit: limit})
	if r.summaryErr != nil {
		return nil, r.summaryErr
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []GroupPricingSummary
	for _, g := range r.summaries {
		if len(ids) > 0 && !want[g.GroupID] {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, g)
	}
	return out, nil
}

func (r *mxFakeMatrixRepo) ApplyPlans(ctx context.Context, ids []int64, plan func(map[int64]GroupStateSnapshot) ([]GroupApplyPlan, error)) error {
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	n := r.inflight.Add(1)
	defer r.inflight.Add(-1)
	for {
		cur := r.maxInflight.Load()
		if n <= cur || r.maxInflight.CompareAndSwap(cur, n) {
			break
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	time.Sleep(time.Millisecond) // 给并发的刷新留出重叠的机会
	r.ctxErrSeen = ctx.Err()
	r.applyCalls = append(r.applyCalls, append([]int64(nil), ids...))
	if r.applyPanic {
		panic("matrix repo exploded")
	}
	if r.applyErr != nil {
		return r.applyErr
	}
	if r.beforeRead != nil {
		r.beforeRead()
	}
	snaps := map[int64]GroupStateSnapshot{}
	for _, id := range ids {
		snaps[id] = r.state[id]
	}
	plans, err := plan(snaps)
	if err != nil {
		return err
	}
	for _, p := range plans {
		r.state[p.GroupID] = mxApplyPlan(snaps[p.GroupID], p)
	}
	return nil
}

// mxEnv 把假的矩阵仓库、假的渠道仓库与派生服务装在一起。
type mxEnv struct {
	svc      *PricingDerivationService
	matrix   *mxFakeMatrixRepo
	repo     *mockChannelRepository
	channels map[int64]*Channel
	owner    map[int64]int64
	facts    *mxFactSource
}

func newMxEnv(groups ...DeriveGroup) *mxEnv {
	e := &mxEnv{
		matrix:   &mxFakeMatrixRepo{groups: map[int64]DeriveGroup{}, state: map[int64]GroupStateSnapshot{}},
		channels: map[int64]*Channel{},
		owner:    map[int64]int64{},
		facts:    &mxFactSource{facts: map[string]OfficialPriceFact{}},
	}
	for _, g := range groups {
		e.matrix.groups[g.ID] = g
	}
	e.repo = &mockChannelRepository{
		getByIDFn: func(_ context.Context, id int64) (*Channel, error) {
			ch, ok := e.channels[id]
			if !ok {
				return nil, ErrChannelNotFound
			}
			return ch.Clone(), nil
		},
		getChannelIDByGroupIDFn: func(_ context.Context, gid int64) (int64, error) { return e.owner[gid], nil },
	}
	e.svc = NewPricingDerivationService(e.matrix, e.repo, e.facts)
	return e
}

// setChannel 保存（或更新）渠道，同步分组归属。
func (e *mxEnv) setChannel(ch *Channel) {
	e.channels[ch.ID] = ch.Clone()
	for gid, cid := range e.owner {
		if cid == ch.ID {
			delete(e.owner, gid)
		}
	}
	for _, gid := range ch.GroupIDs {
		e.owner[gid] = ch.ID
	}
}

func (e *mxEnv) removeChannel(id int64) {
	delete(e.channels, id)
	for gid, cid := range e.owner {
		if cid == id {
			delete(e.owner, gid)
		}
	}
}

func mxOpenAIGroups(ids ...int64) []DeriveGroup {
	out := make([]DeriveGroup, 0, len(ids))
	for _, id := range ids {
		out = append(out, DeriveGroup{ID: id, Platform: PlatformOpenAI})
	}
	return out
}

func mxChannelWithRule(id int64, price float64, groups ...int64) *Channel {
	ch := mxChannel(id, groups...)
	p := mxPricing(id, PlatformOpenAI, BillingModeToken, "gpt-5.5")
	p.InputPrice = mxF(price)
	ch.ModelPricing = []ChannelModelPricing{p}
	ch.AccountStatsPricingRules = []AccountStatsPricingRule{{ID: id, ChannelID: id, Name: "r", SortOrder: 1}}
	return ch
}

func mxGroupResult(t *testing.T, report *PricingRefreshReport, gid int64) PricingRefreshGroupResult {
	t.Helper()
	for _, g := range report.Groups {
		if g.GroupID == gid {
			return g
		}
	}
	t.Fatalf("报告里没有分组 %d", gid)
	return PricingRefreshGroupResult{}
}

// ---------------------------------------------------------------------------
// 刷新：派生、落库、幂等
// ---------------------------------------------------------------------------

func TestPricingDerivationService_RefreshDerivesAndIsIdempotent(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))

	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), report.ChannelID)
	require.Len(t, report.Groups, 2)
	for _, gid := range []int64{10, 11} {
		g := mxGroupResult(t, report, gid)
		require.Equal(t, int64(1), g.ChannelID)
		require.True(t, g.ConfigWritten)
		require.Equal(t, 1, g.CellsInserted)
		require.Equal(t, 1, g.RulesReplaced)
		require.NotEmpty(t, g.Revision)

		st := e.matrix.state[gid]
		require.Equal(t, PricingStageLegacy, st.Config.PricingStage, "新建的行是 legacy 阶段")
		require.Len(t, st.Cells, 1)
		require.Len(t, st.Rules, 1)
		require.Equal(t, MatrixSourceLegacyDerived, st.Rules[0].Source)
	}
	require.Equal(t, [][]int64{{10, 11}}, e.matrix.applyCalls, "分组按升序一次性加锁")

	again, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	for _, g := range again.Groups {
		require.False(t, g.ConfigWritten)
		require.Zero(t, g.CellsInserted+g.CellsUpdated+g.CellsDeleted+g.RulesReplaced, "第二次什么都不写")
	}
}

func TestPricingDerivationService_RefreshPicksUpChannelChanges(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)

	e.setChannel(mxChannelWithRule(1, 2e-6, 10))
	report, err := e.svc.RefreshChannel(context.Background(), 1, []int64{10})
	require.NoError(t, err)
	g := mxGroupResult(t, report, 10)
	require.Equal(t, 1, g.CellsUpdated)
	require.Zero(t, g.CellsInserted+g.CellsDeleted)
	require.Equal(t, mxF(2e-6), e.matrix.state[10].Cells[0].CustomPrice.InputPrice)
}

// 混合阶段：同一渠道的分组处在不同阶段，v2 的那个一个字节都不能动。
func TestPricingDerivationService_MixedStagesHook(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11, 12)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11, 12))

	e.matrix.state[10] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, PricingStage: PricingStageLegacy, MatrixGroupConfig: defaultMatrixGroupConfig()}}
	e.matrix.state[11] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 11, PricingStage: PricingStageShadow, MatrixGroupConfig: defaultMatrixGroupConfig()}}
	sentinel := GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: 12, PricingStage: PricingStageV2, Revision: 9,
			MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessAllowlist, CostMode: MatrixCostFollowBilling, ModelMapping: []MatrixMappingEntry{}, Features: map[string]any{}}},
		Cells: []StoredMatrixCell{mxStoredCell(1, MatrixCell{ModelKey: "hand-edited", Open: false, PriceMode: MatrixPriceExtra, ExtraMultiplier: mxF(2), Source: MatrixSourceManual})},
		Rules: []StoredMatrixCostRule{mxStoredRule(2, MatrixSourceLegacyDerived, MatrixCostRule{Name: "old", SourceChannelID: 1, SourceOrdinal: 1})},
	}
	e.matrix.state[12] = sentinel

	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)

	require.Equal(t, sentinel, e.matrix.state[12], "v2 分组的配置、单元格、成本核算规则都不能被动")
	skipped := mxGroupResult(t, report, 12)
	require.True(t, skipped.Skipped)
	require.Equal(t, planSkipStageV2, skipped.SkipReason)
	require.False(t, skipped.ConfigWritten)

	for gid, stage := range map[int64]PricingStage{10: PricingStageLegacy, 11: PricingStageShadow} {
		g := mxGroupResult(t, report, gid)
		require.False(t, g.Skipped)
		st := e.matrix.state[gid]
		require.Equal(t, stage, st.Config.PricingStage, "钩子不改阶段")
		require.Equal(t, int64(1), st.Config.Revision, "内容变化时 revision 加一")
		require.Len(t, st.Cells, 1)
		require.Len(t, st.Rules, 1)
	}
}

// 阶段在加锁之后才读取：派生计算期间分组被切到 v2，落库时必须看到新阶段并跳过。
func TestPricingDerivationService_StageIsReadUnderTheLock(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.matrix.state[10] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, PricingStage: PricingStageShadow, MatrixGroupConfig: defaultMatrixGroupConfig()}}
	e.matrix.beforeRead = func() {
		st := e.matrix.state[10]
		cfg := *st.Config
		cfg.PricingStage = PricingStageV2
		st.Config = &cfg
		e.matrix.state[10] = st
	}

	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	require.True(t, mxGroupResult(t, report, 10).Skipped)
	require.Empty(t, e.matrix.state[10].Cells)
	require.Equal(t, defaultMatrixGroupConfig(), e.matrix.state[10].Config.MatrixGroupConfig)
}

func TestPricingDerivationService_GroupLeavingChannelIsCleared(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	require.Len(t, e.matrix.state[11].Cells, 1)

	e.setChannel(mxChannelWithRule(1, 1e-6, 10)) // 11 被移出渠道
	report, err := e.svc.RefreshChannel(context.Background(), 1, []int64{10, 11})
	require.NoError(t, err)

	g := mxGroupResult(t, report, 11)
	require.Zero(t, g.ChannelID, "离开渠道的分组按无渠道处理")
	require.Equal(t, 1, g.CellsDeleted)
	require.Empty(t, e.matrix.state[11].Cells)
	require.Empty(t, e.matrix.state[11].Rules)
	require.Equal(t, defaultMatrixGroupConfig(), e.matrix.state[11].Config.MatrixGroupConfig)
	require.Nil(t, e.matrix.state[11].Config.BillingModelSource)
	require.Len(t, e.matrix.state[10].Cells, 1, "留在渠道里的分组不受影响")
}

func TestPricingDerivationService_GroupMovedToAnotherChannelIsDerivedFromItsNewOwner(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)

	// 分组 11 先被搬到渠道 2，然后才保存渠道 1：渠道 1 的钩子不能把 11 当成无渠道清掉。
	e.setChannel(mxChannelWithRule(2, 5e-6, 11))
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	report, err := e.svc.RefreshChannel(context.Background(), 1, []int64{10, 11})
	require.NoError(t, err)

	g := mxGroupResult(t, report, 11)
	require.Equal(t, int64(2), g.ChannelID)
	require.Len(t, e.matrix.state[11].Cells, 1)
	require.Equal(t, mxF(5e-6), e.matrix.state[11].Cells[0].CustomPrice.InputPrice, "按新所属渠道派生")
	require.Equal(t, int64(2), e.matrix.state[11].Rules[0].SourceChannelID)
	require.Len(t, e.matrix.state[11].Rules, 1)
}

func TestPricingDerivationService_DeletedChannelClearsItsGroups(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))
	_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)

	e.removeChannel(1)
	// 没有传入保存前的分组：仍带有该渠道派生成本核算行的分组也会被找出来清理。
	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	require.Len(t, report.Groups, 2)
	for _, gid := range []int64{10, 11} {
		require.Empty(t, e.matrix.state[gid].Cells)
		require.Empty(t, e.matrix.state[gid].Rules)
		require.Nil(t, e.matrix.state[gid].Config.BillingModelSource)
	}
}

func TestPricingDerivationService_SoftDeletedAndMissingGroups(t *testing.T) {
	e := newMxEnv(
		DeriveGroup{ID: 10, Platform: PlatformOpenAI, Deleted: true},
		DeriveGroup{ID: 12, Platform: PlatformOpenAI},
	) // 分组 11 在 groups 表里已经不存在
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11, 12))
	stale := MatrixCell{ModelKey: "stale", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}
	for _, gid := range []int64{10, 11} {
		e.matrix.state[gid] = GroupStateSnapshot{
			Config: &StoredGroupConfig{GroupID: gid, PricingStage: PricingStageShadow, MatrixGroupConfig: mxChannelConfigForTest()},
			Cells:  []StoredMatrixCell{mxStoredCell(50+gid, stale)},
		}
	}

	report, err := e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	for _, gid := range []int64{10, 11} {
		g := mxGroupResult(t, report, gid)
		require.Zero(t, g.ChannelID, "软删除或不存在的分组按无渠道处理")
		require.Equal(t, 1, g.CellsDeleted)
		require.Empty(t, e.matrix.state[gid].Cells)
		require.Equal(t, PricingStageShadow, e.matrix.state[gid].Config.PricingStage)
	}
	require.Equal(t, 1, mxGroupResult(t, report, 12).CellsInserted)
}

func mxChannelConfigForTest() MatrixGroupConfig {
	cfg := DeriveGroupState(mxChannel(1, 10), mxGroup(10, PlatformOpenAI), nil).Config
	return cfg
}

func TestPricingDerivationService_FactsAreQueriedOnlyForNeededModels(t *testing.T) {
	build := func() *Channel {
		ch := mxChannel(1, 10)
		empty := mxPricing(1, PlatformOpenAI, BillingModeToken, "gpt-5.6-luna", "wild*")
		priced := mxPricing(2, PlatformOpenAI, BillingModeToken, "gpt-5.5")
		priced.InputPrice = mxF(1)
		perReq := mxPricing(3, PlatformOpenAI, BillingModePerRequest, "gpt-image-2")
		ch.ModelPricing = []ChannelModelPricing{empty, priced, perReq}
		return ch
	}

	t.Run("有事实来源：不敏感的空价条目开放分组不写行", func(t *testing.T) {
		e := newMxEnv(mxOpenAIGroups(10)...)
		e.facts.facts["gpt-5.6-luna"] = OfficialPriceFact{HasPrice: true}
		e.setChannel(build())
		_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"gpt-5.6-luna"}, e.facts.calls, "只查空价 token 精确条目")
		keys := map[string]bool{}
		for _, c := range e.matrix.state[10].Cells {
			keys[c.ModelKey] = true
		}
		require.False(t, keys["gpt-5.6-luna"])
		require.True(t, keys["gpt-5.5"])
		require.True(t, keys["gpt-image-2"])
	})
	t.Run("没有事实来源：按事实未知处理，写空 custom", func(t *testing.T) {
		e := newMxEnv(mxOpenAIGroups(10)...)
		e.svc = NewPricingDerivationService(e.matrix, e.repo, nil)
		e.setChannel(build())
		_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
		require.NoError(t, err)
		var found *StoredMatrixCell
		for i := range e.matrix.state[10].Cells {
			if e.matrix.state[10].Cells[i].ModelKey == "gpt-5.6-luna" {
				found = &e.matrix.state[10].Cells[i]
			}
		}
		require.NotNil(t, found)
		require.Equal(t, MatrixPriceCustom, found.PriceMode)
	})
}

// ---------------------------------------------------------------------------
// best-effort：失败、panic、超时、请求取消
// ---------------------------------------------------------------------------

func TestPricingDerivationService_HookNeverPropagatesFailures(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(e *mxEnv)
		wantPanics int64
	}{
		{"分组元数据读取失败", func(e *mxEnv) { e.matrix.metaErr = errors.New("meta down") }, 0},
		{"成本核算行列表读取失败", func(e *mxEnv) { e.matrix.ruleErr = errors.New("rules down") }, 0},
		{"落库失败", func(e *mxEnv) { e.matrix.applyErr = errors.New("tx failed") }, 0},
		{"落库时 panic", func(e *mxEnv) { e.matrix.applyPanic = true }, 1},
		{"渠道读取失败", func(e *mxEnv) {
			e.repo.getByIDFn = func(context.Context, int64) (*Channel, error) { return nil, errors.New("db down") }
		}, 0},
		{"查询分组所属渠道失败", func(e *mxEnv) {
			e.setChannel(mxChannelWithRule(1, 1e-6, 10))
			e.repo.getChannelIDByGroupIDFn = func(context.Context, int64) (int64, error) { return 0, errors.New("db down") }
			e.owner = map[int64]int64{}
			// 保存的渠道不含分组 11，必须去查它的所属渠道。
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newMxEnv(mxOpenAIGroups(10, 11)...)
			e.setChannel(mxChannelWithRule(1, 1e-6, 10))
			c.setup(e)
			require.NotPanics(t, func() { e.svc.AfterChannelSaved(context.Background(), 1, []int64{10, 11}) })

			st := e.svc.Stats()
			require.Equal(t, int64(1), st.Runs)
			require.Equal(t, int64(1), st.Failures)
			require.Equal(t, c.wantPanics, st.Panics)
			require.Empty(t, e.matrix.state, "失败时不留下半成品")
		})
	}
}

func TestPricingDerivationService_HookSuccessCountsOnlyRuns(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.svc.AfterChannelSaved(context.Background(), 1, nil)
	require.Equal(t, PricingDeriveStats{Runs: 1}, e.svc.Stats())
	require.Len(t, e.matrix.state[10].Cells, 1)
}

func TestPricingDerivationService_HookTimesOut(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.svc.timeout = 30 * time.Millisecond
	e.matrix.block = true

	done := make(chan struct{})
	go func() {
		e.svc.AfterChannelSaved(context.Background(), 1, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("钩子没有按超时返回")
	}
	require.Equal(t, int64(1), e.svc.Stats().Failures)
}

func TestPricingDerivationService_HookIgnoresRequestCancellation(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 管理员的请求已经结束

	e.svc.AfterChannelSaved(ctx, 1, nil)
	require.NoError(t, e.matrix.ctxErrSeen, "钩子用脱离请求的 context，不会因请求取消而半途而废")
	require.Len(t, e.matrix.state[10].Cells, 1)
	require.Zero(t, e.svc.Stats().Failures)
}

func TestPricingDerivationService_RefreshesAreSerialized(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.RefreshChannel(context.Background(), 1, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), e.matrix.maxInflight.Load(), "同一进程里的刷新串行执行")
	require.Len(t, e.matrix.state[10].Cells, 1)
	require.Len(t, e.matrix.state[10].Rules, 1)
}

// ---------------------------------------------------------------------------
// 只读查看
// ---------------------------------------------------------------------------

func TestPricingDerivationService_ViewGroupAndChannelAreReadOnly(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10, 11)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10, 11))

	view, err := e.svc.ViewGroup(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(10), view.GroupID)
	require.Equal(t, PlatformOpenAI, view.Platform)
	require.Equal(t, int64(1), view.Derived.ChannelID)
	require.Nil(t, view.StoredConfig)
	require.Equal(t, []StoredMatrixCell{}, view.StoredCells)
	require.Equal(t, []StoredMatrixCostRule{}, view.StoredRules)
	require.False(t, view.InSync)
	require.True(t, view.Plan.ConfigWrite)
	require.Equal(t, 1, view.Plan.CellsInsert)
	require.Equal(t, 1, view.Plan.RulesReplaced)
	require.Empty(t, e.matrix.applyCalls, "只读查看不写任何东西")
	require.Empty(t, e.matrix.state)

	views, err := e.svc.ViewChannel(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, int64(10), views[0].GroupID)
	require.Equal(t, int64(11), views[1].GroupID)
	require.Empty(t, e.matrix.applyCalls)

	_, err = e.svc.RefreshChannel(context.Background(), 1, nil)
	require.NoError(t, err)
	view, err = e.svc.ViewGroup(context.Background(), 10)
	require.NoError(t, err)
	require.True(t, view.InSync)
	require.NotNil(t, view.StoredConfig)
	require.Len(t, view.StoredCells, 1)
}

func TestPricingDerivationService_ViewGroupV2IsInSyncBecauseHookSkipsIt(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	e.matrix.state[10] = GroupStateSnapshot{Config: &StoredGroupConfig{GroupID: 10, PricingStage: PricingStageV2, MatrixGroupConfig: defaultMatrixGroupConfig()}}

	view, err := e.svc.ViewGroup(context.Background(), 10)
	require.NoError(t, err)
	require.True(t, view.InSync)
	require.True(t, view.Plan.Skipped)
	require.Equal(t, planSkipStageV2, view.Plan.SkipReason)
}

func TestPricingDerivationService_ViewNotFound(t *testing.T) {
	e := newMxEnv(mxOpenAIGroups(10)...)
	_, err := e.svc.ViewGroup(context.Background(), 99)
	require.ErrorIs(t, err, ErrGroupNotFound)
	_, err = e.svc.ViewChannel(context.Background(), 99)
	require.ErrorIs(t, err, ErrChannelNotFound)

	// 没有渠道的分组：按默认状态展示。
	view, err := e.svc.ViewGroup(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, view.Derived.ChannelID)
	require.True(t, view.InSync)
}

func TestUniqueSortedIDs(t *testing.T) {
	require.Equal(t, []int64{1, 2, 3}, uniqueSortedIDs([]int64{3, 1}, nil, []int64{2, 3, 1}))
	require.Nil(t, uniqueSortedIDs())
}

// ---------------------------------------------------------------------------
// ChannelService 的保存钩子：零行为变化
// ---------------------------------------------------------------------------

type mxHookCall struct {
	channelID int64
	previous  []int64
}

type mxRecordingHook struct {
	mu        sync.Mutex
	calls     []mxHookCall
	panicking bool
}

func (h *mxRecordingHook) AfterChannelSaved(_ context.Context, channelID int64, previous []int64) {
	h.mu.Lock()
	h.calls = append(h.calls, mxHookCall{channelID, previous})
	h.mu.Unlock()
	if h.panicking {
		panic("hook exploded")
	}
}

type mxChannelSvcEnv struct {
	svc           *ChannelService
	repo          *mockChannelRepository
	getGroupIDsFn int
}

func newMxChannelSvcEnv() *mxChannelSvcEnv {
	e := &mxChannelSvcEnv{}
	stored := mxChannel(5, 10)
	e.repo = &mockChannelRepository{
		createFn:  func(_ context.Context, ch *Channel) error { ch.ID = 5; return nil },
		getByIDFn: func(context.Context, int64) (*Channel, error) { return stored.Clone(), nil },
		getGroupIDsFn: func(context.Context, int64) ([]int64, error) {
			e.getGroupIDsFn++
			return []int64{10, 11}, nil
		},
	}
	e.svc = NewChannelService(e.repo, nil, nil, nil, nil)
	return e
}

func mxSaveChannelThreeWays(t *testing.T, svc *ChannelService) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.Create(ctx, &CreateChannelInput{Name: "c", GroupIDs: []int64{10}})
	require.NoError(t, err)
	desc := "d"
	_, err = svc.Update(ctx, 5, &UpdateChannelInput{Description: &desc})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, 5))
}

func TestChannelService_SaveHookIsCalledAfterEachSave(t *testing.T) {
	e := newMxChannelSvcEnv()
	hook := &mxRecordingHook{}
	e.svc.SetSaveHook(hook)
	mxSaveChannelThreeWays(t, e.svc)

	require.Equal(t, []mxHookCall{
		{channelID: 5, previous: nil},
		{channelID: 5, previous: []int64{10, 11}},
		{channelID: 5, previous: []int64{10, 11}},
	}, hook.calls)
}

func TestChannelService_SaveHookNotCalledWhenSaveFails(t *testing.T) {
	e := newMxChannelSvcEnv()
	hook := &mxRecordingHook{}
	e.svc.SetSaveHook(hook)
	boom := errors.New("write failed")
	e.repo.createFn = func(context.Context, *Channel) error { return boom }
	e.repo.updateFn = func(context.Context, *Channel) error { return boom }
	e.repo.deleteFn = func(context.Context, int64) error { return boom }

	ctx := context.Background()
	_, err := e.svc.Create(ctx, &CreateChannelInput{Name: "c", GroupIDs: []int64{10}})
	require.Error(t, err)
	desc := "d"
	_, err = e.svc.Update(ctx, 5, &UpdateChannelInput{Description: &desc})
	require.Error(t, err)
	require.Error(t, e.svc.Delete(ctx, 5))
	require.Empty(t, hook.calls)
}

func TestChannelService_PanickingHookDoesNotAffectSave(t *testing.T) {
	e := newMxChannelSvcEnv()
	hook := &mxRecordingHook{panicking: true}
	e.svc.SetSaveHook(hook)
	require.NotPanics(t, func() { mxSaveChannelThreeWays(t, e.svc) })
	require.Len(t, hook.calls, 3)
}

func TestChannelService_FailingDerivationHookDoesNotAffectSave(t *testing.T) {
	e := newMxChannelSvcEnv()
	matrix := &mxFakeMatrixRepo{groups: map[int64]DeriveGroup{10: {ID: 10, Platform: PlatformOpenAI}}, state: map[int64]GroupStateSnapshot{}}
	matrix.applyErr = errors.New("matrix tables unavailable")
	derive := NewPricingDerivationService(matrix, e.repo, nil)
	e.svc.SetSaveHook(derive)

	mxSaveChannelThreeWays(t, e.svc)
	require.Equal(t, int64(3), derive.Stats().Runs)
	require.Equal(t, int64(3), derive.Stats().Failures)
}

func TestChannelService_RealDerivationHookWritesAfterSave(t *testing.T) {
	e := newMxChannelSvcEnv()
	matrix := &mxFakeMatrixRepo{groups: map[int64]DeriveGroup{10: {ID: 10, Platform: PlatformOpenAI}}, state: map[int64]GroupStateSnapshot{}}
	derive := NewPricingDerivationService(matrix, e.repo, nil)
	e.svc.SetSaveHook(derive)

	_, err := e.svc.Create(context.Background(), &CreateChannelInput{Name: "c", GroupIDs: []int64{10}})
	require.NoError(t, err)
	require.Equal(t, int64(1), derive.Stats().Runs)
	require.NotNil(t, matrix.state[10].Config)
}

// 零行为变化：没有设置钩子时，渠道保存的数据库调用与此前完全一致。
func TestChannelService_WithoutHookNoExtraRepositoryCalls(t *testing.T) {
	e := newMxChannelSvcEnv()
	desc := "d"
	_, err := e.svc.Update(context.Background(), 5, &UpdateChannelInput{Description: &desc})
	require.NoError(t, err)
	require.Zero(t, e.getGroupIDsFn, "没有钩子也没有 auth 缓存失效器时，Update 不读取分组列表")

	e2 := newMxChannelSvcEnv()
	e2.svc.SetSaveHook(&mxRecordingHook{})
	_, err = e2.svc.Update(context.Background(), 5, &UpdateChannelInput{Description: &desc})
	require.NoError(t, err)
	require.Equal(t, 1, e2.getGroupIDsFn, "设置钩子之后只多一次分组列表读取")

	// Delete 原本就会读取分组列表，次数不变。
	e3 := newMxChannelSvcEnv()
	require.NoError(t, e3.svc.Delete(context.Background(), 5))
	require.Equal(t, 1, e3.getGroupIDsFn)
}
