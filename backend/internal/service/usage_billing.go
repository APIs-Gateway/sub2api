package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"
)

var ErrUsageBillingRequestIDRequired = errors.New("usage billing request_id is required")
var ErrUsageBillingRequestConflict = errors.New("usage billing request fingerprint conflict")

// UsageBillingCommand describes one billable request that must be applied at most once.
type UsageBillingCommand struct {
	RequestID          string
	APIKeyID           int64
	RequestFingerprint string
	RequestPayloadHash string

	UserID              int64
	AccountID           int64
	SubscriptionID      *int64
	AccountType         string
	Model               string
	ServiceTier         string
	ReasoningEffort     string
	BillingType         int8
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	ImageCount          int
	MediaType           string

	BalanceCost         float64
	SubscriptionCost    float64
	APIKeyQuotaCost     float64
	APIKeyRateLimitCost float64
	AccountQuotaCost    float64

	// 三窗口结算输入（per-day redesign）：
	// OfficialCost = 官方价（= CostBreakdown.TotalCost）；结算层会先乘 RateMultiplier 得到实际计费金额。
	// OfficialCost>0 触发三窗口结算。
	// RateMultiplier = 售价倍率（= ActualCost/TotalCost）；订阅额度和钱包都按该实际计费金额扣减
	// （<=0 时结算层按 1 处理）；同时也是 usage_log 展示/审计与幂等指纹分量。
	OfficialCost   float64
	RateMultiplier float64
}

func (c *UsageBillingCommand) Normalize() {
	if c == nil {
		return
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	if strings.TrimSpace(c.RequestFingerprint) == "" {
		c.RequestFingerprint = buildUsageBillingFingerprint(c)
	}
	// 量化必须在指纹计算之后：指纹是请求幂等键，保持由原始金额派生可以避免升级前后同一
	// request_id 的重试算出不同指纹而被误判为 fingerprint conflict（对齐 upstream #5232）。
	c.quantizeMonetaryFields()
}

// UsageBillingMonetaryScale 是所有计费金额的规范小数位数，
// 对齐 users.balance / api_keys.quota_used 的 decimal(20,8)（见 ent/schema/user.go、api_key.go）。
const UsageBillingMonetaryScale = 8

// quantizeMonetaryFields 把命令中会被仓储层用原始 SQL 算术应用到 NUMERIC(20,8) 列的金额
// 字段统一量化到该刻度：BalanceCost（legacy 兜底路径 1:1 扣）、SubscriptionCost、
// APIKeyQuotaCost、APIKeyRateLimitCost、AccountQuotaCost。
//
// 不量化时，同一笔金额在多条独立 SQL 上各自被 PostgreSQL 分别舍入，例如：
//
//	quota_used = quota_used + $1              // api_keys，累加
//	usage_5h   = usage_5h + $1                // api_keys 限流窗口，累加
//	extra.quota_used = extra.quota_used + $1  // accounts，jsonb 累加
//
// 若 $1 的十进制表示超过 8 位（浮点乘除很容易产生，例如 token 数 × 单价 × 分组倍率），
// PostgreSQL 落库时按 half-away-from-zero 舍入到列刻度；不同调用点各自独立舍入，无法
// 保证与同一笔金额在别处（如 usage_log 展示值、审计汇总）的舍入方向一致，长期下来产生
// 无法用 epsilon 消除的系统性偏差（原始案例见 upstream #5229/#5232：balance 减法与
// quota_used 加法对同一金额舍入方向相反，二者 delta 相差 1e-8 且随请求量线性累积）。
// 在参数进入 SQL 之前统一量化一次，所有消费者拿到的都是已经落在 8 位刻度上的同一个值，
// 存储阶段不再发生任何进一步舍入。
//
// 注意：OfficialCost / RateMultiplier 不在此列——它们是 per-day 三窗口结算
// （settleSubscriptionWindow → service.SettleWindow）的输入，钱包余额与订阅三窗口用量
// 在 Go 侧算完后整体以绝对值 SET 落库（不是 SQL 算术 += ），不存在本函数要防的“同一笔
// 金额被两条方向相反的 SQL 算术分别舍入”问题，因此不需要也不应该在此量化。
func (c *UsageBillingCommand) quantizeMonetaryFields() {
	c.BalanceCost = QuantizeUsageBillingAmount(c.BalanceCost)
	c.SubscriptionCost = QuantizeUsageBillingAmount(c.SubscriptionCost)
	c.APIKeyQuotaCost = QuantizeUsageBillingAmount(c.APIKeyQuotaCost)
	c.APIKeyRateLimitCost = QuantizeUsageBillingAmount(c.APIKeyRateLimitCost)
	c.AccountQuotaCost = QuantizeUsageBillingAmount(c.AccountQuotaCost)
}

// QuantizeUsageBillingAmount 把金额舍入到 UsageBillingMonetaryScale 位小数，
// 采用与 PostgreSQL NUMERIC 一致的 half-away-from-zero 规则。
//
// 走 decimal 而不是 math.Round(v*1e8)/1e8：后者在乘除过程中会引入额外的二进制
// 误差，边界值可能被推到错误的一侧。decimal.NewFromFloat 取 float64 的最短十进制
// 表示，正是 PostgreSQL 把 float8 参数转成 numeric 时所用的表示。
func QuantizeUsageBillingAmount(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	quantized, _ := decimal.NewFromFloat(v).Round(UsageBillingMonetaryScale).Float64()
	return quantized
}

func buildUsageBillingFingerprint(c *UsageBillingCommand) string {
	if c == nil {
		return ""
	}
	raw := fmt.Sprintf(
		"%d|%d|%d|%s|%s|%s|%s|%d|%d|%d|%d|%d|%d|%s|%d|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f",
		c.UserID,
		c.AccountID,
		c.APIKeyID,
		strings.TrimSpace(c.AccountType),
		strings.TrimSpace(c.Model),
		strings.TrimSpace(c.ServiceTier),
		strings.TrimSpace(c.ReasoningEffort),
		c.BillingType,
		c.InputTokens,
		c.OutputTokens,
		c.CacheCreationTokens,
		c.CacheReadTokens,
		c.ImageCount,
		strings.TrimSpace(c.MediaType),
		valueOrZero(c.SubscriptionID),
		c.BalanceCost,
		c.SubscriptionCost,
		c.APIKeyQuotaCost,
		c.APIKeyRateLimitCost,
		c.AccountQuotaCost,
		// per-day 结算实际副作用依赖官方价与倍率：两者共同决定实际计费金额和套餐/钱包拆分，
		// 纳入指纹避免幂等键误覆盖/漏冲突。
		c.OfficialCost,
		c.RateMultiplier,
	)
	if payloadHash := strings.TrimSpace(c.RequestPayloadHash); payloadHash != "" {
		raw += "|" + payloadHash
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func HashUsageRequestPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func valueOrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// AccountQuotaState holds the post-increment quota state returned by the DB transaction.
// All values are post-update (i.e., already include the increment).
type AccountQuotaState struct {
	TotalUsed   float64
	TotalLimit  float64
	DailyUsed   float64
	DailyLimit  float64
	WeeklyUsed  float64
	WeeklyLimit float64
}

type UsageBillingApplyResult struct {
	Applied              bool
	APIKeyQuotaExhausted bool
	NewBalance           *float64           // post-settlement wallet balance (nil = no settlement)
	QuotaState           *AccountQuotaState // post-increment quota state (nil = no quota increment)
	// WalletDebit 本次结算从钱包（users.balance）实际扣减的售价货币额（订阅覆盖部分不计入）。
	// = 钱包正余额扣 + 钱包负数兜底扣。供余额提醒按真实扣减重建旧余额、缓存按真实变化回写。
	// nil = 未发生三窗口结算。
	WalletDebit *float64
	// OverdraftApplied 本次结算发生了透支（改了 users.monthly_overdraft_count）。
	// 上层据此失效该用户鉴权快照，让准入读到最新月度透支计数（否则缓存计数偏低、误放行已满额用户）。
	OverdraftApplied bool
	// SubscriptionID 本次结算命中用户有效订阅卡时填该卡 ID，否则 nil。
	// 上层据此把 usage_log 标为 subscription 计费 + 写 subscription_id；有效卡即订阅瀑布请求，
	// 即使套餐余额为 0、费用全由钱包层支付也仍应标 subscription。过期但 status='active' 的假
	// active 卡会被惰性标 expired，不应填。
	SubscriptionID *int64
	// DepletedSubscriptionGroupIDs 本次扣费把哪些订阅卡的剩余额度扣到 0（burn-down 用完），
	// 并已在同一事务内即时标记为 expired（用完即失效，不必等到期日）。调用方据此失效对应
	// (user, group) 的订阅缓存，让"我的订阅 / active 列表"立即反映。
	DepletedSubscriptionGroupIDs []int64
}

type UsageBillingRepository interface {
	Apply(ctx context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error)
}
