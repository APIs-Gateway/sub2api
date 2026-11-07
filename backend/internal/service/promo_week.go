package service

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// 限时活动计费开关（2026-11 活动周）。
//
// 全部配置存在 settings 表（key/value），缺省/空/非法 = 活动关闭，行为与未上线本功能时完全一致：
//
//	PROMO_START_AT                      RFC3339（含时区），含
//	PROMO_END_AT                        RFC3339，不含
//	PROMO_SUBSCRIPTION_DAILY_MULTIPLIER 套餐额度倍率，如 1.5；仅 (1, promoMaxSubscriptionMultiplier] 生效
//	PROMO_BALANCE_RATE_MULTIPLIER       钱包（无生效套餐卡）扣费再乘的倍率，如 0.52；仅 (0, 1) 生效
//	PROMO_EXCLUDED_USER_IDS             逗号分隔的 user id，不参与任何活动
//
// 实现要点（与读代码前的假设不同，见 PR 说明）：生效中的套餐额度闸门是「三窗口」
// （user_subscriptions.daily/weekly/monthly_limit_usd 对 *_usage_usd），不是 today_remaining。
// 因此「额度 ×1.5」落在三窗口限额上，在引擎入口（SubWindow 构造点）把有效限额临时放大；
// 卡上存的 daily_amount_usd / *_limit_usd 以及一切按 D 计价的退款/转套餐/合并兑换路径都不动，
// 赠送部分只存在于「内存里的有效限额」，从不落库、不进入任何价值换算。
const (
	SettingKeyPromoStartAt                     = "PROMO_START_AT"
	SettingKeyPromoEndAt                       = "PROMO_END_AT"
	SettingKeyPromoSubscriptionDailyMultiplier = "PROMO_SUBSCRIPTION_DAILY_MULTIPLIER"
	SettingKeyPromoBalanceRateMultiplier       = "PROMO_BALANCE_RATE_MULTIPLIER"
	SettingKeyPromoExcludedUserIDs             = "PROMO_EXCLUDED_USER_IDS"

	// promoSettingsRefreshInterval 是后台刷新间隔：改库后最长此时间（每个实例各自）生效/失效，无需重启。
	promoSettingsRefreshInterval = 30 * time.Second
	// promoSettingsLoadTimeout 是单次读库超时（启动同步加载与后台刷新共用）。
	promoSettingsLoadTimeout = 2 * time.Second
	// promoMaxSubscriptionMultiplier 防手滑（写成 15 而不是 1.5）。
	promoMaxSubscriptionMultiplier = 10.0
)

var promoSettingKeys = []string{
	SettingKeyPromoStartAt,
	SettingKeyPromoEndAt,
	SettingKeyPromoSubscriptionDailyMultiplier,
	SettingKeyPromoBalanceRateMultiplier,
	SettingKeyPromoExcludedUserIDs,
}

// PromoSettingsSource 批量读取 settings 表中指定 key（缺失的 key 不出现在结果里）。
type PromoSettingsSource func(ctx context.Context, keys []string) (map[string]string, error)

// promoSettings 是解析后的活动配置；nil 或零值 = 活动关闭。
type promoSettings struct {
	start, end  time.Time
	subMult     float64 // 0 = 套餐侧不生效
	balanceRate float64 // 0 = 钱包侧不生效
	excluded    map[int64]struct{}
}

var (
	promoSource atomic.Pointer[PromoSettingsSource]
	// promoState 是热路径唯一读取的配置快照（nil = 活动关闭）。只由启动加载和后台刷新写入；
	// 请求/事务里读它只是一次 atomic load，绝不触库。
	promoState atomic.Pointer[promoSettings]
)

// SetPromoSettingsSource 注册设置读取源并【同步】加载一次（短超时；失败记日志、按关闭处理）。
// 只在启动期（PromoSettingsRefresher.Start）和测试里调用。src=nil 清空读取源并关闭活动。
func SetPromoSettingsSource(src PromoSettingsSource) {
	if src == nil {
		promoSource.Store(nil)
		promoState.Store(nil)
		return
	}
	promoSource.Store(&src)
	promoState.Store(nil)
	_ = refreshPromoSettings()
}

// refreshPromoSettings 读库并替换快照；读库失败保留旧快照（首次失败则保持关闭）。
func refreshPromoSettings() error {
	srcPtr := promoSource.Load()
	if srcPtr == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), promoSettingsLoadTimeout)
	defer cancel()
	m, err := (*srcPtr)(ctx, promoSettingKeys)
	if err != nil {
		logger.LegacyPrintf("service.promo", "Warning: load promo settings failed, keep previous value: %v", err)
		return err
	}
	promoState.Store(parsePromoSettings(m))
	return nil
}

// PromoSettingsRefresher 每 30 秒后台刷新活动配置；Start 时先同步加载一次。随服务关闭 Stop 退出。
type PromoSettingsRefresher struct {
	source   PromoSettingsSource
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewPromoSettingsRefresher(source PromoSettingsSource, interval time.Duration) *PromoSettingsRefresher {
	return &PromoSettingsRefresher{source: source, interval: interval, stopCh: make(chan struct{})}
}

func (r *PromoSettingsRefresher) Start() {
	if r == nil || r.source == nil || r.interval <= 0 {
		return
	}
	SetPromoSettingsSource(r.source) // 启动同步加载
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							logger.LegacyPrintf("service.promo", "ALERT: panic in promo settings refresh: %v", rec)
						}
					}()
					_ = refreshPromoSettings()
				}()
			case <-r.stopCh:
				return
			}
		}
	}()
}

func (r *PromoSettingsRefresher) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

// parsePromoSettings 把 key/value 解析成活动配置；任何一项非法按「该项不生效」处理，
// 唯独排除名单含无法解析的项时整体关闭活动（宁可不搞活动，也不能让排除名单里的用户被误打折）。
func parsePromoSettings(m map[string]string) *promoSettings {
	if len(m) == 0 {
		return nil
	}
	start, err1 := time.Parse(time.RFC3339, strings.TrimSpace(m[SettingKeyPromoStartAt]))
	end, err2 := time.Parse(time.RFC3339, strings.TrimSpace(m[SettingKeyPromoEndAt]))
	if err1 != nil || err2 != nil || !end.After(start) {
		return nil
	}
	p := &promoSettings{start: start, end: end, excluded: map[int64]struct{}{}}

	if v, err := strconv.ParseFloat(strings.TrimSpace(m[SettingKeyPromoSubscriptionDailyMultiplier]), 64); err == nil &&
		!math.IsNaN(v) && v > 1 && v <= promoMaxSubscriptionMultiplier {
		p.subMult = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(m[SettingKeyPromoBalanceRateMultiplier]), 64); err == nil &&
		!math.IsNaN(v) && v > 0 && v < 1 {
		p.balanceRate = v
	}
	if raw := strings.TrimSpace(m[SettingKeyPromoExcludedUserIDs]); raw != "" {
		for _, tok := range strings.Split(raw, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			id, err := strconv.ParseInt(tok, 10, 64)
			if err != nil || id <= 0 {
				logger.LegacyPrintf("service.promo", "ALERT: %s has invalid token %q, promo disabled", SettingKeyPromoExcludedUserIDs, tok)
				return nil
			}
			p.excluded[id] = struct{}{}
		}
	}
	if p.subMult == 0 && p.balanceRate == 0 {
		return nil
	}
	return p
}

func (p *promoSettings) inWindow(t time.Time) bool {
	return p != nil && !t.Before(p.start) && t.Before(p.end)
}

func (p *promoSettings) isExcluded(userID int64) bool {
	if p == nil {
		return false
	}
	_, ok := p.excluded[userID]
	return ok
}

// currentPromoSettings 返回当前活动配置快照（热路径：只做一次 atomic load，不触库）。
func currentPromoSettings() *promoSettings { return promoState.Load() }

// PromoBalanceRateFactor 返回 now 时刻对该用户的钱包扣费折扣倍率（1 = 不打折）。
// 调用方只能在「本次请求最终全额走钱包（用户没有生效套餐卡）」时使用。
func PromoBalanceRateFactor(userID int64, now time.Time) float64 {
	p := currentPromoSettings()
	if p == nil || p.balanceRate <= 0 || p.balanceRate >= 1 || p.isExcluded(userID) {
		return 1
	}
	if !p.inWindow(now) {
		return 1
	}
	return p.balanceRate
}

// promoActiveDayCount 统计东八区日序号 [fromDay, toDay]（含）内「当天 0 点落在 [START, END)」的活动日数。
func (p *promoSettings) promoActiveDayCount(fromDay, toDay int) int {
	n := 0
	for d := fromDay; d <= toDay; d++ {
		if p.inWindow(EastDayStart(d)) {
			n++
		}
	}
	return n
}

// PromoEffectiveLimits 是准入、结算、CheckUsageLimits、预占四处共用的唯一算式：
// 由卡上的原始日/周/月限额 (d, w, m) 与 now 算出活动期间的有效限额。
//
//	日：今天是活动日（当天 0 点 ∈ [START,END)）→ d×f，否则 d
//	周：w + (f−1)×d×n_w，n_w = 当前周窗口内、当天 0 点 ≤ now 的活动日数
//	月：m + (f−1)×d×n_m，n_m 按当前月窗口同理
//
// 周/月加成按「已发生的活动日」累计，活动结束后在该窗口剩余时间里继续有效，窗口重置后归零。
// 窗口起点沿用引擎口径 timezone.StartOfWeek/StartOfMonth（即 ResetWindows 对齐后的 *_window_start）；
// 日界沿用 EastDayNumber。<=0 的限额表示「不限」，保持不变，也不产生加成。
// 无活动、排除名单、设置缺失/非法时原样返回（加成为 0）。
func PromoEffectiveLimits(userID int64, now time.Time, d, w, m float64) (float64, float64, float64) {
	p := currentPromoSettings()
	if p == nil || p.subMult <= 1 || d <= 0 || p.isExcluded(userID) {
		return d, w, m
	}
	today := EastDayNumber(now)
	extra := (p.subMult - 1) * d
	if p.inWindow(EastDayStart(today)) {
		d *= p.subMult
	}
	if w > 0 {
		w += extra * float64(p.promoActiveDayCount(EastDayNumber(timezone.StartOfWeek(now)), today))
	}
	if m > 0 {
		m += extra * float64(p.promoActiveDayCount(EastDayNumber(timezone.StartOfMonth(now)), today))
	}
	return d, w, m
}

// ApplyPromo 按 PromoEffectiveLimits 改写这份内存副本的三窗口有效限额；从不写回卡。
func (c *SubWindow) ApplyPromo(userID int64, now time.Time) {
	if c == nil {
		return
	}
	c.DailyLimitUSD, c.WeeklyLimitUSD, c.MonthlyLimitUSD =
		PromoEffectiveLimits(userID, now, c.DailyLimitUSD, c.WeeklyLimitUSD, c.MonthlyLimitUSD)
}

// IsActive 报告卡此刻是否生效（status=active 且未过期）。
func (c *SubWindow) IsActive(now time.Time) bool {
	return c != nil && c.active(now)
}

// PromoEffectiveLimitPtrs 是 PromoEffectiveLimits 的指针版（nil 视为不限），不修改入参指向的值。
func PromoEffectiveLimitPtrs(userID int64, now time.Time, d, w, m *float64) (*float64, *float64, *float64) {
	val := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	d2, w2, m2 := PromoEffectiveLimits(userID, now, val(d), val(w), val(m))
	pick := func(orig *float64, v float64) *float64 {
		if orig == nil || *orig <= 0 || v == *orig {
			return orig
		}
		return &v
	}
	return pick(d, d2), pick(w, w2), pick(m, m2)
}
