package service

import (
	"math"

	"github.com/shopspring/decimal"
)

// 站内额度（用量页历史上以 "$" 呈现）既不是法币，也不是官方价美元：结算时
// SettleWindow 把「模型官方价 × 分组倍率」累加进钱包和订阅三窗口，两者同口径。
// 所以「本次扣 5」不等于 5 美元，也不等于 5 元——这 5 个额度值多少钱，取决于
// 用户当初是走哪条路径买回来的，而两条路径的单价差得很远：
//
//	钱包充值：付 1 法币得 m 个额度（m = BALANCE_RECHARGE_MULTIPLIER），单价 = 1 / m。
//	订阅卡：  付 P = D × T × u(D) 得 D × T 个额度，单价 = u(D)——T 在分子分母约掉，
//	          所以单价只取决于每日额度 D，与买了多少天无关。
//
// 两条路径的单价量纲相同（法币/额度），可以统一由 CreditFiatRate 承载。之所以
// 必须分开算而不能统一按充值价折算：生产上 m = 13 而 u(D) ∈ [0.04, 0.05]（相当于
// 1 法币换 20~25 个额度），订阅比充值便宜 54%~92%，一律按 1/m 折算会把订阅用户的
// 真实花费高估到接近两倍，等于把本该展示成优惠的部分反过来展示成多花的钱。
type CreditFiatRate struct {
	// walletFiatPerCredit 是钱包额度的法币单价（1 / multiplier）。
	walletFiatPerCredit float64
	// subFiatPerCredit 按订阅卡 ID 索引该卡的法币单价 u(D)。未登记的卡
	// （已删除的历史卡、跨卡的旧记录等）回落到钱包单价，宁可高估也不报错。
	subFiatPerCredit map[int64]float64
}

// creditFiatRateScale 是折算结果保留的小数位。单笔请求低至 0.0397 额度，
// 按 1/13 折算后约 0.003 法币，位数太少会把大量小额消费直接截成 0。
const creditFiatRateScale int32 = 8

// NewCreditFiatRate 按充值倍率构造折算器。multiplier 缺失/损坏时
// normalizeBalanceRechargeMultiplier 会回落到 1.0，此时折算退化为恒等
// （1 额度 = 1 法币），即「不折算」，而不是除以 0。
func NewCreditFiatRate(multiplier float64) *CreditFiatRate {
	m := normalizeBalanceRechargeMultiplier(multiplier)
	return &CreditFiatRate{
		walletFiatPerCredit: decimal.NewFromInt(1).
			Div(decimal.NewFromFloat(m)).
			InexactFloat64(),
		subFiatPerCredit: make(map[int64]float64),
	}
}

// RegisterSubscription 登记一张订阅卡的法币单价，供该卡产生的用量记录折算。
// dailyAmountUSD 是卡的每日额度 D；单价直接取定价公式的 u(D)，与下单时冻结的
// 快照口径一致（ChangeSubscriptionPlan 估值也是这么反算的）。
// D 非法（<=0/NaN/Inf）时不登记，该卡的记录自动回落到钱包单价。
func (r *CreditFiatRate) RegisterSubscription(subID int64, dailyAmountUSD float64, cfg SubscriptionPricingConfig) {
	if r == nil || subID <= 0 {
		return
	}
	if math.IsNaN(dailyAmountUSD) || math.IsInf(dailyAmountUSD, 0) || dailyAmountUSD <= 0 {
		return
	}
	unit := cfg.UnitPrice(dailyAmountUSD)
	if math.IsNaN(unit) || math.IsInf(unit, 0) || unit <= 0 {
		return
	}
	r.subFiatPerCredit[subID] = unit
}

// FiatPerCredit 返回这笔用量所消耗额度的法币单价。
func (r *CreditFiatRate) FiatPerCredit(billingType int8, subscriptionID int64) float64 {
	if r == nil {
		return 0
	}
	if billingType == BillingTypeSubscription && subscriptionID > 0 {
		if unit, ok := r.subFiatPerCredit[subscriptionID]; ok {
			return unit
		}
	}
	return r.walletFiatPerCredit
}

// Convert 把站内额度折算成法币金额。
//
// 注意一个已知的口径近似：单笔请求可能同时跨订阅和钱包（SettleWindow 的瀑布会先
// 用订阅覆盖、不够的部分落到钱包），但 usage_logs 每条只记录一个 billing_type，
// 拆分比例没有落库，所以这里只能整笔按主计费类型折算。这只影响「订阅日额度刚好
// 用尽」的那一笔，占比极低，不值得为它加列改结算路径。
func (r *CreditFiatRate) Convert(credits float64, billingType int8, subscriptionID int64) float64 {
	if r == nil || math.IsNaN(credits) || math.IsInf(credits, 0) || credits == 0 {
		return 0
	}
	unit := r.FiatPerCredit(billingType, subscriptionID)
	if unit <= 0 {
		return 0
	}
	return decimal.NewFromFloat(credits).
		Mul(decimal.NewFromFloat(unit)).
		Round(creditFiatRateScale).
		InexactFloat64()
}
