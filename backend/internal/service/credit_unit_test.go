package service

import (
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func cuDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func requireCUDec(t *testing.T, want string, got decimal.Decimal, msgAndArgs ...any) {
	t.Helper()
	require.Truef(t, cuDec(want).Equal(got), "want %s, got %s %v", want, got.String(), msgAndArgs)
}

var (
	cuUSD   = DefaultCreditUnit()
	cuCNY13 = CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13}
	cuCNY1  = CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 1}
)

// 设计 2.4 的倍率换算表：4 位小数、向下取整。
func TestCreditUnit_LegacyRateToCurrent_DesignTable(t *testing.T) {
	cases := []struct{ old, want string }{
		{"0.585", "0.045"},
		{"1", "0.0769"},
		{"1.35", "0.1038"},
		{"1.4", "0.1076"},
		{"2", "0.1538"},
		{"3", "0.2307"},
		{"4", "0.3076"},
		{"5", "0.3846"},
	}
	for _, tc := range cases {
		t.Run(tc.old, func(t *testing.T) {
			requireCUDec(t, tc.want, cuCNY13.LegacyRateToCurrent(cuDec(tc.old)))
		})
	}
}

// 向下取整、而不是四舍五入：1.4/13 = 0.107692… 四舍五入会得到 0.1077（多收），必须是 0.1076。
func TestCreditUnit_LegacyRateToCurrent_FloorNotRound(t *testing.T) {
	requireCUDec(t, "0.1076", cuCNY13.LegacyRateToCurrent(cuDec("1.4")))
	require.NotEqual(t, "0.1077", cuCNY13.LegacyRateToCurrent(cuDec("1.4")).String())
	// 5/13 = 0.384615…：四舍五入是 0.3846，向下取整也是 0.3846；换一个会不同的：6/13 = 0.461538…
	requireCUDec(t, "0.4615", cuCNY13.LegacyRateToCurrent(cuDec("6")))
	// 7/13 = 0.538461538…：四舍五入 0.5385，向下取整 0.5384。
	requireCUDec(t, "0.5384", cuCNY13.LegacyRateToCurrent(cuDec("7")))
}

func TestCreditUnit_LegacyRateToCurrent_Guards(t *testing.T) {
	// 原倍率 > 0 但向下取整成 0：取 0.0001，不能把收费分组悄悄变免费。
	requireCUDec(t, "0.0001", cuCNY13.LegacyRateToCurrent(cuDec("0.001")))
	requireCUDec(t, "0.0001", cuCNY13.LegacyRateToCurrent(cuDec("0.0004")))
	// 0 和负数原样返回（0 = 免费分组）。
	requireCUDec(t, "0", cuCNY13.LegacyRateToCurrent(cuDec("0")))
	requireCUDec(t, "-1", cuCNY13.LegacyRateToCurrent(cuDec("-1")))
	// 恰好整除不被多减一格。
	requireCUDec(t, "0.1", cuCNY13.LegacyRateToCurrent(cuDec("1.3")))
	requireCUDec(t, "1", cuCNY13.LegacyRateToCurrent(cuDec("13")))
}

// USD 模式、除数为 1 时，倍率原样不动。
func TestCreditUnit_LegacyRateToCurrent_NoopModes(t *testing.T) {
	requireCUDec(t, "1.4", cuUSD.LegacyRateToCurrent(cuDec("1.4")))
	requireCUDec(t, "1.4", CreditUnit{Currency: CreditCurrencyUSD, LegacyDivisor: 13}.LegacyRateToCurrent(cuDec("1.4")))
	requireCUDec(t, "1.4", cuCNY1.LegacyRateToCurrent(cuDec("1.4")))
	// 零值 CreditUnit（没走构造函数）按 USD 处理。
	requireCUDec(t, "1.4", CreditUnit{}.LegacyRateToCurrent(cuDec("1.4")))
}

// 设计 Q2：零碎金额 ÷13 后向上取到分。
func TestCreditUnit_LegacyAmountToCurrentCeilCent(t *testing.T) {
	cases := []struct{ old, want string }{
		{"1", "0.08"},    // 默认余额 1
		{"5", "0.39"},    // 签到上限 5
		{"10", "0.77"},   // 公益 Key 单 IP 上限 10 / 未用余额码 10
		{"50", "3.85"},   // 每满消费 50
		{"100", "7.70"},  // 旧返利单人上限 100
		{"3", "0.24"},    // 未用余额码 3
		{"130", "10"},    // 恰好整除不多进一分
		{"13", "1"},      // 同上
		{"0", "0"},       // 0 = 不限 / 未设置，保持 0
		{"0.01", "0.01"}, // 极小金额不会被取成 0
		{"-1", "-0.07"},  // 负数向 0 方向取（Ceil = 向 +∞）
	}
	for _, tc := range cases {
		t.Run(tc.old, func(t *testing.T) {
			requireCUDec(t, tc.want, cuCNY13.LegacyAmountToCurrentCeilCent(cuDec(tc.old)))
		})
	}
	// USD 模式原样。
	requireCUDec(t, "1", cuUSD.LegacyAmountToCurrentCeilCent(cuDec("1")))
	requireCUDec(t, "12.345", cuUSD.LegacyAmountToCurrentCeilCent(cuDec("12.345")))
}

// 三种取整方向在 1e-8 位上的行为（余额 Ceil、已用量 Floor、历史记录 Nearest）。
func TestCreditUnit_LegacyToCurrent_RoundingModes(t *testing.T) {
	// 1/13 = 0.0769230769230…
	requireCUDec(t, "0.07692308", cuCNY13.LegacyToCurrent(cuDec("1"), 8, CreditRoundCeil))
	requireCUDec(t, "0.07692307", cuCNY13.LegacyToCurrent(cuDec("1"), 8, CreditRoundFloor))
	requireCUDec(t, "0.07692308", cuCNY13.LegacyToCurrent(cuDec("1"), 8, CreditRoundNearest))
	// 2/13 = 0.153846153846…：第 9 位起是 1538…，四舍五入应当向下。
	requireCUDec(t, "0.15384616", cuCNY13.LegacyToCurrent(cuDec("2"), 8, CreditRoundCeil))
	requireCUDec(t, "0.15384615", cuCNY13.LegacyToCurrent(cuDec("2"), 8, CreditRoundFloor))
	requireCUDec(t, "0.15384615", cuCNY13.LegacyToCurrent(cuDec("2"), 8, CreditRoundNearest))
	// 负数：Ceil 向 0 方向，Floor 远离 0（负余额不能被取成更欠钱，也不能少记）。
	requireCUDec(t, "-0.07692307", cuCNY13.LegacyToCurrent(cuDec("-1"), 8, CreditRoundCeil))
	requireCUDec(t, "-0.07692308", cuCNY13.LegacyToCurrent(cuDec("-1"), 8, CreditRoundFloor))
	requireCUDec(t, "-0.07692308", cuCNY13.LegacyToCurrent(cuDec("-1"), 8, CreditRoundNearest))
	// 整除：三种方向一致，不多不少。
	for _, mode := range []CreditRounding{CreditRoundCeil, CreditRoundFloor, CreditRoundNearest} {
		requireCUDec(t, "10", cuCNY13.LegacyToCurrent(cuDec("130"), 2, mode), "mode", mode)
		requireCUDec(t, "0", cuCNY13.LegacyToCurrent(cuDec("0"), 8, mode), "mode", mode)
	}
	// Nearest 在恰好一半时远离 0：0.065 / 13 = 0.005 整，取 2 位得 0.01。
	requireCUDec(t, "0.01", cuCNY13.LegacyToCurrent(cuDec("0.065"), 2, CreditRoundNearest))
	// 未知取整方向按四舍五入处理，不偏向某一侧。
	requireCUDec(t, "0.15384615", cuCNY13.LegacyToCurrent(cuDec("2"), 8, CreditRounding(99)))
	// USD 模式：不换算、不取整。
	requireCUDec(t, "1.123456789", cuUSD.LegacyToCurrent(cuDec("1.123456789"), 2, CreditRoundCeil))
}

func TestCreditUnit_CurrentToLegacy(t *testing.T) {
	requireCUDec(t, "13", cuCNY13.CurrentToLegacy(cuDec("1")))
	requireCUDec(t, "1.3", cuCNY13.CurrentToLegacy(cuDec("0.1")))
	requireCUDec(t, "-13", cuCNY13.CurrentToLegacy(cuDec("-1")))
	requireCUDec(t, "1", cuUSD.CurrentToLegacy(cuDec("1")))
	requireCUDec(t, "7", cuCNY1.CurrentToLegacy(cuDec("7")))
	// 往返：先 Ceil 到分再还原，差值小于 13 个分之一分以内。
	cur := cuCNY13.LegacyAmountToCurrentCeilCent(cuDec("1")) // 0.08
	back := cuCNY13.CurrentToLegacy(cur)                     // 1.04
	require.True(t, back.GreaterThanOrEqual(cuDec("1")), "Ceil 不能让用户少")
	require.True(t, back.Sub(cuDec("1")).LessThan(cuDec("0.13")))
}

func TestCreditUnit_FloatWrappers(t *testing.T) {
	require.Equal(t, 0.1076, cuCNY13.LegacyRateToCurrentFloat(1.4))
	require.Equal(t, 0.0769, cuCNY13.LegacyRateToCurrentFloat(1))
	require.Equal(t, 1.4, cuUSD.LegacyRateToCurrentFloat(1.4))
	require.Equal(t, 0.77, cuCNY13.LegacyAmountToCurrentCeilCentFloat(10))
	require.Equal(t, 0.08, cuCNY13.LegacyAmountToCurrentCeilCentFloat(1))
	// NaN / Inf 原样返回，不 panic。
	require.True(t, math.IsNaN(cuCNY13.LegacyRateToCurrentFloat(math.NaN())))
	require.True(t, math.IsInf(cuCNY13.LegacyRateToCurrentFloat(math.Inf(1)), 1))
	require.True(t, math.IsNaN(cuCNY13.LegacyAmountToCurrentCeilCentFloat(math.NaN())))
	require.True(t, math.IsInf(cuCNY13.LegacyAmountToCurrentCeilCentFloat(math.Inf(-1)), -1))
}

func TestCreditUnit_UsageAmountAndRateToCurrent(t *testing.T) {
	one := UsageLogCostUnitCNY
	unknown := int16(2)

	// 当前 CNY：历史行（NULL）÷13，人民币行原样。
	requireCUDec(t, "1", cuCNY13.UsageAmountToCurrent(cuDec("13"), nil))
	requireCUDec(t, "0.0769230769", cuCNY13.UsageAmountToCurrent(cuDec("1"), nil))
	requireCUDec(t, "1", cuCNY13.UsageAmountToCurrent(cuDec("1"), &one))
	requireCUDec(t, "1", cuCNY13.UsageAmountToCurrent(cuDec("13"), &unknown), "未知 cost_unit 按历史额度处理")
	requireCUDec(t, "0.1076", cuCNY13.UsageRateToCurrent(cuDec("1.4"), nil))
	requireCUDec(t, "0.1076", cuCNY13.UsageRateToCurrent(cuDec("0.1076"), &one))

	// 当前 USD（切换后回滚）：历史行原样，人民币行 ×13。
	requireCUDec(t, "13", cuUSD.UsageAmountToCurrent(cuDec("13"), nil))
	cuUSD13 := CreditUnit{Currency: CreditCurrencyUSD, LegacyDivisor: 13}
	requireCUDec(t, "13", cuUSD13.UsageAmountToCurrent(cuDec("1"), &one))
	requireCUDec(t, "1.3988", cuUSD13.UsageRateToCurrent(cuDec("0.1076"), &one))
	requireCUDec(t, "1.4", cuUSD13.UsageRateToCurrent(cuDec("1.4"), nil))

	// 除数为 1 的 CNY（新站直接以人民币起步）：历史行也不变。
	requireCUDec(t, "5", cuCNY1.UsageAmountToCurrent(cuDec("5"), nil))
}

func TestFormatCreditRate(t *testing.T) {
	require.Equal(t, "0.1076", FormatCreditRate(cuDec("0.1076")))
	require.Equal(t, "0.045", FormatCreditRate(cuDec("0.0450")))
	require.Equal(t, "0.0769", FormatCreditRate(cuDec("0.0769")))
	require.Equal(t, "1", FormatCreditRate(cuDec("1.0000")))
	require.Equal(t, "5", FormatCreditRate(cuDec("5")))
	require.Equal(t, "0.3846", FormatCreditRate(cuDec("0.38461538")), "最多 4 位")
}

func TestCreditUnit_CostUnit(t *testing.T) {
	require.Nil(t, cuUSD.CostUnit())
	require.Nil(t, CreditUnit{Currency: CreditCurrencyUSD, LegacyDivisor: 13}.CostUnit())
	require.Nil(t, CreditUnit{}.CostUnit())

	got := cuCNY13.CostUnit()
	require.NotNil(t, got)
	require.Equal(t, int16(1), *got)
	require.Equal(t, UsageLogCostUnitCNY, *got)

	// 每次返回新指针：调用方改动不会污染下一次。
	*got = 9
	require.Equal(t, int16(1), *cuCNY13.CostUnit())
}

func TestCreditUnit_AuthSnapshotVersion(t *testing.T) {
	require.Equal(t, 17, cuUSD.AuthSnapshotVersion(17), "USD 模式保持原版本号，部署不让缓存全部失效")
	require.Equal(t, 1017, cuCNY13.AuthSnapshotVersion(17))
	require.NotEqual(t, cuUSD.AuthSnapshotVersion(17), cuCNY13.AuthSnapshotVersion(17))
	require.Equal(t, cuCNY13.AuthSnapshotVersion(17), cuCNY1.AuthSnapshotVersion(17), "版本只看币种，不看除数")
}

func TestParseCreditUnit(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		u, warns, err := ParseCreditUnit(nil)
		require.NoError(t, err)
		require.Empty(t, warns)
		require.Equal(t, DefaultCreditUnit(), u)
		require.Equal(t, CreditCurrencyUSD, u.Currency)
		require.EqualValues(t, 1, u.LegacyDivisor)
		require.True(t, u.CutoverAt.IsZero())

		u, _, err = ParseCreditUnit(map[string]string{SettingCreditCurrency: "  ", SettingLegacyCreditDivisor: "", SettingCNYCutoverAt: ""})
		require.NoError(t, err)
		require.Equal(t, DefaultCreditUnit(), u)
	})

	t.Run("cny_with_divisor_and_cutover", func(t *testing.T) {
		u, warns, err := ParseCreditUnit(map[string]string{
			SettingCreditCurrency:      "CNY",
			SettingLegacyCreditDivisor: "13",
			SettingCNYCutoverAt:        "2026-10-12T21:40:00Z",
		})
		require.NoError(t, err)
		require.Empty(t, warns)
		require.True(t, u.IsCNY())
		require.EqualValues(t, 13, u.LegacyDivisor)
		require.True(t, u.CutoverAt.Equal(time.Date(2026, 10, 12, 21, 40, 0, 0, time.UTC)))
		require.Contains(t, u.String(), "currency=CNY")
		require.Contains(t, u.String(), "legacy_divisor=13")
		require.Contains(t, u.String(), "2026-10-12T21:40:00Z")
	})

	t.Run("currency_is_case_and_space_insensitive", func(t *testing.T) {
		for _, raw := range []string{"cny", " Cny ", "CNY"} {
			u, _, err := ParseCreditUnit(map[string]string{SettingCreditCurrency: raw})
			require.NoError(t, err, raw)
			require.True(t, u.IsCNY(), raw)
		}
		u, _, err := ParseCreditUnit(map[string]string{SettingCreditCurrency: "usd"})
		require.NoError(t, err)
		require.False(t, u.IsCNY())
	})

	t.Run("invalid_currency_rejected", func(t *testing.T) {
		for _, raw := range []string{"EUR", "RMB", "1", "true"} {
			_, _, err := ParseCreditUnit(map[string]string{SettingCreditCurrency: raw})
			require.Error(t, err, raw)
			require.Contains(t, err.Error(), SettingCreditCurrency)
		}
	})

	t.Run("divisor_accepts_integers_only", func(t *testing.T) {
		for raw, want := range map[string]int64{"1": 1, "13": 13, "13.00": 13, " 13 ": 13, "100": 100} {
			u, _, err := ParseCreditUnit(map[string]string{SettingLegacyCreditDivisor: raw})
			require.NoError(t, err, raw)
			require.Equal(t, want, u.LegacyDivisor, raw)
		}
		for _, raw := range []string{"0", "-13", "13.5", "abc", "1e3x", "99999999999"} {
			_, _, err := ParseCreditUnit(map[string]string{SettingLegacyCreditDivisor: raw})
			require.Error(t, err, raw)
			require.Contains(t, err.Error(), SettingLegacyCreditDivisor)
		}
	})

	t.Run("bad_cutover_is_warning_not_error", func(t *testing.T) {
		u, warns, err := ParseCreditUnit(map[string]string{SettingCreditCurrency: "CNY", SettingCNYCutoverAt: "yesterday"})
		require.NoError(t, err)
		require.True(t, u.IsCNY())
		require.True(t, u.CutoverAt.IsZero())
		require.Len(t, warns, 1)
		require.Contains(t, warns[0], SettingCNYCutoverAt)
	})
}

// 启动检查：CNY 模式要求 BALANCE_RECHARGE_MULTIPLIER 为 1，否则拒绝启动。
func TestResolveCreditUnitAtStartup_CNYRequiresRechargeMultiplierOne(t *testing.T) {
	cny := func(extra map[string]string) map[string]string {
		m := map[string]string{SettingCreditCurrency: "CNY", SettingLegacyCreditDivisor: "13"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// 拒绝：充值倍率还是 13（或其它不是 1 的值）。
	for _, m := range []string{"13", "13.00", "2", "0.5", "1.01"} {
		_, _, err := ResolveCreditUnitAtStartup(cny(map[string]string{SettingBalanceRechargeMult: m}), 0.0769)
		require.Error(t, err, "m=%s", m)
		require.Contains(t, err.Error(), SettingBalanceRechargeMult)
		require.Contains(t, err.Error(), "拒绝启动")
	}

	// 通过：显式是 1，或没配 / 空 / 非法 / <=0（运行时都会被规范成 1，所以不会误拦）。
	for name, vals := range map[string]map[string]string{
		"explicit_1":      cny(map[string]string{SettingBalanceRechargeMult: "1"}),
		"explicit_1.00":   cny(map[string]string{SettingBalanceRechargeMult: "1.00"}),
		"explicit_spaces": cny(map[string]string{SettingBalanceRechargeMult: " 1 "}),
		"missing":         cny(nil),
		"empty":           cny(map[string]string{SettingBalanceRechargeMult: ""}),
		"garbage":         cny(map[string]string{SettingBalanceRechargeMult: "abc"}),
		"zero":            cny(map[string]string{SettingBalanceRechargeMult: "0"}),
		"negative":        cny(map[string]string{SettingBalanceRechargeMult: "-3"}),
	} {
		t.Run(name, func(t *testing.T) {
			u, _, err := ResolveCreditUnitAtStartup(vals, 0.0769)
			require.NoError(t, err)
			require.True(t, u.IsCNY())
		})
	}
}

// USD 模式不检查充值倍率（codex 站切换前就是 13）。
func TestResolveCreditUnitAtStartup_USDDoesNotCheckRechargeMultiplier(t *testing.T) {
	for _, vals := range []map[string]string{
		nil,
		{SettingBalanceRechargeMult: "13"},
		{SettingCreditCurrency: "USD", SettingBalanceRechargeMult: "13.00", SettingLegacyCreditDivisor: "13"},
	} {
		u, warns, err := ResolveCreditUnitAtStartup(vals, 1)
		require.NoError(t, err)
		require.False(t, u.IsCNY())
		require.Empty(t, warns, "USD 模式下默认倍率 1 是正常值，不能告警")
	}
}

func TestResolveCreditUnitAtStartup_DefaultRateWarning(t *testing.T) {
	vals := map[string]string{SettingCreditCurrency: "CNY", SettingLegacyCreditDivisor: "13"}

	// > 0.5：告警（还是历史额度口径的 1）。
	for _, rate := range []float64{1, 0.51, 5} {
		_, warns, err := ResolveCreditUnitAtStartup(vals, rate)
		require.NoError(t, err)
		require.Len(t, warns, 1, "rate=%v", rate)
		require.Contains(t, warns[0], "default.rate_multiplier")
	}
	// <= 0.5：不告警（边界值 0.5 本身不告警）。
	for _, rate := range []float64{0, 0.0769, 0.3846, 0.5} {
		_, warns, err := ResolveCreditUnitAtStartup(vals, rate)
		require.NoError(t, err)
		require.Empty(t, warns, "rate=%v", rate)
	}
}

func TestResolveCreditUnitAtStartup_InvalidSettingsRejected(t *testing.T) {
	_, _, err := ResolveCreditUnitAtStartup(map[string]string{SettingCreditCurrency: "EUR"}, 1)
	require.Error(t, err)
	_, _, err = ResolveCreditUnitAtStartup(map[string]string{SettingLegacyCreditDivisor: "0"}, 1)
	require.Error(t, err)
}

func TestCreditUnitStartupSettingKeys(t *testing.T) {
	require.ElementsMatch(t,
		[]string{"CREDIT_CURRENCY", "LEGACY_CREDIT_DIVISOR", "CNY_CUTOVER_AT", "BALANCE_RECHARGE_MULTIPLIER"},
		CreditUnitStartupSettingKeys())
}

// 进程级单位：默认历史口径；InitCreditUnit 只能固定一次；测试夹具能临时替换并恢复。
func TestProcessCreditUnit_InitOnceAndTestFixture(t *testing.T) {
	prev := processCreditUnit.Load()
	t.Cleanup(func() { processCreditUnit.Store(prev) })

	// 未初始化：按默认值（USD / 1）。
	processCreditUnit.Store(nil)
	require.Equal(t, DefaultCreditUnit(), CurrentCreditUnit())
	require.False(t, CurrentCreditUnit().IsCNY())

	// 非法参数被拒绝，且不改变状态。
	require.Error(t, InitCreditUnit(CreditUnit{Currency: "EUR", LegacyDivisor: 1}))
	require.Error(t, InitCreditUnit(CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 0}))
	require.Equal(t, DefaultCreditUnit(), CurrentCreditUnit())

	// 第一次设置成功；同值重复是空操作；不同值被拒绝，单位在进程内不变。
	cutover := time.Date(2026, 10, 12, 21, 40, 0, 0, time.UTC)
	cnyAt := CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13, CutoverAt: cutover}
	require.NoError(t, InitCreditUnit(cnyAt))
	require.True(t, CurrentCreditUnit().IsCNY())
	require.NoError(t, InitCreditUnit(CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13, CutoverAt: cutover}))
	require.Error(t, InitCreditUnit(DefaultCreditUnit()))
	require.Error(t, InitCreditUnit(CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 1, CutoverAt: cutover}))
	require.Error(t, InitCreditUnit(CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13}), "少了 CutoverAt 也算不同的值")
	require.True(t, CurrentCreditUnit().IsCNY())
	require.EqualValues(t, 13, CurrentCreditUnit().LegacyDivisor)
	require.True(t, CurrentCreditUnit().CutoverAt.Equal(cutover))

	// 测试夹具：无视「只能一次」，restore 后回到之前的状态。
	restore := SetCreditUnitForTest(DefaultCreditUnit())
	require.False(t, CurrentCreditUnit().IsCNY())
	restore()
	require.True(t, CurrentCreditUnit().IsCNY())
}
