//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-2a 的集成测试（真实 PostgreSQL）：分组配置写入器，以及保存时校验（白名单分组不能出现无价单元格）。

type pwiPrices map[string]service.OfficialPriceState

func (p pwiPrices) LookupOfficialPriceState(model string) service.OfficialPriceState { return p[model] }

// pwiGuard 保存时校验关口：官方价由测试给定（nil 即没有任何官方价）。
func pwiGuard(prices pwiPrices) *service.ExposureGuard {
	return service.NewExposureGuard(NewPricingExposureReader(), service.NewExposureValidator(prices, nil))
}

func pwiSetAccess(t *testing.T, gid int64, mode string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(),
		`UPDATE group_model_config SET access_mode = $2 WHERE group_id = $1`, gid, mode)
	require.NoError(t, err)
}

func pwiAccessMode(t *testing.T, gid int64) string {
	t.Helper()
	var mode string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT access_mode FROM group_model_config WHERE group_id = $1`, gid).Scan(&mode))
	return mode
}

func pwiGroupConfigService(prices pwiPrices, inv *pwiInvalidator) *service.GroupConfigService {
	var invalidator service.MatrixSnapshotInvalidator // 不能直接传 nil 的 *pwiInvalidator：那会是非 nil 的接口值
	if inv != nil {
		invalidator = inv
	}
	return service.NewGroupConfigService(NewPricingWriteStore(integrationDB),
		service.NewMatrixTxWriter(nil, NewPricingGroupConfigWriter(), pwiGuard(prices)), nil, invalidator)
}

// pwiGcApply 走完整的预览加提交：涉价的改动带着预览凭证，用交互式管理员会话提交。
func pwiGcApply(ctx context.Context, svc *service.GroupConfigService, req service.GroupConfigWriteRequest) (*service.GroupConfigWriteResult, error) {
	ticket, err := svc.Propose(ctx, req)
	if err != nil {
		return nil, err
	}
	return svc.Commit(ctx, service.GroupConfigCommit{
		ApprovalID: ticket.ApprovalID, Request: req, Confirm: true,
		Actor: service.PriceWriteActor{ID: req.OperatorID, Interactive: true},
	})
}

func TestGroupConfigService_Integration_PriceTouchingNeedsApproval(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t) // revision 3
	svc := pwiGroupConfigService(nil, nil)
	bms := service.BillingModelSourceRequested
	req := service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 3, OperatorID: 5, BillingModelSource: &bms}

	// 没有凭证：拒绝，库里不变。
	_, err := svc.Commit(ctx, service.GroupConfigCommit{Request: req, Confirm: true, Actor: service.PriceWriteActor{ID: 5, Interactive: true}})
	require.Equal(t, service.ReasonPriceWriteApproval, pwiReason(t, err))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))

	// 没有估算器时方向是 unknown：机器令牌（非交互式）拒绝，凭证被事务回滚而保持可用。
	ticket, err := svc.Propose(ctx, req)
	require.NoError(t, err)
	require.NotZero(t, ticket.ApprovalID)
	require.True(t, ticket.TouchesPrice)
	require.Equal(t, service.PriceDeltaUnknown, ticket.Delta)
	_, err = svc.Commit(ctx, service.GroupConfigCommit{ApprovalID: ticket.ApprovalID, Request: req, Confirm: true, Actor: service.PriceWriteActor{ID: 5}})
	require.Equal(t, service.ReasonPriceWriteInteractive, pwiReason(t, err))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid))

	// 交互式会话：成功，凭证用掉，不能重放。
	commit := service.GroupConfigCommit{ApprovalID: ticket.ApprovalID, Request: req, Confirm: true, Actor: service.PriceWriteActor{ID: 5, Interactive: true}}
	_, err = svc.Commit(ctx, commit)
	require.NoError(t, err)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	_, err = svc.Commit(ctx, commit)
	require.Error(t, err)
}

func TestPricingGroupConfigWriter_Integration_WritesAndBumpsRevision(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t) // revision 3
	inv := &pwiInvalidator{}
	svc := pwiGroupConfigService(nil, inv)
	allow, costMode, bms := service.MatrixAccessAllowlist, service.MatrixCostFollowBilling, service.BillingModelSourceChannelMapped
	mapping := []service.MatrixMappingEntry{{Src: "b-*", Dst: "x"}, {Src: "B-long", Dst: "y"}, {Src: "a", Dst: ""}}
	features := map[string]any{"bedrock_cc_compat": true, "web_search_emulation": map[string]any{"anthropic": true}}

	res, err := pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{
		GroupID: gid, BaselineRevision: 3, OperatorID: 5,
		AccessMode: &allow, CostMode: &costMode, BillingModelSource: &bms, ModelMapping: &mapping, Features: &features,
	})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.ExposureRelevant)
	require.Equal(t, int64(4), res.After.Revision)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	require.Equal(t, []int64{gid}, inv.groups)

	got := mxLoad(t, NewPricingMatrixRepository(integrationDB), gid).Config
	require.Equal(t, service.MatrixAccessAllowlist, got.AccessMode)
	require.Equal(t, service.MatrixCostFollowBilling, got.CostMode)
	require.Equal(t, bms, *got.BillingModelSource)
	require.Equal(t, []service.MatrixMappingEntry{{Src: "a", Dst: ""}, {Src: "B-long", Dst: "y"}, {Src: "b-*", Dst: "x"}}, got.ModelMapping,
		"映射写入时规范成固定顺序：精确名在前，通配符在后")
	require.Equal(t, true, got.Features["bedrock_cc_compat"])

	// 同样的内容再写一次：不加 revision，不失效缓存。
	res, err = pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 4, OperatorID: 5, AccessMode: &allow})
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	require.Len(t, inv.groups, 1)

	// 基线过期。
	open := service.MatrixAccessOpen
	_, err = pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 3, OperatorID: 5, AccessMode: &open})
	require.Equal(t, service.ReasonPriceBaselineChanged, pwiReason(t, err))
	require.Equal(t, "allowlist", pwiAccessMode(t, gid))

	// 清空计费来源；只改成本模式不算涉价，也不过保存时校验。
	res, err = pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 4, OperatorID: 5, ClearBillingModelSource: true})
	require.NoError(t, err)
	require.True(t, res.ExposureRelevant)
	require.Nil(t, mxLoad(t, NewPricingMatrixRepository(integrationDB), gid).Config.BillingModelSource)
}

func TestPricingGroupConfigWriter_Integration_OnlyV2Groups(t *testing.T) {
	for _, stage := range []string{"legacy", "shadow"} {
		gid := pwiGroup(t, stage, 1)
		open := service.MatrixAccessOpen
		_, err := pwiGcApply(context.Background(), pwiGroupConfigService(nil, nil),
			service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 1, OperatorID: 5, AccessMode: &open})
		require.Equal(t, service.ReasonGroupConfigNotV2, pwiReason(t, err), stage)
		require.Equal(t, int64(1), pwiConfigRevision(t, gid))
	}
	// 没有配置行的分组同样拒绝。
	open := service.MatrixAccessOpen
	_, err := pwiGcApply(context.Background(), pwiGroupConfigService(nil, nil),
		service.GroupConfigWriteRequest{GroupID: mxIntGroup(t), BaselineRevision: 1, OperatorID: 5, AccessMode: &open})
	require.Equal(t, service.ReasonGroupConfigNotV2, pwiReason(t, err))
}

func TestPricingGroupConfigWriter_Integration_AllowlistSwitchIsBlockedByUnpricedCells(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	store := NewPricingWriteStore(integrationDB)
	prices := pwiPrices{"pw-official": {Known: true, TokenNonZero: true}, "pw-zero": {Known: true}}

	// 开放分组里可以先放无价的 open 单元格（只对白名单分组阻止）。
	custom := pwiUpsert(gid, "pw-custom", true, service.MatrixPriceCustom, 0)
	custom.CustomPrice = &service.MatrixCustomPrice{BillingMode: service.BillingModeToken, InputPrice: mxF(1e-6)}
	closed := pwiUpsert(gid, "pw-closed", false, service.MatrixPriceInherit, 0)
	_, err := pwiApply(store, service.CellWriteRequest{
		Ops: []service.CellOp{
			pwiUpsert(gid, "pw-official", true, service.MatrixPriceInherit, 0),
			pwiUpsert(gid, "pw-nothing", true, service.MatrixPriceInherit, 0),
			pwiUpsert(gid, "pw-zero", true, service.MatrixPriceInherit, 0),
			custom, closed,
		},
		GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 1,
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))

	allow := service.MatrixAccessAllowlist
	svc := pwiGroupConfigService(prices, nil)
	_, err = pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 4, OperatorID: 5, AccessMode: &allow})
	require.Equal(t, service.ReasonExposureUnpriced, pwiReason(t, err))
	require.Equal(t, "open", pwiAccessMode(t, gid), "违规时整个事务回滚")
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))

	// 把两个违规的单元格关掉或补上价格之后，可以切成白名单。
	fix := pwiUpsert(gid, "pw-nothing", false, service.MatrixPriceInherit, 1)
	fixZero := pwiUpsert(gid, "pw-zero", true, service.MatrixPriceCustom, 1)
	fixZero.CustomPrice = &service.MatrixCustomPrice{BillingMode: service.BillingModeToken, OutputPrice: mxF(2e-6)}
	_, err = pwiApply(store, service.CellWriteRequest{Ops: []service.CellOp{fix, fixZero}, GroupRevisions: map[int64]int64{gid: 4}, OperatorID: 1})
	require.NoError(t, err)
	_, err = pwiGcApply(ctx, svc, service.GroupConfigWriteRequest{GroupID: gid, BaselineRevision: 5, OperatorID: 5, AccessMode: &allow})
	require.NoError(t, err)
	require.Equal(t, "allowlist", pwiAccessMode(t, gid))
}

func TestInterimPriceWriteGate_Integration_AllowlistGroupRejectsUnpricedOpenCells(t *testing.T) {
	ctx := context.Background()
	gid := pwiV2Group(t)
	pwiSetAccess(t, gid, "allowlist")
	store := NewPricingWriteStore(integrationDB)
	gate := service.NewInterimPriceWriteGate(store,
		service.NewMatrixTxWriter(NewPricingCellWriter(), nil, pwiGuard(pwiPrices{"pw-official": {Known: true, TokenNonZero: true}})), nil, nil)
	req := func(op service.CellOp) service.CellWriteRequest {
		return service.CellWriteRequest{Ops: []service.CellOp{op}, GroupRevisions: map[int64]int64{gid: 3}, OperatorID: 21}
	}
	// 新建单元格一律算涉价：先预览，再用交互式会话带着凭证提交。
	commit := func(r service.CellWriteRequest) error {
		ticket, err := gate.Propose(ctx, service.PriceWriteProposal{Request: r})
		if err != nil {
			return err
		}
		_, err = gate.Commit(ctx, service.PriceWriteCommit{
			ApprovalID: ticket.ApprovalID, Request: r, Confirm: true, Actor: service.PriceWriteActor{ID: 22, Interactive: true},
		})
		return err
	}

	// 只改 open 的写入同样要过校验：这个模型没有官方价，开放就是无价。
	unpriced := req(pwiUpsert(gid, "pw-nothing", true, service.MatrixPriceInherit, 0))
	_, err := gate.Propose(ctx, service.PriceWriteProposal{Request: unpriced})
	require.Equal(t, service.ReasonExposureUnpriced, pwiReason(t, err), "预览就阻止")
	require.Equal(t, service.ReasonExposureUnpriced, pwiReason(t, commit(unpriced)))
	require.Empty(t, pwiCells(t, gid))
	require.Empty(t, pwiHistory(t, gid))
	require.Equal(t, int64(3), pwiConfigRevision(t, gid), "被阻止的写入不留任何痕迹")

	// 有官方价的模型、关闭的单元格都可以。
	require.NoError(t, commit(req(pwiUpsert(gid, "pw-official", true, service.MatrixPriceInherit, 0))))
	require.NoError(t, commit(service.CellWriteRequest{
		Ops: []service.CellOp{pwiUpsert(gid, "pw-nothing", false, service.MatrixPriceInherit, 0)}, GroupRevisions: map[int64]int64{gid: 4}, OperatorID: 21,
	}))
	require.Len(t, pwiCells(t, gid), 2)
}
