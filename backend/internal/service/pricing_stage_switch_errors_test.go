//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ssFailing 在指定原语的第 n 次调用上失败（n 从 1 数起），其余原样转给内存库。
// 用来覆盖阶段切换在每一个存储原语出错时都整体回滚、把错误交回调用方。
type ssFailing struct {
	*ssStore
	failAt map[string]int
	calls  map[string]int
}

// failNext 让 op 的下一次调用失败（之前已经发生的调用不算）。
func (s *ssFailing) failNext(op string) {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.failAt[op] = s.calls[op] + 1
}

func (s *ssFailing) trip(op string) error {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[op]++
	if n, ok := s.failAt[op]; ok && s.calls[op] == n {
		return fmt.Errorf("injected failure in %s #%d", op, n)
	}
	return nil
}

func (s *ssFailing) LockConfig(ctx context.Context, tx MatrixExecutor, id int64) (*StoredGroupConfig, error) {
	if err := s.trip("LockConfig"); err != nil {
		return nil, err
	}
	return s.ssStore.LockConfig(ctx, tx, id)
}

func (s *ssFailing) LoadGateFacts(ctx context.Context, tx MatrixExecutor, id int64, now time.Time) (*StageGateFacts, error) {
	if err := s.trip("LoadGateFacts"); err != nil {
		return nil, err
	}
	return s.ssStore.LoadGateFacts(ctx, tx, id, now)
}

func (s *ssFailing) LoadSnapshot(ctx context.Context, tx MatrixExecutor, id int64) (GroupStateSnapshot, error) {
	if err := s.trip("LoadSnapshot"); err != nil {
		return GroupStateSnapshot{}, err
	}
	return s.ssStore.LoadSnapshot(ctx, tx, id)
}

func (s *ssFailing) ConsumeApproval(ctx context.Context, tx MatrixExecutor, id int64, hash, kind string, by int64, now time.Time) (*PriceWriteApproval, error) {
	if err := s.trip("ConsumeApproval"); err != nil {
		return nil, err
	}
	return s.ssStore.ConsumeApproval(ctx, tx, id, hash, kind, by, now)
}

func (s *ssFailing) InsertApproval(ctx context.Context, a PriceWriteApproval) (int64, error) {
	if err := s.trip("InsertApproval"); err != nil {
		return 0, err
	}
	return s.ssStore.InsertApproval(ctx, a)
}

func (s *ssFailing) PurgeStale(ctx context.Context, before time.Time) (int64, error) {
	if err := s.trip("PurgeStale"); err != nil {
		return 0, err
	}
	return s.ssStore.PurgeStale(ctx, before)
}

func (s *ssFailing) RecentUsageModels(ctx context.Context, tx MatrixExecutor, id int64, since time.Time) (map[string]int64, error) {
	if err := s.trip("RecentUsageModels"); err != nil {
		return nil, err
	}
	return s.ssStore.RecentUsageModels(ctx, tx, id, since)
}

func (s *ssFailing) ListAudit(ctx context.Context, tx MatrixExecutor, id int64, limit int) ([]StageAuditEntry, error) {
	if err := s.trip("ListAudit"); err != nil {
		return nil, err
	}
	return s.ssStore.ListAudit(ctx, tx, id, limit)
}

func (s *ssFailing) ApplyPlan(ctx context.Context, tx MatrixExecutor, plan GroupApplyPlan) error {
	if err := s.trip("ApplyPlan"); err != nil {
		return err
	}
	return s.ssStore.ApplyPlan(ctx, tx, plan)
}

func (s *ssFailing) FreezeDerived(ctx context.Context, tx MatrixExecutor, id int64) (int64, int64, error) {
	if err := s.trip("FreezeDerived"); err != nil {
		return 0, 0, err
	}
	return s.ssStore.FreezeDerived(ctx, tx, id)
}

func (s *ssFailing) ArchiveNonDerived(ctx context.Context, tx MatrixExecutor, id, op, appr int64) (StageArchive, error) {
	if err := s.trip("ArchiveNonDerived"); err != nil {
		return StageArchive{}, err
	}
	return s.ssStore.ArchiveNonDerived(ctx, tx, id, op, appr)
}

func (s *ssFailing) SetStage(ctx context.Context, tx MatrixExecutor, id int64, to PricingStage, op int64, now time.Time) (int64, error) {
	if err := s.trip("SetStage"); err != nil {
		return 0, err
	}
	return s.ssStore.SetStage(ctx, tx, id, to, op, now)
}

func (s *ssFailing) InsertAudit(ctx context.Context, tx MatrixExecutor, rec StageAuditRecord) (int64, error) {
	if err := s.trip("InsertAudit"); err != nil {
		return 0, err
	}
	return s.ssStore.InsertAudit(ctx, tx, rec)
}

// newFailingFixture 把固定件的切换器换成带故障注入的存储。
func newFailingFixture(stage PricingStage) (*ssFixture, *ssFailing) {
	f := newSSFixture(stage)
	w := &ssFailing{ssStore: f.store, failAt: map[string]int{}}
	f.sw = NewPricingStageSwitcher(w, f.derive, f.fp, nil, f.sync, nil)
	f.sw.now = func() time.Time { return sgNow }
	f.sw.SetExposureChecker(ssExposureChecker(f.store, f.settings))
	return f, w
}

var jwtReq = func(to PricingStage, approval int64) PricingStageSwitchRequest {
	return PricingStageSwitchRequest{GroupID: 7, To: to, OperatorID: 42, Confirm: true, ApprovalID: approval, AuthMethod: AuditAuthMethodJWT}
}

// 派生结果比库里多一行，这样 ApplyPlan 一定会执行。
func addDrift(f *ssFixture) {
	f.derive.state.Cells = append(f.derive.state.Cells, MatrixCell{ModelKey: "new", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyDerived})
}

func TestStageSwitchCommit_V2StorageErrorsRollBackAndSurface(t *testing.T) {
	for _, op := range []string{"LockConfig", "LoadGateFacts", "ConsumeApproval", "LoadSnapshot", "ApplyPlan", "FreezeDerived", "SetStage", "InsertAudit"} {
		f, w := newFailingFixture(PricingStageShadow)
		addDrift(f)
		p := f.preview(t)
		require.NotZero(t, p.ApprovalID, op)
		w.failNext(op)
		_, err := f.sw.Commit(context.Background(), jwtReq(PricingStageV2, p.ApprovalID))
		require.ErrorContains(t, err, "injected failure in "+op, op)
		require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage, op)
		require.False(t, f.store.approvals[p.ApprovalID].consumed, op)
		require.Empty(t, f.store.audit, op)
		require.Empty(t, f.sync.invalidated, op)
	}
}

func TestStageSwitchCommit_RollbackStorageErrorsRollBackAndSurface(t *testing.T) {
	cases := []struct {
		op string
		n  int
	}{{"LoadSnapshot", 1}, {"ArchiveNonDerived", 1}, {"SetStage", 1}, {"LoadSnapshot", 2}, {"ApplyPlan", 1}, {"InsertAudit", 1}}
	for _, c := range cases {
		f, w := newFailingFixture(PricingStageV2)
		addDrift(f)
		f.store.cells = []StoredMatrixCell{
			{ID: 1, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "gpt-5.4", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceLegacyFrozen}},
			{ID: 2, GroupID: 7, MatrixCell: MatrixCell{ModelKey: "edited", Open: true, PriceMode: MatrixPriceInherit, Source: MatrixSourceManual}},
		}
		w.failAt[c.op] = c.n
		name := fmt.Sprintf("%s #%d", c.op, c.n)
		_, err := f.sw.Commit(context.Background(), jwtReq(PricingStageShadow, 0))
		require.ErrorContains(t, err, "injected failure in "+name, name)
		require.Equal(t, PricingStageV2, f.store.cfg.PricingStage, name)
		require.Len(t, f.store.cells, 2, name)
		require.Empty(t, f.store.audit, name)
	}

	// 渠道当前配置派生不出来：回拨提交与回拨预览都把错误交回去。
	f, _ := newFailingFixture(PricingStageV2)
	f.derive.err = errors.New("derive down")
	_, err := f.sw.Commit(context.Background(), jwtReq(PricingStageShadow, 0))
	require.ErrorContains(t, err, "derive down")
	_, err = f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42})
	require.ErrorContains(t, err, "derive down")
}

func TestStageSwitchCommit_LateralStorageErrorsSurface(t *testing.T) {
	for _, op := range []string{"SetStage", "InsertAudit"} {
		f, w := newFailingFixture(PricingStageShadow)
		w.failAt[op] = 1
		_, err := f.sw.Commit(context.Background(), jwtReq(PricingStageLegacy, 0))
		require.ErrorContains(t, err, "injected failure in "+op, op)
		require.Equal(t, PricingStageShadow, f.store.cfg.PricingStage, op)
		require.Empty(t, f.store.audit, op)
	}
}

func TestStageSwitchCommit_RejectsAnInvalidRequestBeforeOpeningATransaction(t *testing.T) {
	f := newSSFixture(PricingStageShadow)
	for _, req := range []PricingStageSwitchRequest{
		{GroupID: 7, To: PricingStageV2, OperatorID: 0, Confirm: true},
		{GroupID: 0, To: PricingStageV2, OperatorID: 42, Confirm: true},
		{GroupID: 7, To: PricingStage("bogus"), OperatorID: 42, Confirm: true},
	} {
		_, err := f.sw.Commit(context.Background(), req)
		require.Error(t, err)
		_, err = f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: req.GroupID, To: req.To, OperatorID: req.OperatorID})
		require.Error(t, err)
	}
	require.Zero(t, f.store.txRuns.Load())
}

func TestStageSwitchPreview_StorageAndEvidenceErrors(t *testing.T) {
	ctx := context.Background()
	req := PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42}

	f, w := newFailingFixture(PricingStageShadow)
	w.failAt["LoadGateFacts"] = 1
	_, err := f.sw.Preview(ctx, req)
	require.ErrorContains(t, err, "injected failure in LoadGateFacts")

	f, w = newFailingFixture(PricingStageShadow)
	w.failAt["InsertApproval"] = 1
	_, err = f.sw.Preview(ctx, req)
	require.ErrorContains(t, err, "injected failure in InsertApproval")

	// 清理过期预览失败只记日志，预览照常成功。
	f, w = newFailingFixture(PricingStageShadow)
	w.failAt["PurgeStale"] = 1
	p, err := f.sw.Preview(ctx, req)
	require.NoError(t, err)
	require.NotZero(t, p.ApprovalID)

	// 没有渠道摘要读口：当前证据取不到，闸门以 derive_failed 拒绝，不登记凭证。
	f = newSSFixture(PricingStageShadow)
	f.sw.fingerprint = nil
	p, err = f.sw.Preview(ctx, req)
	require.NoError(t, err)
	require.Zero(t, p.ApprovalID)
	require.Contains(t, sgCodes(*p.Gate), ReasonPricingGateDeriveFailed)

	// 进程内比对次数来自注入的读口。
	f = newSSFixture(PricingStageShadow)
	f.sw.compared = func(id int64) int64 { return 5 }
	p, err = f.sw.Preview(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 5, p.Gate.Shadow.ComparedInProcess)
}

func TestStageSwitchPreview_RollbackCountsArchivedRulesAndRederivesRules(t *testing.T) {
	ctx := context.Background()
	f, w := newFailingFixture(PricingStageV2)
	f.store.rules = []StoredMatrixCostRule{
		{ID: 1, ScopeGroupID: 7, Source: MatrixSourceManual},
		{ID: 2, ScopeGroupID: 7, Source: MatrixSourceLegacyFrozen},
	}
	p, err := f.sw.Preview(ctx, PricingStagePreviewRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42})
	require.NoError(t, err)
	require.Equal(t, StageKindRollback, p.Kind)
	require.NotNil(t, p.Rollback)
	require.EqualValues(t, 2, p.Rollback.ArchivedRules, "every rule that is not derived is archived")

	w.failNext("LoadSnapshot")
	_, err = f.sw.Preview(ctx, PricingStagePreviewRequest{GroupID: 7, To: PricingStageShadow, OperatorID: 42})
	require.ErrorContains(t, err, "injected failure in LoadSnapshot")
}

func TestStageSwitchPreview_CatalogRetiredAndUsageErrors(t *testing.T) {
	ctx := context.Background()
	req := PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42}

	f := newSSFixture(PricingStageShadow)
	f.store.usage = map[string]int64{"gpt-5.4": 4}
	f.sw.catalog = &spCatalogSource{entries: []ModelCatalogEntry{catalogEntry(PlatformOpenAI, "gpt-5.4", ModelCatalogRetired)}}
	p, err := f.sw.Preview(ctx, req)
	require.NoError(t, err)
	require.Len(t, p.Accepted, 1)
	require.Equal(t, AcceptedReasonCatalogRetired, p.Accepted[0].Reason)
	require.Equal(t, PriceDeltaUnknown, p.PriceDelta)

	// 近 7 天用量读不出来：证明不了没有被挡的模型，留一条 unknown。
	f, w := newFailingFixture(PricingStageShadow)
	f.sw.catalog = &spCatalogSource{}
	w.failAt["RecentUsageModels"] = 1
	p, err = f.sw.Preview(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "catalog_check_unavailable", p.Accepted[0].Reason)
}

func TestStageSwitchAudit_ClampsTheLimitAndSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	f, w := newFailingFixture(PricingStageShadow)
	for i := 0; i < 60; i++ {
		f.store.audit = append(f.store.audit, StageAuditRecord{GroupID: 7, From: PricingStageLegacy, To: PricingStageShadow, Kind: StageKindAdvance})
	}
	items, err := f.sw.Audit(ctx, 7, 0)
	require.NoError(t, err)
	require.Len(t, items, 50, "a missing limit falls back to the default")
	items, err = f.sw.Audit(ctx, 7, 1000)
	require.NoError(t, err)
	require.Len(t, items, 50, "a limit past the cap falls back to the default")

	w.failNext("ListAudit")
	_, err = f.sw.Audit(ctx, 7, 10)
	require.ErrorContains(t, err, "injected failure in ListAudit")

	// 服务层：接上切换器就透传，没接就是空列表。
	svc := NewPricingStageService(nil, nil, nil)
	empty, err := svc.Audit(ctx, 7, 10)
	require.NoError(t, err)
	require.Empty(t, empty)
	svc.SetSwitcher(f.sw)
	items, err = svc.Audit(ctx, 7, 10)
	require.NoError(t, err)
	require.Len(t, items, 10)
}

// 阶段切换用的实时派生：派生结果、分组平台，以及每一步读取失败时把错误交回去。
func TestPricingDerivationService_DeriveGroupCurrent(t *testing.T) {
	ctx := context.Background()
	e := newMxEnv(mxOpenAIGroups(10)...)
	e.setChannel(mxChannelWithRule(1, 1e-6, 10))
	state, platform, err := e.svc.DeriveGroupCurrent(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, platform)
	require.Equal(t, int64(1), state.ChannelID)
	require.NotEmpty(t, state.Revision)
	require.Empty(t, e.matrix.applyCalls, "read only")

	_, _, err = e.svc.DeriveGroupCurrent(ctx, 99)
	require.ErrorIs(t, err, ErrGroupNotFound)

	e.matrix.metaErr = errors.New("meta down")
	_, _, err = e.svc.DeriveGroupCurrent(ctx, 10)
	require.ErrorContains(t, err, "meta down")
	e.matrix.metaErr = nil

	e.repo.getChannelIDByGroupIDFn = func(context.Context, int64) (int64, error) { return 0, errors.New("owner down") }
	_, _, err = e.svc.DeriveGroupCurrent(ctx, 10)
	require.ErrorContains(t, err, "owner down")

	e.repo.getChannelIDByGroupIDFn = func(context.Context, int64) (int64, error) { return 1, nil }
	e.repo.getByIDFn = func(context.Context, int64) (*Channel, error) { return nil, errors.New("channel down") }
	_, _, err = e.svc.DeriveGroupCurrent(ctx, 10)
	require.ErrorContains(t, err, "channel down")
}

// 观察期起点在「现在」之后（时钟漂移、刚写入）时，已观察时长按 0 算，不会出现负数。
func TestEvaluateStageGate_ObservationNeverGoesNegative(t *testing.T) {
	future := sgNow.Add(2 * time.Hour)
	report := EvaluateStageGate(StageGateInput{
		Facts: &StageGateFacts{Config: StageGateConfig{GroupID: 7, Stage: PricingStageShadow, StageChangedAt: &future, UpdatedAt: future}},
		Now:   sgNow, ObservationRequired: PricingGateObservationDefaultHours * time.Hour,
	})
	require.Zero(t, report.Observation.ObservedHours)
	require.False(t, report.Observation.Satisfied)
}

// 被接受的差异按来源、原因、种类、模型排序，预览与审计证据的输出才稳定。
func TestStageSwitchAccepted_OrdersByReasonThenKindThenModel(t *testing.T) {
	replay := &ReplayEvidence{RowsReplayed: 10, Diffs: []PricingReplayDiffCount{
		{Kind: "cost", Class: "expected", Reason: "b_reason", Count: 1},
		{Kind: "quote", Class: "expected", Reason: "a_reason", Count: 1},
		{Kind: "quote", Class: "expected", Reason: "b_reason", Count: 1},
	}}
	_, accepted := StageSwitchAccepted(replay, ShadowEvidence{}, nil)
	require.Len(t, accepted, 3)
	require.Equal(t, "a_reason", accepted[0].Reason)
	require.Equal(t, "cost", accepted[1].Kind)
	require.Equal(t, "quote", accepted[2].Kind)
	require.Equal(t, "b_reason", accepted[2].Reason)
}
