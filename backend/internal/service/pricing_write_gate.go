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
type InterimPriceWriteGate struct {
	store       PriceWriteStore
	writer      CellWriter
	invalidator MatrixSnapshotInvalidator
	now         func() time.Time
}

// NewInterimPriceWriteGate 创建过渡审批关口。invalidator 可为 nil（没有读取方）。
func NewInterimPriceWriteGate(store PriceWriteStore, writer CellWriter, invalidator MatrixSnapshotInvalidator) *InterimPriceWriteGate {
	return &InterimPriceWriteGate{store: store, writer: writer, invalidator: invalidator, now: time.Now}
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
	planned, err := g.writer.PlanTx(ctx, g.store.Reader(), req)
	if err != nil {
		return nil, err
	}
	touches := PlannedTouchesPrice(planned)
	delta, err := resolveProposalDelta(in.Delta, touches)
	if err != nil {
		return nil, err
	}

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
		ExpiresAt: approval.ExpiresAt, Planned: planned,
	}, nil
}

func resolveProposalDelta(delta PriceDelta, touches bool) (PriceDelta, error) {
	switch delta {
	case "":
		if touches {
			return PriceDeltaUnknown, nil
		}
		return PriceDeltaNone, nil
	case PriceDeltaUp, PriceDeltaDown, PriceDeltaUnknown:
		if !touches {
			return "", infraerrors.BadRequest(ReasonPriceDeltaInvalid, "a write that does not touch prices has no price direction")
		}
		return delta, nil
	case PriceDeltaNone:
		return delta, nil
	default:
		return "", infraerrors.BadRequest(ReasonPriceDeltaInvalid, "unknown price direction")
	}
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
	err = g.store.WithTx(ctx, func(ctx context.Context, tx MatrixExecutor) error {
		res, err := g.writer.ApplyTx(ctx, tx, req)
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
	if res.TouchesPrice && !a.TouchesPrice {
		return infraerrors.Conflict(ReasonPriceWritePlanChanged, "the write now touches prices but the preview did not, preview again")
	}
	if a.TouchesPrice && a.Delta != PriceDeltaNone && !in.Actor.Interactive {
		return infraerrors.Forbidden(ReasonPriceWriteInteractive, "price changes must be confirmed in an interactive administrator session")
	}
	return nil
}
