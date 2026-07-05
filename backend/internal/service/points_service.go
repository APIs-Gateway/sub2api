package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 提现收款字段长度上限（纯文本，无文件上传）。
const (
	pointsAlipayAccountMaxLen = 128
	pointsAlipayNameMaxLen    = 64
	pointsUSDTChainMaxLen     = 16
	pointsUSDTAddressMaxLen   = 128

	firstSuccessfulPaymentEarnMultiplier = 2
	pointsDailyAmountEpsilon             = 1e-9
)

var pointsSupportedUSDTChains = map[string]string{
	"TRC20": "TRC20",
	"ERC20": "ERC20",
	"BEP20": "BEP20",
}

var pointsSuccessfulPaymentOrderStatuses = []string{OrderStatusPaid, OrderStatusRecharging, OrderStatusCompleted}

// PointsService 邀请返利积分制（issue #11）业务编排。
type PointsService struct {
	repo                 PointsRepository
	settingService       *SettingService
	entClient            *dbent.Client
	subscriptionSvc      *SubscriptionService
	groupRepo            GroupRepository
	affiliateService     *AffiliateService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCacheService  *BillingCacheService
}

func NewPointsService(
	repo PointsRepository,
	settingService *SettingService,
	entClient *dbent.Client,
	subscriptionSvc *SubscriptionService,
	groupRepo GroupRepository,
	affiliateService *AffiliateService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	billingCacheService *BillingCacheService,
) *PointsService {
	return &PointsService{
		repo:                 repo,
		settingService:       settingService,
		entClient:            entClient,
		subscriptionSvc:      subscriptionSvc,
		groupRepo:            groupRepo,
		affiliateService:     affiliateService,
		authCacheInvalidator: authCacheInvalidator,
		billingCacheService:  billingCacheService,
	}
}

func (s *PointsService) IsEnabled(ctx context.Context) bool {
	return s.settingService != nil && s.settingService.IsPointsEnabled(ctx)
}

func (s *PointsService) peg(ctx context.Context) float64 {
	return s.settingService.GetPointsPeg(ctx)
}

func (s *PointsService) resolveRate(ctx context.Context, inviter *AffiliateSummary) float64 {
	if inviter != nil && inviter.AffRebateRatePercent != nil {
		v := *inviter.AffRebateRatePercent
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return clampAffiliateRebateRate(v)
		}
	}
	return s.settingService.GetPointsCashbackRate(ctx)
}

func (s *PointsService) invalidateCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		if err := s.billingCacheService.InvalidateUserBalance(ctx, userID); err != nil {
			logger.LegacyPrintf("service.points", "[Points] invalidate billing cache for user %d failed: %v", userID, err)
		}
	}
}

// --- 用户查询 ---

// GetUserPoints 确保账户存在、惰性解冻到期冻结、返回账户快照。
func (s *PointsService) GetUserPoints(ctx context.Context, userID int64) (*PointsAccount, error) {
	if _, err := s.repo.EnsureAccount(ctx, userID); err != nil {
		return nil, err
	}
	if _, err := s.repo.ThawDuePoints(ctx, userID); err != nil {
		return nil, err
	}
	return s.repo.GetAccount(ctx, userID)
}

func (s *PointsService) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]PointsLedgerEntry, int64, error) {
	return s.repo.ListUserLedger(ctx, userID, page, pageSize)
}

func (s *PointsService) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]PointsWithdrawal, error) {
	return s.repo.ListUserWithdrawals(ctx, userID, limit)
}

// --- Earning / Clawback（PR #8 支付链路钩子调用，最佳努力非阻断由调用方负责） ---

// AccrueEarnForOrder 被邀请人法币付款成功 → 邀请人返积分。
// base = order.PayAmount（实付充值金额）优先，旧订单无实付快照时回退 order.Amount；幂等（按来源单）。
// 返回实际入账积分数（0 = 无邀请人/比例 0/重复回调）。
func (s *PointsService) AccrueEarnForOrder(ctx context.Context, order *dbent.PaymentOrder) (int64, error) {
	baseAmount, ok := pointsEarnBaseAmountForOrder(order)
	if !s.IsEnabled(ctx) || !ok {
		return 0, nil
	}
	invitee, err := s.affiliateService.EnsureUserAffiliate(ctx, order.UserID)
	if err != nil {
		return 0, err
	}
	if invitee.InviterID == nil || *invitee.InviterID <= 0 || *invitee.InviterID == order.UserID {
		return 0, nil
	}
	inviterID := *invitee.InviterID
	inviter, err := s.affiliateService.EnsureUserAffiliate(ctx, inviterID)
	if err != nil {
		return 0, err
	}
	rate := s.resolveRate(ctx, inviter)
	if rate <= 0 {
		return 0, nil
	}
	peg := s.peg(ctx)
	multiplier, err := s.pointsEarnMultiplierForOrder(ctx, order)
	if err != nil {
		return 0, err
	}
	pts := ComputeEarnPoints(baseAmount*multiplier, rate, peg)
	if pts <= 0 {
		return 0, nil
	}
	applied, err := s.repo.EarnPoints(ctx, EarnPointsInput{
		InviterID:     inviterID,
		SourceUserID:  order.UserID,
		SourceOrderID: order.ID,
		Points:        pts,
		FreezeHours:   s.settingService.GetPointsFreezeHours(ctx),
		PegAt:         peg,
	})
	if err != nil {
		return 0, err
	}
	if !applied {
		return 0, nil
	}
	return pts, nil
}

func (s *PointsService) pointsEarnMultiplierForOrder(ctx context.Context, order *dbent.PaymentOrder) (float64, error) {
	if s == nil || s.entClient == nil || order == nil || order.ID <= 0 || order.UserID <= 0 {
		return 1, nil
	}
	hasPrior, err := s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(order.UserID),
			paymentorder.IDNEQ(order.ID),
			paymentorder.StatusIn(pointsSuccessfulPaymentOrderStatuses...),
		).
		Limit(1).
		Exist(ctx)
	if err != nil {
		return 0, fmt.Errorf("check first successful payment order: %w", err)
	}
	if !hasPrior {
		return firstSuccessfulPaymentEarnMultiplier, nil
	}
	return 1, nil
}

func pointsEarnBaseAmountForOrder(order *dbent.PaymentOrder) (float64, bool) {
	if order == nil {
		return 0, false
	}
	if order.PayAmount > 0 {
		return order.PayAmount, true
	}
	if order.Amount > 0 {
		return order.Amount, true
	}
	return 0, false
}

// AccrueEarnForRedeem 被邀请人兑换码兑付成功 → 邀请人返积分（方案 C：替代旧 cashback→$）。
// base：balance 码 = 面值 Value；subscription 码 = 订阅返利 base 映射（复用）。pts = floor(base × rate% / 100 / peg)；
// 按来源兑换码幂等（partial-unique on (user_id, source_redeem_code_id) WHERE kind='earn'）。
// 返回实际入账积分数（0 = 无邀请人/比例 0/base 0/重复回调）。
func (s *PointsService) AccrueEarnForRedeem(ctx context.Context, inviteeUserID int64, redeemCode *RedeemCode) (int64, error) {
	if !s.IsEnabled(ctx) || redeemCode == nil || redeemCode.ID <= 0 || inviteeUserID <= 0 {
		return 0, nil
	}
	// base 取兑换码官方价口径（balance 单位）。
	var base float64
	switch redeemCode.Type {
	case RedeemTypeBalance:
		base = redeemCode.Value
	case RedeemTypeSubscription:
		if redeemCode.GroupID == nil || redeemCode.ValidityDays <= 0 {
			return 0, nil
		}
		b, found, err := s.affiliateService.SubscriptionRebateBaseAmount(ctx, *redeemCode.GroupID, redeemCode.ValidityDays)
		if err != nil || !found || b <= 0 {
			return 0, err
		}
		base = b
	default:
		return 0, nil
	}
	if base <= 0 {
		return 0, nil
	}
	invitee, err := s.affiliateService.EnsureUserAffiliate(ctx, inviteeUserID)
	if err != nil {
		return 0, err
	}
	if invitee.InviterID == nil || *invitee.InviterID <= 0 || *invitee.InviterID == inviteeUserID {
		return 0, nil
	}
	inviterID := *invitee.InviterID
	inviter, err := s.affiliateService.EnsureUserAffiliate(ctx, inviterID)
	if err != nil {
		return 0, err
	}
	rate := s.resolveRate(ctx, inviter)
	if rate <= 0 {
		return 0, nil
	}
	peg := s.peg(ctx)
	pts := ComputeEarnPoints(base, rate, peg)
	if pts <= 0 {
		return 0, nil
	}
	applied, err := s.repo.EarnPoints(ctx, EarnPointsInput{
		InviterID:          inviterID,
		SourceUserID:       inviteeUserID,
		SourceRedeemCodeID: redeemCode.ID,
		Points:             pts,
		FreezeHours:        s.settingService.GetPointsFreezeHours(ctx),
		PegAt:              peg,
	})
	if err != nil {
		return 0, err
	}
	if !applied {
		return 0, nil
	}
	return pts, nil
}

// ClawbackForOrder 退款撤回积分（仅由退款最终落单点调用）。按实退比例 floor 撤、可转负、一单一撤幂等。
func (s *PointsService) ClawbackForOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	if !s.IsEnabled(ctx) {
		return 0, nil
	}
	return s.repo.ClawbackByOrder(ctx, sourceOrderID, refundAmount, originalAmount)
}

// --- Spending ① 换余额 ---

func (s *PointsService) RedeemToBalance(ctx context.Context, userID, points int64) (float64, error) {
	if !s.IsEnabled(ctx) {
		return 0, ErrPointsDisabled
	}
	if !s.settingService.IsPointsRedeemBalanceOn(ctx) {
		return 0, ErrPointsRedeemBalanceOff
	}
	if points <= 0 {
		return 0, ErrPointsAmountInvalid
	}
	peg := s.peg(ctx)
	delta := PointsToBalance(points, peg)
	if delta <= 0 {
		return 0, ErrPointsAmountInvalid
	}
	if _, err := s.repo.EnsureAccount(ctx, userID); err != nil {
		return 0, err
	}
	bal, err := s.repo.RedeemToBalance(ctx, userID, points, delta, peg)
	if err != nil {
		return 0, err
	}
	s.invalidateCaches(ctx, userID)
	return bal, nil
}

// --- Spending ② 提现 ---

func (s *PointsService) CreateWithdrawal(ctx context.Context, userID, points int64, method, alipayAccount, alipayName, usdtChain, usdtAddress string) (*PointsWithdrawal, error) {
	if !s.IsEnabled(ctx) {
		return nil, ErrPointsDisabled
	}
	if !s.settingService.IsPointsWithdrawOn(ctx) {
		return nil, ErrPointsWithdrawDisabled
	}
	if points <= 0 {
		return nil, ErrPointsAmountInvalid
	}
	if minPts := s.settingService.GetPointsWithdrawMin(ctx); minPts > 0 && points < minPts {
		return nil, ErrPointsWithdrawBelowMin
	}
	method = strings.TrimSpace(method)
	alipayAccount = strings.TrimSpace(alipayAccount)
	alipayName = strings.TrimSpace(alipayName)
	usdtChain = normalizeUSDTChain(usdtChain)
	usdtAddress = strings.TrimSpace(usdtAddress)
	switch method {
	case PointsPayoutMethodAlipay:
		if alipayAccount == "" || len(alipayAccount) > pointsAlipayAccountMaxLen ||
			alipayName == "" || len(alipayName) > pointsAlipayNameMaxLen {
			return nil, ErrPointsWithdrawPayout
		}
		usdtChain = ""
		usdtAddress = ""
	case PointsPayoutMethodUSDT:
		if usdtChain == "" || len(usdtChain) > pointsUSDTChainMaxLen ||
			usdtAddress == "" || len(usdtAddress) > pointsUSDTAddressMaxLen {
			return nil, ErrPointsWithdrawPayout
		}
		alipayAccount = ""
		alipayName = ""
	default:
		return nil, ErrPointsWithdrawPayout
	}
	peg := s.peg(ctx)
	feePercent := s.settingService.GetPointsWithdrawFeePercent(ctx)
	gross, fee, net := ComputeWithdrawalAmounts(points, peg, feePercent)
	// 应付为 0（peg 过小取整 / 手续费 100% 等 admin 配置）→ 拒，别凭空冻结用户积分换 0 打款。
	if net <= 0 {
		return nil, ErrPointsAmountInvalid
	}
	if _, err := s.repo.EnsureAccount(ctx, userID); err != nil {
		return nil, err
	}
	return s.repo.CreateWithdrawal(ctx, CreateWithdrawalInput{
		UserID:              userID,
		Points:              points,
		GrossAmount:         gross,
		FeeAmount:           fee,
		NetAmount:           net,
		PegAt:               peg,
		FeePercentAt:        feePercent,
		PayoutMethod:        method,
		PayoutAlipayAccount: alipayAccount,
		PayoutAlipayName:    alipayName,
		PayoutUSDTChain:     usdtChain,
		PayoutUSDTAddress:   usdtAddress,
	})
}

func normalizeUSDTChain(chain string) string {
	normalized := strings.ToUpper(strings.TrimSpace(chain))
	if canonical, ok := pointsSupportedUSDTChains[normalized]; ok {
		return canonical
	}
	return ""
}

// --- Spending ③ 换套餐（全额、直接开通、扣积分、单事务原子） ---

func (s *PointsService) RedeemToPlan(ctx context.Context, userID int64, dailyAmountUSD float64, validityDays int, idempotencyKey string) (*UserSubscription, error) {
	if !s.IsEnabled(ctx) {
		return nil, ErrPointsDisabled
	}
	if !s.settingService.IsPointsRedeemPlanOn(ctx) {
		return nil, ErrPointsRedeemPlanDisabled
	}
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	quote, err := s.subscriptionSvc.QuoteSubscription(ctx, dailyAmountUSD, validityDays)
	if err != nil {
		return nil, err
	}
	peg := s.peg(ctx)

	mode := "purchase"
	chargeAmount := quote.Price
	targetSubscriptionID := int64(0)
	renewDays := 0
	if s.subscriptionSvc.userSubRepo != nil {
		active, err := s.subscriptionSvc.userSubRepo.GetActiveByUserID(ctx, userID)
		switch {
		case err == nil && active != nil:
			if active.DailyAmountUSD > quote.DailyAmountUSD+pointsDailyAmountEpsilon {
				return nil, ErrChangePlanDowngradeNotAllowed
			}
			if math.Abs(active.DailyAmountUSD-quote.DailyAmountUSD) <= pointsDailyAmountEpsilon {
				renewQuote, err := s.subscriptionSvc.QuoteRenewOrder(ctx, userID, quote.ValidityDays)
				if err != nil {
					return nil, err
				}
				mode = "renew"
				chargeAmount = renewQuote.Price
				targetSubscriptionID = renewQuote.SubscriptionID
				renewDays = renewQuote.AddedDays
			} else {
				changeQuote, err := s.subscriptionSvc.QuoteChangePlanOrder(ctx, userID, quote.DailyAmountUSD, quote.ValidityDays)
				if err != nil {
					return nil, err
				}
				mode = "change_plan"
				chargeAmount = changeQuote.Diff
				targetSubscriptionID = changeQuote.OldSubscriptionID
			}
		case errors.Is(err, ErrSubscriptionNotFound):
			// 无生效卡：按新购开通。
		case err != nil:
			return nil, err
		}
	}
	if chargeAmount < 0 {
		return nil, ErrChangePlanDowngradeNotAllowed
	}
	need := ComputePlanPoints(chargeAmount, peg)
	if chargeAmount > 0 && need <= 0 {
		return nil, ErrPointsAmountInvalid
	}
	if _, err := s.repo.EnsureAccount(ctx, userID); err != nil {
		return nil, err
	}

	// 单事务：扣积分 + 购买/续费/转套餐。生命周期方法复用 txCtx 中的 tx，失败整体回滚。
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin points redeem-plan tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)

	note := fmt.Sprintf("points redeem plan mode=%s daily=%.4f days=%d charge=%.4f", mode, quote.DailyAmountUSD, quote.ValidityDays, chargeAmount)
	// 幂等键先于发卡：重复请求命中 to_plan partial-unique → ErrPointsPlanDuplicate → 回滚、不二次扣分/延卡。
	if need > 0 {
		if err := s.repo.DeductForPlan(txCtx, userID, need, peg, note, strings.TrimSpace(idempotencyKey)); err != nil {
			return nil, err
		}
	}

	var sub *UserSubscription
	switch mode {
	case "purchase":
		// 无卡新购在事务内二次确认，避免并发兑换开出第二张 active 卡。
		hasActive, err := tx.UserSubscription.Query().
			Where(
				usersubscription.UserIDEQ(userID),
				usersubscription.StatusEQ(SubscriptionStatusActive),
				usersubscription.ExpiresAtGT(time.Now()),
			).
			Exist(txCtx)
		if err != nil {
			return nil, fmt.Errorf("check active subscription before points redeem: %w", err)
		}
		if hasActive {
			return nil, ErrActiveSubscriptionExists
		}
		var assignErr error
		sub, _, assignErr = s.subscriptionSvc.AssignOrExtendSubscription(txCtx, &AssignSubscriptionInput{
			UserID:          userID,
			GroupID:         0,
			ValidityDays:    quote.ValidityDays,
			DailyAmountUSD:  quote.DailyAmountUSD,
			WeeklyLimitUSD:  quote.WeeklyCapUSD,
			MonthlyLimitUSD: quote.MonthlyCapUSD,
			AssignedBy:      0,
			Notes:           "points-exchange",
		})
		if assignErr != nil {
			return nil, fmt.Errorf("assign subscription for points redeem: %w", assignErr)
		}
	case "renew":
		var renewErr error
		sub, renewErr = s.subscriptionSvc.ApplyRenewFromOrder(txCtx, targetSubscriptionID, renewDays)
		if renewErr != nil {
			return nil, fmt.Errorf("renew subscription for points redeem: %w", renewErr)
		}
	case "change_plan":
		res, applyErr := s.subscriptionSvc.ApplyChangePlanFromOrder(txCtx, targetSubscriptionID, quote.DailyAmountUSD, quote.ValidityDays)
		if applyErr != nil {
			return nil, fmt.Errorf("change subscription plan for points redeem: %w", applyErr)
		}
		var getErr error
		sub, getErr = s.subscriptionSvc.GetByID(txCtx, res.NewSubscriptionID)
		if getErr != nil {
			return nil, fmt.Errorf("load changed subscription for points redeem: %w", getErr)
		}
	default:
		return nil, fmt.Errorf("unknown points redeem-plan mode %q", mode)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit points redeem-plan tx: %w", err)
	}
	committed = true
	return sub, nil
}

// --- Admin ---

func (s *PointsService) AdminListWithdrawals(ctx context.Context, filter PointsWithdrawalFilter) ([]PointsWithdrawal, int64, error) {
	return s.repo.ListWithdrawals(ctx, filter)
}

func (s *PointsService) AdminReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*PointsWithdrawal, error) {
	// review_note 列为 VARCHAR(255)；超长截断避免撞 DB 长度上限（payout_proof 为 TEXT，无需截断）。
	return s.repo.ReviewWithdrawal(ctx, id, adminID, approve, truncateRunes(strings.TrimSpace(note), 255), strings.TrimSpace(payoutProof))
}

// truncateRunes 按 rune 截断到 max（避免切坏多字节字符）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func (s *PointsService) AdminListLedger(ctx context.Context, filter PointsLedgerFilter) ([]PointsLedgerEntry, int64, error) {
	return s.repo.ListLedger(ctx, filter)
}

func (s *PointsService) AdminGetSettings(ctx context.Context) *PointsSettings {
	return s.settingService.GetPointsSettings(ctx)
}

func (s *PointsService) AdminUpdateSettings(ctx context.Context, in PointsSettingsInput) (*PointsSettings, error) {
	return s.settingService.UpdatePointsSettings(ctx, in)
}

// PointsPublicConfig 用户端可见的积分规则（不含全局返现率，返现率按用户取 EffectiveRateForUser）。
type PointsPublicConfig struct {
	Enabled            bool    `json:"enabled"`
	Peg                float64 `json:"peg"`
	WithdrawEnabled    bool    `json:"withdraw_enabled"`
	WithdrawMinPoints  int64   `json:"withdraw_min_points"`
	WithdrawFeePercent float64 `json:"withdraw_fee_percent"`
	RedeemBalanceOn    bool    `json:"redeem_balance_on"`
	RedeemPlanOn       bool    `json:"redeem_plan_on"`
}

// PublicConfig 返回用户端规则展示所需配置。
func (s *PointsService) PublicConfig(ctx context.Context) PointsPublicConfig {
	st := s.settingService.GetPointsSettings(ctx)
	return PointsPublicConfig{
		Enabled:            st.Enabled,
		Peg:                st.Peg,
		WithdrawEnabled:    st.WithdrawEnabled,
		WithdrawMinPoints:  st.WithdrawMinPoints,
		WithdrawFeePercent: st.WithdrawFeePercent,
		RedeemBalanceOn:    st.RedeemBalanceOn,
		RedeemPlanOn:       st.RedeemPlanOn,
	}
}

// EffectiveRateForUser 该用户作为邀请人时的实际返现比例（专属优先，回退全局）。
func (s *PointsService) EffectiveRateForUser(ctx context.Context, userID int64) float64 {
	sum, err := s.affiliateService.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return s.settingService.GetPointsCashbackRate(ctx)
	}
	return s.resolveRate(ctx, sum)
}

// PointsPlanOption 可用积分兑换的订阅 D/T 组合（含积分价）。
type PointsPlanOption struct {
	ValidityDays   int     `json:"validity_days"`
	DailyAmountUSD float64 `json:"daily_amount_usd"`
	UnitPrice      float64 `json:"unit_price"`
	Price          float64 `json:"price"`
	PointsPrice    int64   `json:"points_price"`
	WeeklyCapUSD   float64 `json:"weekly_cap_usd"`
	MonthlyCapUSD  float64 `json:"monthly_cap_usd"`
}

// ListRedeemablePlans 列出可用积分兑换的订阅 D/T 组合及其积分价。
func (s *PointsService) ListRedeemablePlans(ctx context.Context) ([]PointsPlanOption, error) {
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	bounds := s.subscriptionSvc.PricingBounds(ctx)
	peg := s.peg(ctx)
	dMin := math.Ceil(bounds.DMin/subscriptionDailyAmountStep) * subscriptionDailyAmountStep
	dMax := math.Floor(bounds.DMax/subscriptionDailyAmountStep) * subscriptionDailyAmountStep
	if dMax < dMin {
		dMax = dMin
	}
	validities := []int{30, 90, 180, 360}
	out := make([]PointsPlanOption, 0, int((dMax-dMin)/subscriptionDailyAmountStep+1)*len(validities))
	for d := dMin; d <= dMax+1e-9; d += subscriptionDailyAmountStep {
		for _, t := range validities {
			if t < bounds.TMin || t > bounds.TMax || (bounds.TStep > 0 && t%bounds.TStep != 0) {
				continue
			}
			quote, qErr := s.subscriptionSvc.QuoteSubscription(ctx, d, t)
			if qErr != nil {
				continue
			}
			out = append(out, PointsPlanOption{
				ValidityDays:   quote.ValidityDays,
				DailyAmountUSD: quote.DailyAmountUSD,
				UnitPrice:      quote.UnitPrice,
				Price:          quote.Price,
				PointsPrice:    ComputePlanPoints(quote.Price, peg),
				WeeklyCapUSD:   quote.WeeklyCapUSD,
				MonthlyCapUSD:  quote.MonthlyCapUSD,
			})
		}
	}
	return out, nil
}

// --- SettingService 上的 points 设置读写（同 service 包，访问 settingRepo） ---

type PointsSettings struct {
	Enabled             bool    `json:"enabled"`
	Peg                 float64 `json:"peg"`
	CashbackRatePercent float64 `json:"cashback_rate_percent"`
	FreezeHours         int     `json:"freeze_hours"`
	WithdrawEnabled     bool    `json:"withdraw_enabled"`
	WithdrawMinPoints   int64   `json:"withdraw_min_points"`
	WithdrawFeePercent  float64 `json:"withdraw_fee_percent"`
	RedeemBalanceOn     bool    `json:"redeem_balance_on"`
	RedeemPlanOn        bool    `json:"redeem_plan_on"`
}

type PointsSettingsInput struct {
	Enabled             bool    `json:"enabled"`
	Peg                 float64 `json:"peg"`
	CashbackRatePercent float64 `json:"cashback_rate_percent"`
	FreezeHours         int     `json:"freeze_hours"`
	WithdrawEnabled     bool    `json:"withdraw_enabled"`
	WithdrawMinPoints   int64   `json:"withdraw_min_points"`
	WithdrawFeePercent  float64 `json:"withdraw_fee_percent"`
	RedeemBalanceOn     bool    `json:"redeem_balance_on"`
	RedeemPlanOn        bool    `json:"redeem_plan_on"`
}

func (s *SettingService) IsPointsEnabled(ctx context.Context) bool {
	return s.boolSettingOr(ctx, SettingKeyPointsEnabled, PointsEnabledDefault)
}

func (s *SettingService) GetPointsPeg(ctx context.Context) float64 {
	v := s.floatSettingOr(ctx, SettingKeyPointsPeg, PointsPegDefault)
	if v < PointsPegMin {
		return PointsPegDefault
	}
	return v
}

func (s *SettingService) GetPointsCashbackRate(ctx context.Context) float64 {
	v := s.floatSettingOr(ctx, SettingKeyPointsCashbackRate, PointsCashbackRateDefault)
	return clampAffiliateRebateRate(v)
}

func (s *SettingService) GetPointsFreezeHours(ctx context.Context) int {
	v := s.intSettingOr(ctx, SettingKeyPointsFreezeHours, PointsFreezeHoursDefault)
	if v < 0 {
		return 0
	}
	if v > PointsFreezeHoursMax {
		return PointsFreezeHoursMax
	}
	return v
}

func (s *SettingService) IsPointsWithdrawOn(ctx context.Context) bool {
	return s.boolSettingOr(ctx, SettingKeyPointsWithdrawEnabled, PointsWithdrawEnabledDefault)
}

func (s *SettingService) GetPointsWithdrawMin(ctx context.Context) int64 {
	v := s.intSettingOr(ctx, SettingKeyPointsWithdrawMin, PointsWithdrawMinDefault)
	if v < 0 {
		return 0
	}
	return int64(v)
}

func (s *SettingService) GetPointsWithdrawFeePercent(ctx context.Context) float64 {
	v := s.floatSettingOr(ctx, SettingKeyPointsWithdrawFeePercent, PointsWithdrawFeePercentDefault)
	if v < 0 {
		return 0
	}
	if v > PointsWithdrawFeePercentMax {
		return PointsWithdrawFeePercentMax
	}
	return v
}

func (s *SettingService) IsPointsRedeemBalanceOn(ctx context.Context) bool {
	return s.boolSettingOr(ctx, SettingKeyPointsRedeemBalanceOn, PointsRedeemBalanceOnDefault)
}

func (s *SettingService) IsPointsRedeemPlanOn(ctx context.Context) bool {
	return s.boolSettingOr(ctx, SettingKeyPointsRedeemPlanOn, PointsRedeemPlanOnDefault)
}

func (s *SettingService) GetPointsSettings(ctx context.Context) *PointsSettings {
	return &PointsSettings{
		Enabled:             s.IsPointsEnabled(ctx),
		Peg:                 s.GetPointsPeg(ctx),
		CashbackRatePercent: s.GetPointsCashbackRate(ctx),
		FreezeHours:         s.GetPointsFreezeHours(ctx),
		WithdrawEnabled:     s.IsPointsWithdrawOn(ctx),
		WithdrawMinPoints:   s.GetPointsWithdrawMin(ctx),
		WithdrawFeePercent:  s.GetPointsWithdrawFeePercent(ctx),
		RedeemBalanceOn:     s.IsPointsRedeemBalanceOn(ctx),
		RedeemPlanOn:        s.IsPointsRedeemPlanOn(ctx),
	}
}

func (s *SettingService) UpdatePointsSettings(ctx context.Context, in PointsSettingsInput) (*PointsSettings, error) {
	peg := in.Peg
	if peg < PointsPegMin {
		peg = PointsPegDefault
	}
	rate := clampAffiliateRebateRate(in.CashbackRatePercent)
	freeze := in.FreezeHours
	if freeze < 0 {
		freeze = 0
	}
	if freeze > PointsFreezeHoursMax {
		freeze = PointsFreezeHoursMax
	}
	withdrawMin := in.WithdrawMinPoints
	if withdrawMin < 0 {
		withdrawMin = 0
	}
	fee := in.WithdrawFeePercent
	if fee < 0 {
		fee = 0
	}
	if fee > PointsWithdrawFeePercentMax {
		fee = PointsWithdrawFeePercentMax
	}
	values := map[string]string{
		SettingKeyPointsEnabled:            strconv.FormatBool(in.Enabled),
		SettingKeyPointsPeg:                strconv.FormatFloat(peg, 'f', -1, 64),
		SettingKeyPointsCashbackRate:       strconv.FormatFloat(rate, 'f', -1, 64),
		SettingKeyPointsFreezeHours:        strconv.Itoa(freeze),
		SettingKeyPointsWithdrawEnabled:    strconv.FormatBool(in.WithdrawEnabled),
		SettingKeyPointsWithdrawMin:        strconv.FormatInt(withdrawMin, 10),
		SettingKeyPointsWithdrawFeePercent: strconv.FormatFloat(fee, 'f', -1, 64),
		SettingKeyPointsRedeemBalanceOn:    strconv.FormatBool(in.RedeemBalanceOn),
		SettingKeyPointsRedeemPlanOn:       strconv.FormatBool(in.RedeemPlanOn),
	}
	if err := s.settingRepo.SetMultiple(ctx, values); err != nil {
		return nil, err
	}
	return s.GetPointsSettings(ctx), nil
}

// --- 通用设置解析小助手（带默认值） ---

func (s *SettingService) boolSettingOr(ctx context.Context, key string, def bool) bool {
	raw, err := s.settingRepo.GetValue(ctx, key)
	if err != nil || strings.TrimSpace(raw) == "" {
		return def
	}
	return strings.TrimSpace(raw) == "true"
}

func (s *SettingService) floatSettingOr(ctx context.Context, key string, def float64) float64 {
	raw, err := s.settingRepo.GetValue(ctx, key)
	if err != nil {
		return def
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

func (s *SettingService) intSettingOr(ctx context.Context, key string, def int) int {
	raw, err := s.settingRepo.GetValue(ctx, key)
	if err != nil {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}
