package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// --- Order Creation ---

func (s *PaymentService) CreateOrder(ctx context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	if req.OrderType == "" {
		req.OrderType = payment.OrderTypeBalance
	}
	if normalized := NormalizeVisibleMethod(req.PaymentType); normalized != "" {
		req.PaymentType = normalized
	}
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("get payment config: %w", err)
	}
	if !cfg.Enabled {
		return nil, infraerrors.Forbidden("PAYMENT_DISABLED", "payment system is disabled")
	}
	spec, err := s.validateOrderInput(ctx, req, cfg)
	if err != nil {
		return nil, err
	}
	// plan 仅供下游展示/计费用途（支付主题、provider 调用）；自定义订阅单 spec.plan 为 nil。
	var plan *dbent.SubscriptionPlan
	if spec != nil {
		plan = spec.plan
	}
	if err := s.checkCancelRateLimit(ctx, req.UserID, cfg); err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user.Status != payment.EntityStatusActive {
		return nil, infraerrors.Forbidden("USER_INACTIVE", "user account is disabled")
	}
	if s.notificationEmailService != nil {
		s.notificationEmailService.RememberRecipientLocale(ctx, req.UserID, user.Email, req.Locale)
	}
	if spec != nil && spec.intent != SubscriptionIntentRenew && spec.intent != SubscriptionIntentChangePlan {
		// 仅购买（intent 空/"purchase"）需要「无生效卡 + 无 pending 订阅单」准入（会惰性杀掉假 active 卡）。
		// renew/change_plan 反而要求当前有生效卡（已在报价时校验），绝不能杀卡——故跳过此闸，
		// pending 订阅单的串行化由 createOrderInTx 内的 hasPendingSubscriptionOrder 兜底。
		if err := s.ensureCanCreateSubscriptionOrder(ctx, req.UserID); err != nil {
			return nil, err
		}
	}
	orderAmount := req.Amount
	limitAmount := req.Amount
	if spec != nil {
		// 订阅单（购买/续费/转套餐）实收以后端权威 spec.chargeAmount 为准（购买/续费=全价；转套餐=补差价），
		// 绝不信任前端 req.Amount；order.Amount 记录 USD 口径的套餐价值。
		orderAmount = spec.chargeAmount
	} else if req.OrderType == payment.OrderTypeBalance {
		orderAmount = calculateCreditedBalance(req.Amount, cfg.BalanceRechargeMultiplier)
	}
	feeRate := cfg.RechargeFeeRate
	methodCurrency := payment.DefaultPaymentCurrency
	if s.configService != nil {
		methodCurrency, err = s.configService.ValidateMethodCurrencyConsistency(ctx, req.PaymentType)
		if err != nil {
			return nil, err
		}
	}
	if spec != nil {
		// 订阅单实际向法币网关收取的是「套餐价值 / 订阅付款倍率」；该倍率与余额充值到账倍率分离。
		limitAmount = calculateGatewayPaymentAmountForCreditedValue(spec.chargeAmount, cfg.SubscriptionPayMultiplier, methodCurrency)
		if cfg.MinAmount > 0 && limitAmount > 0 && limitAmount < cfg.MinAmount {
			return nil, infraerrors.BadRequest("CHARGE_BELOW_MIN_AMOUNT", "charge amount is below the minimum payable amount").
				WithMetadata(map[string]string{"charge": fmt.Sprintf("%.2f", limitAmount), "min": fmt.Sprintf("%.2f", cfg.MinAmount)})
		}
	}
	payAmountStr, payAmount, err := calculateCreateOrderPayAmount(limitAmount, feeRate, methodCurrency)
	if err != nil {
		return nil, err
	}
	sel, err := s.selectCreateOrderInstance(ctx, req, cfg, payAmount)
	if err != nil {
		return nil, err
	}
	if err := s.validateSelectedCreateOrderInstance(ctx, req, sel); err != nil {
		return nil, err
	}
	selectedCurrency := payment.DefaultPaymentCurrency
	if sel != nil {
		selectedCurrency = paymentProviderConfigCurrency(sel.ProviderKey, sel.Config)
	}
	if selectedCurrency != methodCurrency {
		if spec != nil {
			limitAmount = calculateGatewayPaymentAmountForCreditedValue(spec.chargeAmount, cfg.SubscriptionPayMultiplier, selectedCurrency)
		}
		payAmountStr, payAmount, err = calculateCreateOrderPayAmount(limitAmount, feeRate, selectedCurrency)
		if err != nil {
			return nil, err
		}
	}
	if err := validateSelectedCreateOrderAmountCurrency(payAmountStr, sel); err != nil {
		return nil, err
	}
	oauthResp, err := s.maybeBuildWeChatOAuthRequiredResponseForSelection(ctx, req, limitAmount, payAmount, feeRate, sel)
	if err != nil {
		return nil, err
	}
	if oauthResp != nil {
		return oauthResp, nil
	}
	order, err := s.createOrderInTx(ctx, req, user, spec, cfg, orderAmount, limitAmount, feeRate, payAmount, sel)
	if err != nil {
		return nil, err
	}
	resp, err := s.invokeProvider(ctx, order, req, cfg, limitAmount, payAmountStr, payAmount, plan, sel)
	if err != nil {
		_, _ = s.entClient.PaymentOrder.UpdateOneID(order.ID).
			SetStatus(OrderStatusFailed).
			Save(ctx)
		return nil, err
	}
	return resp, nil
}

func (s *PaymentService) validateOrderInput(ctx context.Context, req CreateOrderRequest, cfg *PaymentConfig) (*subscriptionOrderSpec, error) {
	if req.OrderType == payment.OrderTypeBalance && cfg.BalanceDisabled {
		return nil, infraerrors.Forbidden("BALANCE_PAYMENT_DISABLED", "balance recharge has been disabled")
	}
	if req.OrderType == payment.OrderTypeSubscription {
		spec, err := s.validateSubOrder(ctx, req)
		if err != nil {
			return nil, err
		}
		return spec, nil
	}
	if math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || req.Amount <= 0 {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "amount must be a positive number")
	}
	if (cfg.MinAmount > 0 && req.Amount < cfg.MinAmount) || (cfg.MaxAmount > 0 && req.Amount > cfg.MaxAmount) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "amount out of range").
			WithMetadata(map[string]string{"min": fmt.Sprintf("%.2f", cfg.MinAmount), "max": fmt.Sprintf("%.2f", cfg.MaxAmount)})
	}
	return nil, nil
}

// subscriptionOrderSpec 是订阅订单（固定套餐 / 自定义 D+T）在下单期解析出的**权威**参数。
// 价格/额度/有效期/group 一律以此为准（绝不信任前端 req.Amount），并冻结进订单快照，履约严格按此发卡。
type subscriptionOrderSpec struct {
	plan         *dbent.SubscriptionPlan // 固定套餐单持有其套餐；自定义单为 nil
	dailyAmount  float64                 // D（每日额度 / 日限额）；renew 取自目标卡，change_plan 为新档 D
	validityDays int                     // T（有效天数）
	groupID      int64                   // 来源 group；购买/续费/转套餐最终发卡归属
	unitPrice    float64                 // u(D)
	price        float64                 // 该卡的权威全价 P（plan.Price 或 quote.Price；change_plan=新档全价 P_新）
	// 生命周期意图（per-day redesign §5/§7）：
	intent      string // purchase（默认）/ renew / change_plan
	targetSubID int64  // renew/change_plan 的目标当前生效卡 ID（purchase=0）
	// chargeAmount 是 USD 口径的订阅成交价值：purchase/renew = price；change_plan = diff（P_新 − 旧卡剩余价值 V，恒 >0）。
	// 实际网关收款金额会在 CreateOrder 中按 subscription_payment_multiplier 换算为支付币种金额。
	chargeAmount float64
}

func (s *PaymentService) validateSubOrder(ctx context.Context, req CreateOrderRequest) (*subscriptionOrderSpec, error) {
	intent, ok := normalizeSubscriptionIntent(req.SubscriptionIntent)
	if !ok {
		return nil, infraerrors.BadRequest("INVALID_SUBSCRIPTION_INTENT", "unknown subscription intent")
	}
	switch intent {
	case SubscriptionIntentRenew:
		return s.validateSubRenewOrder(ctx, req)
	case SubscriptionIntentChangePlan:
		return s.validateSubChangePlanOrder(ctx, req)
	}
	// intent == purchase（默认）：购买新卡。
	if req.PlanID == 0 {
		// 自定义购买（无固定套餐，规格第 2/3 节）：用 D+T 直接定价，不依赖 plan。
		if req.DailyAmountUSD > 0 && req.ValidityDays > 0 {
			if s.subscriptionSvc == nil {
				return nil, fmt.Errorf("subscription service not configured")
			}
			// 自定义套餐是用户级 active card，全分组通用；group_id 只保留为兼容旧前端/旧恢复 token
			// 的记录字段。新订单不传 group_id 时保持为空，履约会创建无 group 卡。
			groupID := req.GroupID
			if groupID > 0 {
				if s.groupRepo == nil {
					return nil, fmt.Errorf("group repository not configured")
				}
				group, err := s.groupRepo.GetByID(ctx, groupID)
				if err != nil || group == nil || group.Status != payment.EntityStatusActive {
					return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "subscription group is no longer available")
				}
			}
			// 按 u(D) 公式校验 D/T 区间并产出**权威**报价：成交价完全由后端公式决定，
			// 绝不信任前端 req.Amount（否则客户端可篡改价格 → 资损）。越界返回 INVALID_SUBSCRIPTION_PARAMS。
			quote, err := s.subscriptionSvc.QuoteSubscription(ctx, req.DailyAmountUSD, req.ValidityDays)
			if err != nil {
				return nil, err
			}
			return &subscriptionOrderSpec{
				plan:         nil,
				dailyAmount:  quote.DailyAmountUSD,
				validityDays: quote.ValidityDays,
				groupID:      groupID,
				unitPrice:    quote.UnitPrice,
				price:        quote.Price,
				intent:       SubscriptionIntentPurchase,
				chargeAmount: quote.Price,
			}, nil
		}
		return nil, infraerrors.BadRequest("INVALID_INPUT", "subscription order requires a plan")
	}
	plan, err := s.configService.GetPlan(ctx, req.PlanID)
	if err != nil || !plan.ForSale {
		return nil, infraerrors.NotFound("PLAN_NOT_AVAILABLE", "plan not found or not for sale")
	}
	// per-day：下单期 fail-fast——套餐每日额度 D 必须为正。否则下单冻结的快照 D=0 会在履约时被判
	// 坏快照而失败（已付款却无法履约、无自动退款）。存量套餐（迁移 155 从无 daily_limit 的 group
	// 回填）可能 D=0 且仍在售，故必须在收款前拦截，而非等到履约。
	if plan.DailyAmountUsd <= 0 {
		return nil, infraerrors.BadRequest("PLAN_DAILY_AMOUNT_INVALID", "plan daily amount is invalid")
	}
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	// per-day：套餐与 group 解耦，不再校验「订阅型分组」。但套餐卡仍带 group_id（历史快照/路由），
	// 故校验所挂 group 存在且 active（保证下单冻结的 subscription_group_id 可用）。
	group, err := s.groupRepo.GetByID(ctx, plan.GroupID)
	if err != nil || group == nil || group.Status != payment.EntityStatusActive {
		return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "subscription group is no longer available")
	}
	return &subscriptionOrderSpec{
		plan:         plan,
		dailyAmount:  plan.DailyAmountUsd,
		validityDays: psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit),
		groupID:      plan.GroupID,
		unitPrice:    s.subscriptionSvc.subscriptionPricingConfig(ctx).UnitPrice(plan.DailyAmountUsd),
		price:        plan.Price,
		intent:       SubscriptionIntentPurchase,
		chargeAmount: plan.Price,
	}, nil
}

// validateSubRenewOrder 续费下单的权威解析：D 取自当前生效卡、续 T'(整月)天，价 cfg.Price(D,T')，
// 实收=全价(>0)。目标卡由后端按用户唯一生效卡派生（不信前端）。无生效卡 → ErrNoActiveSubscription（应购买）。
func (s *PaymentService) validateSubRenewOrder(ctx context.Context, req CreateOrderRequest) (*subscriptionOrderSpec, error) {
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	q, err := s.subscriptionSvc.QuoteRenewOrder(ctx, req.UserID, req.ValidityDays)
	if err != nil {
		return nil, err
	}
	return &subscriptionOrderSpec{
		plan:         nil,
		dailyAmount:  q.DailyAmountUSD,
		validityDays: q.AddedDays,
		groupID:      q.GroupID, // 沿用当前卡 group（仅订单记录用；履约只延长 expires_at、不建卡）
		unitPrice:    q.UnitPrice,
		price:        q.Price,
		intent:       SubscriptionIntentRenew,
		targetSubID:  q.SubscriptionID,
		chargeAmount: q.Price,
	}, nil
}

// validateSubChangePlanOrder 转套餐下单的权威解析：新档 D+T 走 QuoteSubscription，差价 diff = P_新 − 旧卡剩余价值 V。
// diff<0(降档赔钱) → 拒（ErrChangePlanDowngradeNotAllowed）；diff==0 不该走网关（由 endpoint 同步换卡）→ 防御性拒。
// diff>0 → 实收=diff，新卡参数(D/T,履约时派生 W/M)冻结进快照。
func (s *PaymentService) validateSubChangePlanOrder(ctx context.Context, req CreateOrderRequest) (*subscriptionOrderSpec, error) {
	if s.subscriptionSvc == nil {
		return nil, fmt.Errorf("subscription service not configured")
	}
	q, err := s.subscriptionSvc.QuoteChangePlanOrder(ctx, req.UserID, req.DailyAmountUSD, req.ValidityDays)
	if err != nil {
		return nil, err
	}
	if q.Diff < 0 {
		return nil, ErrChangePlanDowngradeNotAllowed
	}
	if q.Diff == 0 {
		// 持平：无差价可收，网关无法收 $0；应由 endpoint 走同步换卡。到这里属调用方未分流，防御性拒。
		return nil, infraerrors.BadRequest("CHANGE_PLAN_NO_PAYMENT_REQUIRED", "no payment difference; change plan should be applied synchronously")
	}
	return &subscriptionOrderSpec{
		plan:         nil,
		dailyAmount:  q.DailyAmountUSD,
		validityDays: q.ValidityDays,
		groupID:      q.GroupID, // 沿用当前卡 group
		unitPrice:    q.UnitPrice,
		price:        q.NewPlanPrice, // 新卡全价 P_新（快照/审计）
		intent:       SubscriptionIntentChangePlan,
		targetSubID:  q.OldSubscriptionID,
		chargeAmount: q.Diff, // 实收补差价
	}, nil
}

func (s *PaymentService) ensureCanCreateSubscriptionOrder(ctx context.Context, userID int64) error {
	if s.subscriptionSvc == nil {
		return fmt.Errorf("subscription service not configured")
	}
	if s.entClient == nil {
		return fmt.Errorf("ent client not configured")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin subscription admission transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.subscriptionSvc.enforceSingleActiveSubscription(txCtx, userID, TodayEastDayNumber()); err != nil {
		return err
	}
	pending, err := s.hasPendingSubscriptionOrder(ctx, tx, userID)
	if err != nil {
		return err
	}
	if pending {
		return errPendingSubscriptionOrder()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subscription admission transaction: %w", err)
	}
	return nil
}

// errPendingSubscriptionOrder 是用户已有未完成订阅订单时的拒绝错误（单卡模式不允许并存第二张 pending 订阅单）。
func errPendingSubscriptionOrder() error {
	return infraerrors.Conflict("PENDING_SUBSCRIPTION_ORDER", "you already have a pending subscription order; complete or cancel it first")
}

// hasPendingSubscriptionOrder 判断用户是否已有 pending 订阅订单。per-day 单卡模式下卡在【履约时】
// 才创建，故"无 active 卡"不足以放行第二单——必须在同一用户锁内拒绝第二张 pending 订阅单，否则
// 并发/连点双单都过准入、都付款，第二单履约会撞 ErrActiveSubscriptionExists 而 markFailed
// （已付款却无法开卡、无自动退款）。本查询须与 enforceSingleActiveSubscription 处于同一事务内
// （后者已 FOR UPDATE 锁住 user 行），以串行化同一用户的并发下单。
func (s *PaymentService) hasPendingSubscriptionOrder(ctx context.Context, tx *dbent.Tx, userID int64) (bool, error) {
	exist, err := tx.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.StatusEQ(OrderStatusPending),
			paymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
		).Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("check pending subscription order: %w", err)
	}
	return exist, nil
}

func (s *PaymentService) createOrderInTx(ctx context.Context, req CreateOrderRequest, user *User, spec *subscriptionOrderSpec, cfg *PaymentConfig, orderAmount, limitAmount, feeRate, payAmount float64, sel *payment.InstanceSelection) (*dbent.PaymentOrder, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if spec != nil {
		if s.subscriptionSvc == nil {
			return nil, fmt.Errorf("subscription service not configured")
		}
		// 仅购买（intent 空/"purchase"）在收款前杀掉假 active 卡 + 拒「已有生效卡」（单卡模式）；
		// renew/change_plan 必须保留当前生效卡,只 FOR UPDATE 锁 user 行 + 清假 active(不报已有卡),
		// 与购买共用同一把用户锁串行化下单。
		if spec.intent == SubscriptionIntentRenew || spec.intent == SubscriptionIntentChangePlan {
			if err := s.subscriptionSvc.lockUserAndPruneStaleForLifecycle(txCtx, req.UserID, TodayEastDayNumber()); err != nil {
				return nil, err
			}
		} else {
			if err := s.subscriptionSvc.enforceSingleActiveSubscription(txCtx, req.UserID, TodayEastDayNumber()); err != nil {
				return nil, err
			}
		}
		// 权威拦截（user 行已 FOR UPDATE 锁）：禁止并存第二张 pending 订阅单，杜绝并发双单都付款、
		// 第二单履约撞冲突→已付款无法履约。购买/续费/转套餐共用此串行化。
		pending, err := s.hasPendingSubscriptionOrder(ctx, tx, req.UserID)
		if err != nil {
			return nil, err
		}
		if pending {
			return nil, errPendingSubscriptionOrder()
		}
	}
	if err := s.checkPendingLimit(ctx, tx, req.UserID, cfg.MaxPendingOrders); err != nil {
		return nil, err
	}
	if err := s.checkDailyLimit(ctx, tx, req.UserID, limitAmount, cfg.DailyLimit); err != nil {
		return nil, err
	}
	tm := cfg.OrderTimeoutMin
	if tm <= 0 {
		tm = defaultOrderTimeoutMin
	}
	exp := time.Now().Add(time.Duration(tm) * time.Minute)
	outTradeNo, err := s.allocateOutTradeNo(ctx, tx)
	if err != nil {
		return nil, err
	}
	providerSnapshot := buildPaymentOrderProviderSnapshot(sel, req)
	// 订阅订单（套餐 / 自定义）：把权威定价 D/T/u/price/formula_version 冻结进快照，回调严格按此发卡、不重算。
	if spec != nil {
		if providerSnapshot == nil {
			providerSnapshot = map[string]any{"schema_version": 2}
		}
		providerSnapshot[subscriptionSnapshotKey] = buildSubscriptionOrderSnapshot(spec, providerSnapshot)
	}
	selectedInstanceID := ""
	selectedProviderKey := ""
	if sel != nil {
		selectedInstanceID = strings.TrimSpace(sel.InstanceID)
		selectedProviderKey = strings.TrimSpace(sel.ProviderKey)
	}
	b := tx.PaymentOrder.Create().
		SetUserID(req.UserID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetNillableUserNotes(psNilIfEmpty(user.Notes)).
		SetAmount(orderAmount).
		SetPayAmount(payAmount).
		SetFeeRate(feeRate).
		SetRechargeCode("").
		SetOutTradeNo(outTradeNo).
		SetPaymentType(req.PaymentType).
		SetPaymentTradeNo("").
		SetOrderType(req.OrderType).
		SetStatus(OrderStatusPending).
		SetExpiresAt(exp).
		SetClientIP(req.ClientIP).
		SetSrcHost(req.SrcHost)
	if req.SrcURL != "" {
		b.SetSrcURL(req.SrcURL)
	}
	if selectedInstanceID != "" {
		b.SetProviderInstanceID(selectedInstanceID)
	}
	if selectedProviderKey != "" {
		b.SetProviderKey(selectedProviderKey)
	}
	if providerSnapshot != nil {
		b.SetProviderSnapshot(providerSnapshot)
	}
	if spec != nil {
		// 有效天数对套餐/自定义单均必设（履约据此 + 快照发卡）；group_id 记录本次发卡归属。
		b.SetSubscriptionDays(spec.validityDays)
		if spec.plan != nil {
			b.SetPlanID(spec.plan.ID)
		}
		if spec.groupID > 0 {
			b.SetSubscriptionGroupID(spec.groupID)
		}
	}
	order, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	code := fmt.Sprintf("PAY-%d-%d", order.ID, time.Now().UnixNano()%100000)
	order, err = tx.PaymentOrder.UpdateOneID(order.ID).SetRechargeCode(code).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("set recharge code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit order transaction: %w", err)
	}
	return order, nil
}

func (s *PaymentService) allocateOutTradeNo(ctx context.Context, tx *dbent.Tx) (string, error) {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidate := generateOutTradeNo()
		exists, err := tx.PaymentOrder.Query().Where(paymentorder.OutTradeNo(candidate)).Exist(ctx)
		if err != nil {
			return "", fmt.Errorf("check out_trade_no uniqueness: %w", err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("generate unique out_trade_no: exhausted %d attempts", maxAttempts)
}

func (s *PaymentService) checkPendingLimit(ctx context.Context, tx *dbent.Tx, userID int64, max int) error {
	if max <= 0 {
		max = defaultMaxPendingOrders
	}
	c, err := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID), paymentorder.StatusEQ(OrderStatusPending)).Count(ctx)
	if err != nil {
		return fmt.Errorf("count pending orders: %w", err)
	}
	if c >= max {
		return infraerrors.TooManyRequests("TOO_MANY_PENDING", "too_many_pending").
			WithMetadata(map[string]string{"max": strconv.Itoa(max)})
	}
	return nil
}

func buildPaymentOrderProviderSnapshot(sel *payment.InstanceSelection, req CreateOrderRequest) map[string]any {
	if sel == nil {
		return nil
	}

	snapshot := map[string]any{}
	snapshot["schema_version"] = 2

	instanceID := strings.TrimSpace(sel.InstanceID)
	if instanceID != "" {
		snapshot["provider_instance_id"] = instanceID
	}

	providerKey := strings.TrimSpace(sel.ProviderKey)
	if providerKey != "" {
		snapshot["provider_key"] = providerKey
	}

	paymentMode := strings.TrimSpace(sel.PaymentMode)
	if paymentMode != "" {
		snapshot["payment_mode"] = paymentMode
	}

	if providerKey == payment.TypeWxpay {
		if merchantAppID := paymentOrderSnapshotWxpayAppID(sel, req); merchantAppID != "" {
			snapshot["merchant_app_id"] = merchantAppID
		}
		if merchantID := strings.TrimSpace(sel.Config["mchId"]); merchantID != "" {
			snapshot["merchant_id"] = merchantID
		}
		snapshot["currency"] = payment.DefaultPaymentCurrency
	}
	if providerKey == payment.TypeAlipay {
		if merchantAppID := strings.TrimSpace(sel.Config["appId"]); merchantAppID != "" {
			snapshot["merchant_app_id"] = merchantAppID
		}
	}
	if providerKey == payment.TypeEasyPay {
		if merchantID := strings.TrimSpace(sel.Config["pid"]); merchantID != "" {
			snapshot["merchant_id"] = merchantID
		}
	}
	if providerKey == payment.TypeStripe {
		snapshot["currency"] = paymentProviderConfigCurrency(providerKey, sel.Config)
	}
	if providerKey == payment.TypeAirwallex {
		if accountID := strings.TrimSpace(sel.Config["accountId"]); accountID != "" {
			snapshot["merchant_id"] = accountID
		}
		snapshot["currency"] = paymentProviderConfigCurrency(providerKey, sel.Config)
	}

	if len(snapshot) == 1 {
		return nil
	}
	return snapshot
}

// subscriptionSnapshotKey 是 provider_snapshot 里冻结订阅定价快照的子键。
const subscriptionSnapshotKey = "subscription"

// 订阅订单意图（intent）：决定支付回调履约时对订阅卡做什么。
// purchase=建新卡（默认/老订单兼容）；renew=延长目标卡有效期；change_plan=关旧卡开新卡。
// 续费/转套餐与购买一样走法币支付网关（异步 下单→支付→履约），不扣钱包余额（见 docs/billing-perday-redesign.md §5/§7）。
const (
	SubscriptionIntentPurchase   = "purchase"
	SubscriptionIntentRenew      = "renew"
	SubscriptionIntentChangePlan = "change_plan"
)

// normalizeSubscriptionIntent 把入参意图归一化（空 → purchase）；非法值返回 ("", false)。
func normalizeSubscriptionIntent(raw string) (string, bool) {
	switch strings.TrimSpace(raw) {
	case "", SubscriptionIntentPurchase:
		return SubscriptionIntentPurchase, true
	case SubscriptionIntentRenew:
		return SubscriptionIntentRenew, true
	case SubscriptionIntentChangePlan:
		return SubscriptionIntentChangePlan, true
	default:
		return "", false
	}
}

// readSubscriptionIntent 从订单冻结快照读出意图与目标卡 ID。
// 无快照 / 无 intent 字段 → ("purchase", 0)（老订单兼容,默认购买建新卡）。
func readSubscriptionIntent(order *dbent.PaymentOrder) (intent string, targetSubID int64) {
	intent = SubscriptionIntentPurchase
	if order == nil || order.ProviderSnapshot == nil {
		return intent, 0
	}
	raw, exists := order.ProviderSnapshot[subscriptionSnapshotKey]
	if !exists {
		return intent, 0
	}
	sub, ok := raw.(map[string]any)
	if !ok {
		return intent, 0
	}
	if i, ok := sub["intent"].(string); ok {
		if norm, valid := normalizeSubscriptionIntent(i); valid {
			intent = norm
		}
	}
	targetSubID, _ = snapshotInt64(sub["target_subscription_id"])
	return intent, targetSubID
}

// PaymentOrderSubscriptionIntent 返回订单冻结的订阅动作，供 API 展示层使用。
func PaymentOrderSubscriptionIntent(order *dbent.PaymentOrder) string {
	if order == nil || order.OrderType != payment.OrderTypeSubscription {
		return ""
	}
	intent, _ := readSubscriptionIntent(order)
	return intent
}

// PaymentOrderProductName 返回面向站内订单记录的业务商品名；不依赖支付上游的 name/subject。
func PaymentOrderProductName(order *dbent.PaymentOrder) string {
	if order == nil {
		return ""
	}
	switch order.OrderType {
	case payment.OrderTypeSubscription:
		return paymentOrderSubscriptionProductName(order)
	case payment.OrderTypeBalance:
		return fmt.Sprintf("余额充值 $%s", formatPaymentProductUSD(order.Amount))
	default:
		return strings.TrimSpace(order.OrderType)
	}
}

func paymentOrderSubscriptionProductName(order *dbent.PaymentOrder) string {
	intent := PaymentOrderSubscriptionIntent(order)
	label := subscriptionIntentProductLabel(intent)
	dailyAmount, validityDays, present, err := readSubscriptionSnapshotDT(order)
	if err == nil && present {
		return subscriptionProductName(label, dailyAmount, validityDays)
	}
	if order.SubscriptionDays != nil && *order.SubscriptionDays > 0 {
		return fmt.Sprintf("%s %d天", label, *order.SubscriptionDays)
	}
	if order.PlanID != nil && *order.PlanID > 0 {
		return fmt.Sprintf("%s #%d", label, *order.PlanID)
	}
	return label
}

func subscriptionIntentProductLabel(intent string) string {
	switch intent {
	case SubscriptionIntentRenew:
		return "续费套餐"
	case SubscriptionIntentChangePlan:
		return "转套餐"
	default:
		return "购买套餐"
	}
}

func subscriptionProductName(label string, dailyAmount float64, validityDays int) string {
	if dailyAmount > 0 && validityDays > 0 {
		return fmt.Sprintf("%s 每日$%s / %d天", label, formatPaymentProductUSD(dailyAmount), validityDays)
	}
	if validityDays > 0 {
		return fmt.Sprintf("%s %d天", label, validityDays)
	}
	return label
}

func formatPaymentProductUSD(v float64) string {
	rounded := math.Round(v*100) / 100
	if math.Abs(rounded-math.Round(rounded)) < 1e-9 {
		return strconv.FormatInt(int64(math.Round(rounded)), 10)
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(rounded, 'f', 2, 64), "0"), ".")
}

// buildSubscriptionOrderSnapshot 在下单时把订阅定价**冻结**进订单快照（D/T/u/price/formula_version/
// currency）。支付回调严格按此快照发卡，绝不按回调时的当前公式/配置重算（防下单后管理员改价/范围
// 导致发出与用户当时所付不一致的卡）。currency 复用基础快照里的值（缺省站点本币）。
func buildSubscriptionOrderSnapshot(spec *subscriptionOrderSpec, base map[string]any) map[string]any {
	currency := payment.DefaultPaymentCurrency
	if base != nil {
		if c, ok := base["currency"].(string); ok && strings.TrimSpace(c) != "" {
			currency = c
		}
	}
	intent := spec.intent
	if intent == "" {
		intent = SubscriptionIntentPurchase
	}
	// W/M 与 D/T/u/price 一并冻结进快照（spec §2：订单必须存 D/W/M/T/u/price/formula_version/currency）；
	// 发卡读快照 W/M、不按履约时派生系数重算。派生系数当前为常量，冻结保证日后调系数不影响存量订单。
	weeklyLimit, monthlyLimit := DeriveWindowCaps(spec.dailyAmount, spec.validityDays)
	snap := map[string]any{
		"daily_amount_usd":  spec.dailyAmount,
		"weekly_limit_usd":  weeklyLimit,
		"monthly_limit_usd": monthlyLimit,
		"validity_days":     spec.validityDays,
		"unit_price":        spec.unitPrice,
		"price":             spec.price,
		"formula_version":   SubscriptionFormulaVersion,
		"currency":          currency,
		"intent":            intent,
	}
	// renew/change_plan：冻结目标卡 ID + 实收金额（diff），履约据此延长/换卡。
	if spec.targetSubID > 0 {
		snap["target_subscription_id"] = spec.targetSubID
	}
	if intent != SubscriptionIntentPurchase {
		snap["charge_amount"] = spec.chargeAmount
	}
	return snap
}

// readSubscriptionSnapshotDT 从订单冻结快照读出每日额度 D 与有效天数 T（JSON 数值回读为 float64）。
// present=false 表示无该快照（老订单）——调用方回退到 subscription_days + group 兼容路径；
// present=true 且 err!=nil 表示新订单快照损坏，必须失败，不能按回调时 group 配置重算。
func readSubscriptionSnapshotDT(order *dbent.PaymentOrder) (dailyAmount float64, validityDays int, present bool, err error) {
	if order == nil || order.ProviderSnapshot == nil {
		return 0, 0, false, nil
	}
	raw, exists := order.ProviderSnapshot[subscriptionSnapshotKey]
	if !exists {
		return 0, 0, false, nil
	}
	sub, ok := raw.(map[string]any)
	if !ok {
		return 0, 0, true, fmt.Errorf("invalid subscription snapshot: expected object")
	}
	d, dOK := snapshotFloat64(sub["daily_amount_usd"])
	tf, tOK := snapshotFloat64(sub["validity_days"])
	if !dOK || !tOK || d <= 0 || tf <= 0 {
		return 0, 0, true, fmt.Errorf("invalid subscription snapshot: invalid D/T")
	}
	days := int(tf)
	if float64(days) != tf {
		return 0, 0, true, fmt.Errorf("invalid subscription snapshot: validity_days must be integer")
	}
	return d, days, true, nil
}

// readSubscriptionSnapshotWM 从订单冻结快照读出周/月封顶 W/M（spec §2 要求与 D/T 一并冻结）。
// ok=false 表示老订单无 W/M 快照（回退到按 D/T 派生）；新订单两者俱在。任一缺失/非法 → ok=false。
func readSubscriptionSnapshotWM(order *dbent.PaymentOrder) (weekly, monthly float64, ok bool) {
	if order == nil || order.ProviderSnapshot == nil {
		return 0, 0, false
	}
	raw, exists := order.ProviderSnapshot[subscriptionSnapshotKey]
	if !exists {
		return 0, 0, false
	}
	sub, isMap := raw.(map[string]any)
	if !isMap {
		return 0, 0, false
	}
	w, wOK := snapshotFloat64(sub["weekly_limit_usd"])
	m, mOK := snapshotFloat64(sub["monthly_limit_usd"])
	if !wOK || !mOK || w < 0 || m < 0 {
		return 0, 0, false
	}
	return w, m, true
}

func readSubscriptionSnapshotSubscriptionID(order *dbent.PaymentOrder) (int64, bool) {
	if order == nil || order.ProviderSnapshot == nil {
		return 0, false
	}
	raw, exists := order.ProviderSnapshot[subscriptionSnapshotKey]
	if !exists {
		return 0, false
	}
	sub, ok := raw.(map[string]any)
	if !ok {
		return 0, false
	}
	return snapshotInt64(sub["subscription_id"])
}

func withSubscriptionIDInSnapshot(snapshot map[string]any, subscriptionID int64) (map[string]any, bool) {
	if subscriptionID <= 0 || snapshot == nil {
		return nil, false
	}
	raw, exists := snapshot[subscriptionSnapshotKey]
	if !exists {
		return nil, false
	}
	sub, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	next := make(map[string]any, len(snapshot))
	for k, v := range snapshot {
		next[k] = v
	}
	nextSub := make(map[string]any, len(sub)+1)
	for k, v := range sub {
		nextSub[k] = v
	}
	nextSub["subscription_id"] = subscriptionID
	next[subscriptionSnapshotKey] = nextSub
	return next, true
}

func (s *PaymentService) writeSubscriptionIDToOrderSnapshot(ctx context.Context, order *dbent.PaymentOrder, subscriptionID int64) error {
	if s == nil || s.entClient == nil || order == nil {
		return nil
	}
	snapshot, ok := withSubscriptionIDInSnapshot(order.ProviderSnapshot, subscriptionID)
	if !ok {
		return nil
	}
	if _, err := s.entClientForCtx(ctx).PaymentOrder.UpdateOneID(order.ID).SetProviderSnapshot(snapshot).Save(ctx); err != nil {
		return err
	}
	order.ProviderSnapshot = snapshot
	return nil
}

func snapshotFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

func snapshotInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, n > 0
	case int:
		return int64(n), n > 0
	case int32:
		return int64(n), n > 0
	case float64:
		i := int64(n)
		return i, n > 0 && float64(i) == n
	case float32:
		i := int64(n)
		return i, n > 0 && float32(i) == n
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil && i > 0
	default:
		return 0, false
	}
}

func paymentOrderSnapshotWxpayAppID(sel *payment.InstanceSelection, req CreateOrderRequest) string {
	if sel == nil || strings.TrimSpace(sel.ProviderKey) != payment.TypeWxpay {
		return ""
	}
	if strings.TrimSpace(req.OpenID) != "" {
		return strings.TrimSpace(provider.ResolveWxpayJSAPIAppID(sel.Config))
	}
	return strings.TrimSpace(sel.Config["appId"])
}

func (s *PaymentService) checkDailyLimit(ctx context.Context, tx *dbent.Tx, userID int64, amount, limit float64) error {
	if limit <= 0 {
		return nil
	}
	ts := psStartOfDayUTC(time.Now())
	orders, err := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID), paymentorder.StatusIn(OrderStatusPaid, OrderStatusRecharging, OrderStatusCompleted), paymentorder.PaidAtGTE(ts)).All(ctx)
	if err != nil {
		return fmt.Errorf("query daily usage: %w", err)
	}
	var used float64
	for _, o := range orders {
		if o.OrderType == payment.OrderTypeBalance {
			used += o.PayAmount
			continue
		}
		used += o.Amount
	}
	if used+amount > limit {
		return infraerrors.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily_limit_exceeded").
			WithMetadata(map[string]string{"remaining": fmt.Sprintf("%.2f", math.Max(0, limit-used))})
	}
	return nil
}

func (s *PaymentService) selectCreateOrderInstance(ctx context.Context, req CreateOrderRequest, cfg *PaymentConfig, payAmount float64) (*payment.InstanceSelection, error) {
	selectCtx, err := s.prepareCreateOrderSelectionContext(ctx, req)
	if err != nil {
		return nil, err
	}
	sel, err := s.loadBalancer.SelectInstance(selectCtx, "", req.PaymentType, payment.Strategy(cfg.LoadBalanceStrategy), payAmount)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_GATEWAY_ERROR", "method_not_configured").
			WithMetadata(map[string]string{"payment_type": req.PaymentType})
	}
	if sel == nil {
		return nil, infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "no_available_instance")
	}
	return sel, nil
}

func (s *PaymentService) prepareCreateOrderSelectionContext(ctx context.Context, req CreateOrderRequest) (context.Context, error) {
	if !requestNeedsWeChatJSAPICompatibility(req) {
		return ctx, nil
	}
	if !s.usesOfficialWxpayVisibleMethod(ctx) {
		return ctx, nil
	}
	expectedAppID, _, err := s.getWeChatPaymentOAuthCredential(ctx)
	if err != nil {
		return nil, err
	}
	return payment.WithWxpayJSAPIAppID(ctx, expectedAppID), nil
}

func requestNeedsWeChatJSAPICompatibility(req CreateOrderRequest) bool {
	if payment.GetBasePaymentType(req.PaymentType) != payment.TypeWxpay {
		return false
	}
	return req.IsWeChatBrowser || strings.TrimSpace(req.OpenID) != ""
}

func (s *PaymentService) usesOfficialWxpayVisibleMethod(ctx context.Context) bool {
	if s == nil || s.configService == nil {
		return false
	}
	inst, err := s.configService.resolveEnabledVisibleMethodInstance(ctx, payment.TypeWxpay)
	if err != nil {
		return false
	}
	if inst == nil {
		return false
	}
	return inst.ProviderKey == payment.TypeWxpay
}

func (s *PaymentService) invokeProvider(ctx context.Context, order *dbent.PaymentOrder, req CreateOrderRequest, cfg *PaymentConfig, limitAmount float64, payAmountStr string, payAmount float64, plan *dbent.SubscriptionPlan, sel *payment.InstanceSelection) (*CreateOrderResponse, error) {
	prov, err := provider.CreateProvider(sel.ProviderKey, sel.InstanceID, sel.Config)
	if err != nil {
		slog.Error("[PaymentService] CreateProvider failed", "provider", sel.ProviderKey, "instance", sel.InstanceID, "error", err)
		// If the provider returned a structured ApplicationError (e.g. WXPAY_CONFIG_MISSING_KEY),
		// pass it through with provider context added to metadata. Otherwise wrap as PAYMENT_PROVIDER_MISCONFIGURED.
		if appErr := new(infraerrors.ApplicationError); errors.As(err, &appErr) {
			md := map[string]string{"provider": sel.ProviderKey, "instance_id": sel.InstanceID}
			for k, v := range appErr.Metadata {
				md[k] = v
			}
			return nil, appErr.WithMetadata(md)
		}
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_MISCONFIGURED", "provider_misconfigured").
			WithMetadata(map[string]string{"provider": sel.ProviderKey, "instance_id": sel.InstanceID})
	}
	subject := s.buildPaymentSubject(plan, order, limitAmount, cfg, sel)
	outTradeNo := order.OutTradeNo
	canonicalReturnURL, err := CanonicalizeReturnURL(req.ReturnURL, req.SrcHost, req.SrcURL)
	if err != nil {
		return nil, err
	}
	resumeToken := ""
	if resume := s.paymentResume(); resume != nil {
		if canonicalReturnURL != "" && resume.isSigningConfigured() {
			resumeToken, err = resume.CreateToken(ResumeTokenClaims{
				OrderID:            order.ID,
				UserID:             order.UserID,
				ProviderInstanceID: sel.InstanceID,
				ProviderKey:        sel.ProviderKey,
				PaymentType:        req.PaymentType,
				CanonicalReturnURL: canonicalReturnURL,
			})
			if err != nil {
				return nil, fmt.Errorf("create payment resume token: %w", err)
			}
		}
	}
	providerReturnURL, err := buildPaymentReturnURL(canonicalReturnURL, order.ID, outTradeNo, resumeToken)
	if err != nil {
		return nil, err
	}
	providerReq := buildProviderCreatePaymentRequest(CreateOrderRequest{
		PaymentType: req.PaymentType,
		OpenID:      req.OpenID,
		ClientIP:    req.ClientIP,
		IsMobile:    req.IsMobile,
		ReturnURL:   providerReturnURL,
	}, sel, outTradeNo, payAmountStr, subject)
	pr, err := prov.CreatePayment(ctx, providerReq)
	if err != nil {
		slog.Error("[PaymentService] CreatePayment failed", "provider", sel.ProviderKey, "instance", sel.InstanceID, "error", err)
		if appErr := new(infraerrors.ApplicationError); errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, classifyCreatePaymentError(req, sel.ProviderKey, err)
	}
	pr = sanitizeCreatePaymentResponseDetails(pr)
	_, err = s.entClient.PaymentOrder.UpdateOneID(order.ID).
		SetNillablePaymentTradeNo(psNilIfEmpty(pr.TradeNo)).
		SetNillablePayURL(psNilIfEmpty(pr.PayURL)).
		SetNillableQrCode(psNilIfEmpty(pr.QRCode)).
		SetNillableProviderInstanceID(psNilIfEmpty(sel.InstanceID)).
		SetNillableProviderKey(psNilIfEmpty(sel.ProviderKey)).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update order with payment details: %w", err)
	}
	s.writeAuditLog(ctx, order.ID, "ORDER_CREATED", fmt.Sprintf("user:%d", req.UserID), map[string]any{
		"paymentAmount":  req.Amount,
		"creditedAmount": order.Amount,
		"payAmount":      order.PayAmount,
		"paymentType":    req.PaymentType,
		"orderType":      req.OrderType,
		"paymentSource":  NormalizePaymentSource(req.PaymentSource),
	})
	resultType := pr.ResultType
	if resultType == "" {
		resultType = payment.CreatePaymentResultOrderCreated
	}
	resp := buildCreateOrderResponse(order, req, payAmount, sel, pr, resultType)
	resp.ResumeToken = resumeToken
	return resp, nil
}

func sanitizeCreatePaymentResponseDetails(resp *payment.CreatePaymentResponse) *payment.CreatePaymentResponse {
	if resp == nil {
		return &payment.CreatePaymentResponse{}
	}
	resp.TradeNo = stripNULBytes(resp.TradeNo)
	resp.PayURL = stripNULBytes(resp.PayURL)
	resp.QRCode = stripNULBytes(resp.QRCode)
	resp.ClientSecret = stripNULBytes(resp.ClientSecret)
	resp.IntentID = stripNULBytes(resp.IntentID)
	resp.Currency = stripNULBytes(resp.Currency)
	resp.CountryCode = stripNULBytes(resp.CountryCode)
	resp.PaymentEnv = stripNULBytes(resp.PaymentEnv)
	if resp.OAuth != nil {
		resp.OAuth.AuthorizeURL = stripNULBytes(resp.OAuth.AuthorizeURL)
		resp.OAuth.AppID = stripNULBytes(resp.OAuth.AppID)
		resp.OAuth.OpenID = stripNULBytes(resp.OAuth.OpenID)
		resp.OAuth.Scope = stripNULBytes(resp.OAuth.Scope)
		resp.OAuth.State = stripNULBytes(resp.OAuth.State)
		resp.OAuth.RedirectURL = stripNULBytes(resp.OAuth.RedirectURL)
	}
	if resp.JSAPI != nil {
		resp.JSAPI.AppID = stripNULBytes(resp.JSAPI.AppID)
		resp.JSAPI.TimeStamp = stripNULBytes(resp.JSAPI.TimeStamp)
		resp.JSAPI.NonceStr = stripNULBytes(resp.JSAPI.NonceStr)
		resp.JSAPI.Package = stripNULBytes(resp.JSAPI.Package)
		resp.JSAPI.SignType = stripNULBytes(resp.JSAPI.SignType)
		resp.JSAPI.PaySign = stripNULBytes(resp.JSAPI.PaySign)
	}
	return resp
}

func stripNULBytes(value string) string {
	return strings.ReplaceAll(value, "\x00", "")
}

func buildProviderCreatePaymentRequest(req CreateOrderRequest, sel *payment.InstanceSelection, orderID, amount, subject string) payment.CreatePaymentRequest {
	return payment.CreatePaymentRequest{
		OrderID:            orderID,
		Amount:             amount,
		PaymentType:        req.PaymentType,
		Subject:            subject,
		ReturnURL:          req.ReturnURL,
		OpenID:             strings.TrimSpace(req.OpenID),
		ClientIP:           req.ClientIP,
		IsMobile:           req.IsMobile,
		InstanceSubMethods: selectedInstanceSupportedTypes(sel),
	}
}

func selectedInstanceSupportedTypes(sel *payment.InstanceSelection) string {
	if sel == nil {
		return ""
	}
	return sel.SupportedTypes
}

func (s *PaymentService) buildPaymentSubject(plan *dbent.SubscriptionPlan, order *dbent.PaymentOrder, limitAmount float64, cfg *PaymentConfig, sel *payment.InstanceSelection) string {
	if order != nil && order.OrderType == payment.OrderTypeSubscription {
		productName := PaymentOrderProductName(order)
		if strings.TrimSpace(productName) != "" {
			return applyPaymentProductNameAffix(productName, cfg)
		}
	}
	if plan != nil {
		productName := plan.ProductName
		if productName == "" {
			productName = "Sub2API Subscription " + plan.Name
		}
		return applyPaymentProductNameAffix(productName, cfg)
	}
	currency := payment.DefaultPaymentCurrency
	if sel != nil {
		currency = paymentProviderConfigCurrency(sel.ProviderKey, sel.Config)
	}
	amountStr := payment.FormatAmountForCurrency(limitAmount, currency)
	if hasPaymentProductNameAffix(cfg) {
		return applyPaymentProductNameAffix(amountStr, cfg)
	}
	return "Sub2API " + amountStr + " " + currency
}

func hasPaymentProductNameAffix(cfg *PaymentConfig) bool {
	if cfg == nil {
		return false
	}
	pf := strings.TrimSpace(cfg.ProductNamePrefix)
	sf := strings.TrimSpace(cfg.ProductNameSuffix)
	return pf != "" || sf != ""
}

func applyPaymentProductNameAffix(productName string, cfg *PaymentConfig) string {
	if !hasPaymentProductNameAffix(cfg) {
		return productName
	}
	pf := strings.TrimSpace(cfg.ProductNamePrefix)
	sf := strings.TrimSpace(cfg.ProductNameSuffix)
	return strings.TrimSpace(pf + " " + productName + " " + sf)
}

func (s *PaymentService) maybeBuildWeChatOAuthRequiredResponse(ctx context.Context, req CreateOrderRequest, amount, payAmount, feeRate float64) (*CreateOrderResponse, error) {
	return s.maybeBuildWeChatOAuthRequiredResponseForSelection(ctx, req, amount, payAmount, feeRate, nil)
}

func (s *PaymentService) maybeBuildWeChatOAuthRequiredResponseForSelection(ctx context.Context, req CreateOrderRequest, amount, payAmount, feeRate float64, sel *payment.InstanceSelection) (*CreateOrderResponse, error) {
	if sel != nil && sel.ProviderKey != "" && sel.ProviderKey != payment.TypeWxpay {
		return nil, nil
	}
	if strings.TrimSpace(req.OpenID) != "" || !req.IsWeChatBrowser || payment.GetBasePaymentType(req.PaymentType) != payment.TypeWxpay {
		return nil, nil
	}
	return s.buildWeChatOAuthRequiredResponse(ctx, req, amount, payAmount, feeRate)
}

func (s *PaymentService) buildWeChatOAuthRequiredResponse(ctx context.Context, req CreateOrderRequest, amount, payAmount, feeRate float64) (*CreateOrderResponse, error) {
	appID, _, err := s.getWeChatPaymentOAuthCredential(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.paymentResume().ensureSigningKey(); err != nil {
		return nil, err
	}

	authorizeURL, err := buildWeChatPaymentOAuthStartURL(req, "snsapi_base")
	if err != nil {
		return nil, err
	}

	return &CreateOrderResponse{
		Amount:      amount,
		PayAmount:   payAmount,
		FeeRate:     feeRate,
		ResultType:  payment.CreatePaymentResultOAuthRequired,
		PaymentType: req.PaymentType,
		OAuth: &payment.WechatOAuthInfo{
			AuthorizeURL: authorizeURL,
			AppID:        appID,
			Scope:        "snsapi_base",
			RedirectURL:  "/auth/wechat/payment/callback",
		},
	}, nil
}

func (s *PaymentService) validateSelectedCreateOrderInstance(ctx context.Context, req CreateOrderRequest, sel *payment.InstanceSelection) error {
	if !requiresWeChatJSAPICompatibleSelection(req, sel) {
		return nil
	}
	expectedAppID, _, err := s.getWeChatPaymentOAuthCredential(ctx)
	if err != nil {
		return err
	}
	selectedAppID := provider.ResolveWxpayJSAPIAppID(sel.Config)
	if selectedAppID == "" || selectedAppID != expectedAppID {
		return infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "selected payment instance is not compatible with the current WeChat OAuth app")
	}
	return nil
}

func calculateCreateOrderPayAmount(limitAmount, feeRate float64, currency string) (string, float64, error) {
	if err := validateCreateOrderAmountCurrency(limitAmount, currency); err != nil {
		return "", 0, err
	}
	payAmountStr := payment.CalculatePayAmountForCurrency(limitAmount, feeRate, currency)
	if _, err := payment.AmountToMinorUnit(payAmountStr, currency); err != nil {
		return "", 0, infraerrors.BadRequest("INVALID_AMOUNT", err.Error()).
			WithMetadata(map[string]string{"currency": currency})
	}
	payAmount, err := strconv.ParseFloat(payAmountStr, 64)
	if err != nil {
		return "", 0, infraerrors.BadRequest("INVALID_AMOUNT", "invalid payment amount").
			WithMetadata(map[string]string{"currency": currency})
	}
	return payAmountStr, payAmount, nil
}

func validateCreateOrderAmountCurrency(amount float64, currency string) error {
	amountStr := strconv.FormatFloat(amount, 'f', -1, 64)
	if _, err := payment.AmountToMinorUnit(amountStr, currency); err != nil {
		return infraerrors.BadRequest("INVALID_AMOUNT", err.Error()).
			WithMetadata(map[string]string{"currency": currency})
	}
	return nil
}

func validateSelectedCreateOrderAmountCurrency(payAmount string, sel *payment.InstanceSelection) error {
	if sel == nil {
		return nil
	}
	currency := paymentProviderConfigCurrency(sel.ProviderKey, sel.Config)
	if _, err := payment.AmountToMinorUnit(payAmount, currency); err != nil {
		return infraerrors.BadRequest("INVALID_AMOUNT", err.Error()).
			WithMetadata(map[string]string{"currency": currency})
	}
	return nil
}

func requiresWeChatJSAPICompatibleSelection(req CreateOrderRequest, sel *payment.InstanceSelection) bool {
	if sel == nil || sel.ProviderKey != payment.TypeWxpay || payment.GetBasePaymentType(req.PaymentType) != payment.TypeWxpay {
		return false
	}
	return req.IsWeChatBrowser || strings.TrimSpace(req.OpenID) != ""
}

func (s *PaymentService) getWeChatPaymentOAuthCredential(ctx context.Context) (string, string, error) {
	if s == nil || s.configService == nil || s.configService.settingRepo == nil {
		return "", "", infraerrors.ServiceUnavailable(
			"WECHAT_PAYMENT_MP_NOT_CONFIGURED",
			"wechat in-app payment requires a complete WeChat MP OAuth credential",
		)
	}
	cfg, err := (&SettingService{settingRepo: s.configService.settingRepo}).GetWeChatConnectOAuthConfig(ctx)
	appID := strings.TrimSpace(cfg.AppIDForMode("mp"))
	appSecret := strings.TrimSpace(cfg.AppSecretForMode("mp"))
	if err != nil || !cfg.SupportsMode("mp") || appID == "" || appSecret == "" {
		return "", "", infraerrors.ServiceUnavailable(
			"WECHAT_PAYMENT_MP_NOT_CONFIGURED",
			"wechat in-app payment requires a complete WeChat MP OAuth credential",
		)
	}
	return appID, appSecret, nil
}

func classifyCreatePaymentError(req CreateOrderRequest, providerKey string, err error) error {
	if err == nil {
		return nil
	}
	if providerKey == payment.TypeWxpay &&
		payment.GetBasePaymentType(req.PaymentType) == payment.TypeWxpay &&
		strings.Contains(err.Error(), "wxpay h5 payments are not authorized for this merchant") {
		return infraerrors.ServiceUnavailable(
			"WECHAT_H5_NOT_AUTHORIZED",
			"wechat h5 payment is not available for this merchant",
		).WithMetadata(map[string]string{
			"action": "open_in_wechat_or_scan_qr",
		})
	}
	return infraerrors.ServiceUnavailable("PAYMENT_GATEWAY_ERROR", fmt.Sprintf("payment gateway error: %s", err.Error()))
}

func buildCreateOrderResponse(order *dbent.PaymentOrder, req CreateOrderRequest, payAmount float64, sel *payment.InstanceSelection, pr *payment.CreatePaymentResponse, resultType payment.CreatePaymentResultType) *CreateOrderResponse {
	return &CreateOrderResponse{
		OrderID:      order.ID,
		Amount:       order.Amount,
		PayAmount:    payAmount,
		FeeRate:      order.FeeRate,
		Status:       OrderStatusPending,
		ResultType:   resultType,
		PaymentType:  req.PaymentType,
		OutTradeNo:   order.OutTradeNo,
		PayURL:       pr.PayURL,
		QRCode:       pr.QRCode,
		ClientSecret: pr.ClientSecret,
		IntentID:     pr.IntentID,
		Currency:     pr.Currency,
		CountryCode:  pr.CountryCode,
		PaymentEnv:   pr.PaymentEnv,
		OAuth:        pr.OAuth,
		JSAPI:        pr.JSAPI,
		JSAPIPayload: pr.JSAPI,
		ExpiresAt:    order.ExpiresAt,
		PaymentMode:  sel.PaymentMode,
	}
}

func buildWeChatPaymentOAuthStartURL(req CreateOrderRequest, scope string) (string, error) {
	u, err := url.Parse("/api/v1/auth/oauth/wechat/payment/start")
	if err != nil {
		return "", fmt.Errorf("build wechat payment oauth start url: %w", err)
	}
	q := u.Query()
	q.Set("payment_type", strings.TrimSpace(req.PaymentType))
	if req.Amount > 0 {
		q.Set("amount", strconv.FormatFloat(req.Amount, 'f', -1, 64))
	}
	if orderType := strings.TrimSpace(req.OrderType); orderType != "" {
		q.Set("order_type", orderType)
	}
	if req.PlanID > 0 {
		q.Set("plan_id", strconv.FormatInt(req.PlanID, 10))
	}
	if req.GroupID > 0 {
		q.Set("group_id", strconv.FormatInt(req.GroupID, 10))
	}
	if req.DailyAmountUSD > 0 {
		q.Set("daily_amount_usd", strconv.FormatFloat(req.DailyAmountUSD, 'f', -1, 64))
	}
	if req.ValidityDays > 0 {
		q.Set("validity_days", strconv.Itoa(req.ValidityDays))
	}
	if intent := strings.TrimSpace(req.SubscriptionIntent); intent != "" {
		q.Set("subscription_intent", intent) // 续费/转套餐意图须随 OAuth 往返,否则 resume 退化为购买(P2#5)
	}
	if scope = strings.TrimSpace(scope); scope != "" {
		q.Set("scope", scope)
	}
	if redirectTo := paymentRedirectPathFromURL(req.SrcURL); redirectTo != "" {
		q.Set("redirect", redirectTo)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func paymentRedirectPathFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "/purchase"
	}
	if strings.HasPrefix(rawURL, "/") && !strings.HasPrefix(rawURL, "//") {
		return normalizePaymentRedirectPath(rawURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "/purchase"
	}
	path := strings.TrimSpace(u.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(u.Path)
	}
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "/purchase"
	}
	if strings.TrimSpace(u.RawQuery) != "" {
		path += "?" + u.RawQuery
	}
	return normalizePaymentRedirectPath(path)
}

func normalizePaymentRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/purchase"
	}
	if path == "/payment" {
		return "/purchase"
	}
	if strings.HasPrefix(path, "/payment?") {
		return "/purchase" + strings.TrimPrefix(path, "/payment")
	}
	return path
}

// --- Order Queries ---

func (s *PaymentService) GetOrder(ctx context.Context, orderID, userID int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != userID {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission for this order")
	}
	return o, nil
}

func (s *PaymentService) GetOrderByID(ctx context.Context, orderID int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	return o, nil
}

func (s *PaymentService) GetUserOrders(ctx context.Context, userID int64, p OrderListParams) ([]*dbent.PaymentOrder, int, error) {
	q := s.entClient.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID))
	if p.Status != "" {
		q = q.Where(paymentorder.StatusEQ(p.Status))
	}
	if p.OrderType != "" {
		q = q.Where(paymentorder.OrderTypeEQ(p.OrderType))
	}
	if p.PaymentType != "" {
		q = q.Where(paymentorder.PaymentTypeEQ(p.PaymentType))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count user orders: %w", err)
	}
	ps, pg := applyPagination(p.PageSize, p.Page)
	orders, err := q.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(ps).Offset((pg - 1) * ps).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query user orders: %w", err)
	}
	return orders, total, nil
}

// AdminListOrders returns a paginated list of orders. If userID > 0, filters by user.
func (s *PaymentService) AdminListOrders(ctx context.Context, userID int64, p OrderListParams) ([]*dbent.PaymentOrder, int, error) {
	q := s.entClient.PaymentOrder.Query()
	if userID > 0 {
		q = q.Where(paymentorder.UserIDEQ(userID))
	}
	if p.Status != "" {
		q = q.Where(paymentorder.StatusEQ(p.Status))
	}
	if p.OrderType != "" {
		q = q.Where(paymentorder.OrderTypeEQ(p.OrderType))
	}
	if p.PaymentType != "" {
		q = q.Where(paymentorder.PaymentTypeEQ(p.PaymentType))
	}
	if p.Keyword != "" {
		q = q.Where(paymentorder.Or(
			paymentorder.OutTradeNoContainsFold(p.Keyword),
			paymentorder.UserEmailContainsFold(p.Keyword),
			paymentorder.UserNameContainsFold(p.Keyword),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count admin orders: %w", err)
	}
	ps, pg := applyPagination(p.PageSize, p.Page)
	orders, err := q.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(ps).Offset((pg - 1) * ps).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query admin orders: %w", err)
	}
	return orders, total, nil
}
