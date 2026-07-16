package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type checkinSettingsRepoStub struct{}

func (checkinSettingsRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}
func (checkinSettingsRepoStub) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}
func (checkinSettingsRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}
func (checkinSettingsRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{
		SettingKeyCheckinEnabled:       "true",
		SettingKeyCheckinAmountMin:     "0.25",
		SettingKeyCheckinAmountMax:     "0.25",
		SettingKeyCheckinSpendPerExtra: "0",
		SettingKeyCheckinMinTokens:     "0",
	}, nil
}
func (checkinSettingsRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (checkinSettingsRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}
func (checkinSettingsRepoStub) Delete(context.Context, string) error { panic("unexpected Delete call") }

type checkinClaimRepoStub struct {
	dailyClaims int
}

func (r *checkinClaimRepoStub) HasDailyCheckin(context.Context, int64, string) (bool, error) {
	return r.dailyClaims > 0, nil
}
func (checkinClaimRepoStub) CountBonusOnDate(context.Context, int64, string) (int, error) {
	return 0, nil
}
func (r *checkinClaimRepoStub) ClaimDaily(context.Context, int64, string, float64) (float64, error) {
	r.dailyClaims++
	return 12.75, nil
}
func (checkinClaimRepoStub) ClaimBonus(context.Context, int64, string, float64, int) (float64, error) {
	panic("unexpected ClaimBonus call")
}

type checkinUsageRepoStub struct{ UsageLogRepository }

func (checkinUsageRepoStub) GetUserStatsAggregated(context.Context, int64, time.Time, time.Time) (*usagestats.UsageStats, error) {
	return &usagestats.UsageStats{}, nil
}

type checkinBlockingBalanceCache struct {
	billingCacheWorkerStub
	started chan struct{}
	release chan struct{}
}

func (c *checkinBlockingBalanceCache) InvalidateUserBalance(_ context.Context, _ int64) error {
	close(c.started)
	<-c.release
	return nil
}

func TestCheckinInvalidatesBalanceCacheBeforeReturning(t *testing.T) {
	cache := &checkinBlockingBalanceCache{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := &CheckinService{
		billingCache: &BillingCacheService{cache: cache},
	}

	done := make(chan struct{})
	go func() {
		svc.invalidateBalanceCaches(context.Background(), 42)
		close(done)
	}()

	select {
	case <-cache.started:
	case <-time.After(time.Second):
		t.Fatal("balance cache invalidation was not started")
	}

	select {
	case <-done:
		close(cache.release)
		t.Fatal("checkin returned before the balance cache was invalidated")
	case <-time.After(100 * time.Millisecond):
	}

	close(cache.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checkin did not return after balance cache invalidation completed")
	}
}

func TestCheckinClaimReturnsTransactionBalance(t *testing.T) {
	repo := &checkinClaimRepoStub{}
	svc := &CheckinService{
		repo:     repo,
		settings: &SettingService{settingRepo: checkinSettingsRepoStub{}},
		usage:    checkinUsageRepoStub{},
	}

	result, err := svc.Claim(context.Background(), 42)
	if err != nil {
		t.Fatalf("claim checkin: %v", err)
	}
	if result.Balance != 12.75 {
		t.Fatalf("balance = %v, want 12.75", result.Balance)
	}
	if result.Type != "daily" {
		t.Fatalf("type = %q, want daily", result.Type)
	}
	if repo.dailyClaims != 1 {
		t.Fatalf("daily claims = %d, want 1", repo.dailyClaims)
	}
}

func TestCheckinEarnedBonus(t *testing.T) {
	cases := []struct {
		spend, perExtra float64
		want            int
	}{
		{0.3, 0.1, 3},  // 浮点边界：0.3/0.1=2.9999… 必须算作 3
		{0.0, 0.1, 0},  // 未消费
		{0.1, 0.1, 1},  // 恰好一档
		{0.29, 0.1, 2}, // 未到第 3 档
		{1.0, 0.5, 2},  // 精确可表示
		{2.5, 2.5, 1},  // 恰好一档
		{5.0, 0.0, 0},  // perExtra=0 不开放
		{-1.0, 0.1, 0}, // 负消费
		{9.9999, 0.1, 99},
	}
	for _, c := range cases {
		if got := checkinEarnedBonus(c.spend, c.perExtra); got != c.want {
			t.Errorf("checkinEarnedBonus(%v, %v) = %d, want %d", c.spend, c.perExtra, got, c.want)
		}
	}
}

func TestCheckinRandomAmountBounds(t *testing.T) {
	// min==max：必须恰好返回该值。
	if got := checkinRandomAmount(CheckinConfig{AmountMin: 0.5, AmountMax: 0.5}); got != 0.5 {
		t.Errorf("equal min/max: got %v, want 0.5", got)
	}
	// min<max：多次采样都应落在 [min,max]。
	cfg := CheckinConfig{AmountMin: 0.1, AmountMax: 1.0}
	for i := 0; i < 1000; i++ {
		v := checkinRandomAmount(cfg)
		if v < cfg.AmountMin-1e-9 || v > cfg.AmountMax+1e-9 {
			t.Fatalf("random amount %v out of [%v,%v]", v, cfg.AmountMin, cfg.AmountMax)
		}
	}
	// max<=min（理论上 normalize 后不会出现）：返回 min。
	if got := checkinRandomAmount(CheckinConfig{AmountMin: 0.8, AmountMax: 0.2}); got != 0.8 {
		t.Errorf("max<min: got %v, want 0.8", got)
	}
}

func TestCheckinTodayShanghai(t *testing.T) {
	// 2026-06-21T20:00:00Z 在 Asia/Shanghai(+8) 已是 2026-06-22 04:00。
	now := time.Date(2026, 6, 21, 20, 0, 0, 0, time.UTC)
	date, dayStart := checkinToday(now)
	if date != "2026-06-22" {
		t.Errorf("date = %q, want 2026-06-22", date)
	}
	// dayStart 应为该自然日 00:00 (CST)，即 2026-06-21T16:00:00Z。
	wantStart := time.Date(2026, 6, 21, 16, 0, 0, 0, time.UTC)
	if !dayStart.Equal(wantStart) {
		t.Errorf("dayStart = %v, want %v", dayStart.UTC(), wantStart)
	}
}

func TestCheckinConfigNormalize(t *testing.T) {
	c := CheckinConfig{AmountMin: -1, AmountMax: -2, SpendPerExtra: -5, MinTokens: -100}
	c.normalize()
	if c.AmountMin != 0 || c.AmountMax != 0 || c.SpendPerExtra != 0 {
		t.Errorf("negative values not clamped: %+v", c)
	}
	if c.MinTokens != 0 {
		t.Errorf("negative MinTokens not clamped: %+v", c)
	}
	c = CheckinConfig{AmountMin: 1.0, AmountMax: 0.5}
	c.normalize()
	if c.AmountMax < c.AmountMin {
		t.Errorf("max<min not corrected: %+v", c)
	}
}

func TestParseInt64Default(t *testing.T) {
	cases := []struct {
		raw  string
		def  int64
		want int64
	}{
		{"1000000", 0, 1000000},
		{"  500000 ", 0, 500000},  // 容忍前后空白
		{"", 1000000, 1000000},    // 空串回退默认
		{"abc", 1000000, 1000000}, // 非法回退默认
		{"0", 1000000, 0},         // 显式 0 不回退
		{"-5", 1000000, -5},       // 解析层不做钳制（由 normalize 负责）
	}
	for _, c := range cases {
		if got := parseInt64Default(c.raw, c.def); got != c.want {
			t.Errorf("parseInt64Default(%q, %d) = %d, want %d", c.raw, c.def, got, c.want)
		}
	}
}
