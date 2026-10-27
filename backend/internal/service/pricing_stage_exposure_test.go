//go:build unit

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// W6 B1：shadow 切 v2 时的无价与开放范围检查（pricing_stage_exposure.go）。
// 固定件沿用 pricing_stage_switch_test.go 的内存库；暴露检查读的是内存库自己的状态（和真实库里「读事务」一样，
// 提交时读到的是追平、冻结并改成 v2 之后的状态），官方价沿用 exOfficial：priced 有价，zero 价全 0，nothing 没有价。

// ssSettings 已知免费名单（只实现 GetValue，其余方法留给内嵌的 nil 接口）。
type ssSettings struct {
	SettingRepository
	value string
}

func (s *ssSettings) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyBillingKnownFreeList {
		panic("unexpected setting key " + key)
	}
	return s.value, nil
}

// ssExposureReader 从内存库读准入模式与 open 单元格，不看阶段（和生产实现一样按分组 id 读）。
type ssExposureReader struct {
	s   *ssStore
	err error
}

func (r ssExposureReader) AccessModesTx(context.Context, MatrixExecutor, []int64) (map[int64]MatrixAccessMode, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[int64]MatrixAccessMode{r.s.cfg.GroupID: r.s.cfg.AccessMode}, nil
}

func (r ssExposureReader) OpenCellsTx(context.Context, MatrixExecutor, []int64) ([]ExposureCell, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []ExposureCell
	for _, c := range r.s.cells {
		if c.Open && c.EffectiveFrom == nil && c.EffectiveTo == nil {
			out = append(out, ExposureCell{GroupID: c.GroupID, Cell: c.MatrixCell})
		}
	}
	return out, nil
}

func ssExposureChecker(s *ssStore, settings SettingRepository) *StageExposureChecker {
	return NewStageExposureChecker(ssExposureReader{s: s}, NewExposureValidator(exOfficial, settings), exOfficial)
}

func derivedCell(key string, mode MatrixPriceMode) MatrixCell {
	return MatrixCell{ModelKey: key, Open: true, PriceMode: mode, Source: MatrixSourceLegacyDerived}
}

// 三类问题单元格：空 custom 的 per_request（0 元）、没有官方价的 inherit、open 的通配符。
func zeroPerRequestCell() MatrixCell {
	c := derivedCell("nothing", MatrixPriceCustom)
	c.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModePerRequest}
	return c
}

func unpricedInheritCell() MatrixCell { return derivedCell("nothing", MatrixPriceInherit) }

func wildcardCell() MatrixCell {
	c := derivedCell("claude-*", MatrixPriceInherit)
	c.IsPattern = true
	return c
}

// setCells 让渠道派生结果与库里的派生行同时是这些单元格（计划为空，提交不会重写它们）。
func (f *ssFixture) setCells(cells ...MatrixCell) {
	f.derive.state.Cells = append([]MatrixCell(nil), cells...)
	f.store.cells = nil
	for i, c := range cells {
		f.store.cells = append(f.store.cells, StoredMatrixCell{ID: int64(i + 1), GroupID: 7, Revision: 1, MatrixCell: c})
	}
}

// newExposureFixture 一个 shadow 白名单分组：一个有价的 open 单元格，加上 bad（可为空）。
func newExposureFixture(allowlist bool, bad ...MatrixCell) *ssFixture {
	f := newSSFixture(PricingStageShadow)
	if allowlist {
		f.store.cfg.AccessMode = MatrixAccessAllowlist
	}
	f.derive.state.Config = f.store.cfg.MatrixGroupConfig
	f.setCells(append([]MatrixCell{derivedCell("priced", MatrixPriceInherit)}, bad...)...)
	return f
}

func (f *ssFixture) requireExposureBlocked(t *testing.T, err error, wantIssue string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, ReasonPricingGateExposureBlocked, infraerrors.Reason(err))
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, "7", md["group_id"])
	require.Equal(t, ReasonPricingGateExposureBlocked, md["failures"])
	require.Contains(t, md["issues"], wantIssue)
	require.NotEmpty(t, md["count"])
	// 整体回滚：阶段、版本、派生行、审计、失效通知都没有变。
	f.requireUntouched(t, PricingStageShadow, 3)
	for _, c := range f.store.cells {
		require.Equal(t, MatrixSourceLegacyDerived, c.Source, "nothing stayed frozen")
	}
}

var exposureCases = []struct {
	name      string
	cell      func() MatrixCell
	issue     string // metadata.issues 里应有的「分组:模型:原因」
	exactOnly bool   // 精确单元格，加进已知免费名单可以放行；通配符不行
}{
	{"empty custom per_request cell is zero yuan", zeroPerRequestCell, "7:nothing:zero_price", true},
	{"inherit without an official price", unpricedInheritCell, "7:nothing:unpriced", true},
	{"open wildcard cell", wildcardCell, "7:claude-*:wildcard_unverifiable", false},
}

func TestStageSwitchExposure_PreviewListsTheProblemsAndRegistersNothing(t *testing.T) {
	for _, tc := range exposureCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newExposureFixture(true, tc.cell())
			p := f.preview(t)

			require.False(t, p.Executable)
			require.Zero(t, p.ApprovalID, "no approval is registered")
			require.Empty(t, p.PlanHash)
			require.Empty(t, f.store.approvals)
			require.NotNil(t, p.Gate)
			require.False(t, p.Gate.Passed)
			require.Equal(t, []string{ReasonPricingGateExposureBlocked}, sgCodes(*p.Gate), "the other gate conditions are fine")
			msg := p.Gate.Failures[0].Message
			require.Contains(t, msg, strings.Split(tc.issue, ":")[1], "the model is named")
			require.Contains(t, msg, strings.Split(tc.issue, ":")[2], "the kind of problem is named")
			require.NotContains(t, msg, "priced (", "the priced cell is not a problem")
			// 两种出口都说清楚。
			require.Contains(t, msg, "known-free list")
			require.Contains(t, msg, "channel")
			// 预览不改数据。
			f.requireUntouched(t, PricingStageShadow, 3)
		})
	}
}

func TestStageSwitchExposure_CommitIsRejectedAndNothingIsConsumed(t *testing.T) {
	for _, tc := range exposureCases {
		t.Run(tc.name, func(t *testing.T) {
			// 预览时分组是干净的，登记了凭证；预览之后分组里出现了问题单元格（例如批准删掉了官方价）。
			f := newExposureFixture(true)
			p := f.preview(t)
			require.True(t, p.Executable)
			require.NotZero(t, p.ApprovalID)
			f.setCells(derivedCell("priced", MatrixPriceInherit), tc.cell())

			_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
			f.requireExposureBlocked(t, err, tc.issue)
			require.False(t, f.store.approvals[p.ApprovalID].consumed, "the approval is not consumed")
			require.Contains(t, infraerrors.Message(err), "known-free list")
		})
	}
}

func TestStageSwitchExposure_ExactCellsAreAcceptedThroughTheKnownFreeList(t *testing.T) {
	for _, tc := range exposureCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newExposureFixture(true, tc.cell())
			model := strings.Split(tc.issue, ":")[1]
			f.settings.value = `[{"group_id":7,"model":"` + model + `"}]`

			p := f.preview(t)
			if !tc.exactOnly {
				// 通配符放行的是整个前缀，没有办法逐个模型验证：名单救不了它，只能在渠道上去掉。
				require.False(t, p.Executable)
				require.Equal(t, []string{ReasonPricingGateExposureBlocked}, sgCodes(*p.Gate))
				f.setCells(derivedCell("priced", MatrixPriceInherit)) // 渠道上去掉通配符
				p = f.preview(t)
			}
			require.True(t, p.Executable, "%+v", p.Gate)
			require.NotZero(t, p.ApprovalID)

			res, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
			require.NoError(t, err)
			require.True(t, res.Changed)
			require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
			require.True(t, f.store.approvals[p.ApprovalID].consumed)
			require.Len(t, f.store.audit, 1)
		})
	}
}

func TestStageSwitchExposure_WildcardIsStillBlockedWhenTheNameIsOnTheKnownFreeList(t *testing.T) {
	f := newExposureFixture(true)
	p := f.preview(t)
	f.setCells(derivedCell("priced", MatrixPriceInherit), wildcardCell())
	f.settings.value = `[{"group_id":7,"model":"claude-*"}]`
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	f.requireExposureBlocked(t, err, "7:claude-*:wildcard_unverifiable")
}

// 开放（非白名单）分组不受影响：未定价的单元格只是警告，不是切换的前置条件。
func TestStageSwitchExposure_OpenGroupsAreNotAffected(t *testing.T) {
	f := newExposureFixture(false, zeroPerRequestCell(), unpricedInheritCell(), wildcardCell())
	p := f.preview(t)
	require.True(t, p.Executable, "%+v", p.Gate)
	require.NotZero(t, p.ApprovalID)
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.NoError(t, err)
	require.Equal(t, PricingStageV2, f.store.cfg.PricingStage)
}

// 预览按派生之后的目标态检查：库里现有的行是干净的，渠道派生结果里却有问题单元格（派生写入豁免保存时校验），
// 预览也要拦住；提交时追平派生行之后读到同样的状态，同样被拒绝。
func TestStageSwitchExposure_ChecksTheDerivedTargetNotTheCurrentRows(t *testing.T) {
	f := newExposureFixture(true)
	f.derive.state.Cells = append(f.derive.state.Cells, zeroPerRequestCell()) // 库里没有这一行，追平时才会写入
	f.store.replay.DeriveRevision = "rev-1"

	p := f.preview(t)
	require.False(t, p.Executable)
	require.Equal(t, []string{ReasonPricingGateExposureBlocked}, sgCodes(*p.Gate))
	require.Len(t, f.store.cells, 1, "the preview wrote nothing")

	_, err := f.sw.Commit(context.Background(), f.commitReq(1, AuditAuthMethodJWT))
	f.requireExposureBlocked(t, err, "7:nothing:zero_price")
	require.Len(t, f.store.cells, 1, "the derived row that was written during the transaction is rolled back")
}

// 映射目标：精确映射命中 open 单元格时，目标要官方有价且 token 价非零。
func TestStageSwitchExposure_MappingTargetsAreChecked(t *testing.T) {
	for target, reason := range map[string]string{"nothing": "mapping_target_unpriced", "zero": "mapping_target_zero_price"} {
		f := newExposureFixture(true)
		f.store.cfg.ModelMapping = []MatrixMappingEntry{{Src: "priced", Dst: target}}
		f.derive.state.Config = f.store.cfg.MatrixGroupConfig
		p := f.preview(t)
		require.False(t, p.Executable, target)
		require.Contains(t, p.Gate.Failures[0].Message, reason)
		require.Contains(t, p.Gate.Failures[0].Message, "-> "+target)

		_, err := f.sw.Commit(context.Background(), f.commitReq(1, AuditAuthMethodJWT))
		f.requireExposureBlocked(t, err, "7:priced:"+reason+"->"+target)
	}
}

// 暴露检查失败与其他闸门条件并列：管理员一次看到全部原因。
func TestStageSwitchExposure_ListedAlongsideTheOtherGateFailures(t *testing.T) {
	f := newExposureFixture(true, unpricedInheritCell())
	f.store.shadow.TranslationDiffs = 1
	p := f.preview(t)
	require.False(t, p.Executable)
	require.Equal(t, []string{ReasonPricingGateExposureBlocked, ReasonPricingGateShadowDiffs}, sgCodes(*p.Gate))
}

// 其他闸门条件不满足时，提交先返回闸门错误（事务一开始就评估），不会走到暴露检查。
func TestStageSwitchExposure_CommitReportsTheGateFirst(t *testing.T) {
	f := newExposureFixture(true, unpricedInheritCell())
	f.store.shadow.TranslationDiffs = 1
	_, err := f.sw.Commit(context.Background(), f.commitReq(1, AuditAuthMethodJWT))
	require.Equal(t, ReasonPricingGateShadowDiffs, infraerrors.Reason(err))
}

// 没有接上检查器：v2 的预览与提交都失败关闭，不会放行；其他阶段的切换不受影响。
func TestStageSwitchExposure_MissingCheckerFailsClosed(t *testing.T) {
	f := newExposureFixture(true)
	f.sw.exposure = nil
	_, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42})
	require.Equal(t, ReasonExposureGuardMissing, infraerrors.Reason(err))
	require.Empty(t, f.store.approvals)

	other := newExposureFixture(true)
	p := other.preview(t)
	other.sw.exposure = nil
	_, err = other.sw.Commit(context.Background(), other.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.Equal(t, ReasonExposureGuardMissing, infraerrors.Reason(err))
	other.requireUntouched(t, PricingStageShadow, 3)
	require.False(t, other.store.approvals[p.ApprovalID].consumed)

	// shadow 回 legacy 不涉价，不需要检查器。
	lateral := newExposureFixture(true)
	lateral.sw.exposure = nil
	_, err = lateral.sw.Commit(context.Background(), PricingStageSwitchRequest{GroupID: 7, To: PricingStageLegacy, OperatorID: 42, Confirm: true, AuthMethod: AuditAuthMethodJWT})
	require.NoError(t, err)
}

// 读库失败（不是违规）：原样交回去，整体回滚；预览也把错误交回去。
func TestStageSwitchExposure_ReaderErrorsSurface(t *testing.T) {
	f := newExposureFixture(true)
	p := f.preview(t)
	f.sw.exposure = NewStageExposureChecker(ssExposureReader{s: f.store, err: errors.New("pg down")}, NewExposureValidator(exOfficial, f.settings), exOfficial)
	_, err := f.sw.Commit(context.Background(), f.commitReq(p.ApprovalID, AuditAuthMethodJWT))
	require.ErrorContains(t, err, "pg down")
	f.requireUntouched(t, PricingStageShadow, 3)
	require.False(t, f.store.approvals[p.ApprovalID].consumed)
}

// 快照读取失败（预览与提交）：错误交回去。
func TestStageSwitchExposure_SnapshotErrorsSurface(t *testing.T) {
	f, w := newFailingFixture(PricingStageShadow)
	f.store.cfg.AccessMode = MatrixAccessAllowlist
	w.failNext("LoadSnapshot")
	_, err := f.sw.Preview(context.Background(), PricingStagePreviewRequest{GroupID: 7, To: PricingStageV2, OperatorID: 42})
	require.ErrorContains(t, err, "injected failure in LoadSnapshot")

	// 提交里的第二次快照读取（改完阶段之后）失败：整体回滚。
	f2, w2 := newFailingFixture(PricingStageShadow)
	p := f2.preview(t)
	w2.failAt["LoadSnapshot"] = w2.calls["LoadSnapshot"] + 2
	_, err = f2.sw.Commit(context.Background(), jwtReq(PricingStageV2, p.ApprovalID))
	require.ErrorContains(t, err, "injected failure in LoadSnapshot")
	f2.requireUntouched(t, PricingStageShadow, 3)
	require.False(t, f2.store.approvals[p.ApprovalID].consumed)
}

// 检查必须读事务自己的状态：连接池读（PrecheckGroups 的口径）在分组仍是 shadow 时什么也查不出，
// EvaluateAsV2 忽略快照里的阶段。
func TestOpenPrechecker_EvaluateAsV2IgnoresTheStageInTheSnapshot(t *testing.T) {
	ctx := context.Background()
	p := NewOpenPrechecker(nil, NewExposureValidator(exOfficial, &ssSettings{value: `[]`}), exOfficial)
	cfg := StoredGroupConfig{GroupID: 7, PricingStage: PricingStageShadow}
	cfg.AccessMode = MatrixAccessAllowlist
	snap := GroupStateSnapshot{Config: &cfg, Cells: []StoredMatrixCell{{ID: 1, GroupID: 7, MatrixCell: unpricedInheritCell()}}}

	require.False(t, p.evaluate(ctx, 7, snap, nil).Applicable, "the regular precheck does not apply to a shadow group")

	rep, err := p.EvaluateAsV2(ctx, 7, snap)
	require.NoError(t, err)
	require.True(t, rep.Applicable)
	require.Equal(t, PricingStageV2, rep.Stage)
	require.Len(t, rep.Blocking, 1)
	require.Equal(t, "unpriced", rep.Blocking[0].Reason)

	// 没有配置行：不适用。
	rep, err = p.EvaluateAsV2(ctx, 7, GroupStateSnapshot{})
	require.NoError(t, err)
	require.False(t, rep.Applicable)

	// 没有配置验证器：失败关闭。
	_, err = (*OpenPrechecker)(nil).EvaluateAsV2(ctx, 7, snap)
	require.Equal(t, ReasonExposureGuardMissing, infraerrors.Reason(err))
	_, err = (&StageExposureChecker{}).CheckInTx(ctx, nil, 7, snap)
	require.Equal(t, ReasonExposureGuardMissing, infraerrors.Reason(err))
	_, err = (*StageExposureChecker)(nil).CheckTarget(ctx, 7, snap)
	require.Equal(t, ReasonExposureGuardMissing, infraerrors.Reason(err))
}

func TestApplyPlanToSnapshot(t *testing.T) {
	cfg := &StoredGroupConfig{GroupID: 7, PricingStage: PricingStageShadow}
	rules := []StoredMatrixCostRule{{ID: 9}}
	snap := GroupStateSnapshot{Config: cfg, Rules: rules, Cells: []StoredMatrixCell{
		{ID: 1, GroupID: 7, MatrixCell: derivedCell("keep", MatrixPriceInherit)},
		{ID: 2, GroupID: 7, MatrixCell: derivedCell("drop", MatrixPriceInherit)},
		{ID: 3, GroupID: 7, MatrixCell: derivedCell("change", MatrixPriceInherit)},
	}}
	newCfg := MatrixGroupConfig{AccessMode: MatrixAccessAllowlist}
	plan := GroupApplyPlan{
		GroupID:     7,
		ConfigWrite: &newCfg,
		CellDeletes: []int64{2},
		CellUpdates: []StoredMatrixCell{{ID: 3, GroupID: 7, MatrixCell: derivedCell("changed", MatrixPriceInherit)}},
		CellInserts: []MatrixCell{derivedCell("new", MatrixPriceInherit)},
	}
	out := applyPlanToSnapshot(snap, plan)
	keys := []string{}
	for _, c := range out.Cells {
		keys = append(keys, c.ModelKey)
	}
	require.Equal(t, []string{"keep", "changed", "new"}, keys)
	require.Equal(t, MatrixAccessAllowlist, out.Config.AccessMode)
	require.Equal(t, PricingStageShadow, out.Config.PricingStage, "the stage is not part of the plan")
	require.Equal(t, rules, out.Rules)
	require.Equal(t, MatrixAccessMode(""), cfg.AccessMode, "the input snapshot is not modified")
	require.Len(t, snap.Cells, 3)

	// 没有配置行时按计划创建。
	out = applyPlanToSnapshot(GroupStateSnapshot{}, GroupApplyPlan{GroupID: 7, ConfigWrite: &newCfg})
	require.Equal(t, MatrixAccessAllowlist, out.Config.AccessMode)

	// 计划被跳过（分组已经是 v2）：原样返回。
	require.Equal(t, snap, applyPlanToSnapshot(snap, GroupApplyPlan{Skipped: true, CellDeletes: []int64{1}}))
}

func TestStageExposureMessage_TruncatesLongLists(t *testing.T) {
	var issues []OpenPrecheckIssue
	for i := 0; i < 25; i++ {
		issues = append(issues, OpenPrecheckIssue{GroupID: 7, ModelKey: "m" + string(rune('a'+i)), Reason: "unpriced"})
	}
	issues[0].Reason, issues[0].Target = "mapping_target_unpriced", "dst"
	msg := stageExposureMessage(issues)
	require.Contains(t, msg, "25 model(s)")
	require.Contains(t, msg, "ma (mapping_target_unpriced -> dst)")
	require.Contains(t, msg, "and 15 more")
	err := StageExposureError(7, issues)
	require.Equal(t, "25", infraerrors.FromError(err).Metadata["count"])
	require.Equal(t, 20, len(strings.Split(infraerrors.FromError(err).Metadata["issues"], ";")), "metadata lists at most 20 issues")
}

// 源码守卫：v2 提交里，暴露检查必须在 FreezeDerived 与 SetStage 之后、消耗凭证之前，并且读事务（CheckInTx）。
// 被挪到 SetStage 之前（#1661 之后读不到任何白名单 v2 分组）或挪到凭证之后，这里会失败。
func TestCommitV2ExposureCheckSitsBetweenSetStageAndConsumeApproval(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "internal/service/pricing_stage_switch.go"))
	require.NoError(t, err)
	text := string(raw)
	commitV2 := text[strings.Index(text, "func (sw *PricingStageSwitcher) commitV2("):strings.Index(text, "func (sw *PricingStageSwitcher) commitRollback(")]
	freeze := strings.Index(commitV2, "FreezeDerived(ctx, tx,")
	setStage := strings.Index(commitV2, "SetStage(ctx, tx, req.GroupID, PricingStageV2")
	check := strings.Index(commitV2, "sw.exposure.CheckInTx(ctx, tx,")
	consume := strings.Index(commitV2, "ConsumeApproval(")
	audit := strings.Index(commitV2, "InsertAudit(")
	require.True(t, freeze > 0 && setStage > freeze && check > setStage && consume > check && audit > consume,
		"order must be FreezeDerived, SetStage, CheckInTx, ConsumeApproval, InsertAudit (got %d %d %d %d %d)", freeze, setStage, check, consume, audit)
	require.NotContains(t, commitV2, "PrecheckGroups(", "the pool-backed precheck cannot see the transaction's state")
}
