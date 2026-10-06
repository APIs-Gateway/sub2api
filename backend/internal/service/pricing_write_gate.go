package service

import (
	"context"
	"log/slog"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// InterimPriceWriteGate W5 落地之前的价格写入审批（设计文档 6.3、附录 S-13）：
//
//   - 涉价的写入必须先 Propose 出预览（记录前后对比与价格方向），再带着凭证 Commit；
//   - Commit 要求管理员二次确认（Confirm）；价格方向不是 none 的写入还必须是交互式管理员会话，
//     机器令牌（admin token）不行；
//   - 凭证绑定计划指纹（操作、单元格基线、分组基线），在写入的同一个事务里被原子消耗，
//     消耗后的记录就是审批记录（预览人、批准人、前后对比），历史行带着它的 id；
//   - 预览之后库里有人动过同一批单元格，基线对不上，写入被拒绝，必须重新预览。
//
// 不涉价的写入（只改 open、删除 inherit 单元格）可以不带凭证，但仍要二次确认并留历史。
//
// 保存时校验（设计 5.2）：所有写入（包括不涉价、只改 open 的）在写事务里、消耗审批之前过 ExposureGuard，
// 白名单分组里不能出现无价或 0 元的 open 单元格；预览（Propose）也过同一道校验，让管理员提前看到阻止原因。
// 写入与校验做成一体，由 MatrixTxWriter.ApplyCellWritesTx 提供；没有配置 ExposureGuard 时写入与预览一律失败关闭。
//
// 价格方向只由估算器给出（estimateCellDelta）：请求里没有这个字段，估算器缺失、出错或返回不认识的值都按 unknown。
type InterimPriceWriteGate struct {
	store       PriceWriteStore
	tx          *MatrixTxWriter
	estimator   PriceDeltaEstimator
	invalidator MatrixSnapshotInvalidator
	precheck    *OpenPrechecker
	now         func() time.Time
}

// WithOpenPrecheck 接上开放时预检（W6 PR4b-2b-2）：预览时对写入之后的目标态跑一遍，白名单分组的阻止项直接拒绝，
// 开放分组的问题随凭证返回给管理员确认。没接时预览不做这一项（保存时校验仍在写事务里兜底）。
func (g *InterimPriceWriteGate) WithOpenPrecheck(p *OpenPrechecker) *InterimPriceWriteGate {
	g.precheck = p
	return g
}

// NewInterimPriceWriteGate 创建过渡审批关口。tx 是写入与保存时校验的唯一入口（nil 则失败关闭）；
// estimator 是价格方向的唯一来源（nil 时涉价写入一律按 unknown，即必须交互式会话）；
// invalidator 可为 nil（没有读取方）。
func NewInterimPriceWriteGate(store PriceWriteStore, tx *MatrixTxWriter, estimator PriceDeltaEstimator, invalidator MatrixSnapshotInvalidator) *InterimPriceWriteGate {
	return &InterimPriceWriteGate{store: store, tx: tx, estimator: estimator, invalidator: invalidator, now: time.Now}
}

// priceWriteStaleAfter 从未被消耗的预览记录保留多久再清理。
const priceWriteStaleAfter = 24 * time.Hour

type priceWriteSummaryItem struct {
	GroupID      int64           `json:"group_id"`
	ModelKey     string          `json:"model_key"`
	Action       CellWriteAction `json:"action"`
	TouchesPrice bool            `json:"touches_price"`
	Before       *MatrixCell     `json:"before,omitempty"`
	After        *MatrixCell     `json:"after,omitempty"`
}

// Propose 实现 PriceWriteGate。
func (g *InterimPriceWriteGate) Propose(ctx context.Context, in PriceWriteProposal) (*PriceWriteTicket, error) {
	if in.Request.OperatorID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	req, err := NormalizeCellWriteRequest(in.Request)
	if err != nil {
		return nil, err
	}
	planned, err := g.tx.PreviewCellWrites(ctx, g.store.Reader(), req)
	if err != nil {
		return nil, err
	}
	var precheck []OpenPrecheckReport
	if g.precheck != nil {
		if precheck, err = g.precheck.PrecheckPlanned(ctx, planned); err != nil {
			return nil, err
		}
		if err := BlockingError(precheck); err != nil {
			return nil, err
		}
	}
	touches := PlannedTouchesPrice(planned)
	delta := g.estimateCellDelta(ctx, planned, touches)

	now := g.now()
	approval := PriceWriteApproval{
		Kind:         PriceWriteKindCells,
		PlanHash:     PriceWritePlanHash(req),
		TouchesPrice: touches,
		Delta:        delta,
		GroupIDs:     CellWriteGroupIDs(req.Ops),
		Summary:      []byte(matrixCanonicalJSON(priceWriteSummary(planned))),
		PreviewedBy:  req.OperatorID,
		CreatedAt:    now,
		ExpiresAt:    now.Add(PriceWriteApprovalTTL),
	}
	id, err := g.store.InsertApproval(ctx, approval)
	if err != nil {
		return nil, err
	}
	if _, perr := g.store.PurgeStale(ctx, now.Add(-priceWriteStaleAfter)); perr != nil {
		slog.Warn("purge stale price write previews failed", "error", perr)
	}
	return &PriceWriteTicket{
		ApprovalID: id, PlanHash: approval.PlanHash, TouchesPrice: touches, Delta: delta,
		ExpiresAt: approval.ExpiresAt, Planned: planned, Precheck: precheck,
	}, nil
}

// estimateCellDelta 价格方向：不涉价是 none；涉价时只取估算器的结果，估算器缺失、出错或返回不认识的值都是 unknown。
func (g *InterimPriceWriteGate) estimateCellDelta(ctx context.Context, planned []PlannedCellWrite, touches bool) PriceDelta {
	if !touches {
		return PriceDeltaNone
	}
	if g.estimator == nil {
		return PriceDeltaUnknown
	}
	return safePriceDelta(g.estimator.EstimateCellWrites(ctx, planned))
}

// safePriceDelta 把估算器的返回值收成一个可信的方向：出错或不认识的值都是 unknown（最严，必须交互式会话）。
func safePriceDelta(delta PriceDelta, err error) PriceDelta {
	if err != nil {
		slog.Warn("price delta estimation failed, treating the direction as unknown", "error", err)
		return PriceDeltaUnknown
	}
	if validPriceDelta(delta) != nil {
		return PriceDeltaUnknown
	}
	return delta
}

func priceWriteSummary(planned []PlannedCellWrite) []priceWriteSummaryItem {
	out := make([]priceWriteSummaryItem, 0, len(planned))
	for _, p := range planned {
		item := priceWriteSummaryItem{
			GroupID: p.Op.GroupID, ModelKey: p.Op.ModelKey, Action: p.Action, TouchesPrice: p.TouchesPrice, After: p.After,
		}
		if p.Before != nil {
			b := p.Before.MatrixCell
			item.Before = &b
		}
		out = append(out, item)
	}
	return out
}

// Commit 实现 PriceWriteGate。
func (g *InterimPriceWriteGate) Commit(ctx context.Context, in PriceWriteCommit) (*CellWriteResult, error) {
	if in.Actor.ID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	if !in.Confirm {
		return nil, infraerrors.BadRequest(ReasonPriceWriteConfirm, "the write must be confirmed a second time")
	}
	req, err := NormalizeCellWriteRequest(in.Request)
	if err != nil {
		return nil, err
	}
	req.OperatorID = in.Actor.ID
	req.ApprovalID = in.ApprovalID
	hash := PriceWritePlanHash(req)

	var result *CellWriteResult
	err = g.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		// 写入与保存时校验在 ApplyCellWritesTx 里是一体的：写入之后、消耗审批之前校验，
		// 违规就整个事务回滚，审批也不会被消耗。
		res, err := g.tx.ApplyCellWritesTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := g.authorize(ctx, tx, in, hash, res); err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	if g.invalidator != nil && len(result.ChangedGroupIDs) > 0 {
		g.invalidator.InvalidateGroups(result.ChangedGroupIDs...)
	}
	return result, nil
}

// authorize 在写入事务里核对并消耗审批；返回错误会让整个事务回滚（写入与历史一并撤销）。
func (g *InterimPriceWriteGate) authorize(ctx context.Context, tx MatrixExecutor, in PriceWriteCommit, hash string, res *CellWriteResult) error {
	if in.ApprovalID == 0 {
		if res.TouchesPrice {
			return infraerrors.Forbidden(ReasonPriceWriteApproval, "a write that touches prices needs a previewed approval")
		}
		return nil
	}
	a, err := g.store.ConsumeApproval(ctx, tx, in.ApprovalID, hash, PriceWriteKindCells, in.Actor.ID, g.now())
	if err != nil {
		return err
	}
	return checkApprovalForWrite(a, res.TouchesPrice, in.Actor)
}

// checkApprovalForWrite 核对被消耗的审批与这次写入：预览时没涉价、实际写入涉价，要重新预览；
// 涉价且方向不是 none 的写入必须是交互式管理员会话，机器令牌不行。单元格与分组配置的写入共用。
func checkApprovalForWrite(a *PriceWriteApproval, writeTouchesPrice bool, actor PriceWriteActor) error {
	if writeTouchesPrice && !a.TouchesPrice {
		return infraerrors.Conflict(ReasonPriceWritePlanChanged, "the write now touches prices but the preview did not, preview again")
	}
	if a.TouchesPrice && a.Delta != PriceDeltaNone && !actor.Interactive {
		return infraerrors.Forbidden(ReasonPriceWriteInteractive, "price changes must be confirmed in an interactive administrator session")
	}
	return nil
}
