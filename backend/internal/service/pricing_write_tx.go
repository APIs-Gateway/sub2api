package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-1：价格写入的事务入口只留一个（REVIEW_OPUS_2 给 4b-2b 的要求 ①）。
//
// 单元格与分组配置的写入器（CellWriter、GroupConfigWriter）只负责写；「写完以后保存时校验必须通过」这条不变量
// （设计 5.2，ExposureGuard）由本文件的两个入口负责，把两件事做成一体：
//   - ApplyCellWritesTx：单元格写入 + 对写入后的 open 单元格做保存时校验；
//   - ApplyGroupConfigTx：分组配置写入 + 对改成白名单的分组做保存时校验。
//
// 内置的过渡审批（InterimPriceWriteGate、GroupConfigService）、HTTP 接口、W5 的 change-set 动作
// （拿着 *ent.Tx 过来）都只调这两个入口，不直接调用写入器的 ApplyTx，否则就绕过了保存时校验。
// pricing_write_tx_guard_test.go 的静态守卫保证除本文件以外没有别处调用写入器的 ApplyTx。
//
// 两个入口都必须在事务里调用（参数类型 MatrixTx）：校验读到的是已加锁、已写入的状态，违规时由调用方回滚整个事务。
// 提交之后调用方要对结果里的分组调用 MatrixSnapshotInvalidator.InvalidateGroups。

// MatrixTxWriter 把写入器与保存时校验接成一体。
type MatrixTxWriter struct {
	cells    CellWriter
	groups   GroupConfigWriter
	exposure *ExposureGuard
}

// NewMatrixTxWriter 创建写入入口。exposure 为 nil 时所有会留下 open 单元格或改成白名单的写入失败关闭；
// cells 或 groups 为 nil 时对应的入口返回内部错误。
func NewMatrixTxWriter(cells CellWriter, groups GroupConfigWriter, exposure *ExposureGuard) *MatrixTxWriter {
	return &MatrixTxWriter{cells: cells, groups: groups, exposure: exposure}
}

// ReasonPriceWriterMissing 写入入口没有配置对应的写入器。
const ReasonPriceWriterMissing = "PRICE_WRITER_UNAVAILABLE"

func writerMissing() error {
	return infraerrors.InternalServer(ReasonPriceWriterMissing, "price writer is not configured")
}

// ApplyCellWritesTx 在调用方的事务里写入单元格，并在同一个事务里对写入之后的 open 单元格做保存时校验
// （白名单分组里不能出现无价或 0 元的 open 单元格）。校验不过返回错误，调用方必须回滚。
func (w *MatrixTxWriter) ApplyCellWritesTx(ctx context.Context, tx MatrixTx, req CellWriteRequest) (*CellWriteResult, error) {
	if w == nil || w.cells == nil {
		return nil, writerMissing()
	}
	res, err := w.cells.ApplyTx(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	// 配置行已被 ApplyTx 锁住，这里读到的准入模式不会在校验与提交之间变化。
	if err := w.exposure.CheckCellWrites(ctx, tx, res.Planned); err != nil {
		return nil, err
	}
	return res, nil
}

// ApplyGroupConfigTx 在调用方的事务里写入分组配置；内容变了、涉准入且目标是白名单时，
// 在同一个事务里对这个分组现有的 open 单元格做保存时校验。校验不过返回错误，调用方必须回滚。
func (w *MatrixTxWriter) ApplyGroupConfigTx(ctx context.Context, tx MatrixTx, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	if w == nil || w.groups == nil {
		return nil, writerMissing()
	}
	res, err := w.groups.ApplyTx(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if res.Changed && res.ExposureRelevant && res.After.AccessMode == MatrixAccessAllowlist {
		if err := w.exposure.CheckGroups(ctx, tx, []int64{req.GroupID}); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// PreviewCellWrites 预览用：读取现状、规划，并对规划结果做保存时校验（结果只作提示，提交时会在事务里重算）。
// exec 是只读连接，不加锁。
func (w *MatrixTxWriter) PreviewCellWrites(ctx context.Context, exec MatrixExecutor, req CellWriteRequest) ([]PlannedCellWrite, error) {
	if w == nil || w.cells == nil {
		return nil, writerMissing()
	}
	planned, err := w.cells.PlanTx(ctx, exec, req)
	if err != nil {
		return nil, err
	}
	if err := w.exposure.CheckCellWrites(ctx, exec, planned); err != nil {
		return nil, err
	}
	return planned, nil
}

// PreviewGroupConfig 预览用：读取现状、规划（不写入、不加锁），内容变了、涉准入且目标是白名单时，
// 按「假如它已经是白名单」对现有的 open 单元格做保存时校验，让管理员提前看到阻止原因。
func (w *MatrixTxWriter) PreviewGroupConfig(ctx context.Context, exec MatrixExecutor, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	if w == nil || w.groups == nil {
		return nil, writerMissing()
	}
	res, err := w.groups.PlanTx(ctx, exec, req)
	if err != nil {
		return nil, err
	}
	if res.Changed && res.ExposureRelevant && res.After.AccessMode == MatrixAccessAllowlist {
		if err := w.exposure.CheckGroupAsAllowlist(ctx, exec, req.GroupID); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// PriceWriteActorFromAuthMethod 由鉴权方式构造操作人：只有交互式管理员会话（JWT）算 Interactive，
// 全局管理员密钥与机器令牌（admin token）都不是。authMethod 取自鉴权中间件写进上下文的 "auth_method"
// （AuditAuthMethodJWT、AuditAuthMethodAdminAPIKey、AuditAuthMethodAdminToken），不取自请求体，
// 所以请求方没法自己声明「我是交互式会话」。空串与未知取值一律按非交互式。
func PriceWriteActorFromAuthMethod(userID int64, authMethod string) PriceWriteActor {
	return PriceWriteActor{ID: userID, Interactive: authMethod == AuditAuthMethodJWT}
}
