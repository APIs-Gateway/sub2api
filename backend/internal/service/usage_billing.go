package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
