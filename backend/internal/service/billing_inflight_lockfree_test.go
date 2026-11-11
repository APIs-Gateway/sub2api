//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// lockFreeSpyRepo 记录预占仓储的每一次调用，用来证明「跳过」时一次都没碰过（不碰就不会锁 users 行）。
type lockFreeSpyRepo struct {
	inflightCaptureRepo
	reserves, resizes, renews, releases, stages, finishes atomic.Int64

	// 只读准入（BillingLockFreeAdmissionReader）：特权用户跳过预占时改走这里。
	admits     atomic.Int64
	admitDeny  bool  // allowed=false
	admitErr   error // 非 nil 时返回该错误
	admitBlock bool  // 阻塞到 ctx 结束（模拟读库超时）
}

func (r *lockFreeSpyRepo) AdmitBillingLockFree(ctx context.Context, userID int64) (bool, error) {
	r.admits.Add(1)
	if r.admitBlock {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if r.admitErr != nil {
		return false, r.admitErr
	}
	return !r.admitDeny, nil
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

const (
	lockFreePrivilegedUserID   = int64(1) // 在特权名单里
	lockFreeUnprivilegedUserID = int64(2) // 不在名单里
)

// lockFreeRequest 构造某个用户的预占请求；余额只用来证明「余额不影响特权判定」。
func lockFreeRequest(userID int64, balance float64) BillingInflightRequest {
	return BillingInflightRequest{APIKey: &APIKey{User: &User{ID: userID, Balance: balance}}}
}

var lockFreeBalances = []float64{0, -5, 0.01, 500000, 991228, 1e12}

// ---- 有特权：有价请求跳过预占 ----

func TestBillingInflight_LockFreePrivilegedPricedRequestSkipsReservation(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for _, balance := range lockFreeBalances { // 特权与余额无关
		repo := newLockFreeSpyRepo()
		lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, balance), 2, false)
		require.NoError(t, err)
		require.Nil(t, lease, "balance=%v", balance)
		repo.requireUntouched(t, "privileged priced request")
		require.EqualValues(t, 1, repo.admits.Load(), "跳过预占，但做一次只读准入")
	}
}

// ---- 无特权：与改动前（上游原版）逐项相同 ----

type lockFreeBaselineCase struct {
	name      string
	amount    float64
	exclusive bool
	hasLease  bool
}

var lockFreeBaselineCases = []lockFreeBaselineCase{
	{"priced", 2, false, false},
	{"priced with existing lease", 3, false, true},
	{"zero price non-exclusive", 0, false, false},
	{"zero price non-exclusive with existing lease", 0, false, true},
	{"exclusive unknown price", 0, true, false},
	{"exclusive priced", 2, true, false},
	{"exclusive with existing lease", 0, true, true},
	{"NaN amount rewritten to exclusive", math.NaN(), false, false},
	{"+Inf amount rewritten to exclusive", math.Inf(1), false, false},
	{"negative amount rewritten to exclusive", -1, false, false},
}

type lockFreeOutcome struct {
	lease                    bool
	reserves, resizes        int64
	amount                   float64
	exclusive                bool
	leaseIsTheOneFromContext bool
}

func runLockFreeBaselineCase(t *testing.T, resolver *BillingLockFreeUsers, userID int64, tc lockFreeBaselineCase) lockFreeOutcome {
	t.Helper()
	cfg := inflightTestConfig()
	repo := newLockFreeSpyRepo()
	ctx := context.Background()
	var existing *BillingInflightLease
	if tc.hasLease {
		var err error
		existing, err = applyInflightEstimate(ctx, repo, cfg, nil, lockFreeRequest(userID, 10), 1, true) // 用 exclusive 建 lease（不受特权影响）
		require.NoError(t, err)
		require.NotNil(t, existing)
		defer existing.HandlerDone()
		ctx = WithBillingInflightLease(ctx, existing)
		repo.reserves.Store(0)
		repo.resizes.Store(0)
	}
	lease, err := applyInflightEstimate(ctx, repo, cfg, resolver, lockFreeRequest(userID, 991228), tc.amount, tc.exclusive)
	require.NoError(t, err)
	if lease != nil && !tc.hasLease {
		defer lease.HandlerDone()
	}
	out := lockFreeOutcome{lease: lease != nil, reserves: repo.reserves.Load(), resizes: repo.resizes.Load()}
	repo.inflightCaptureRepo.mu.Lock()
	out.amount, out.exclusive = repo.amount, repo.exclusive
	repo.inflightCaptureRepo.mu.Unlock()
	out.leaseIsTheOneFromContext = lease != nil && lease == existing
	return out
}

// 没有特权的用户（解析器为 nil、名单为空、用户不在名单里）：三种解析器状态下的结果必须逐项一致，
// 且与上游原版语义一致：有价预占、exclusive 预占、已有 lease Resize、金额 0 且非 exclusive 不预占。
func TestBillingInflight_LockFreeUnprivilegedBehavesLikeUpstream(t *testing.T) {
	emptyResolver := lockFreeTestResolver(t)
	otherUserResolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for _, tc := range lockFreeBaselineCases {
		t.Run(tc.name, func(t *testing.T) {
			nilOut := runLockFreeBaselineCase(t, nil, lockFreeUnprivilegedUserID, tc)
			require.Equal(t, nilOut, runLockFreeBaselineCase(t, emptyResolver, lockFreeUnprivilegedUserID, tc), "空名单")
			require.Equal(t, nilOut, runLockFreeBaselineCase(t, otherUserResolver, lockFreeUnprivilegedUserID, tc), "名单里是别人")

			exclusive := tc.exclusive || math.IsNaN(tc.amount) || math.IsInf(tc.amount, 0) || tc.amount < 0
			switch {
			case tc.hasLease:
				require.True(t, nilOut.lease)
				require.True(t, nilOut.leaseIsTheOneFromContext, "已有 lease：返回同一个 lease")
				require.EqualValues(t, 1, nilOut.resizes, "已有 lease 照旧 Resize")
				require.Zero(t, nilOut.reserves)
			case exclusive:
				require.True(t, nilOut.lease, "exclusive 照旧预占")
				require.EqualValues(t, 1, nilOut.reserves)
				require.True(t, nilOut.exclusive)
			case tc.amount == 0:
				require.False(t, nilOut.lease, "金额 0 且非 exclusive 本来就不预占")
				require.Zero(t, nilOut.reserves)
			default:
				require.True(t, nilOut.lease, "有价请求照旧预占")
				require.EqualValues(t, 1, nilOut.reserves)
				require.InDelta(t, tc.amount, nilOut.amount, 1e-9)
				require.False(t, nilOut.exclusive)
			}
		})
	}
}

// 无特权的用户不管余额多少都预占（余额不再影响判定）。
func TestBillingInflight_LockFreeUnprivilegedReservesRegardlessOfBalance(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for _, balance := range lockFreeBalances {
		for name, r := range map[string]*BillingLockFreeUsers{"nil": nil, "other privileged": resolver} {
			repo := newLockFreeSpyRepo()
			lease, err := applyInflightEstimate(context.Background(), repo, cfg, r, lockFreeRequest(lockFreeUnprivilegedUserID, balance), 1.5, false)
			require.NoError(t, err)
			require.NotNil(t, lease, "%s balance=%v", name, balance)
			require.EqualValues(t, 1, repo.reserves.Load())
			lease.HandlerDone()
		}
	}
	for _, balance := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		repo := newLockFreeSpyRepo()
		lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, balance), 1.5, false)
		require.NoError(t, err)
		require.NotNil(t, lease, "balance=%v", balance)
		lease.HandlerDone()
	}
}

// 无特权用户的预占拒绝语义不变：余额不足与三种订阅限额错误原样返回。
func TestBillingInflight_LockFreeUnprivilegedDenialsStillSurface(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for _, balance := range []float64{0, -3, 0.5, 991228} {
		for _, denial := range []error{ErrInsufficientBalance, ErrDailyLimitExceeded, ErrWeeklyLimitExceeded, ErrMonthlyLimitExceeded} {
			repo := newLockFreeSpyRepo()
			repo.allow, repo.err = false, denial
			lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, balance), 1, false)
			require.Nil(t, lease)
			require.ErrorIs(t, err, denial, "balance=%v", balance)
			require.EqualValues(t, 1, repo.reserves.Load())
		}
		// 仓储判定并发容量不足（allowed=false, err=nil）→ ErrInsufficientBalance。
		repo := newLockFreeSpyRepo()
		repo.allow = false
		lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, balance), 1, false)
		require.Nil(t, lease)
		require.ErrorIs(t, err, ErrInsufficientBalance)
	}
}

// ---- 有特权但 exclusive：仍预占 ----

// exclusive 是无价模型的并发串行保护（无价按 0 元结算，不串行就能无限并发）：特权用户也必须走 repo 预占。
// 非有限估算金额会被改写成 exclusive，同样不跳过。
func TestBillingInflight_LockFreePrivilegedExclusiveStillReserves(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for name, tc := range map[string]struct {
		amount    float64
		exclusive bool
	}{
		"unknown price exclusive":         {0, true},
		"priced estimate but exclusive":   {2, true},
		"NaN estimate (rewritten)":        {math.NaN(), false},
		"+Inf estimate (rewritten)":       {math.Inf(1), false},
		"-Inf estimate (rewritten)":       {math.Inf(-1), false},
		"negative estimate (rewritten)":   {-1, false},
		"NaN estimate with exclusive set": {math.NaN(), true},
	} {
		t.Run(name, func(t *testing.T) {
			for _, balance := range []float64{0, 991228} {
				repo := newLockFreeSpyRepo()
				lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, balance), tc.amount, tc.exclusive)
				require.NoError(t, err)
				require.NotNil(t, lease, "exclusive hold must not be skipped (balance=%v)", balance)
				require.EqualValues(t, 1, repo.reserves.Load())
				require.True(t, repo.exclusive)
				lease.HandlerDone()
			}
		})
	}
	// 对照：特权用户有价且非 exclusive 跳过；金额 0 且非 exclusive 本来就不预占。
	repo := newLockFreeSpyRepo()
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 991228), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	lease, err = applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 991228), 0, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	repo.requireUntouched(t, "priced / zero-price non-exclusive")
}

// 无价模型的串行保护对特权用户依然生效：第二个 exclusive 请求在 repo 里仍会被拒（这里用 repo 桩模拟 others>0 的拒绝）。
func TestBillingInflight_LockFreePrivilegedExclusiveDenialStillSurfaces(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	repo := newLockFreeSpyRepo()
	repo.allow = false // repo: 已有其它 attempt 持有，exclusive 被拒
	lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 991228), 0, true)
	require.Nil(t, lease)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.EqualValues(t, 1, repo.reserves.Load())
}

// ---- 有特权且已有 lease：照旧 Resize ----

// 已有 lease（同一请求重试换号）：特权用户也走 Resize，不丢 lease、不跳过。
func TestBillingInflight_LockFreePrivilegedExistingLeaseStillResizes(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	repo := newLockFreeSpyRepo()
	request := lockFreeRequest(lockFreePrivilegedUserID, 991228)
	first, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, request, 1, true) // exclusive：建 lease
	require.NoError(t, err)
	require.NotNil(t, first)
	defer first.HandlerDone()
	require.EqualValues(t, 1, repo.reserves.Load())

	ctx := WithBillingInflightLease(context.Background(), first)
	got, err := applyInflightEstimate(ctx, repo, cfg, resolver, request, 3, false)
	require.NoError(t, err)
	require.Same(t, first, got)
	require.EqualValues(t, 1, repo.resizes.Load(), "existing lease must keep Resize")
	require.EqualValues(t, 1, repo.reserves.Load(), "no second owner reservation")
	require.InDelta(t, 3, repo.amount, 1e-9)

	// Resize 被拒时错误照常返回（不被跳过逻辑吞掉）。
	repo.allow = false
	_, err = applyInflightEstimate(ctx, repo, cfg, resolver, request, 3, false)
	require.ErrorIs(t, err, ErrInsufficientBalance)
}

// ---- 两个入口 ----

// 入口完整链路（有价模型）：特权 -> 无 lease 且仓储未碰；无特权 -> 照常估价并建 lease；特权且已有 lease -> Resize。
func TestBillingInflight_LockFreeBothReserveEntrancesFullPath(t *testing.T) {
	cfg := inflightTestConfig()
	body := []byte(`{"model":"deepseek-v4-flash","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`)
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)

	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			newSvc := func(repo UsageBillingRepository) (*APIKey, func(context.Context, BillingInflightRequest) (*BillingInflightLease, error)) {
				key := gatewayDeepSeekAPIKey(1701, platform)
				gateway := newGatewayDeepSeekBillingService(nil, *key.GroupID, platform)
				gateway.cfg = cfg
				gateway.usageBillingRepo = repo
				gateway.billingLockFree = resolver
				if platform == PlatformOpenAI {
					openAI := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: gateway.billingService, resolver: gateway.resolver, channelService: gateway.channelService, billingLockFree: resolver}
					return key, openAI.ReserveBillingInflight
				}
				return key, gateway.ReserveBillingInflight
			}

			// 特权：跳过（余额为 0 也一样）。
			repo := newLockFreeSpyRepo()
			key, reserve := newSvc(repo)
			key.User = &User{ID: lockFreePrivilegedUserID, Balance: 0}
			lease, err := reserve(context.Background(), BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.Nil(t, lease)
			repo.requireUntouched(t, "privileged")

			// 无特权（即使余额很高）：照常建 lease。
			repo = newLockFreeSpyRepo()
			key, reserve = newSvc(repo)
			key.User = &User{ID: lockFreeUnprivilegedUserID, Balance: 991228}
			lease, err = reserve(context.Background(), BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.NotNil(t, lease)
			defer lease.HandlerDone()
			require.EqualValues(t, 1, repo.reserves.Load())
			require.Positive(t, repo.amount)

			// 特权 + ctx 里已有 lease：走 Resize，不丢 lease。
			key.User = &User{ID: lockFreePrivilegedUserID, Balance: 991228}
			ctx := WithBillingInflightLease(context.Background(), lease)
			got, err := reserve(ctx, BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body})
			require.NoError(t, err)
			require.Same(t, lease, got)
			require.EqualValues(t, 1, repo.resizes.Load())
			require.EqualValues(t, 1, repo.reserves.Load())
		})
	}
}

// 未知价格模型经两个真实入口：特权用户也仍是 exclusive 预占（串行保护不能被跳过）。
func TestBillingInflight_LockFreeUnknownPriceThroughEntrances(t *testing.T) {
	cfg := inflightTestConfig()
	body := []byte(`{"input":"hello"}`)
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)

	run := func(t *testing.T, build func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error)) {
		for _, userID := range []int64{lockFreePrivilegedUserID, lockFreeUnprivilegedUserID} {
			repo := newLockFreeSpyRepo()
			reserve := build(repo)
			lease, err := reserve(context.Background(), BillingInflightRequest{APIKey: &APIKey{User: &User{ID: userID, Balance: 991228}}, Model: "unknown-unpriced-1515", Body: body})
			require.NoError(t, err)
			require.NotNil(t, lease, "user=%d", userID)
			require.EqualValues(t, 1, repo.reserves.Load())
			require.True(t, repo.exclusive, "unknown models keep the exclusive hold regardless of privilege")
			lease.HandlerDone()
		}
	}
	t.Run("openai", func(t *testing.T) {
		run(t, func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error) {
			return (&OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil), billingLockFree: resolver}).ReserveBillingInflight
		})
	})
	t.Run("gateway", func(t *testing.T) {
		run(t, func(repo UsageBillingRepository) func(context.Context, BillingInflightRequest) (*BillingInflightLease, error) {
			gateway := newGatewayDeepSeekBillingService(nil, 1701, PlatformAnthropic)
			gateway.cfg = cfg
			gateway.usageBillingRepo = repo
			gateway.billingLockFree = resolver
			return gateway.ReserveBillingInflight
		})
	})
}

// 入口仍遵守总开关与 user==nil 的既有早退，不因新逻辑变化。
func TestBillingInflight_LockFreeEntranceKeepsEarlyReturns(t *testing.T) {
	cfg := inflightTestConfig()
	cfg.Billing.InflightReservation.Enabled = false
	repo := newLockFreeSpyRepo()
	svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil), billingLockFree: lockFreeTestResolver(t, lockFreePrivilegedUserID)}
	lease, err := svc.ReserveBillingInflight(context.Background(), lockFreeRequest(lockFreeUnprivilegedUserID, 1))
	require.NoError(t, err)
	require.Nil(t, lease)

	cfg.Billing.InflightReservation.Enabled = true
	lease, err = svc.ReserveBillingInflight(context.Background(), BillingInflightRequest{APIKey: &APIKey{}})
	require.NoError(t, err)
	require.Nil(t, lease)
	repo.requireUntouched(t, "early returns")
}

// ---- 并发（配合 -race）----

func TestBillingInflight_LockFreeConcurrentMixedUsers(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	repo := newLockFreeSpyRepo()
	const workers, perWorker = 32, 20

	var wg sync.WaitGroup
	var privilegedNonNil, unprivilegedNil, errs atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				privileged := (w+i)%2 == 0
				userID := lockFreeUnprivilegedUserID
				if privileged {
					userID = lockFreePrivilegedUserID
				}
				lease, err := applyInflightEstimate(context.Background(), repo, cfg, resolver, lockFreeRequest(userID, 10), 1, false)
				if err != nil {
					errs.Add(1)
					continue
				}
				if privileged && lease != nil {
					privilegedNonNil.Add(1)
				}
				if !privileged && lease == nil {
					unprivilegedNil.Add(1)
				}
				lease.HandlerDone() // nil 安全
			}
		}(w)
	}
	wg.Wait()
	require.Zero(t, errs.Load())
	require.Zero(t, privilegedNonNil.Load(), "privileged requests never get a lease")
	require.Zero(t, unprivilegedNil.Load(), "unprivileged requests always get a lease")
	require.EqualValues(t, workers*perWorker/2, repo.reserves.Load(), "only unprivileged requests reserve")
	require.EqualValues(t, workers*perWorker/2, repo.releases.Load(), "every created lease is released")
	require.EqualValues(t, workers*perWorker/2, repo.admits.Load(), "每个特权请求恰好做一次只读准入")
}

// ---- 特权用户的只读准入（余额 / 订阅限额），不丢准入 ----

func lockFreePrivilegedApply(t *testing.T, repo UsageBillingRepository, cfg *config.Config, ctx context.Context, amount float64, exclusive bool) (*BillingInflightLease, error) {
	t.Helper()
	return applyInflightEstimate(ctx, repo, cfg, lockFreeTestResolver(t, lockFreePrivilegedUserID), lockFreeRequest(lockFreePrivilegedUserID, 991228), amount, exclusive)
}

// 只读准入被拒：返回与「无特权用户预占被拒」相同的错误；不建 lease、预占 repo 一次没碰。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionDenialMatchesReservationDenial(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)

	// allowed=false -> ErrInsufficientBalance（与预占 allowed=false 一致）。
	priv := newLockFreeSpyRepo()
	priv.admitDeny = true
	lease, privErr := applyInflightEstimate(context.Background(), priv, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 0), 2, false)
	require.Nil(t, lease)
	require.ErrorIs(t, privErr, ErrInsufficientBalance)
	priv.requireUntouched(t, "denied read-only admission")
	require.EqualValues(t, 1, priv.admits.Load())

	unpriv := newLockFreeSpyRepo()
	unpriv.allow = false
	lease, unprivErr := applyInflightEstimate(context.Background(), unpriv, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, 0), 2, false)
	require.Nil(t, lease)
	require.Equal(t, unprivErr, privErr, "错误与无特权用户预占被拒完全相同")
	require.Equal(t, unprivErr.Error(), privErr.Error())

	// 三种订阅限额错误原样返回。
	for _, denial := range []error{ErrInsufficientBalance, ErrDailyLimitExceeded, ErrWeeklyLimitExceeded, ErrMonthlyLimitExceeded} {
		priv := newLockFreeSpyRepo()
		priv.admitErr = denial
		lease, err := applyInflightEstimate(context.Background(), priv, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 5), 1, false)
		require.Nil(t, lease)
		require.Equal(t, denial, err)
		priv.requireUntouched(t, "denial")

		unpriv := newLockFreeSpyRepo()
		unpriv.allow, unpriv.err = false, denial
		_, unprivErr := applyInflightEstimate(context.Background(), unpriv, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, 5), 1, false)
		require.Equal(t, unprivErr, err)
	}
}

// 读失败：与预占失败一致，fail-open（不拒绝、没有 lease）。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionFailureFailsOpenLikeReservation(t *testing.T) {
	cfg := inflightTestConfig()
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	boom := errors.New("connection refused")

	priv := newLockFreeSpyRepo()
	priv.admitErr = boom
	lease, err := applyInflightEstimate(context.Background(), priv, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 0), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	priv.requireUntouched(t, "failed read-only admission")

	unpriv := newLockFreeSpyRepo()
	unpriv.err = boom
	lease, err = applyInflightEstimate(context.Background(), unpriv, cfg, resolver, lockFreeRequest(lockFreeUnprivilegedUserID, 0), 2, false)
	require.NoError(t, err, "对照：预占失败同样 fail-open")
	require.Nil(t, lease)

	// 用户不存在属于「非拒绝错误」，两边都放行。
	priv = newLockFreeSpyRepo()
	priv.admitErr = ErrUserNotFound
	lease, err = applyInflightEstimate(context.Background(), priv, cfg, resolver, lockFreeRequest(lockFreePrivilegedUserID, 0), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease)
}

// 读库超时（沿用预占的 2 秒超时）：fail-open，且不会无限等。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionTimeoutFailsOpen(t *testing.T) {
	cfg := inflightTestConfig()
	repo := newLockFreeSpyRepo()
	repo.admitBlock = true
	start := time.Now()
	lease, err := lockFreePrivilegedApply(t, repo, cfg, context.Background(), 2, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 1900*time.Millisecond, "超时应是 2 秒")
	require.Less(t, elapsed, 4*time.Second)
	repo.requireUntouched(t, "timed out read-only admission")
}

// 没有实现只读准入的 repo：fail-closed——不跳过，照旧预占。
func TestBillingInflight_LockFreePrivilegedWithoutAdmissionReaderFallsBackToReservation(t *testing.T) {
	cfg := inflightTestConfig()
	repo := &inflightCaptureRepo{allow: true}
	_, isReader := interface{}(repo).(BillingLockFreeAdmissionReader)
	require.False(t, isReader, "测试前提：这个 repo 没有只读准入")
	lease, err := lockFreePrivilegedApply(t, repo, cfg, context.Background(), 2, false)
	require.NoError(t, err)
	require.NotNil(t, lease, "类型断言失败：回退到预占")
	defer lease.HandlerDone()
	require.InDelta(t, 2, repo.amount, 1e-9)

	// 回退后的拒绝照旧。
	denied := &inflightCaptureRepo{allow: false}
	lease, err = lockFreePrivilegedApply(t, denied, cfg, context.Background(), 2, false)
	require.Nil(t, lease)
	require.ErrorIs(t, err, ErrInsufficientBalance)

	// repo 为 nil 接口时也不 panic，行为与无特权一致（没有预占能力）。
	require.NotPanics(t, func() {
		lease, err := applyInflightEstimate(context.Background(), nil, cfg, lockFreeTestResolver(t, lockFreePrivilegedUserID), lockFreeRequest(lockFreePrivilegedUserID, 1), 2, false)
		require.NoError(t, err)
		require.Nil(t, lease)
	})
}

// 预占本身不生效的配置下（总开关关、简单模式、nil 配置），只读准入也不做事，与预占一致。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionHonorsReservationSwitches(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config) *config.Config{
		"disabled":    func(c *config.Config) *config.Config { c.Billing.InflightReservation.Enabled = false; return c },
		"simple mode": func(c *config.Config) *config.Config { c.RunMode = config.RunModeSimple; return c },
		"nil config":  func(c *config.Config) *config.Config { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			repo := newLockFreeSpyRepo()
			repo.admitDeny = true // 即使会拒绝，也不应被调用
			lease, err := lockFreePrivilegedApply(t, repo, mutate(inflightTestConfig()), context.Background(), 2, false)
			require.NoError(t, err)
			require.Nil(t, lease)
			require.Zero(t, repo.admits.Load())
			repo.requireUntouched(t, name)
		})
	}
}

// 不该准入的情形：金额 0 且非 exclusive（本来就不预占）；已有 lease（走 Resize）；exclusive（走预占）。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionOnlyForPricedFreshRequests(t *testing.T) {
	cfg := inflightTestConfig()

	repo := newLockFreeSpyRepo()
	repo.admitDeny = true
	lease, err := lockFreePrivilegedApply(t, repo, cfg, context.Background(), 0, false)
	require.NoError(t, err)
	require.Nil(t, lease)
	require.Zero(t, repo.admits.Load(), "金额 0 且非 exclusive：不预占也不准入（与无特权一致）")

	repo = newLockFreeSpyRepo()
	repo.admitDeny = true
	lease, err = lockFreePrivilegedApply(t, repo, cfg, context.Background(), 2, true)
	require.NoError(t, err)
	require.NotNil(t, lease, "exclusive 仍预占")
	defer lease.HandlerDone()
	require.Zero(t, repo.admits.Load())
	require.EqualValues(t, 1, repo.reserves.Load())

	ctx := WithBillingInflightLease(context.Background(), lease)
	got, err := lockFreePrivilegedApply(t, repo, cfg, ctx, 3, false)
	require.NoError(t, err)
	require.Same(t, lease, got)
	require.Zero(t, repo.admits.Load(), "已有 lease：照旧 Resize，不再准入")
	require.EqualValues(t, 1, repo.resizes.Load())
}

// 两个真实入口：特权用户余额 / 限额不足时被拒，错误与无特权一致。
func TestBillingInflight_LockFreePrivilegedReadOnlyAdmissionDeniesThroughEntrances(t *testing.T) {
	cfg := inflightTestConfig()
	body := []byte(`{"model":"deepseek-v4-flash","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`)
	resolver := lockFreeTestResolver(t, lockFreePrivilegedUserID)
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			reserve := func(repo UsageBillingRepository, userID int64) (*BillingInflightLease, error) {
				key := gatewayDeepSeekAPIKey(1701, platform)
				key.User = &User{ID: userID, Balance: 991228}
				gateway := newGatewayDeepSeekBillingService(nil, *key.GroupID, platform)
				gateway.cfg = cfg
				gateway.usageBillingRepo = repo
				gateway.billingLockFree = resolver
				req := BillingInflightRequest{APIKey: key, Model: "deepseek-v4-flash", Body: body}
				if platform == PlatformOpenAI {
					openAI := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: gateway.billingService, resolver: gateway.resolver, channelService: gateway.channelService, billingLockFree: resolver}
					return openAI.ReserveBillingInflight(context.Background(), req)
				}
				return gateway.ReserveBillingInflight(context.Background(), req)
			}
			for _, denial := range []error{nil, ErrDailyLimitExceeded, ErrWeeklyLimitExceeded, ErrMonthlyLimitExceeded} {
				priv := newLockFreeSpyRepo()
				unpriv := newLockFreeSpyRepo()
				if denial == nil {
					priv.admitDeny, unpriv.allow = true, false
				} else {
					priv.admitErr, unpriv.allow, unpriv.err = denial, false, denial
				}
				lease, privErr := reserve(priv, lockFreePrivilegedUserID)
				require.Nil(t, lease)
				require.Error(t, privErr)
				_, unprivErr := reserve(unpriv, lockFreeUnprivilegedUserID)
				require.Equal(t, unprivErr, privErr, "denial=%v", denial)
				priv.requireUntouched(t, "denied")
				require.EqualValues(t, 1, priv.admits.Load())
			}
		})
	}
}
