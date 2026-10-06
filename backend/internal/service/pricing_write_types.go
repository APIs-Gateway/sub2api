package service

import (
	"context"
	"database/sql"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-1「价格写入路径」的类型：单元格写入器（CellWriter）与 W5 落地之前的过渡审批（PriceWriteGate）。
//
// 本 PR 之前矩阵表里没有任何可写的 v2 单元格（线上没有 v2 分组），所以这里的写入只对
// pricing_stage = 'v2' 的分组生效；其余分组一律被拒绝，线上计费与管理端行为不变。
// 没有 HTTP 入口、没有生产路径构造这些类型：入口、预览估算（QuoteWith / PriceDiff）、批量接口
// 在 PR4b-2 接入。

// MatrixExecutor 写入器需要的最小执行器接口。
// *sql.DB、*sql.Tx 满足它；ent 生成时开了 sql/execquery，*ent.Tx 同样带这两个方法，
// 所以同一份写入逻辑既能跑在本包的事务里，也能跑在 W5 change-set 的 ent 事务里。
// 刻意不含 QueryRowContext：*ent.Tx 没有它。
type MatrixExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MatrixTx 写入用的事务句柄：在 MatrixExecutor 之上要求 Commit / Rollback，*sql.Tx 与 *ent.Tx 都满足，
// *sql.DB 不满足。写入器的 ApplyTx 收它而不是 MatrixExecutor，是为了让「传了不在事务里的连接」在编译期就报错：
// 在自动提交的连接上，FOR UPDATE 的行锁会在语句结束时立刻放掉，写入不再是原子的，也和钩子、阶段切换互斥不了。
// 调用方负责提交与回滚，写入器不会调用 Commit / Rollback。
type MatrixTx interface {
	MatrixExecutor
	Commit() error
	Rollback() error
}

// CellOpKind 单元格操作的种类。
type CellOpKind string

const (
	// CellOpUpsert 把单元格设成目标态（不存在就创建）。
	CellOpUpsert CellOpKind = "upsert"
	// CellOpDelete 删除单元格。
	CellOpDelete CellOpKind = "delete"
)

// CellOp 对一个精确模型名单元格的操作。只写目标态：「设为 x1.5」，不能是「再乘 1.5」（S-14）。
// 通配符单元格只由迁移派生产生，写入器不创建、不修改它们。
type CellOp struct {
	GroupID  int64      `json:"group_id"`
	ModelKey string     `json:"model_key"`
	Kind     CellOpKind `json:"kind"`

	// 以下是 CellOpUpsert 的目标态；CellOpDelete 忽略。
	Open            bool               `json:"open"`
	PriceMode       MatrixPriceMode    `json:"price_mode"`
	ExtraMultiplier *float64           `json:"extra_multiplier,omitempty"`
	CustomPrice     *MatrixCustomPrice `json:"custom_price,omitempty"`
	// Source 空串按 manual；只允许 manual 与 copied。
	Source MatrixSource `json:"source,omitempty"`

	// BaselineRevision 调用方读到的单元格 revision；0 表示调用方认为单元格不存在。
	// 与库里不一致就拒绝写入（PRICE_BASELINE_CHANGED），由调用方重新预览。
	BaselineRevision int64 `json:"baseline_revision"`
}

// CellWriteRequest 一次单元格写入。
type CellWriteRequest struct {
	Ops []CellOp `json:"ops"`
	// GroupRevisions 每个涉及分组的 group_model_config.revision 基线，键必须正好等于 Ops 涉及的分组集合。
	GroupRevisions map[int64]int64 `json:"group_revisions"`

	// 以下三项只进审计，不参与计划指纹。
	OperatorID  int64 `json:"-"`
	ApprovalID  int64 `json:"-"`
	ChangeSetID int64 `json:"-"` // W5 change-set；过渡阶段为 0
}

// CellWriteAction 一次操作的实际效果；与 model_group_price_history.action 一致，另加 noop。
type CellWriteAction string

const (
	CellWriteCreate CellWriteAction = "create"
	CellWriteUpdate CellWriteAction = "update"
	CellWriteDelete CellWriteAction = "delete"
	CellWriteNoop   CellWriteAction = "noop"
)

// PlannedCellWrite 规划好的一次单元格写入（纯数据，没有 I/O）。
type PlannedCellWrite struct {
	Op     CellOp            `json:"op"`
	Action CellWriteAction   `json:"action"`
	Before *StoredMatrixCell `json:"before,omitempty"`
	After  *MatrixCell       `json:"after,omitempty"`
	// TouchesPrice 价格可能变了：更新时前后的价格字段（price_mode、extra_multiplier、custom_price）不同；
	// 新建与删除一律算（字面名单元格会遮住基名与通配符单元格，增删它就改变了变体名实际生效的价）。
	// 只改 open 的更新不算。
	TouchesPrice bool `json:"touches_price"`
}

// CellWriteResult 一次写入的结果。
type CellWriteResult struct {
	Planned         []PlannedCellWrite `json:"planned"`
	ChangedGroupIDs []int64            `json:"changed_group_ids"`
	TouchesPrice    bool               `json:"touches_price"`
}

// CellGroupState 分组在 group_model_config 里的现状（写入前校验用）。
type CellGroupState struct {
	Stage    PricingStage
	Revision int64
}

// CellWriter 单元格的 tx-aware 窄写入。
type CellWriter interface {
	// PlanTx 读取现状并规划，不写入、不加锁（预览用）；校验与 ApplyTx 完全一致。
	PlanTx(ctx context.Context, exec MatrixExecutor, req CellWriteRequest) ([]PlannedCellWrite, error)
	// ApplyTx 只能在事务里调用（参数类型 MatrixTx 保证这一点），而且只能经 MatrixTxWriter.ApplyCellWritesTx 调用：
	// 保存时校验在那里和写入做成一体，直接调用 ApplyTx 就绕过了它（pricing_write_tx_guard_test.go 守着）。
	// 它在调用方的事务里写入：先按 group_id 升序对 group_model_config 行 SELECT ... FOR UPDATE
	// （与派生钩子、阶段切换互斥），确认分组都是 v2 且基线未变，再逐个单元格写入，
	// 同一事务里追加 model_group_price_history，并把涉及分组的配置 revision 加一。
	// 提交之后调用方必须对 ChangedGroupIDs 调用 MatrixSnapshotInvalidator.InvalidateGroups。
	ApplyTx(ctx context.Context, tx MatrixTx, req CellWriteRequest) (*CellWriteResult, error)
}

// PriceDelta 一次写入对用户实付价格的方向；PR4 的 PriceDiff 给出同样四个值。
type PriceDelta string

const (
	PriceDeltaUp      PriceDelta = "up"
	PriceDeltaDown    PriceDelta = "down"
	PriceDeltaNone    PriceDelta = "none"
	PriceDeltaUnknown PriceDelta = "unknown"
)

// 审批记录的种类（pricing_write_approvals.kind，VARCHAR(24)，库里没有枚举约束）。
const (
	// PriceWriteKindCells 单元格写入。
	PriceWriteKindCells = "cell_write"
	// PriceWriteKindGroupConfig 分组配置写入：改计费来源、改模型映射会改变请求按哪个模型计费，按涉价处理。
	PriceWriteKindGroupConfig = "group_config"
)

// PriceWriteApprovalTTL 预览记录的有效期，过期必须重新预览。
const PriceWriteApprovalTTL = 30 * time.Minute

// PriceWriteApproval pricing_write_approvals 的一行：一次预览，写入时被消耗，消耗后即审批记录。
type PriceWriteApproval struct {
	ID           int64
	Kind         string
	PlanHash     string
	TouchesPrice bool
	Delta        PriceDelta
	GroupIDs     []int64
	// Summary 预览时的前后对比（JSON），只进审计。
	Summary     []byte
	PreviewedBy int64
	ApprovedBy  int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// PriceWriteStore 过渡审批用到的存储。
type PriceWriteStore interface {
	// Reader 返回预览规划用的执行器（不开事务）。
	Reader() MatrixExecutor
	// WithTx 在一个事务里运行 fn（设置 lock_timeout）；fn 返回错误就回滚。
	WithTx(ctx context.Context, fn func(ctx context.Context, tx MatrixTx) error) error
	InsertApproval(ctx context.Context, a PriceWriteApproval) (int64, error)
	// ConsumeApproval 在 tx 里原子地把 previewed 且未过期、指纹与种类都匹配的记录置为 consumed，并返回它；
	// 其他情况返回 ClassifyApprovalRejection 给出的错误。
	ConsumeApproval(ctx context.Context, tx MatrixExecutor, id int64, planHash, kind string, approverID int64, now time.Time) (*PriceWriteApproval, error)
	// PurgeStale 删除创建于 before 之前、从未被消耗的预览记录；已消耗的是审计记录，不删。
	PurgeStale(ctx context.Context, before time.Time) (int64, error)
}

// PriceWriteGate 单元格写入的审批关口（设计文档 6.3、附录 S-13）。
// W5 落地之前是 InterimPriceWriteGate（管理员二次确认 + 审批记录）；W5 落地后同一入口改为创建 change-set。
type PriceWriteGate interface {
	// Propose 登记一次预览：规划写入（不落库）、生成计划指纹、记录审批行，返回凭证。
	Propose(ctx context.Context, in PriceWriteProposal) (*PriceWriteTicket, error)
	// Commit 消耗凭证并写入。涉价的写入没有凭证一律拒绝。
	Commit(ctx context.Context, in PriceWriteCommit) (*CellWriteResult, error)
}

// PriceWriteProposal 预览请求。价格方向不在这里：它只能由服务端估算器（PriceDeltaEstimator）给出，
// 请求里没有这个字段，所以调用方（包括 HTTP 层）没有办法自己声明一个 none。
type PriceWriteProposal struct {
	Request CellWriteRequest
}

// PriceWriteTicket 预览凭证。
type PriceWriteTicket struct {
	ApprovalID   int64              `json:"approval_id"`
	PlanHash     string             `json:"plan_hash"`
	TouchesPrice bool               `json:"touches_price"`
	Delta        PriceDelta         `json:"price_delta"`
	ExpiresAt    time.Time          `json:"expires_at"`
	Planned      []PlannedCellWrite `json:"planned"`
}

// PriceWriteActor 提交写入的操作人。
type PriceWriteActor struct {
	ID int64
	// Interactive 是交互式管理员会话（JWT）；机器令牌（admin token）与全局管理员密钥不是。
	// 只能由 PriceWriteActorFromAuthMethod 按鉴权中间件记下的 auth_method 构造，不取自请求体。
	Interactive bool
}

// PriceWriteCommit 提交请求。
type PriceWriteCommit struct {
	// ApprovalID 预览凭证；0 表示没有预览，只允许不涉价的写入。
	ApprovalID int64
	Request    CellWriteRequest
	// Confirm 管理员的二次确认。
	Confirm bool
	Actor   PriceWriteActor
}

// 价格写入路径的错误原因。
const (
	ReasonCellOpsEmpty             = "CELL_OPS_EMPTY"
	ReasonCellOpsTooMany           = "CELL_OPS_TOO_MANY"
	ReasonCellOpInvalid            = "CELL_OP_INVALID"
	ReasonCellOpDuplicate          = "CELL_OP_DUPLICATE"
	ReasonCellGroupBaselineMissing = "CELL_GROUP_BASELINE_MISSING"
	ReasonCellGroupNotV2           = "CELL_GROUP_NOT_V2"
	ReasonCellReadonly             = "CELL_READONLY"
	ReasonPriceBaselineChanged     = "PRICE_BASELINE_CHANGED"
	ReasonPriceWriteActorRequired  = "PRICE_WRITE_ACTOR_REQUIRED"
	ReasonPriceWriteConfirm        = "PRICE_WRITE_CONFIRM_REQUIRED"
	ReasonPriceWriteApproval       = "PRICE_WRITE_APPROVAL_REQUIRED"
	ReasonPriceWriteInteractive    = "PRICE_WRITE_INTERACTIVE_REQUIRED"
	ReasonPriceWritePlanChanged    = "PRICE_WRITE_PLAN_CHANGED"
	ReasonApprovalNotFound         = "PRICE_WRITE_APPROVAL_NOT_FOUND"
	ReasonApprovalExpired          = "PRICE_WRITE_APPROVAL_EXPIRED"
	ReasonApprovalConsumed         = "PRICE_WRITE_APPROVAL_CONSUMED"
	ReasonApprovalMismatch         = "PRICE_WRITE_APPROVAL_MISMATCH"
)

// ClassifyApprovalRejection 消耗审批行失败时，根据行的现状给出具体原因。found 为 false 表示行不存在。
func ClassifyApprovalRejection(found bool, status, planHash, kind string, expiresAt time.Time, wantHash, wantKind string, now time.Time) error {
	switch {
	case !found:
		return infraerrors.NotFound(ReasonApprovalNotFound, "price write approval not found")
	case status != "previewed":
		return infraerrors.Conflict(ReasonApprovalConsumed, "price write approval was already used")
	case !now.Before(expiresAt):
		return infraerrors.Conflict(ReasonApprovalExpired, "price write preview expired, preview again")
	case planHash != wantHash || kind != wantKind:
		return infraerrors.Conflict(ReasonApprovalMismatch, "write request differs from the previewed one, preview again")
	default:
		// 行本身合法却没消耗成功：只可能是并发消耗。
		return infraerrors.Conflict(ReasonApprovalConsumed, "price write approval was already used")
	}
}
