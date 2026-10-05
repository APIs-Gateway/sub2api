package service

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
)

// credit_unit.go 是「站内额度记账单位」的唯一换算入口。
//
// 背景：codex 站历史上以「额度」记账（充 ¥1 得 13 额度，BALANCE_RECHARGE_MULTIPLIER=13），
// 正在改成直接以人民币记账（¥1 = 1）。切换分两步：先让代码认识两种单位（本文件，默认仍是
// 历史口径，行为不变），最后在停服窗口里一次性换算数据并打开开关。
//
// 约定：
//   - 余额、订阅额度、Key 额度与限额、签到、兑换码、usage_logs.actual_cost 等「额度类」数值，
//     统一叫「当前单位」：CREDIT_CURRENCY=USD 时是历史额度，CREDIT_CURRENCY=CNY 时是人民币。
//   - 任何要在「历史额度」和「当前单位」之间换算、或要把历史倍率换成当前倍率的地方，
//     都必须走本文件，不要在业务代码里自己 ÷13 / ×13 / 读 BALANCE_RECHARGE_MULTIPLIER 去推
//     （CI 有 grep 闸门盯着，见 tools/check_credit_multiplier.py）。
//   - CREDIT_CURRENCY / LEGACY_CREDIT_DIVISOR / CNY_CUTOVER_AT 只在进程启动时读一次，
//     之后进程内固定：运行中改 settings 表不会生效，换单位必须重启全部实例。
//     这样同一个进程里不可能同时出现两种单位。

// CreditCurrency 是站内额度当前使用的记账单位。
type CreditCurrency string

const (
	// CreditCurrencyUSD 是历史口径「额度」：free 站 1 额度 = 官方价 1 美元，
	// codex 站切换前 ¥1 = 13 额度。默认值，行为与引入本文件之前完全一致。
	CreditCurrencyUSD CreditCurrency = "USD"
	// CreditCurrencyCNY 是人民币口径，¥1 = 1。
	CreditCurrencyCNY CreditCurrency = "CNY"
)

// settings 表里的键。三个都只在启动时读一次（见文件头）。
const (
	SettingCreditCurrency      = "CREDIT_CURRENCY"
	SettingLegacyCreditDivisor = "LEGACY_CREDIT_DIVISOR"
	SettingCNYCutoverAt        = "CNY_CUTOVER_AT"
)

// UsageLogCostUnitCNY 是 usage_logs.cost_unit 的取值：1 = 这行的 actual_cost 等金额以人民币记。
// NULL = 历史额度（切换前写入的行，包括 CREDIT_CURRENCY=USD 时写入的所有行）。
const UsageLogCostUnitCNY int16 = 1

const (
	// CreditRateScale 是倍率的小数位：groups / user_group_rate_multipliers / usage_logs 的
	// rate_multiplier 都是 DECIMAL(10,4)，加位数要重写 9GB 的 usage_logs，所以倍率固定 4 位。
	CreditRateScale int32 = 4
	// CreditCentScale 是金额「取到分」的小数位。
	CreditCentScale int32 = 2
	// creditHistoryScale 是历史行换算时保留的小数位，只用于展示和对账，不做取整偏向。
	creditHistoryScale int32 = 10

	// creditUnitSnapshotVersionCNYOffset 把币种叠加进鉴权快照版本号：CNY 模式的版本号永远不等于
	// USD 模式的版本号，切换（或回滚）后旧快照自然失效，不会把旧单位的余额、额度读成新单位。
	creditUnitSnapshotVersionCNYOffset = 1000
)

// creditMinPositiveRate 是倍率换算后的下限：原倍率 > 0 但向下取整到 4 位变成 0 时取 0.0001，
// 免得把一个收费的分组悄悄变成免费。
var creditMinPositiveRate = decimal.New(1, -CreditRateScale)

// CreditRounding 是金额换算的取整方向。取整方向由数据类别决定，不能一刀切：
//
//	用户手里的钱和上限（余额、Key 额度、限额、未用兑换码）→ Ceil：不让用户少；
//	已用量（Key / 订阅窗口 / 平台配额的 used）          → Floor：已用算少，剩余就多；
//	纯历史记录（已用兑换码、签到流水、订单金额）          → Nearest：只用来展示和对账；
//	倍率                                                 → Floor：不多收（见 LegacyRateToCurrent）。
type CreditRounding uint8

const (
	// CreditRoundCeil 向正无穷取整（负数向 0 方向取）。
	CreditRoundCeil CreditRounding = iota + 1
	// CreditRoundFloor 向负无穷取整。
	CreditRoundFloor
	// CreditRoundNearest 四舍五入（5 远离 0）。
	CreditRoundNearest
)

// CreditUnit 描述一个进程启动时确定下来的记账单位。零值不可用，请用 DefaultCreditUnit /
// ParseCreditUnit 构造。方法都是纯函数，便于直接在值上做单测；业务代码通过 CurrentCreditUnit() 取
// 进程级的那一个。
type CreditUnit struct {
	// Currency 是当前记账单位。
	Currency CreditCurrency
	// LegacyDivisor 是「历史额度 ÷ 它 = 人民币」的除数：codex 站是 13，free 站是 1。
	// 它只用来解释旧数据（cost_unit 为 NULL 的行、切换前的备份），不参与新请求的扣费。
	LegacyDivisor int64
	// CutoverAt 是切换到人民币的时间，仅审计用；零值 = 未设置。用值而不是指针，CreditUnit 才能放心按值拷贝。
	CutoverAt time.Time
}

// DefaultCreditUnit 是 settings 里什么都没配时的单位：历史口径、除数 1。
func DefaultCreditUnit() CreditUnit {
	return CreditUnit{Currency: CreditCurrencyUSD, LegacyDivisor: 1}
}

// IsCNY 报告当前单位是否为人民币。
func (u CreditUnit) IsCNY() bool {
	return u.Currency == CreditCurrencyCNY
}

// String 用于启动日志；不含任何密钥。
func (u CreditUnit) String() string {
	cutover := "-"
	if !u.CutoverAt.IsZero() {
		cutover = u.CutoverAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("currency=%s legacy_divisor=%d cutover_at=%s", u.Currency, u.LegacyDivisor, cutover)
}

// equal 比较两个单位是否完全一致（InitCreditUnit 的幂等判断用）。
func (u CreditUnit) equal(o CreditUnit) bool {
	return u.Currency == o.Currency && u.LegacyDivisor == o.LegacyDivisor && u.CutoverAt.Equal(o.CutoverAt)
}

// CostUnit 返回写入 usage_logs.cost_unit 的值：CNY 模式是 1，USD 模式是 nil（写 NULL）。
// 每次返回新的指针，调用方可以放心持有。
func (u CreditUnit) CostUnit() *int16 {
	if !u.IsCNY() {
		return nil
	}
	v := UsageLogCostUnitCNY
	return &v
}

// AuthSnapshotVersion 把币种模式叠加进 API Key 鉴权快照的版本号。USD 模式返回 base 本身
// （保持引入前的版本号，部署 USD 模式的新版本不会让缓存全部失效）；CNY 模式返回 base 加固定偏移。
func (u CreditUnit) AuthSnapshotVersion(base int) int {
	if u.IsCNY() {
		return base + creditUnitSnapshotVersionCNYOffset
	}
	return base
}

// legacyDivisorDecimal 返回除数；零值或非法值按 1 处理，避免除零。
func (u CreditUnit) legacyDivisorDecimal() decimal.Decimal {
	if u.LegacyDivisor < 1 {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromInt(u.LegacyDivisor)
}

// LegacyToCurrent 把「历史额度」换成当前单位，结果保留 scale 位小数并按 mode 取整。
// USD 模式下当前单位就是历史额度，原样返回（不做取整）。
func (u CreditUnit) LegacyToCurrent(amount decimal.Decimal, scale int32, mode CreditRounding) decimal.Decimal {
	if !u.IsCNY() {
		return amount
	}
	return divideWithRounding(amount, u.legacyDivisorDecimal(), scale, mode)
}

// CurrentToLegacy 把当前单位的金额换回「历史额度」（回滚用）。精确乘法，不取整。
// USD 模式下原样返回。
func (u CreditUnit) CurrentToLegacy(amount decimal.Decimal) decimal.Decimal {
	if !u.IsCNY() {
		return amount
	}
	return amount.Mul(u.legacyDivisorDecimal())
}

// LegacyAmountToCurrentCeilCent 把历史额度的零碎金额（默认余额、签到额度、公益 Key 单 IP 上限等
// 后台设置，以及用户手里的余额类上限）换成当前单位，÷divisor 后向上取到分。
// 例：divisor=13 时 1 → 0.08，10 → 0.77。USD 模式原样返回。
func (u CreditUnit) LegacyAmountToCurrentCeilCent(amount decimal.Decimal) decimal.Decimal {
	return u.LegacyToCurrent(amount, CreditCentScale, CreditRoundCeil)
}

// LegacyRateToCurrent 把历史分组倍率换成当前倍率：÷divisor，保留 4 位小数，向下取整
// （不多收用户的钱）。原倍率 > 0 但结果取整成 0 时取 0.0001。USD 模式、原倍率 <= 0 时原样返回。
// 例：divisor=13 时 1.4 → 0.1076，1 → 0.0769，5 → 0.3846，0.585 → 0.045。
func (u CreditUnit) LegacyRateToCurrent(rate decimal.Decimal) decimal.Decimal {
	if !u.IsCNY() || !rate.IsPositive() {
		return rate
	}
	out := divideWithRounding(rate, u.legacyDivisorDecimal(), CreditRateScale, CreditRoundFloor)
	if out.IsZero() {
		return creditMinPositiveRate
	}
	return out
}

// LegacyRateToCurrentFloat 是 LegacyRateToCurrent 的 float64 版本，给手里只有 float64 倍率的调用方。
// NaN / Inf 原样返回。
func (u CreditUnit) LegacyRateToCurrentFloat(rate float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return rate
	}
	return u.LegacyRateToCurrent(decimal.NewFromFloat(rate)).InexactFloat64()
}

// LegacyAmountToCurrentCeilCentFloat 是 LegacyAmountToCurrentCeilCent 的 float64 版本。
// NaN / Inf 原样返回。
func (u CreditUnit) LegacyAmountToCurrentCeilCentFloat(amount float64) float64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return amount
	}
	return u.LegacyAmountToCurrentCeilCent(decimal.NewFromFloat(amount)).InexactFloat64()
}

// UsageAmountToCurrent 把一行 usage_logs 里的金额（actual_cost 等）换成当前单位，
// 依据这一行自己的 cost_unit：
//
//	当前 CNY：cost_unit=1 原样；NULL（历史额度）÷divisor；
//	当前 USD：NULL 原样；cost_unit=1（切换后又回滚了）×divisor。
//
// 只用于读路径的展示和汇总，保留 10 位小数、四舍五入。cost_unit 不是 1 的非 NULL 值按历史额度处理。
func (u CreditUnit) UsageAmountToCurrent(amount decimal.Decimal, costUnit *int16) decimal.Decimal {
	rowIsCNY := costUnit != nil && *costUnit == UsageLogCostUnitCNY
	switch {
	case u.IsCNY() && !rowIsCNY:
		return u.LegacyToCurrent(amount, creditHistoryScale, CreditRoundNearest)
	case !u.IsCNY() && rowIsCNY:
		return amount.Mul(u.legacyDivisorDecimal())
	default:
		return amount
	}
}

// UsageRateToCurrent 把一行 usage_logs 里的 rate_multiplier 换成当前倍率。规则同 UsageAmountToCurrent，
// 但历史倍率走 LegacyRateToCurrent（4 位、向下取整），和分组倍率的换算口径一致。
func (u CreditUnit) UsageRateToCurrent(rate decimal.Decimal, costUnit *int16) decimal.Decimal {
	rowIsCNY := costUnit != nil && *costUnit == UsageLogCostUnitCNY
	switch {
	case u.IsCNY() && !rowIsCNY:
		return u.LegacyRateToCurrent(rate)
	case !u.IsCNY() && rowIsCNY:
		return rate.Mul(u.legacyDivisorDecimal())
	default:
		return rate
	}
}

// FormatCreditRate 是界面展示倍率的统一格式：原样显示存储值（4 位以内），去掉末尾的 0。
// 例：0.1076 → "0.1076"，0.0450 → "0.045"，1.0000 → "1"。不含 "x" 后缀。
func FormatCreditRate(rate decimal.Decimal) string {
	return rate.Round(CreditRateScale).String()
}

// divideWithRounding 计算 num/den（den > 0），结果保留 scale 位小数。
// Ceil/Floor 用带余数的精确除法判断，不经过任何中间舍入（shopspring 的 Div 会先按 16 位舍入）。
func divideWithRounding(num, den decimal.Decimal, scale int32, mode CreditRounding) decimal.Decimal {
	if mode == CreditRoundNearest {
		return num.DivRound(den, scale)
	}
	q, r := num.QuoRem(den, scale) // q 向 0 截断到 scale 位，r 与 num 同号
	step := decimal.New(1, -scale)
	switch mode {
	case CreditRoundCeil:
		if r.IsPositive() {
			q = q.Add(step)
		}
	case CreditRoundFloor:
		if r.IsNegative() {
			q = q.Sub(step)
		}
	default:
		// 未知取整方向：按四舍五入处理，避免静默偏向某一侧。
		return num.DivRound(den, scale)
	}
	return q
}

// ---------------------------------------------------------------------------
// 启动时读取 settings 与校验
// ---------------------------------------------------------------------------

// creditCNYDefaultRateWarnThreshold：CNY 模式下默认分组倍率（config.yaml 的 default.rate_multiplier）
// 超过它就告警。切换前该值是 1（历史额度倍率），换算后应是 0.0769；忘了改会让没有分组的 Key 多收 13 倍。
const creditCNYDefaultRateWarnThreshold = 0.5

// ParseCreditUnit 从 settings 键值解析单位。键缺失或为空取默认值（USD / 1 / 无切换时间）。
//
//   - CREDIT_CURRENCY：USD 或 CNY（不区分大小写、忽略首尾空白）；其它值返回错误。
//   - LEGACY_CREDIT_DIVISOR：正整数（允许写成 13.00）；0、负数、小数、非数字返回错误。
//   - CNY_CUTOVER_AT：RFC3339 时间，仅审计用；解析失败不拒绝启动，按未设置处理，由调用方看 warnings。
func ParseCreditUnit(vals map[string]string) (CreditUnit, []string, error) {
	unit := DefaultCreditUnit()
	var warnings []string

	if raw := strings.TrimSpace(vals[SettingCreditCurrency]); raw != "" {
		switch CreditCurrency(strings.ToUpper(raw)) {
		case CreditCurrencyUSD:
			unit.Currency = CreditCurrencyUSD
		case CreditCurrencyCNY:
			unit.Currency = CreditCurrencyCNY
		default:
			return CreditUnit{}, nil, fmt.Errorf("%s 取值 %q 无效，只能是 USD 或 CNY", SettingCreditCurrency, raw)
		}
	}

	if raw := strings.TrimSpace(vals[SettingLegacyCreditDivisor]); raw != "" {
		d, err := decimal.NewFromString(raw)
		if err != nil || !d.IsPositive() || !d.Equal(d.Truncate(0)) || !d.LessThanOrEqual(decimal.NewFromInt(math.MaxInt32)) {
			return CreditUnit{}, nil, fmt.Errorf("%s 取值 %q 无效，必须是正整数", SettingLegacyCreditDivisor, raw)
		}
		unit.LegacyDivisor = d.IntPart()
	}

	if raw := strings.TrimSpace(vals[SettingCNYCutoverAt]); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 取值 %q 不是 RFC3339 时间，已忽略（只影响审计信息）", SettingCNYCutoverAt, raw))
		} else {
			unit.CutoverAt = t
		}
	}
	return unit, warnings, nil
}

// CreditUnitStartupSettingKeys 返回启动时需要从 settings 表读取的键（含启动检查要用的充值倍率）。
func CreditUnitStartupSettingKeys() []string {
	return []string{
		SettingCreditCurrency,
		SettingLegacyCreditDivisor,
		SettingCNYCutoverAt,
		SettingBalanceRechargeMult,
	}
}

// ResolveCreditUnitAtStartup 解析并校验启动时的币种配置。
//
// 返回错误时调用方必须拒绝启动：
//   - 配置值非法（见 ParseCreditUnit）；
//   - CREDIT_CURRENCY=CNY 但 BALANCE_RECHARGE_MULTIPLIER 不是 1：人民币模式下充 ¥1 就是 ¥1，
//     还留着 13 会把每一笔充值多记 13 倍。充值倍率的读取口径与运行时一致
//     （缺失 / 非法 / <=0 都按 1），所以没配这个键等于 1，不会误拦。
//
// 返回的 warnings 由调用方写日志：
//   - CNY 模式下默认分组倍率（defaultRateMultiplier，即 config.yaml 的 default.rate_multiplier）> 0.5，
//     看起来还是历史额度口径。
func ResolveCreditUnitAtStartup(vals map[string]string, defaultRateMultiplier float64) (CreditUnit, []string, error) {
	unit, warnings, err := ParseCreditUnit(vals)
	if err != nil {
		return CreditUnit{}, nil, err
	}
	if !unit.IsCNY() {
		return unit, warnings, nil
	}

	m := normalizeBalanceRechargeMultiplier(pcParseFloat(strings.TrimSpace(vals[SettingBalanceRechargeMult]), defaultBalanceRechargeMultiplier))
	if !decimal.NewFromFloat(m).Equal(decimal.NewFromInt(1)) {
		return CreditUnit{}, nil, fmt.Errorf(
			"%s=%s 要求 %s=1，当前是 %s，拒绝启动：先把 %s 改成 1，或保持 %s=%s",
			SettingCreditCurrency, CreditCurrencyCNY, SettingBalanceRechargeMult,
			decimal.NewFromFloat(m).String(), SettingBalanceRechargeMult, SettingCreditCurrency, CreditCurrencyUSD,
		)
	}
	if defaultRateMultiplier > creditCNYDefaultRateWarnThreshold {
		warnings = append(warnings, fmt.Sprintf(
			"%s=%s 但默认分组倍率 default.rate_multiplier=%v 大于 %v，看起来还是历史额度口径；没有分组的 API Key 会按它扣费，请确认是否漏改",
			SettingCreditCurrency, CreditCurrencyCNY, defaultRateMultiplier, creditCNYDefaultRateWarnThreshold,
		))
	}
	return unit, warnings, nil
}

// ---------------------------------------------------------------------------
// 进程级单位
// ---------------------------------------------------------------------------

// processCreditUnit 是进程级的单位。nil = 还没初始化，按 DefaultCreditUnit 处理，
// 所以没走启动流程的代码路径（单测、工具）看到的永远是历史口径。
var processCreditUnit atomic.Pointer[CreditUnit]

// CurrentCreditUnit 返回本进程的记账单位。热路径（每次写 usage_logs）会调用，无锁。
func CurrentCreditUnit() CreditUnit {
	if p := processCreditUnit.Load(); p != nil {
		return *p
	}
	return DefaultCreditUnit()
}

// InitCreditUnit 在进程启动时固定本进程的记账单位，只能成功设置一次：
// 之后用同样的值重复调用是空操作，用不同的值调用返回错误（单位在进程内不允许变）。
func InitCreditUnit(u CreditUnit) error {
	if u.Currency != CreditCurrencyUSD && u.Currency != CreditCurrencyCNY {
		return fmt.Errorf("credit unit: 无效的币种 %q", u.Currency)
	}
	if u.LegacyDivisor < 1 {
		return fmt.Errorf("credit unit: LegacyDivisor 必须 >= 1，当前 %d", u.LegacyDivisor)
	}
	cp := u
	if processCreditUnit.CompareAndSwap(nil, &cp) {
		return nil
	}
	if existing := processCreditUnit.Load(); existing != nil && existing.equal(u) {
		return nil
	}
	return fmt.Errorf("credit unit: 进程内已固定为 %s，不能改成 %s", CurrentCreditUnit(), u)
}

// SetCreditUnitForTest 仅供测试：无视「只能设置一次」直接替换进程级单位，返回恢复函数
// （测试里 defer / t.Cleanup 调用）。不是并发安全的测试夹具，使用它的测试不要 t.Parallel()。
func SetCreditUnitForTest(u CreditUnit) (restore func()) {
	prev := processCreditUnit.Load()
	cp := u
	processCreditUnit.Store(&cp)
	return func() { processCreditUnit.Store(prev) }
}

// ResetCreditUnitForTest 仅供测试：把进程级单位恢复成「还没初始化」，用来测 InitCreditUnit 的启动流程。
// 返回恢复函数，用法和限制同 SetCreditUnitForTest。
func ResetCreditUnitForTest() (restore func()) {
	prev := processCreditUnit.Load()
	processCreditUnit.Store(nil)
	return func() { processCreditUnit.Store(prev) }
}
