//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 内存里的「库」：WithTx 同时充当行锁（串行执行）与事务（出错整体回滚）。
// ---------------------------------------------------------------------------

type ssApproval struct {
	PriceWriteApproval
	consumed bool
}

type ssState struct {
	cfg       StoredGroupConfig
	cells     []StoredMatrixCell
	rules     []StoredMatrixCostRule
	audit     []StageAuditRecord
	approvals map[int64]*ssApproval
	archived  []StoredMatrixCell
	nextCell  int64
}

func (s ssState) clone() ssState {
	out := s
	out.cells = append([]StoredMatrixCell(nil), s.cells...)
	out.rules = append([]StoredMatrixCostRule(nil), s.rules...)
	out.audit = append([]StageAuditRecord(nil), s.audit...)
	out.archived = append([]StoredMatrixCell(nil), s.archived...)
	out.approvals = map[int64]*ssApproval{}
	for id, a := range s.approvals {
		c := *a
		out.approvals[id] = &c
	}
	return out
}

type ssStore struct {
	mu sync.Mutex
	ssState

	// 闸门读到的、不在事务状态里的事实。
	shadow   ShadowEvidence
	replay   *ReplayEvidence
	owner    *time.Time
	usage    map[string]int64
	failOp   string // 指定的原语失败一次（用来验证整个事务回滚）
	txRuns   atomic.Int32
	applied  []GroupApplyPlan
	nextAppr int64
}

func newSSStore(stage PricingStage) *ssStore {
	changed := sgNow.Add(-100 * time.Hour)
	s := &ssStore{
		ssState: ssState{
			cfg: StoredGroupConfig{
				GroupID:      7,
				PricingStage: stage,
				Revision:     3,
				UpdatedAt:    changed,
			},
			approvals: map[int64]*ssApproval{},
			nextCell:  100,
		},
	}
	s.cfg.StageChangedAt = &changed
	s.cfg.AccessMode = MatrixAccessOpen
	s.cfg.CostMode = MatrixCostAccountRate
	s.replay = &ReplayEvidence{
		ID: 11, GroupID: 7, RecordedAt: sgNow.Add(-2 * time.Hour),
		WindowFrom: sgNow.Add(-31 * 24 * time.Hour), WindowTo: sgNow.Add(-3 * time.Hour),
		MatrixSource: "derived", Passed: true, BindingStable: true, RowsReplayed: 1000,
		ChannelConfigHash: "chan-1", DeriveRevision: "rev-1",
	}
	return s
}

func (s *ssStore) Reader() MatrixExecutor { return nil }

func (s *ssStore) WithTx(ctx context.Context, fn func(ctx context.Context, tx MatrixTx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txRuns.Add(1)
	before := s.ssState.clone()
	if err := fn(ctx, nil); err != nil {
		s.ssState = before
		return err
	}
	return nil
}

func (s *ssStore) InsertApproval(_ context.Context, a PriceWriteApproval) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextAppr++
	a.ID = 500 + s.nextAppr
	s.approvals[a.ID] = &ssApproval{PriceWriteApproval: a}
	return a.ID, nil
}

func (s *ssStore) ConsumeApproval(_ context.Context, _ MatrixExecutor, id int64, planHash, kind string, approverID int64, now time.Time) (*PriceWriteApproval, error) {
	a, ok := s.approvals[id]
	switch {
	case !ok:
		return nil, infraerrors.NotFound(ReasonPriceWriteApproval, "no such approval")
	case a.consumed:
		return nil, infraerrors.Conflict(ReasonPriceWriteApproval, "approval already used")
	case a.PlanHash != planHash || a.Kind != kind:
		return nil, infraerrors.Conflict(ReasonPriceWritePlanChanged, "the plan changed, preview again")
	case now.After(a.ExpiresAt):
		return nil, infraerrors.Conflict(ReasonPriceWriteApproval, "approval expired")
	}
	a.consumed = true
	a.ApprovedBy = approverID
	out := a.PriceWriteApproval
	return &out, nil
}

func (s *ssStore) PurgeStale(context.Context, time.Time) (int64, error) { return 0, nil }

func (s *ssStore) fail(op string) error {
	if s.failOp == op {
		s.failOp = ""
		return errors.New("injected failure in " + op)
	}
	return nil
}

func (s *ssStore) LockConfig(context.Context, MatrixExecutor, int64) (*StoredGroupConfig, error) {
	c := s.cfg
	return &c, nil
}

func (s *ssStore) LoadGateFacts(context.Context, MatrixExecutor, int64, time.Time) (*StageGateFacts, error) {
	facts := &StageGateFacts{
		Config: StageGateConfig{GroupID: 7, Stage: s.cfg.PricingStage, Revision: s.cfg.Revision,
			StageChangedAt: s.cfg.StageChangedAt, UpdatedAt: s.cfg.UpdatedAt},
		OwnerChannelUpdatedAt: s.owner,
		Shadow:                s.shadow,
		Replay:                s.replay,
	}
	return facts, nil
}

func (s *ssStore) LoadSnapshot(context.Context, MatrixExecutor, int64) (GroupStateSnapshot, error) {
	c := s.cfg
	return GroupStateSnapshot{Config: &c, Cells: append([]StoredMatrixCell(nil), s.cells...), Rules: append([]StoredMatrixCostRule(nil), s.rules...)}, nil
}

func (s *ssStore) ApplyPlan(_ context.Context, _ MatrixExecutor, plan GroupApplyPlan) error {
	if err := s.fail("ApplyPlan"); err != nil {
		return err
	}
	s.applied = append(s.applied, plan)
	if plan.ConfigWrite != nil {
		s.cfg.MatrixGroupConfig = *plan.ConfigWrite
	}
	del := map[int64]bool{}
	for _, id := range plan.CellDeletes {
		del[id] = true
	}
	kept := s.cells[:0:0]
	for _, c := range s.cells {
		if !del[c.ID] {
			kept = append(kept, c)
		}
	}
	s.cells = kept
	for _, c := range plan.CellInserts {
		s.nextCell++
		s.cells = append(s.cells, StoredMatrixCell{ID: s.nextCell, GroupID: 7, Revision: 1, MatrixCell: c})
	}
	for _, id := range plan.RuleDeletes {
		kept := s.rules[:0:0]
		for _, r := range s.rules {
			if r.ID != id {
				kept = append(kept, r)
			}
		}
		s.rules = kept
	}
	for _, r := range plan.RuleInserts {
		s.rules = append(s.rules, StoredMatrixCostRule{ID: int64(900 + len(s.rules)), ScopeGroupID: 7, MatrixCostRule: r})
	}
	return nil
}

func (s *ssStore) FreezeDerived(context.Context, MatrixExecutor, int64) (cells, rules int64, err error) {
	if err := s.fail("FreezeDerived"); err != nil {
		return 0, 0, err
	}
	for i := range s.cells {
		if s.cells[i].Source == MatrixSourceLegacyDerived {
			s.cells[i].Source = MatrixSourceLegacyFrozen
			cells++
		}
	}
	for i := range s.rules {
		if s.rules[i].Source == MatrixSourceLegacyDerived {
			s.rules[i].Source = MatrixSourceLegacyFrozen
			rules++
		}
	}
	return cells, rules, nil
}

func (s *ssStore) ArchiveNonDerived(context.Context, MatrixExecutor, int64, int64, int64) (StageArchive, error) {
	if err := s.fail("ArchiveNonDerived"); err != nil {
		return StageArchive{}, err
	}
	var out StageArchive
	kept := s.cells[:0:0]
	for _, c := range s.cells {
		if c.Source == MatrixSourceLegacyDerived {
			kept = append(kept, c)
			continue
		}
		s.archived = append(s.archived, c)
		out.Cells++
	}
	s.cells = kept
	keptRules := s.rules[:0:0]
	for _, r := range s.rules {
		if r.Source == MatrixSourceLegacyDerived {
			keptRules = append(keptRules, r)
			continue
		}
		out.Rules++
	}
	s.rules = keptRules
	out.RulesJSON = []byte(`[]`)
	return out, nil
}

func (s *ssStore) SetStage(_ context.Context, _ MatrixExecutor, _ int64, to PricingStage, _ int64, now time.Time) (int64, error) {
	if err := s.fail("SetStage"); err != nil {
		return 0, err
	}
	s.cfg.PricingStage = to
	s.cfg.Revision++
	s.cfg.StageChangedAt = &now
	s.cfg.UpdatedAt = now
	return s.cfg.Revision, nil
}

func (s *ssStore) InsertAudit(_ context.Context, _ MatrixExecutor, rec StageAuditRecord) (int64, error) {
	if err := s.fail("InsertAudit"); err != nil {
		return 0, err
	}
	s.audit = append(s.audit, rec)
	return int64(len(s.audit)), nil
}

func (s *ssStore) RecentUsageModels(context.Context, MatrixExecutor, int64, time.Time) (map[string]int64, error) {
	return s.usage, nil
}

func (s *ssStore) ListAudit(_ context.Context, _ MatrixExecutor, _ int64, limit int) ([]StageAuditEntry, error) {
	out := []StageAuditEntry{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		a := s.audit[i]
		out = append(out, StageAuditEntry{ID: int64(i + 1), GroupID: a.GroupID, From: a.From, To: a.To, Kind: a.Kind,
			OperatorID: a.Actor.ID, Interactive: a.Actor.Interactive, PriceDelta: a.PriceDelta})
	}
	return out, nil
}

type ssDeriver struct {
	state DerivedGroupState
	err   error
}

func (d *ssDeriver) DeriveGroupCurrent(context.Context, int64) (DerivedGroupState, string, error) {
	return d.state, PlatformOpenAI, d.err
}

type ssFingerprint struct {
	mu   sync.Mutex
	hash string
	err  error
}

func (f *ssFingerprint) Fingerprint(context.Context, []int64) (*PricingReplayFingerprint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &PricingReplayFingerprint{ChannelConfigHash: f.hash, GroupMatrixHash: map[int64]string{}}, nil
}

func (f *ssFingerprint) set(h string) {
	f.mu.Lock()
	f.hash = h
	f.mu.Unlock()
}

type ssSync struct {
	mu          sync.Mutex
	invalidated []int64
	ensured     []int64
	ensureErr   error
}

func (s *ssSync) InvalidateGroups(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated = append(s.invalidated, ids...)
}

func (s *ssSync) EnsureGroupsLoaded(_ context.Context, ids ...int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured = append(s.ensured, ids...)
	return s.ensureErr
}

type ssFixture struct {
	store  *ssStore
	derive *ssDeriver
	fp     *ssFingerprint
	sync   *ssSync
	sw     *PricingStageSwitcher
}

func newSSFixture(stage PricingStage) *ssFixture {
	store := newSSStore(stage)
	f := &ssFixture{
		store: store,
		derive: &ssDeriver{state: DerivedGroupState{GroupID: 7, ChannelID: 3, Revision: "rev-1", Config: store.cfg.MatrixGroupConfig,
			Cells: []MatrixCell{{ModelKey: "gpt-5.4", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}}}},
		fp:   &ssFingerprint{hash: "chan-1"},
		sync: &ssSync{},
	}
	f.sw = NewPricingStageSwitcher(store, f.derive, f.fp, nil, f.sync, nil)
	f.sw.now = func() time.Time { return sgNow }
	// 分组现有的派生行与派生结果一致。
	store.cells = []StoredMatrixCell{{ID: 1, GroupID: 7, Revision: 1,
		MatrixCell: MatrixCell{ModelKey: "gpt-5.4", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived}}}
	return f
}

// jwt、machine 是两种鉴权方式（不能自己构造 Interactive，只能由鉴权方式得出）。
const ssMachine = "admin_token"

func (f *ssFixture) commitReq(approval int64, authMethod string) PricingStageSwitchRequest {
	return PricingStageSwitchRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42, Confirm: true, ApprovalID: approval, AuthMethod: authMethod}
}

// preview 走一遍预览，返回凭证 id（闸门不通过时为 0）。
func (f *ssFixture) preview(t *testing.T) *PricingStagePreview {
	t.Helper()
	p, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42})
	require.NoError(t, err)
	return p
}

func (f *ssFixture) requireUntouched(t *testing.T, stage PricingStage, rev int64) {
	t.Helper()
	require.Equal(t, stage, f.store.cfg.PricingStage)
	require.Equal(t, rev, f.store.cfg.Revision)
	require.Empty(t, f.store.audit, "nothing was audited")
	require.Empty(t, f.sync.invalidated, "nothing was invalidated")
}

// ---------------------------------------------------------------------------
// 预览
// ---------------------------------------------------------------------------

func TestStageSwitchPreview_V2ReturnsGateAndRegistersAnApproval(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.store.replay.Diffs = []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassExpected, Reason: PricingReplayReasonUnpricedZero, Count: 3}}
	p := f.preview(t)

	require.True(t, p.Executable)
	require.Equal(t, StageKindAdvance, p.Kind)
	require.NotZero(t, p.ApprovalID)
	require.Len(t, p.PlanHash, 64)
	require.NotNil(t, p.ExpiresAt)
	require.NotNil(t, p.Gate)
	require.True(t, p.Gate.Passed)
	// 目录检查没接上（catalog 为 nil）：证明不了没有被挡的模型，方向 unknown，必须交互式会话。
	require.Equal(t, PriceDeltaUnknown, p.PriceDelta)
	require.Equal(t, "catalog_check_unavailable", p.Accepted[0].Reason)

	a := f.store.approvals[p.ApprovalID]
	require.Equal(t, PriceWriteKindStageSwitch, a.Kind)
	require.True(t, a.TouchesPrice)
	require.Equal(t, p.PlanHash, a.PlanHash)
	require.Equal(t, []int64{7}, a.GroupIDs)
	require.EqualValues(t, 42, a.PreviewedBy)
	require.Equal(t, PriceDeltaUnknown, a.Delta)
	require.Contains(t, string(a.Summary), "accepted_differences")
	// 预览不改任何数据。
	f.store.audit = nil
	require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage)
	require.Empty(t, f.sync.invalidated)
}

func TestStageSwitchPreview_DirectionIsNoneOnlyWithEvidence(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.sw.catalog = &spCatalogSource{} // 目录可读、近 7 天没有被挡的模型
	f.store.usage = map[string]int64{"gpt-5.4": 10}
	f.store.replay.Diffs = []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassExpected, Reason: PricingReplayReasonUnpricedZero, Count: 3}}
	p := f.preview(t)
	require.Equal(t, PriceDeltaNone, p.PriceDelta)
	require.Equal(t, 1, len(p.Accepted), "the legacy-zero equivalent difference is listed on its own")
	require.Equal(t, PricingReplayReasonUnpricedZero, p.Accepted[0].Reason)

	// 目录里 draft 的模型近 7 天有流量：切过去那一刻起会被挡，方向 unknown。
	f2 := newSSFixture(PricingStageShadow)
	f2.sw.catalog = &spCatalogSource{entries: []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogDraft)}}
	f2.store.usage = map[string]int64{"gpt-5.4": 10}
	p2 := f2.preview(t)
	require.Equal(t, PriceDeltaUnknown, p2.PriceDelta)
	require.Equal(t, AcceptedReasonCatalogDraft, p2.Accepted[0].Reason)

	// 目录读不出来：同样 unknown。
	f3 := newSSFixture(PricingStageShadow)
	f3.sw.catalog = &spCatalogSource{err: errors.New("down")}
	require.Equal(t, PriceDeltaUnknown, f3.preview(t).PriceDelta)
}

// 影子差异表是有损采样：本实例进程内的翻译差异计数大于 0 时，预览与提交都不放行。
func TestStageSwitch_InProcessTranslationDiffsBlockTheGate(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)
	require.True(t, p.Executable)

	f.sw.SetInProcessTranslationDiffs(func(groupID int64) int64 {
		require.EqualValues(t, 7, groupID)
		return 2
	})
	blocked := f.preview(t)
	require.False(t, blocked.Executable)
	require.Zero(t, blocked.ApprovalID)
	require.Equal(t, []string{ReasonPricingGateShadowDiffsInProcess}, sgCodes(*blocked.Gate))
	require.EqualValues(t, 2, blocked.Gate.Shadow.TranslationDiffsInProcess)

	// 提交时重新评估：之前登记的凭证也不能用。
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.Equal(t, ReasonPricingGateShadowDiffsInProcess, infraerrors.Reason(err))
	require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage)
	require.Empty(t, f.store.audit)
}

// 回放窗口内有流量却一行都没回放上：闸门不放行；窗口内确实没有请求时豁免，但价格方向是 unknown。
func TestStageSwitchPreview_EmptyReplayOnlyPassesWithoutTraffic(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.store.replay.RowsReplayed, f.store.replay.RowsInWindow = 0, 9
	p := f.preview(t)
	require.False(t, p.Executable)
	require.Equal(t, []string{ReasonPricingGateReplayEmpty}, sgCodes(*p.Gate))

	f2 := newSSFixture(PricingStageShadow)
	f2.sw.catalog = &spCatalogSource{}
	f2.store.replay.RowsReplayed, f2.store.replay.RowsInWindow = 0, 0
	p2 := f2.preview(t)
	require.True(t, p2.Executable, "no requests in the window: nothing to compare")
	require.Equal(t, PriceDeltaUnknown, p2.PriceDelta, "nothing was replayed, so no price evidence")
}

func TestStageSwitchPreview_FailedGateRegistersNothing(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.store.replay = nil
	f.store.shadow.TranslationDiffs = 4
	p := f.preview(t)
	require.False(t, p.Executable)
	require.Zero(t, p.ApprovalID)
	require.Empty(t, p.PlanHash)
	require.False(t, p.Gate.Passed)
	require.Equal(t, []string{ReasonPricingGateReplayMissing, ReasonPricingGateShadowDiffs}, sgCodes(*p.Gate))
	require.Empty(t, f.store.approvals)
}

func TestStageSwitchPreview_LateralAndNoop(t *testing.T) {
	f := newSSFixture(PricingStageLegacy)
	p, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42})
	require.NoError(t, err)
	require.True(t, p.Executable)
	require.Equal(t, StageKindAdvance, p.Kind)
	require.Nil(t, p.Gate)
	require.Zero(t, p.ApprovalID)

	p, err = f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42})
	require.NoError(t, err)
	require.Equal(t, stageKindNoop, p.Kind)
	require.True(t, p.Executable)

	// legacy 不能直接到 v2：闸门要求先在 shadow 观察。
	p = f.preview(t)
	require.Contains(t, sgCodes(*p.Gate), ReasonPricingGateNotInShadow)
	require.False(t, p.Executable)
}

func TestStageSwitchPreview_Validation(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	_, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2})
	require.Equal(t, ReasonPricingStageActor, infraerrors.Reason(err))
	_, err = f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 0, To: PricingStageV2, OperatorID: 1})
	require.Equal(t, "INVALID_PARAMETER", infraerrors.Reason(err))
	_, err = f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: "other", OperatorID: 1})
	require.Equal(t, ReasonPricingStageNotAllowed, infraerrors.Reason(err))
}

func TestStageSwitchPreview_RollbackListsWhatIsArchived(t *testing.T) {
	f := newSSFixture(PricingStageV2)
	f.store.cells = append(f.store.cells,
		StoredMatrixCell{ID: 2, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "x", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}},
		StoredMatrixCell{ID: 3, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "y", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyFrozen}})
	p, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42})
	require.NoError(t, err)
	require.Equal(t, StageKindRollback, p.Kind)
	require.True(t, p.Executable)
	require.Nil(t, p.Gate)
	require.Zero(t, p.ApprovalID, "a rollback needs no approval")
	require.NotNil(t, p.Rollback)
	require.Equal(t, 2, p.Rollback.ArchivedCells)
	require.Equal(t, PriceDeltaUnknown, p.PriceDelta, "dropped edits cannot be proven price neutral")

	// 干净的 v2 分组（只有冻结的派生行、与派生结果一致）：回拨不改任何东西，方向 none。
	clean := newSSFixture(PricingStageV2)
	clean.store.cells[0].Source = MatrixSourceLegacyFrozen
	p, err = clean.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42})
	require.NoError(t, err)
	require.Equal(t, 1, p.Rollback.ArchivedCells, "the frozen row is archived and then derived again")
	require.False(t, p.Rollback.Drift.Changed)
	require.Equal(t, PriceDeltaNone, p.PriceDelta)
}

// ---------------------------------------------------------------------------
// 提交：shadow 到 v2
// ---------------------------------------------------------------------------

func TestStageSwitchCommit_V2Success(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)
	res, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)

	require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
	require.EqualValues(t, 4, f.store.cfg.Revision)
	require.Equal(t, StageKindAdvance, res.Kind)
	require.True(t, res.Changed)
	require.True(t, res.SnapshotReady)
	require.EqualValues(t, 1, res.AuditID)
	require.NotNil(t, res.Gate)
	require.True(t, res.Gate.Passed)
	require.True(t, f.store.approvals[p.ApprovalID].consumed)

	// 派生行冻结。
	require.Equal(t, MatrixSourceLegacyFrozen, f.store.cells[0].Source)

	// 审计：谁、从哪到哪、闸门证据。
	require.Len(t, f.store.audit, 1)
	a := f.store.audit[0]
	require.EqualValues(t, 42, a.Actor.ID)
	require.True(t, a.Actor.Interactive)
	require.Equal(t, PricingStageShadow, a.From)
	require.Equal(t, PricingStageV2, a.To)
	require.Equal(t, StageKindAdvance, a.Kind)
	require.Equal(t, p.ApprovalID, a.ApprovalID)
	require.EqualValues(t, 3, a.RevisionBefore)
	require.EqualValues(t, 4, a.RevisionAfter)
	for _, want := range []string{`"gate"`, `"plan_hash"`, `"accepted_differences"`, `"frozen_cells"`, `"channel_config_hash"`, `"derive_revision"`} {
		require.Contains(t, string(a.Evidence), want)
	}

	// 提交之后失效并同步加载快照。
	require.Equal(t, []int64{7}, f.sync.invalidated)
	require.Equal(t, []int64{7}, f.sync.ensured)
}

func TestStageSwitchCommit_SnapshotNotReadyIsReportedNotHidden(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.sync.ensureErr = errors.New("db down")
	p := f.preview(t)
	res, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err, "the switch is committed")
	require.False(t, res.SnapshotReady)
	require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
}

func TestStageSwitchCommit_NeedsAnApproval(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	_, err := f.sw.Commit(context.Background(), f.commitReq(0, AuditAuthMethodJWT))
	require.Equal(t, ReasonPriceWriteApproval, infraerrors.Reason(err))
	f.requireUntouched(t, PricingStageShadow, 3)

	_, err = f.sw.Commit(context.Background(), f.commitReq(12345, AuditAuthMethodJWT))
	require.Equal(t, ReasonPriceWriteApproval, infraerrors.Reason(err))
	f.requireUntouched(t, PricingStageShadow, 3)

	req := f.commitReq(1, AuditAuthMethodJWT)
	req.Confirm = false
	_, err = f.sw.Commit(context.Background(), req)
	require.Equal(t, ReasonPricingStageConfirm, infraerrors.Reason(err))
}

func TestStageSwitchCommit_MachineCredentialsCannotConfirmAPriceChange(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)
	require.Equal(t, PriceDeltaUnknown, p.PriceDelta)
	for _, method := range []string{ssMachine, "api_key", ""} {
		_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, method))
		require.Equal(t, ReasonPriceWriteInteractive, infraerrors.Reason(err), method)
		require.False(t, f.store.approvals[p.ApprovalID].consumed, "the approval stays usable after the rolled back attempt")
		f.requireUntouched(t, PricingStageShadow, 3)
	}
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)
}

func TestStageSwitchCommit_GateIsReevaluatedAtCommitTime(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *ssFixture)
		want   string
	}{
		{"shadow translation differences appeared", func(f *ssFixture) { f.store.shadow.TranslationDiffs = 1 }, ReasonPricingGateShadowDiffs},
		{"replay was replaced by a failed one", func(f *ssFixture) { f.store.replay.Passed = false }, ReasonPricingGateReplayFailed},
		{"channel config changed after the preview (S-8)", func(f *ssFixture) { f.fp.set("chan-2") }, ReasonPricingGateReplayStale},
		{"derived matrix changed after the preview (S-8)", func(f *ssFixture) { f.derive.state.Revision = "rev-2" }, ReasonPricingGateReplayStale},
		{"channel saved after the preview restarts the observation", func(f *ssFixture) { n := sgNow.Add(-time.Minute); f.store.owner = &n }, ReasonPricingGateObservation},
		{"group edited after the preview restarts the observation", func(f *ssFixture) { f.store.cfg.UpdatedAt = sgNow.Add(-time.Minute) }, ReasonPricingGateObservation},
		{"current evidence cannot be computed", func(f *ssFixture) { f.derive.err = errors.New("boom") }, ReasonPricingGateDeriveFailed},
		{"fingerprint cannot be computed", func(f *ssFixture) { f.fp.err = errors.New("boom") }, ReasonPricingGateDeriveFailed},
	}
	for _, c := range cases {
		f := newSSFixture(PricingStageShadow)
		p := f.preview(t)
		require.NotZero(t, p.ApprovalID, c.name)
		c.mutate(f)
		_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
		require.Error(t, err, c.name)
		require.Equal(t, 409, infraerrors.Code(err), c.name)
		require.Equal(t, c.want, infraerrors.Reason(err), c.name)
		require.Contains(t, infraerrors.FromError(err).Metadata["failures"], c.want, c.name)
		require.False(t, f.store.approvals[p.ApprovalID].consumed, c.name)
		f.requireUntouched(t, PricingStageShadow, 3)
		require.Equal(t, MatrixSourceLegacyDerived, f.store.cells[0].Source, "nothing was frozen: "+c.name)
	}
}

func TestStageSwitchCommit_PlanChangedSincePreviewIsRejected(t *testing.T) {
	// 门槛的几个条件都没变，只有分组配置版本变了（例如有人改了分组配置）：指纹不同，提交被拒绝。
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)
	f.store.cfg.Revision++
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.Equal(t, ReasonPriceWritePlanChanged, infraerrors.Reason(err))
	require.False(t, f.store.approvals[p.ApprovalID].consumed)
	require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage)
	require.Empty(t, f.store.audit)

	// 凭证只能用一次。
	f2 := newSSFixture(PricingStageShadow)
	p2 := f2.preview(t)
	_, err = f2.sw.Commit(context.Background(), f2.commitReq(p2.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)
	_, err = f2.sw.Commit(context.Background(), f2.commitReq(p2.ApprovalID, AuditAuthMethodJWT))
	require.Error(t, err)
}

func TestStageSwitchCommit_ExpiredApprovalIsRejected(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)
	f.sw.now = func() time.Time { return sgNow.Add(PriceWriteApprovalTTL + time.Minute) }
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.Error(t, err)
	require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage)
}

func TestStageSwitchCommit_AnyFailureRollsBackTheWholeTransaction(t *testing.T) {
	for _, op := range []string{"ApplyPlan", "FreezeDerived", "SetStage", "InsertAudit"} {
		f := newSSFixture(PricingStageShadow)
		// 让派生结果与库里不同，这样 ApplyPlan 会真的执行。
		f.derive.state.Cells = append(f.derive.state.Cells, MatrixCell{ModelKey: "new", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived})
		f.store.replay.DeriveRevision = "rev-1"
		p := f.preview(t)
		f.store.failOp = op
		_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
		require.Error(t, err, op)
		require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage, op)
		require.EqualValues(t, 3, f.store.cfg.Revision, op)
		require.Len(t, f.store.cells, 1, op)
		require.Equal(t, MatrixSourceLegacyDerived, f.store.cells[0].Source, op)
		require.False(t, f.store.approvals[p.ApprovalID].consumed, op)
		require.Empty(t, f.store.audit, op)
		require.Empty(t, f.sync.invalidated, "no snapshot invalidation after a failed switch: "+op)

		// 重试成功。
		_, err = f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
		require.NoError(t, err, op)
		require.Equal(t, PricingStageV2, f.store.cfg.PricingStage, op)
		require.Len(t, f.store.audit, 1, op)
	}
}

func TestStageSwitchCommit_V2AlignsDerivedRowsBeforeFreezing(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	f.derive.state.Cells = append(f.derive.state.Cells, MatrixCell{ModelKey: "new-model", Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: f64(2), Source: MatrixSourceLegacyDerived})
	p := f.preview(t)
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)
	require.Len(t, f.store.cells, 2)
	for _, c := range f.store.cells {
		require.Equal(t, MatrixSourceLegacyFrozen, c.Source)
	}
}

func f64(v float64) *float64 { return &v }

func TestStageSwitchCommit_NotInShadow(t *testing.T) {
	for _, stage := range []PricingStage{PricingStageLegacy} {
		f := newSSFixture(stage)
		_, err := f.sw.Commit(context.Background(), f.commitReq(1, AuditAuthMethodJWT))
		require.Equal(t, ReasonPricingGateNotInShadow, infraerrors.Reason(err))
		f.requireUntouched(t, stage, 3)
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

func TestStageSwitchCommit_ConcurrentSwitchesSerialize(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	p := f.preview(t)

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	changes := make([]*PricingStageSwitchResult, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			changes[i], results[i] = f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
		}(i)
	}
	close(start)
	wg.Wait()

	// 第一次提交成功之后，其余的在锁内看到阶段已经是 v2：走 noop 成功返回（不写库、不审计），或者以冲突失败。
	advanced := 0
	for i, err := range results {
		if err != nil {
			continue
		}
		require.NotNil(t, changes[i])
		if changes[i].Changed {
			advanced++
			require.Equal(t, StageKindAdvance, changes[i].Kind)
		} else {
			require.Equal(t, stageKindNoop, changes[i].Kind)
		}
	}
	require.Equal(t, 1, advanced, "exactly one switch actually changed the stage")
	require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
	require.EqualValues(t, 4, f.store.cfg.Revision, "the stage changed exactly once")
	require.Len(t, f.store.audit, 1)
	require.Equal(t, 1, countSyncs(f.sync))
}

func countSyncs(s *ssSync) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.invalidated)
}

func TestStageSwitchCommit_ConcurrentRollbackAndAdvance(t *testing.T) {
	// 一个分组在 v2：并发回拨到 shadow 与回拨到 legacy，各自在锁里看到「当前阶段」，结果一致且每次变更各记一条审计。
	f := newSSFixture(PricingStageV2)
	var wg sync.WaitGroup
	for _, to := range []PricingStage{PricingStageShadow, PricingStageLegacy, PricingStageShadow, PricingStageLegacy} {
		wg.Add(1)
		go func(to PricingStage) {
			defer wg.Done()
			_, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: to, OperatorID: 42, Confirm: true, AuthMethod: AuditAuthMethodJWT})
			require.NoError(t, err)
		}(to)
	}
	wg.Wait()
	require.NotEqual(t, PricingStageV2, f.store.cfg.PricingStage)
	require.GreaterOrEqual(t, len(f.store.audit), 2)
	require.Equal(t, int64(len(f.store.audit)), f.store.cfg.Revision-3, "every audited change bumped the revision once")
	for _, a := range f.store.audit {
		require.NotEqual(t, a.From, a.To)
	}
}

// ---------------------------------------------------------------------------
// 回拨
// ---------------------------------------------------------------------------

func TestStageSwitchCommit_RollbackNeedsNoGateAndIsAudited(t *testing.T) {
	f := newSSFixture(PricingStageV2)
	// 闸门当然不满足（没有回放、不在 shadow），回拨照样可以。
	f.store.replay = nil
	f.store.cells = []StoredMatrixCell{
		{ID: 1, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "gpt-5.4", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyFrozen}},
		{ID: 2, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "edited", Open: true, PriceMode: MatrixPriceExtra, ExtraMultiplier: f64(3), Source: MatrixSourceManual}},
	}
	res, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42, Confirm: true, AuthMethod: AuditAuthMethodJWT})
	require.NoError(t, err)

	require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage)
	require.Equal(t, StageKindRollback, res.Kind)
	require.NotNil(t, res.Archived)
	require.EqualValues(t, 2, res.Archived.Cells)
	require.Len(t, f.store.archived, 2)
	require.True(t, res.SnapshotReady)
	require.Equal(t, []int64{7}, f.sync.invalidated)

	// 回到「渠道派生」的状态：只剩派生行，来源 legacy_derived，手动编辑被归档、不再生效。
	require.Len(t, f.store.cells, 1)
	require.Equal(t, "gpt-5.4", f.store.cells[0].ModelKey)
	require.Equal(t, MatrixSourceLegacyDerived, f.store.cells[0].Source)
	require.Len(t, f.store.audit, 1)
	a := f.store.audit[0]
	require.Equal(t, StageKindRollback, a.Kind)
	require.Equal(t, PricingStageV2, a.From)
	require.Equal(t, PricingStageShadow, a.To)
	require.Zero(t, a.ApprovalID)
	require.Equal(t, PriceDeltaUnknown, a.PriceDelta)
	require.Contains(t, string(a.Evidence), `"archived_cells":2`)
}

func TestStageSwitchCommit_RollbackToLegacyAndFailureRollsBack(t *testing.T) {
	f := newSSFixture(PricingStageV2)
	f.store.cells[0].Source = MatrixSourceLegacyFrozen
	f.store.failOp = "InsertAudit"
	_, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42, Confirm: true})
	require.Error(t, err)
	require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
	require.Equal(t, MatrixSourceLegacyFrozen, f.store.cells[0].Source, "the archive was rolled back with the rest")
	require.Empty(t, f.store.archived)
	require.Empty(t, f.sync.invalidated)

	res, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, PricingStageLegacy, res.To)
	require.Equal(t, PriceDeltaNone, res.PriceDelta, "nothing differed from the channel derivation and nothing was dropped")
}

func TestStageSwitchCommit_LateralAndNoop(t *testing.T) {
	f := newSSFixture(PricingStageLegacy)
	res, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, StageKindAdvance, res.Kind)
	require.Len(t, f.store.audit, 1)
	require.EqualValues(t, 4, f.store.cfg.Revision)

	res, err = f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, StageKindRollback, res.Kind)

	// 已经是目标阶段：不写库、不审计、不失效。
	n := len(f.store.audit)
	invalidated := countSyncs(f.sync)
	res, err = f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42, Confirm: true})
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Equal(t, stageKindNoop, res.Kind)
	require.Len(t, f.store.audit, n)
	require.Equal(t, invalidated, countSyncs(f.sync))
}

func TestStageSwitchAudit(t *testing.T) {
	f := newSSFixture(PricingStageLegacy)
	for _, to := range []PricingStage{PricingStageShadow, PricingStageLegacy, PricingStageShadow} {
		_, err := f.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: to, OperatorID: 42, Confirm: true, AuthMethod: AuditAuthMethodJWT})
		require.NoError(t, err)
	}
	items, err := f.sw.Audit(context.Background(), 7, 2)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, PricingStageShadow, items[0].To, "newest first")
	require.True(t, items[0].Interactive)
	items, err = f.sw.Audit(context.Background(), 7, 0)
	require.NoError(t, err)
	require.Len(t, items, 3, "a non-positive limit falls back to the default")
}

// 阶段服务：接上切换器之后 v2 才是允许的目标；没接上仍然拒绝，预览与审计也不可用。
func TestPricingStageService_V2OnlyWithTheSwitcher(t *testing.T) {
	svc := NewPricingStageService(&fakePricingStageStore{}, nil, nil)
	_, err := svc.Switch(context.Background(), PricingStageSwitchRequest{GroupID: 1, To: PricingStageV2, OperatorID: 9, Confirm: true})
	require.Equal(t, ReasonPricingStageNotAllowed, infraerrors.Reason(err))
	_, err = svc.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 1, To: PricingStageV2, OperatorID: 9})
	require.Error(t, err)
	items, err := svc.Audit(context.Background(), 1, 10)
	require.NoError(t, err)
	require.Empty(t, items)

	f := newSSFixture(PricingStageShadow)
	svc.SetSwitcher(f.sw)
	p, err := svc.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42})
	require.NoError(t, err)
	res, err := svc.Switch(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)
	require.Equal(t, PricingStageV2, res.To)
	require.Equal(t, StageKindAdvance, res.Kind)
}

// ---------------------------------------------------------------------------
// 回拨之后计费回到 legacy 路径，与 legacy 逐位相同
// ---------------------------------------------------------------------------

func TestStageSwitchRollback_BillingIsBitIdenticalToLegacy(t *testing.T) {
	ctx := context.Background()
	base := goldenBaseline(t, []string{"gpt-5.4"}, goldenResult())
	baseMini := goldenBaseline(t, []string{"gpt-5.4-mini"}, &OpenAIForwardResult{Model: "gpt-5.4-mini"})
	legacyLog := mcRunOpenAIRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{}))

	// v2 阶段：矩阵里的 custom 与 extra 单元格改变账单。
	f := newSPFixture(t, v2Snap(nil, 1, goldenWildCells()...))
	v2 := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
	require.NotEqual(t, base.TotalCost, v2.TotalCost)
	v2Log := mcRunOpenAIRecordUsage(t, f.staged)
	require.NotEqual(t, legacyLog.ActualCost, v2Log.ActualCost)

	for _, stage := range []PricingStage{PricingStageShadow, PricingStageLegacy} {
		f := newSPFixture(t, v2Snap(nil, 1, goldenWildCells()...))
		require.Equal(t, PricingStageV2, f.staged.Stage(ctx, 1))

		// 回拨：库里的阶段变回目标阶段（单元格里的内容无论是否被归档，都不再参与计费），提交之后失效并同步加载。
		snap := v2Snap(nil, 1, goldenWildCells()...)
		snap.Config = mpStoredConfig(stage, nil)
		f.src.setSnapshot(1, snap)
		f.staged.InvalidateGroups(1)
		require.NoError(t, f.staged.EnsureGroupsLoaded(ctx, 1))
		require.Equal(t, stage, f.staged.Stage(ctx, 1))

		got := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
		require.Equal(t, *base, *got, "stage %s after the rollback: bitwise the legacy cost", stage)
		gotMini := goldenCost(t, f, []string{"gpt-5.4-mini"}, &OpenAIForwardResult{Model: "gpt-5.4-mini"})
		require.Equal(t, *baseMini, *gotMini, "stage %s", stage)
		gotLog := mcRunOpenAIRecordUsage(t, f.staged)
		require.Equal(t, legacyLog.ActualCost, gotLog.ActualCost, "stage %s", stage)
		require.Equal(t, legacyLog.RateMultiplier, gotLog.RateMultiplier, "stage %s", stage)
		require.Equal(t, legacyLog.TotalCost, gotLog.TotalCost, "stage %s", stage)
	}
}
