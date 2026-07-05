package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// 邀请返利积分制（issue #11）——领域类型、仓储接口与计价纯函数。
//
// 计价口径（spec §3，全局统一，禁用 math.Round，用 decimal 整除/Floor/Ceil 杜绝 0.5 歧义）：
//   - earning  : floor(amount × rate% / 100 / peg)     —— 偏平台
//   - 换套餐    : ceil(price / peg)                      —— 偏平台
//   - clawback : floor(earned × refundAmount/originalAmount)；全额退特例 = earned —— 偏用户
//   - 换余额    : points × peg                            —— 平价
//   - 提现      : gross = points × peg；fee = gross × fee%；net = gross − fee；支付宝按 CNY，USDT 按 USD/CNY 日价+0.1 折算

// --- 错误 ---

var (
	ErrPointsDisabled           = infraerrors.BadRequest("POINTS_DISABLED", "points feature is not enabled")
	ErrPointsInsufficient       = infraerrors.BadRequest("POINTS_INSUFFICIENT", "insufficient points")
	ErrPointsAmountInvalid      = infraerrors.BadRequest("POINTS_AMOUNT_INVALID", "invalid points amount")
	ErrPointsWithdrawDisabled   = infraerrors.BadRequest("POINTS_WITHDRAW_DISABLED", "points withdrawal is not enabled")
	ErrPointsWithdrawBelowMin   = infraerrors.BadRequest("POINTS_WITHDRAW_BELOW_MIN", "below minimum withdrawal points")
	ErrPointsWithdrawPayout     = infraerrors.BadRequest("POINTS_WITHDRAW_PAYOUT_INVALID", "invalid payout method or destination")
	ErrPointsWithdrawPending    = infraerrors.Conflict("POINTS_WITHDRAW_PENDING_EXISTS", "a pending withdrawal already exists")
	ErrPointsWithdrawNotPending = infraerrors.Conflict("POINTS_WITHDRAW_NOT_PENDING", "withdrawal is not pending")
	ErrPointsRedeemPlanDisabled = infraerrors.BadRequest("POINTS_REDEEM_PLAN_DISABLED", "points-to-plan redeem is not enabled")
	ErrPointsPlanInvalid        = infraerrors.BadRequest("POINTS_PLAN_INVALID", "selected plan is not available for points redeem")
	ErrPointsPlanDuplicate      = infraerrors.Conflict("POINTS_PLAN_DUPLICATE", "this points redemption was already processed")
	ErrPointsRedeemBalanceOff   = infraerrors.BadRequest("POINTS_REDEEM_BALANCE_DISABLED", "points-to-balance redeem is not enabled")
	ErrPointsWithdrawalNotFound = infraerrors.NotFound("POINTS_WITHDRAWAL_NOT_FOUND", "withdrawal not found")
)

// --- 提现方式 / 流水类型 / 提现状态 ---

const (
	PointsPayoutMethodAlipay = "alipay"
	PointsPayoutMethodUSDT   = "usdt"
	PointsPayoutCurrencyCNY  = "CNY"
	PointsPayoutCurrencyUSD  = "USD"

	PointsKindEarn           = "earn"
	PointsKindClawback       = "clawback"
	PointsKindThaw           = "thaw"
	PointsKindToBalance      = "to_balance"
	PointsKindWithdrawHold   = "withdraw_hold"
	PointsKindWithdrawPaid   = "withdraw_paid"
	PointsKindWithdrawRefund = "withdraw_refund"
	PointsKindToPlan         = "to_plan"
	PointsKindAdjust         = "adjust"

	PointsWithdrawalStatusPending  = "pending"
	PointsWithdrawalStatusPaid     = "paid"
	PointsWithdrawalStatusRejected = "rejected"
)

// --- DTO ---

// PointsAccount 用户积分账户。
type PointsAccount struct {
	UserID         int64     `json:"user_id"`
	Available      int64     `json:"available"`
	Frozen         int64     `json:"frozen"`
	LifetimeEarned int64     `json:"lifetime_earned"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PointsLedgerEntry 积分流水行。
type PointsLedgerEntry struct {
	ID                 int64      `json:"id"`
	UserID             int64      `json:"user_id"`
	Kind               string     `json:"kind"`
	Points             int64      `json:"points"`
	PegAt              *float64   `json:"peg_at,omitempty"`
	SourceUserID       *int64     `json:"source_user_id,omitempty"`
	SourceEmail        string     `json:"source_email,omitempty"`
	SourceOrderID      *int64     `json:"source_order_id,omitempty"`
	SourceRedeemCodeID *int64     `json:"source_redeem_code_id,omitempty"`
	WithdrawalID       *int64     `json:"withdrawal_id,omitempty"`
	FrozenUntil        *time.Time `json:"frozen_until,omitempty"`
	AvailableAfter     *int64     `json:"available_after,omitempty"`
	FrozenAfter        *int64     `json:"frozen_after,omitempty"`
	Note               string     `json:"note,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

// PointsWithdrawal 提现单。
type PointsWithdrawal struct {
	ID                  int64      `json:"id"`
	UserID              int64      `json:"user_id"`
	UserEmail           string     `json:"user_email,omitempty"`
	Username            string     `json:"username,omitempty"`
	Points              int64      `json:"points"`
	GrossAmount         float64    `json:"gross_amount"`
	FeeAmount           float64    `json:"fee_amount"`
	NetAmount           float64    `json:"net_amount"`
	PayoutCurrency      string     `json:"payout_currency"`
	USDCNYRateAt        *float64   `json:"usd_cny_rate_at,omitempty"`
	PegAt               *float64   `json:"peg_at,omitempty"`
	FeePercentAt        *float64   `json:"fee_percent_at,omitempty"`
	PayoutMethod        string     `json:"payout_method"`
	PayoutAlipayAccount string     `json:"payout_alipay_account,omitempty"`
	PayoutAlipayName    string     `json:"payout_alipay_name,omitempty"`
	PayoutUSDTChain     string     `json:"payout_usdt_chain,omitempty"`
	PayoutUSDTAddress   string     `json:"payout_usdt_address,omitempty"`
	Status              string     `json:"status"`
	ReviewNote          string     `json:"review_note,omitempty"`
	ReviewedBy          *int64     `json:"reviewed_by,omitempty"`
	PayoutProof         string     `json:"payout_proof,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	ReviewedAt          *time.Time `json:"reviewed_at,omitempty"`
}

// --- Repository 输入 ---

// EarnPointsInput 一次 earning（按来源幂等）。来源锚二选一：
// SourceOrderID（法币订单）或 SourceRedeemCodeID（兑换码，方案 C）。
type EarnPointsInput struct {
	InviterID          int64
	SourceUserID       int64
	SourceOrderID      int64
	SourceRedeemCodeID int64
	Points             int64
	FreezeHours        int
	PegAt              float64
}

// CreateWithdrawalInput 创建一张提现单（金额由 service 用纯函数预算好）。
type CreateWithdrawalInput struct {
	UserID              int64
	Points              int64
	GrossAmount         float64
	FeeAmount           float64
	NetAmount           float64
	PayoutCurrency      string
	USDCNYRateAt        float64
	PegAt               float64
	FeePercentAt        float64
	PayoutMethod        string
	PayoutAlipayAccount string
	PayoutAlipayName    string
	PayoutUSDTChain     string
	PayoutUSDTAddress   string
}

// PointsWithdrawalFilter admin 提现队列筛选。
type PointsWithdrawalFilter struct {
	Status   string
	Search   string
	Page     int
	PageSize int
}

// PointsLedgerFilter admin 流水筛选。
type PointsLedgerFilter struct {
	Kind     string
	Search   string
	Page     int
	PageSize int
}

// --- Repository 接口（impl 在 repository 包） ---

type PointsRepository interface {
	EnsureAccount(ctx context.Context, userID int64) (*PointsAccount, error)
	GetAccount(ctx context.Context, userID int64) (*PointsAccount, error)
	// EarnPoints 幂等返积分（partial-unique on (user_id, source_order_id) WHERE kind='earn'）。返回是否真正入账。
	EarnPoints(ctx context.Context, in EarnPointsInput) (bool, error)
	// ClawbackByOrder 退款撤回积分：读该单 earn 流水取 earned，floor 比例撤、可转负、一单一撤幂等。返回撤回积分数。
	ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error)
	// ThawDuePoints 到期冻结积分 → 可用，返回解冻数。
	ThawDuePoints(ctx context.Context, userID int64) (int64, error)
	// RedeemToBalance 扣积分 + 加钱包余额（平价），单事务。返回新余额。
	RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error)
	// DeductForPlan 仅扣积分写 to_plan 流水（在 service 事务内调用，发卡同事务）。available 不足返回 ErrPointsInsufficient。
	// idempotencyKey 非空时按 (user_id, idempotency_key) 幂等：命中既有 to_plan 行返回 ErrPointsPlanDuplicate。
	DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error
	CreateWithdrawal(ctx context.Context, in CreateWithdrawalInput) (*PointsWithdrawal, error)
	GetWithdrawal(ctx context.Context, id int64) (*PointsWithdrawal, error)
	// ReviewWithdrawal approve→paid（不动账户）；reject→退回 available。状态机 + 幂等。
	ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*PointsWithdrawal, error)
	ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]PointsWithdrawal, error)
	ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]PointsLedgerEntry, int64, error)
	ListWithdrawals(ctx context.Context, filter PointsWithdrawalFilter) ([]PointsWithdrawal, int64, error)
	ListLedger(ctx context.Context, filter PointsLedgerFilter) ([]PointsLedgerEntry, int64, error)
}

// --- 计价纯函数（spec §3；单测覆盖取整边界，含 0.5 截断） ---

// ComputeEarnPoints = floor(amount × ratePercent / 100 / peg)。
func ComputeEarnPoints(amount, ratePercent, peg float64) int64 {
	if amount <= 0 || ratePercent <= 0 || peg <= 0 {
		return 0
	}
	v := decimal.NewFromFloat(amount).
		Mul(decimal.NewFromFloat(ratePercent)).
		Div(decimal.NewFromInt(100)).
		Div(decimal.NewFromFloat(peg)).
		Floor().
		IntPart()
	if v < 0 {
		return 0
	}
	return v
}

// ComputePlanPoints = ceil(price / peg)。
func ComputePlanPoints(price, peg float64) int64 {
	if price <= 0 || peg <= 0 {
		return 0
	}
	v := decimal.NewFromFloat(price).
		Div(decimal.NewFromFloat(peg)).
		Ceil().
		IntPart()
	if v < 0 {
		return 0
	}
	return v
}

// ComputeClawbackPoints = floor(earned × refundAmount / originalAmount)；
// 全额退（refundAmount ≥ originalAmount）特例 = earned（精确反向）。向下取整、不得多撤、上限 earned。
func ComputeClawbackPoints(earned int64, refundAmount, originalAmount float64) int64 {
	if earned <= 0 || refundAmount <= 0 || originalAmount <= 0 {
		return 0
	}
	if refundAmount >= originalAmount {
		return earned
	}
	v := decimal.NewFromInt(earned).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(originalAmount)).
		Floor().
		IntPart()
	if v < 0 {
		return 0
	}
	if v > earned {
		return earned
	}
	return v
}

// PointsToBalance = points × peg（balance 单位，平价；保留至 8 位小数以贴合列精度）。
func PointsToBalance(points int64, peg float64) float64 {
	if points <= 0 || peg <= 0 {
		return 0
	}
	return decimal.NewFromInt(points).Mul(decimal.NewFromFloat(peg)).Round(8).InexactFloat64()
}

// ComputeWithdrawalAmounts 折合金额 / 手续费 / 应付（balance 单位）。
func ComputeWithdrawalAmounts(points int64, peg, feePercent float64) (gross, fee, net float64) {
	if points <= 0 || peg <= 0 {
		return 0, 0, 0
	}
	g := decimal.NewFromInt(points).Mul(decimal.NewFromFloat(peg))
	f := decimal.Zero
	if feePercent > 0 {
		f = g.Mul(decimal.NewFromFloat(feePercent)).Div(decimal.NewFromInt(100))
	}
	n := g.Sub(f)
	return g.Round(8).InexactFloat64(), f.Round(8).InexactFloat64(), n.Round(8).InexactFloat64()
}

func ConvertWithdrawalAmountsForPayout(gross, fee, net float64, method string, usdCNYRate float64) (outGross, outFee, outNet float64, currency string, rateAt float64) {
	if method != PointsPayoutMethodUSDT {
		return gross, fee, net, PointsPayoutCurrencyCNY, 0
	}
	effectiveRate := usdCNYRate + PointsWithdrawUSDCNYSpread
	if effectiveRate < PointsWithdrawUSDCNYRateMin {
		return gross, fee, net, PointsPayoutCurrencyUSD, 0
	}
	rate := decimal.NewFromFloat(effectiveRate)
	return decimal.NewFromFloat(gross).Div(rate).Round(8).InexactFloat64(),
		decimal.NewFromFloat(fee).Div(rate).Round(8).InexactFloat64(),
		decimal.NewFromFloat(net).Div(rate).Round(8).InexactFloat64(),
		PointsPayoutCurrencyUSD,
		decimal.NewFromFloat(effectiveRate).Round(8).InexactFloat64()
}
