//go:build unit

package service

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const lockFreeTestThreshold = 500000.0

// lockFreeSpyRepo 记录预占仓储的每一次调用，用来证明「跳过」时一次都没碰过（不碰就不会锁 users 行）。
type lockFreeSpyRepo struct {
	inflightCaptureRepo
	reserves, resizes, renews, releases, stages, finishes atomic.Int64
}

func newLockFreeSpyRepo() *lockFreeSpyRepo {
	return &lockFreeSpyRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
}

func (r *lockFreeSpyRepo) ReserveBillingInflight(ctx context.Context, userID int64, owner string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	r.reserves.Add(1)
	return r.inflightCaptureRepo.ReserveBillingInflight(ctx, userID, owner, amount, exclusive, ttl)
}

func (r *lockFreeSpyRepo) ResizeBillingInflight(ctx context.Context, userID int64, id, attempt string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	r.resizes.Add(1)
	return r.inflightCaptureRepo.ResizeBillingInflight(ctx, userID, id, attempt, amount, exclusive, ttl)
}

func (r *lockFreeSpyRepo) RenewBillingInflight(ctx context.Context, userID int64, id string, ttl time.Duration) (bool, error) {
	r.renews.Add(1)
	return true, nil
}

func (r *lockFreeSpyRepo) ReleaseBillingInflight(ctx context.Context, userID int64, id string) error {
	r.releases.Add(1)
	return r.inflightCaptureRepo.ReleaseBillingInflight(ctx, userID, id)
}

func (r *lockFreeSpyRepo) StageBillingInflight(ctx context.Context, userID int64, owner, attempt string, cmd *UsageBillingCommand, ttl time.Duration) (string, error) {
	r.stages.Add(1)
	return r.inflightCaptureRepo.StageBillingInflight(ctx, userID, owner, attempt, cmd, ttl)
}

func (r *lockFreeSpyRepo) FinishBillingInflightAttempt(ctx context.Context, userID int64, owner, attempt string) error {
	r.finishes.Add(1)
	return r.inflightCaptureRepo.FinishBillingInflightAttempt(ctx, userID, owner, attempt)
}

func (r *lockFreeSpyRepo) requireUntouched(t *testing.T, msg string) {
	t.Helper()
	require.Zero(t, r.reserves.Load(), "%s: Reserve", msg)
	require.Zero(t, r.resizes.Load(), "%s: Resize", msg)
	require.Zero(t, r.renews.Load(), "%s: Renew", msg)
	require.Zero(t, r.releases.Load(), "%s: Release", msg)
	require.Zero(t, r.stages.Load(), "%s: Stage", msg)
	require.Zero(t, r.finishes.Load(), "%s: Finish", msg)
}

func lockFreeRequest(balance float64) BillingInflightRequest {
	return BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 1, Balance: balance}}}
}

// ---- 阈值 / 余额 边界 ----

func TestBillingInflight_LockFreeSkipPredicateThresholdValues(t *testing.T) {
	const balance = 991228.0
	for name, tc := range map[string]struct {
		threshold float64
		want      bool
	}{
		"zero disables":              {0, false},
		"negative":                   {-1, false},
		"negative tiny":              {-1e-9, false},
		"negative infinity":          {math.Inf(-1), false},
		"NaN must not skip":          {math.NaN(), false},
		"positive infinity":          {math.Inf(1), false},
		"MaxFloat64 not reachable":   {math.MaxFloat64, false},
		"normal threshold":           {lockFreeTestThreshold, true},
		"threshold equals balance":   {balance, true},
		"threshold just above":       {balance + 0.01, false},
		"smallest positive denormal": {math.SmallestNonzeroFloat64, true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := inflightTestConfig()
			cfg.Billing.InflightReservation.LockFreeMinBalance = tc.threshold
			require.Equal(t, tc.want, skipInflightReservationForBalance(context.Background(), cfg, balance))
		})
	}
}

func TestBillingInflight_LockFreeSkipPredicateBalanceValues(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	for name, tc := range map[string]struct {
		balance float64
		want    bool
	}{
		"equal to threshold skips (>=)": {lockFreeTestThreshold, true},
		"threshold minus one cent":      {lockFreeTestThreshold - 0.01, false},
		"threshold plus one cent":       {lockFreeTestThreshold + 0.01, true},
		"prod-like balance":             {991228, true},
		"zero":                          {0, false},
		"negative (arrears)":            {-100, false},
		"NaN":                           {math.NaN(), false},
		"positive infinity":             {math.Inf(1), false},
		"negative infinity":             {math.Inf(-1), false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, skipInflightReservationForBalance(context.Background(), cfg, tc.balance))
		})
	}
	require.False(t, skipInflightReservationForBalance(context.Background(), nil, 1e12), "nil config never skips")
}

// 非法阈值下，请求仍按原逻辑建 lease（不能因为配置写坏而静默放行）；余额 0 与 99 万都一样。
func TestBillingInflight_LockFreeInvalidThresholdKeepsReserving(t *testing.T) {
	for name, threshold := range map[string]float64{"zero": 0, "negative": -1, "NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1)} {
		for _, balance := range []float64{0, 991228} {
			t.Run(name, func(t *testing.T) {
				cfg := inflightTestConfig()
				cfg.Billing.InflightReservation.LockFreeMinBalance = threshold
				repo := newLockFreeSpyRepo()
				lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), 1.5, false)
				require.NoError(t, err)
				require.NotNil(t, lease, "invalid threshold must not skip reservation (balance=%v)", balance)
				defer lease.HandlerDone()
				require.EqualValues(t, 1, repo.reserves.Load())
			})
		}
	}
	// 余额非法同理。
	for name, balance := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1)} {
		t.Run("balance "+name, func(t *testing.T) {
			cfg := inflightTestConfig()
			cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
			repo := newLockFreeSpyRepo()
			lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), 1.5, false)
			require.NoError(t, err)
			require.NotNil(t, lease, "non-finite balance must not skip reservation")
			defer lease.HandlerDone()
			require.EqualValues(t, 1, repo.reserves.Load())
		})
	}
}

// ---- applyInflightEstimate：跳过 / 不跳过 ----

func TestBillingInflight_LockFreeBoundaryThroughApply(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold

	repo := newLockFreeSpyRepo()
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(lockFreeTestThreshold), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease, "balance == threshold skips")
	repo.requireUntouched(t, "balance == threshold")

	lease, err = applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(lockFreeTestThreshold-0.01), 2, false)
	require.NoError(t, err)
	require.NotNil(t, lease, "balance one cent below threshold still reserves")
	defer lease.HandlerDone()
	require.EqualValues(t, 1, repo.reserves.Load())
	require.InDelta(t, 2, repo.amount, 1e-9)
}

// exclusive 是无价模型的并发串行保护（无价按 0 元结算，不串行就能无限并发）：余额再高也必须走 repo 预占。
// 非有限估算金额会被改写成 exclusive，同样不跳过。
func TestBillingInflight_LockFreeExclusiveStillReservesEvenWithHighBalance(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	for name, tc := range map[string]struct {
		amount    float64
		exclusive bool
	}{
		"unknown price exclusive":         {0, true},
		"priced estimate but exclusive":   {2, true},
		"NaN estimate (rewritten)":        {math.NaN(), false},
		"+Inf estimate (rewritten)":       {math.Inf(1), false},
		"negative estimate (rewritten)":   {-1, false},
		"NaN estimate with exclusive set": {math.NaN(), true},
	} {
		t.Run(name, func(t *testing.T) {
			for _, balance := range []float64{lockFreeTestThreshold, 991228} {
				repo := newLockFreeSpyRepo()
				lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), tc.amount, tc.exclusive)
				require.NoError(t, err)
				require.NotNil(t, lease, "exclusive hold must not be skipped (balance=%v)", balance)
				require.EqualValues(t, 1, repo.reserves.Load())
				require.True(t, repo.exclusive)
				lease.HandlerDone()
			}
		})
	}
	// 对照：同样高余额下，有价且非 exclusive 的请求跳过；金额 0 且非 exclusive 本来就不预占。
	repo := newLockFreeSpyRepo()
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(991228), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	lease, err = applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(991228), 0, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	repo.requireUntouched(t, "priced / zero-price non-exclusive")
}

// 无价模型的串行保护在高余额下依然生效：同一用户第二个 exclusive 请求在 repo 里仍会被拒（这里用 repo 桩模拟 others>0 的拒绝）。
func TestBillingInflight_LockFreeExclusiveDenialStillSurfacesWithHighBalance(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	repo := newLockFreeSpyRepo()
	repo.allow = false // repo: 已有其它 attempt 持有，exclusive 被拒
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(991228), 0, true)
	require.Nil(t, lease)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.EqualValues(t, 1, repo.reserves.Load())
}

// 已有 lease（同一请求重试换号）：即使余额达到阈值也走 Resize，不丢 lease、不跳过。
func TestBillingInflight_LockFreeExistingLeaseStillResizes(t *testing.T) {
	cfg := inflightTestConfig()
	repo := newLockFreeSpyRepo()
	request := lockFreeRequest(991228)
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, request, 1, false) // 阈值未配置：建 lease
	require.NoError(t, err)
	require.NotNil(t, lease)
	defer lease.HandlerDone()
	require.EqualValues(t, 1, repo.reserves.Load())

	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	ctx := WithBillingInflightLease(context.Background(), lease)
	got, err := applyInflightEstimate(ctx, repo, cfg, request, 3, false)
	require.NoError(t, err)
	require.Same(t, lease, got)
	require.EqualValues(t, 1, repo.resizes.Load(), "existing lease must keep Resize")
	require.EqualValues(t, 1, repo.reserves.Load(), "no second owner reservation")
	require.InDelta(t, 3, repo.amount, 1e-9)

	// Resize 被拒时错误照常返回（不被跳过逻辑吞掉）。
	repo.allow = false
	_, err = applyInflightEstimate(ctx, repo, cfg, request, 3, false)
	require.ErrorIs(t, err, ErrInsufficientBalance)
}

// ---- 拦截不失效：余额不足 / 订阅限额 ----

// 余额低于阈值的用户（含 0、负数）预占的拒绝语义不变：余额不足与三种订阅限额错误原样返回。
func TestBillingInflight_LockFreeBelowThresholdStillDenied(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	for _, balance := range []float64{0, -3, 0.5, lockFreeTestThreshold - 0.01} {
		for _, denial := range []error{ErrInsufficientBalance, ErrDailyLimitExceeded, ErrWeeklyLimitExceeded, ErrMonthlyLimitExceeded} {
			repo := newLockFreeSpyRepo()
			repo.allow, repo.err = false, denial
			lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), 1, false)
			require.Nil(t, lease)
			require.ErrorIs(t, err, denial, "balance=%v", balance)
			require.EqualValues(t, 1, repo.reserves.Load())
		}
		// 仓储判定并发容量不足（allowed=false, err=nil）→ ErrInsufficientBalance。
		repo := newLockFreeSpyRepo()
		repo.allow = false
		lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), 1, false)
		require.Nil(t, lease)
		require.ErrorIs(t, err, ErrInsufficientBalance)
	}
}

// 订阅卡限额不会被绕过的依据：预占里的限额准入就是 service.AdmitWindow（repository/billing_inflight_repo.go
// billingInflightCapacity 里同一个函数），而 AdmitWindow 在钱包余额 > 0 时恒为 true——撞窗口上限会回落钱包。
// 阈值为正，所以「余额 >= 阈值」的用户在预占里不可能得到 DAILY/WEEKLY/MONTHLY 拒绝，跳过不丢任何限额拦截。
// 余额 <= 0 的用户不会被跳过（见 BelowThresholdStillDenied），原样走预占，限额错误照常返回。
func TestBillingInflight_LockFreeSubscriptionAdmissionNeverRejectsAtOrAboveThreshold(t *testing.T) {
	now := time.Now()
	start := now
	exhausted := func(daily, weekly, monthly bool) *SubWindow {
		c := &SubWindow{
			DailyLimitUSD: 10, WeeklyLimitUSD: 50, MonthlyLimitUSD: 100,
			DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start,
			ExpiresAt: now.Add(24 * time.Hour), Status: SubscriptionStatusActive,
		}
		if daily {
			c.DailyUsageUSD = 10
		}
		if weekly {
			c.WeeklyUsageUSD = 50
		}
		if monthly {
			c.MonthlyUsageUSD = 100
		}
		return c
	}
	cards := map[string]*SubWindow{
		"no card":         nil,
		"unused card":     exhausted(false, false, false),
		"daily exhausted": exhausted(true, false, false),
		"weekly exhaust":  exhausted(false, true, false),
		"monthly exhaust": exhausted(false, false, true),
		"all exhausted":   exhausted(true, true, true),
		"expired card": {
			DailyLimitUSD: 10, DailyUsageUSD: 10, DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start,
			ExpiresAt: now.Add(-time.Hour), Status: SubscriptionStatusActive,
		},
		"status expired": {
			DailyLimitUSD: 10, DailyUsageUSD: 10, DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start,
			ExpiresAt: now.Add(time.Hour), Status: SubscriptionStatusExpired,
		},
	}
	for name, card := range cards {
		for _, balance := range []float64{lockFreeTestThreshold, 991228, 1e9} {
			var c *SubWindow
			if card != nil {
				cp := *card
				c = &cp
			}
			require.True(t, AdmitWindow(c, &WalletState{Balance: balance}, now), "%s balance=%v: admission cannot reject a funded wallet", name, balance)
		}
	}
	// 对照：同一张撞限额的卡，钱包 <= 0 时确实会被拒——这类用户余额低于阈值，不会被跳过。
	c := *cards["all exhausted"]
	require.False(t, AdmitWindow(&c, &WalletState{Balance: 0}, now))
	c = *cards["all exhausted"]
	require.False(t, AdmitWindow(&c, &WalletState{Balance: -1}, now))
}

// ---- 两个入口 ----

// 入口完整链路（有价模型）：余额够 -> 无 lease 且仓储未碰；余额不够 -> 照常估价并建 lease；已有 lease -> Resize。
func TestBillingInflight_LockFreeBothReserveEntrancesFullPath(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	body := []byte(`{"model":"deepseek-v4-flash","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`)

	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			newSvc := func(repo UsageBillingRepository) (*APIKey, func(context.Context, BillingInflightRequest) (*BillingInflightLease, error)) {
				key := gatewayDeepSeekAPIKey(1701, platform)
				gateway := newGatewayDeepSeekBillingService(nil, *key.GroupID, platform)
				gateway.cfg = cfg
				gateway.usageBillingRepo = repo
				if platform == PlatformOpenAI {
					openAI := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: gateway.billingService, resolver: gateway.resolver, channelService: gateway.channelService}
					return key, openAI.ReserveBillingInflight
				}
				return key, gateway.ReserveBillingInflight
			}

			// 余额够：跳过。
			repo := newLockFreeSpyRepo()
			key, reserve := newSvc(repo)
			key.User = &User{ID: 1, Balance: lockFreeTestThreshold}
			lease, err := reserve(context.Background(), BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.Nil(t, lease)
			repo.requireUntouched(t, "high balance")

			// 余额差一分：照常建 lease。
			repo = newLockFreeSpyRepo()
			key, reserve = newSvc(repo)
			key.User = &User{ID: 1, Balance: lockFreeTestThreshold - 0.01}
			lease, err = reserve(context.Background(), BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.NotNil(t, lease)
			defer lease.HandlerDone()
			require.EqualValues(t, 1, repo.reserves.Load())
			require.Positive(t, repo.amount)

			// 余额够 + ctx 里已有 lease：走 Resize，不丢 lease。
			key.User = &User{ID: 1, Balance: 991228}
			ctx := WithBillingInflightLease(context.Background(), lease)
			got, err := reserve(ctx, BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.Same(t, lease, got)
			require.EqualValues(t, 1, repo.resizes.Load())
			require.EqualValues(t, 1, repo.reserves.Load())
		})
	}
}

// 未知价格模型经两个真实入口：余额再高也仍是 exclusive 预占（串行保护不能被跳过）。
func TestBillingInflight_LockFreeUnknownPriceThroughEntrances(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	body := []byte(`{"input":"hello"}`)

	run := func(t *testing.T, build func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error)) {
		for _, balance := range []float64{991228, 100} {
			repo := newLockFreeSpyRepo()
			reserve := build(repo)
			lease, err := reserve(context.Background(), BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 1, Balance: balance}}, Model: "unknown-unpriced-1515", Body: body})
			require.NoError(t, err)
			require.NotNil(t, lease, "balance=%v", balance)
			require.EqualValues(t, 1, repo.reserves.Load())
			require.True(t, repo.exclusive, "unknown models keep the exclusive hold regardless of balance")
			lease.HandlerDone()
		}
	}
	t.Run("openai", func(t *testing.T) {
		run(t, func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error) {
			return (&OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil)}).ReserveBillingInflight
		})
	})
	t.Run("gateway", func(t *testing.T) {
		run(t, func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error) {
			gateway := newGatewayDeepSeekBillingService(nil, 1701, PlatformAnthropic)
			gateway.cfg = cfg
			gateway.usageBillingRepo = repo
			return gateway.ReserveBillingInflight
		})
	})
}

// 入口仍遵守总开关与 user==nil 的既有早退，不因新逻辑变化。
func TestBillingInflight_LockFreeEntranceKeepsEarlyReturns(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	cfg.Billing.InflightReservation.Enabled = false
	repo := newLockFreeSpyRepo()
	svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil)}
	lease, err := svc.ReserveBillingInflight(context.Background(), lockFreeRequest(1))
	require.NoError(t, err)
	require.Nil(t, lease)

	cfg.Billing.InflightReservation.Enabled = true
	lease, err = svc.ReserveBillingInflight(context.Background(), BillingInflightRequest{APIKey: &APIKey{}})
	require.NoError(t, err)
	require.Nil(t, lease)
	repo.requireUntouched(t, "early returns")
}

// ---- 并发（配合 -race）----

func TestBillingInflight_LockFreeConcurrentMixedBalances(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	repo := newLockFreeSpyRepo()
	const workers, perWorker = 32, 20

	var wg sync.WaitGroup
	var highNonNil, lowNil, errs atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				high := (w+i)%2 == 0
				balance := 10.0
				if high {
					balance = 991228
				}
				lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeRequest(balance), 1, false)
				if err != nil {
					errs.Add(1)
					continue
				}
				if high && lease != nil {
					highNonNil.Add(1)
				}
				if !high && lease == nil {
					lowNil.Add(1)
				}
				lease.HandlerDone() // nil 安全
			}
		}(w)
	}
	wg.Wait()
	require.Zero(t, errs.Load())
	require.Zero(t, highNonNil.Load(), "high balance requests never get a lease")
	require.Zero(t, lowNil.Load(), "low balance requests always get a lease")
	require.EqualValues(t, workers*perWorker/2, repo.reserves.Load(), "only low-balance requests reserve")
	require.EqualValues(t, workers*perWorker/2, repo.releases.Load(), "every created lease is released")
}

// 配置在请求间被热改成关闭/开启时，判断只读 cfg，无状态；并发读写下无数据竞争（读写同一 cfg 由调用方保证，这里只并发读）。
func TestBillingInflight_LockFreeConcurrentPredicateIsStateless(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.LockFreeMinBalance = lockFreeTestThreshold
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if !skipInflightReservationForBalance(context.Background(), cfg, lockFreeTestThreshold) {
					t.Error("expected skip")
					return
				}
				if skipInflightReservationForBalance(context.Background(), cfg, lockFreeTestThreshold-0.01) {
					t.Error("expected no skip")
					return
				}
			}
		}()
	}
	wg.Wait()
}
