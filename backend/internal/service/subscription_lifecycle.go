package service

// per-day 订阅生命周期纯计算：续费(renew) / 退款(refund) / 转套餐(change plan)。规格第 5–7 节。
//
// 唯一真相源 = 东八区自然日 expire_day。剩余/可退天数一律用 max(0, expire_day − today)，
// 它天然已含「已用天 + 透支借走天」的扣减（每次透支把 expire_day 提前 1 天）；
// 切勿用 T − 日历已过天数 这种忽略透支的算法（会退多 / 折多）。

// RenewExpireDay 计算同套餐续 addDays 天后的新 expire_day（规格第 5 节，D 不变）：
//
//	未到期(curExpireDay ≥ today) → 从原到期日顺延（无缝衔接）；
//	已到期(curExpireDay < today) → 从今天起算 addDays 天，中间断档不补发。
//
// 即 expire_day = max(curExpireDay, today−1) + addDays，并夹到 MaxExpireDay()。
func RenewExpireDay(curExpireDay, today, addDays int) int {
	base := curExpireDay
	if base < today-1 {
		base = today - 1
	}
	return ClampExpireDay(base + addDays)
}

// RefundAmount 按剩余服务天数比例退款（规格第 6 节）：退款额 = price × refundableDays / originalT。
//
//	refundableDays = max(0, expire_day − today)（调用方用 (*PerDayCard).RefundableDays(today) 取，
//	  已含已用 + 透支借走的扣减）；originalT = 购买时的原始天数 validity_days。
//
// originalT ≤ 0 / refundableDays ≤ 0 / price ≤ 0 → 退款额 0（非法或无可退）。
// 注意：本函数只算「按比例应退额」；退到何处（先填平钱包负债、余量进可用余额 / 原路退）由调用方按
// 规格第 6 节处理。管理员 force 全额退也在调用方分支，不走本函数。
//
// 单笔订单口径：refundableDays 夹到 originalT。规格 §6 的等价式
// 「refundable = T − 已用 − 透支借走」恒有 refundable ≤ T——但调用方常传整卡剩余天
// (*PerDayCard).RefundableDays(today)=expire_day−today，续费叠加后整卡剩余天会 > 本单 T，
// 不夹则退款额 > 本单售价 P → 撞 REFUND_AMOUNT_EXCEEDED 误挡(只能 force 全额退);
// 同样口径下 ChangePlanRemainingValue 复用本函数,旧卡续费叠加会把剩余价值 V 算超 → 超额抵扣资损。
// 一律按「单笔最多退它自己授予的 T 天」夹住,保证退款额/折价 ≤ 本单 P。
func RefundAmount(price float64, refundableDays, originalT int) float64 {
	if price <= 0 || refundableDays <= 0 || originalT <= 0 {
		return 0
	}
	if refundableDays > originalT {
		refundableDays = originalT
	}
	amt := price * float64(refundableDays) / float64(originalT)
	if amt < 0 {
		return 0
	}
	return amt
}

// ChangePlanRemainingValue 旧卡按剩余服务天数折出的剩余价值 V（规格第 7 节，口径与退款一致）：
//
//	V = P_旧 × max(0, expire_day_旧 − today) / T_旧。
//
// 旧卡已被透支借光天数(remaining=0) → V=0，转套餐即全额买新套餐。
func ChangePlanRemainingValue(oldPrice float64, oldRefundableDays, oldT int) float64 {
	return RefundAmount(oldPrice, oldRefundableDays, oldT)
}

// ChangePlanNewCardTodayBalance 转套餐当天新卡的套餐余额（防套利，规格第 7 节）：
//
//	= max(0, D_新 − 旧卡今日已用)。
//
// 旧卡今日已用 = 旧卡今天已从套餐余额扣掉的官方成本，调用方用 (*PerDayCard).TodaySpentFromPackage(today) 取。
// 把「今天已经领过的额度」从新卡当天扣掉、当手续费，堵死「旧卡用光 D → 转新卡再领一份 D」的套利；
// 次日起新卡正常按 D_新 发放。
func ChangePlanNewCardTodayBalance(dNew, oldTodaySpent float64) float64 {
	v := dNew - oldTodaySpent
	if v < 0 {
		return 0
	}
	return v
}

// ChangePlanQuote 转套餐的完整资金/发卡测算（规格第 7 节），纯计算、无副作用，供服务层落账时直接取用。
type ChangePlanQuote struct {
	OldRemainingValue   float64 // V = P_旧 × max(0, expire_day_旧−today) / T_旧（旧卡折剩余价值，已含已用+透支借天扣减）
	NewPlanPrice        float64 // P_新 = D_新 × T_新 × u(D_新)
	Diff                float64 // P_新 − V：>0 需补差价（从其他余额扣或发起支付）；<0 应退差价（进其他余额）
	NewCardTodayBalance float64 // max(0, D_新 − 旧卡今日已用)（防套利；次日起按 D_新 发放）
	NewCardExpireDay    int     // today + T_新 − 1，夹到 MaxExpireDay()（新卡 start_day=today）
}

// QuoteChangePlan 用旧卡的剩余天数/原价/原天数与新套餐 D_新/T_新，测算转套餐的多退少补与新卡发放参数。
//
//	oldRefundableDays 用旧卡 (*PerDayCard).RefundableDays(today) 取（含透支借天扣减）；
//	oldTodaySpent     用旧卡 (*PerDayCard).TodaySpentFromPackage(today) 取。
//
// 仅做纯测算；旧卡关闭、新卡落库、差额补扣/退、发起支付等副作用由服务层按规格第 7 节处理。
func QuoteChangePlan(cfg SubscriptionPricingConfig, oldPrice float64, oldRefundableDays, oldT int, dNew float64, tNew int, oldTodaySpent float64, today int) ChangePlanQuote {
	v := ChangePlanRemainingValue(oldPrice, oldRefundableDays, oldT)
	pNew := cfg.Price(dNew, tNew)
	return ChangePlanQuote{
		OldRemainingValue:   v,
		NewPlanPrice:        pNew,
		Diff:                pNew - v,
		NewCardTodayBalance: ChangePlanNewCardTodayBalance(dNew, oldTodaySpent),
		NewCardExpireDay:    ClampExpireDay(today + tNew - 1),
	}
}
