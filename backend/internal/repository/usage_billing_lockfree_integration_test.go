//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 短持锁结算（BillingLockFree）的集成测试。每个顶层测试用一个独立的 PostgreSQL 库
// （inflightTestDatabase），库里的数据由本测试独占，可以放心并发、可以提交。

type lfEnv struct {
	t      *testing.T
	ctx    context.Context
	client *dbent.Client
	db     *sql.DB
	repo   *usageBillingRepository
}

func newLFEnv(t *testing.T) *lfEnv {
	t.Helper()
	client := inflightTestEntClient(t)
	db := inflightTestDB(t)
	// 测试容器 max_connections 默认 100；并发用例的 goroutine 数会超过它，限制连接池即可。
	db.SetMaxOpenConns(40)
	return &lfEnv{t: t, ctx: context.Background(), client: client, db: db,
		repo: NewUsageBillingRepository(client, db).(*usageBillingRepository)}
}

type lfScenario struct {
	name         string
	balance      float64
	keyQuota     float64
	keyQuotaUsed float64
	rateLimit    float64 // 同时设给 5h/1d/7d
	acctType     string  // "" = 不带账号
	acctExtra    map[string]any
	deleteKey    bool
	inflight     bool

	official     float64
	mult         float64
	balanceCost  float64
	keyQuotaCost float64
	rateCost     float64
	acctCost     float64
}

type lfWorld struct {
	user *service.User
	key  *service.APIKey
	acct *service.Account
}

func (e *lfEnv) world(sc lfScenario) lfWorld {
	e.t.Helper()
	user := mustCreateUser(e.t, e.client, &service.User{
		Email: "lf-" + uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: sc.balance,
	})
	key := mustCreateApiKey(e.t, e.client, &service.APIKey{
		UserID: user.ID, Key: "sk-lf-" + uuid.NewString(), Name: "lf",
		Quota: sc.keyQuota, QuotaUsed: sc.keyQuotaUsed,
		RateLimit5h: sc.rateLimit, RateLimit1d: sc.rateLimit, RateLimit7d: sc.rateLimit,
	})
	w := lfWorld{user: user, key: key}
	if sc.acctType != "" {
		w.acct = mustCreateAccount(e.t, e.client, &service.Account{
			Name: "lf-" + uuid.NewString(), Type: sc.acctType, Extra: cloneAnyMap(sc.acctExtra),
		})
	}
	if sc.deleteKey {
		require.NoError(e.t, NewAPIKeyRepository(e.client, e.db).DeleteWithAudit(e.ctx, key.ID))
	}
	return w
}

func cloneAnyMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *lfEnv) cmd(sc lfScenario, w lfWorld, lockFree bool) *service.UsageBillingCommand {
	c := &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: w.key.ID, UserID: w.user.ID,
		OfficialCost: sc.official, RateMultiplier: sc.mult, BalanceCost: sc.balanceCost,
		APIKeyQuotaCost: sc.keyQuotaCost, APIKeyRateLimitCost: sc.rateCost, AccountQuotaCost: sc.acctCost,
		BillingLockFree: lockFree,
	}
	if w.acct != nil {
		c.AccountID = w.acct.ID
		c.AccountType = sc.acctType
	}
	return c
}

// lfResult 是 UsageBillingApplyResult 的可比较快照（指针解引用）。
type lfResult struct {
	Applied          bool
	HasNewBalance    bool
	NewBalance       float64
	HasWalletDebit   bool
	WalletDebit      float64
	OverdraftApplied bool
	HasSubscription  bool
	Depleted         []int64
	KeyExhausted     bool
	HasQuotaState    bool
	QuotaState       service.AccountQuotaState
}

func snapshotResult(r *service.UsageBillingApplyResult) lfResult {
	if r == nil {
		return lfResult{}
	}
	out := lfResult{Applied: r.Applied, OverdraftApplied: r.OverdraftApplied, HasSubscription: r.SubscriptionID != nil,
		Depleted: r.DepletedSubscriptionGroupIDs, KeyExhausted: r.APIKeyQuotaExhausted}
	if r.NewBalance != nil {
		out.HasNewBalance, out.NewBalance = true, *r.NewBalance
	}
	if r.WalletDebit != nil {
		out.HasWalletDebit, out.WalletDebit = true, *r.WalletDebit
	}
	if r.QuotaState != nil {
		out.HasQuotaState, out.QuotaState = true, *r.QuotaState
	}
	return out
}

type lfCard struct {
	Status                  string
	Daily, Weekly, Monthly  string
	DailyWin, WeeklyWinNull bool
}

// lfState 是落库状态快照；与用户/卡/key ID、时间戳无关的部分都纳入比较。
type lfState struct {
	Balance                              string
	KeyQuotaUsed, KeyStatus              string
	Usage5h, Usage1d, Usage7d            string
	Win5hSet, Win1dSet, Win7dSet         bool
	AcctQuotaUsed, AcctDaily, AcctWeekly string
	Dedup                                int
	Leases                               []string
	Cards                                []lfCard
}

func (e *lfEnv) state(w lfWorld) lfState {
	e.t.Helper()
	var s lfState
	require.NoError(e.t, e.db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, w.user.ID).Scan(&s.Balance))
	require.NoError(e.t, e.db.QueryRow(`SELECT quota_used::text, status, usage_5h::text, usage_1d::text, usage_7d::text,
		window_5h_start IS NOT NULL, window_1d_start IS NOT NULL, window_7d_start IS NOT NULL
		FROM api_keys WHERE id=$1`, w.key.ID).Scan(&s.KeyQuotaUsed, &s.KeyStatus, &s.Usage5h, &s.Usage1d, &s.Usage7d,
		&s.Win5hSet, &s.Win1dSet, &s.Win7dSet))
	if w.acct != nil {
		require.NoError(e.t, e.db.QueryRow(`SELECT COALESCE(extra->>'quota_used','-'), COALESCE(extra->>'quota_daily_used','-'),
			COALESCE(extra->>'quota_weekly_used','-') FROM accounts WHERE id=$1`, w.acct.ID).Scan(&s.AcctQuotaUsed, &s.AcctDaily, &s.AcctWeekly))
	}
	require.NoError(e.t, e.db.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, w.key.ID).Scan(&s.Dedup))
	rows, err := e.db.Query(`SELECT phase||'/'||amount::text||'/'||exclusive::text FROM billing_inflight_leases WHERE user_id=$1 ORDER BY 1`, w.user.ID)
	require.NoError(e.t, err)
	for rows.Next() {
		var l string
		require.NoError(e.t, rows.Scan(&l))
		s.Leases = append(s.Leases, l)
	}
	require.NoError(e.t, rows.Err())
	require.NoError(e.t, rows.Close())
	crows, err := e.db.Query(`SELECT status, daily_usage_usd::text, weekly_usage_usd::text, monthly_usage_usd::text,
		daily_window_start IS NOT NULL, weekly_window_start IS NULL FROM user_subscriptions WHERE user_id=$1 ORDER BY expires_at, id`, w.user.ID)
	require.NoError(e.t, err)
	for crows.Next() {
		var c lfCard
		require.NoError(e.t, crows.Scan(&c.Status, &c.Daily, &c.Weekly, &c.Monthly, &c.DailyWin, &c.WeeklyWinNull))
		s.Cards = append(s.Cards, c)
	}
	require.NoError(e.t, crows.Err())
	require.NoError(e.t, crows.Close())
	return s
}

type lfOutcome struct {
	Err   string
	Res   lfResult
	State lfState
}

// prepare 在需要时为命令建立在途预占（Reserve + Stage），返回可 Apply 的命令。
func (e *lfEnv) prepare(sc lfScenario, w lfWorld, lockFree bool) *service.UsageBillingCommand {
	e.t.Helper()
	c := e.cmd(sc, w, lockFree)
	if sc.inflight {
		owner := uuid.NewString()
		ok, err := e.repo.ReserveBillingInflight(e.ctx, w.user.ID, owner, 0.1, false, time.Minute)
		require.NoError(e.t, err)
		require.True(e.t, ok)
		id, err := e.repo.StageBillingInflight(e.ctx, w.user.ID, owner, owner+":initial", c, time.Minute)
		require.NoError(e.t, err)
		c.InflightObligationID = id
	}
	return c
}

func (e *lfEnv) runOnce(sc lfScenario, lockFree bool) lfOutcome {
	e.t.Helper()
	w := e.world(sc)
	c := e.prepare(sc, w, lockFree)
	res, err := e.repo.Apply(e.ctx, c)
	out := lfOutcome{Res: snapshotResult(res), State: e.state(w)}
	if err != nil {
		out.Err = err.Error()
	}
	return out
}

func TestLockFreeBilling_EquivalentToLockedPath(t *testing.T) {
	e := newLFEnv(t)
	apiKeyAcct := map[string]any{"quota_limit": 100.0}
	dailyAcct := map[string]any{"quota_limit": 100.0, "quota_daily_limit": 10.0, "quota_weekly_limit": 50.0}
	scenarios := []lfScenario{
		{name: "positive-balance", balance: 10, official: 2, mult: 1, keyQuotaCost: 2, rateCost: 2},
		{name: "exactly-enough", balance: 2, official: 2, mult: 1, keyQuotaCost: 2},
		{name: "goes-negative", balance: 1, official: 2.5, mult: 1.5, keyQuotaCost: 3.75},
		{name: "starts-negative", balance: -3.5, official: 1.25, mult: 2},
		{name: "starts-zero", balance: 0, official: 0.75, mult: 1},
		{name: "multiplier-one", balance: 100, official: 0.123456789, mult: 1},
		{name: "multiplier-irrational", balance: 100, official: 0.123456789, mult: 1.3, keyQuotaCost: 0.16049383},
		{name: "multiplier-zero-means-one", balance: 5, official: 1.5, mult: 0},
		{name: "float-noise-small-balance", balance: 0.1, official: 0.3, mult: 1},
		{name: "tiny-cost", balance: 1, official: 0.000000123, mult: 1.7},
		{name: "legacy-balance-cost-only", balance: 100, balanceCost: 1.25, keyQuotaCost: 1.25, rateCost: 1.25},
		{name: "legacy-balance-cost-negative-start", balance: -2, balanceCost: 1.25},
		{name: "official-zero", balance: 100, keyQuotaCost: 0.5, rateCost: 0.5},
		{name: "official-zero-with-account", balance: 100, acctType: service.AccountTypeAPIKey, acctExtra: apiKeyAcct, acctCost: 0.5, keyQuotaCost: 0.5},
		{name: "key-quota-flips-exhausted", balance: 100, keyQuota: 1, keyQuotaUsed: 0.5, official: 0.5, mult: 1, keyQuotaCost: 0.5, rateCost: 0.5},
		{name: "key-quota-already-exhausted", balance: 100, keyQuota: 1, keyQuotaUsed: 1, official: 0.5, mult: 1, keyQuotaCost: 0.5},
		{name: "key-quota-not-reached", balance: 100, keyQuota: 10, keyQuotaUsed: 0.5, official: 0.5, mult: 1, keyQuotaCost: 0.5},
		{name: "key-deleted", balance: 100, keyQuota: 50, rateLimit: 50, deleteKey: true, official: 5, mult: 2, keyQuotaCost: 10, rateCost: 10},
		{name: "account-apikey", balance: 100, acctType: service.AccountTypeAPIKey, acctExtra: apiKeyAcct, official: 5, mult: 2, acctCost: 10},
		{name: "account-bedrock-daily-weekly", balance: 100, acctType: service.AccountTypeBedrock, acctExtra: dailyAcct, official: 5, mult: 2, acctCost: 10, keyQuotaCost: 10},
		{name: "account-oauth-ignored", balance: 100, acctType: service.AccountTypeOAuth, acctExtra: apiKeyAcct, official: 1, mult: 1, acctCost: 10},
		{name: "inflight-obligation", balance: 10, inflight: true, official: 2, mult: 3, keyQuotaCost: 6, rateCost: 6},
		{name: "inflight-obligation-balance-cost", balance: 10, inflight: true, balanceCost: 2, mult: 3},
	}
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			legacy := e.runOnce(sc, false)
			lockFree := e.runOnce(sc, true)
			require.Equal(t, legacy, lockFree)
			require.Empty(t, legacy.Err, "scenarios are all expected to succeed")
			require.True(t, legacy.Res.Applied)
		})
	}
}

// 起始余额与扣款额的随机组合：特权路径与加锁路径的落库余额、返回的 NewBalance/WalletDebit 逐位一致。
// 避开第 9 位恰好为 5 的平局（加锁路径在平局上取决于浮点噪声，见 settleWalletOnlyAtomic 注释）。
func TestLockFreeBilling_EquivalentToLockedPath_BalanceSweep(t *testing.T) {
	e := newLFEnv(t)
	balances := []float64{-1000.5, -1, -0.00000001, 0, 0.00000001, 0.1, 0.5, 1, 7.77, 1234.56789012}
	costs := []float64{0.000001, 0.0012347, 0.1, 0.3, 0.77, 1, 2.2, 13.37, 2000}
	mults := []float64{1, 0.5, 1.3, 2.7, 0.01}
	n := 0
	for _, b := range balances {
		for _, c := range costs {
			for _, m := range mults {
				sc := lfScenario{balance: b, official: c, mult: m}
				legacy := e.runOnce(sc, false)
				lockFree := e.runOnce(sc, true)
				require.Equal(t, legacy, lockFree, "balance=%v cost=%v mult=%v", b, c, m)
				n++
			}
		}
	}
	t.Logf("compared %d balance/cost/multiplier combinations", n)
}

// 8 位量化平局：加锁路径取决于浮点噪声（本身不确定），特权路径固定 half-away-from-zero；两者最多差 1e-8。
func TestLockFreeBilling_QuantizationTieWithinOneUnit(t *testing.T) {
	e := newLFEnv(t)
	for _, sc := range []lfScenario{
		{balance: 1, official: 0.0000000045, mult: 1},
		{balance: 1, official: 1.5e-7, mult: 0.03},
		{balance: -1, official: 0.0000000005, mult: 1},
	} {
		legacy, lockFree := e.runOnce(sc, false), e.runOnce(sc, true)
		a, err := decimal.NewFromString(legacy.State.Balance)
		require.NoError(t, err)
		b, err := decimal.NewFromString(lockFree.State.Balance)
		require.NoError(t, err)
		require.True(t, a.Sub(b).Abs().LessThanOrEqual(decimal.New(1, -8)), "legacy=%s lockfree=%s", a, b)
	}
}

// ---- 锁行方式与锁序 ----
//
// 在 api_keys 行被别人占住时，run 会卡在写 api_keys 上。此时用不同强度的行锁探测 users 行：
//   - 加锁结算持有 FOR UPDATE（连 FOR KEY SHARE 的外键检查都挡）；
//   - 特权短路径持有 FOR NO KEY UPDATE（只挡写和 FOR UPDATE，不挡外键检查），并且已经先于 api_keys 拿到，
//     即锁序 users → api_keys，与加锁结算一致。

func isLockNotAvailable(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "55P03"
}

func (e *lfEnv) waitBlockedOn(substr string) {
	e.t.Helper()
	require.Eventually(e.t, func() bool {
		var n int
		err := e.db.QueryRow(`SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%'||$1||'%'`, substr).Scan(&n)
		return err == nil && n > 0
	}, 15*time.Second, 20*time.Millisecond, "expected a statement blocked on a lock: %s", substr)
}

// userRowLockedWhileBlockedAtKey 先让外部事务占住 key 行，再启动 run；等它卡在 api_keys 上之后，
// 用 SELECT ... FOR <mode> NOWAIT 探测 users 行是否被 run 以冲突的强度持有。
func (e *lfEnv) userRowLockedWhileBlockedAtKey(w lfWorld, mode string, run func() error) bool {
	e.t.Helper()
	holder, err := e.db.BeginTx(e.ctx, nil)
	require.NoError(e.t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(e.ctx, `SELECT 1 FROM api_keys WHERE id=$1 FOR UPDATE`, w.key.ID)
	require.NoError(e.t, err)

	done := make(chan error, 1)
	go func() { done <- run() }()
	e.waitBlockedOn("api_keys")

	probe, err := e.db.BeginTx(e.ctx, nil)
	require.NoError(e.t, err)
	_, probeErr := probe.ExecContext(e.ctx, `SELECT 1 FROM users WHERE id=$1 FOR `+mode+` NOWAIT`, w.user.ID)
	_ = probe.Rollback()
	locked := false
	if probeErr != nil {
		require.True(e.t, isLockNotAvailable(probeErr), "unexpected probe error: %v", probeErr)
		locked = true
	}
	require.NoError(e.t, holder.Rollback())
	require.NoError(e.t, <-done)
	return locked
}

func TestLockFreeBilling_UserRowLockStrengthAndOrder(t *testing.T) {
	e := newLFEnv(t)
	sc := lfScenario{balance: 50, official: 1, mult: 1, keyQuotaCost: 1}
	apply := func(c *service.UsageBillingCommand) func() error {
		return func() error { _, err := e.repo.Apply(e.ctx, c); return err }
	}

	t.Run("locked-path-blocks-key-share", func(t *testing.T) {
		w := e.world(sc)
		require.True(t, e.userRowLockedWhileBlockedAtKey(w, "KEY SHARE", apply(e.cmd(sc, w, false))))
	})
	t.Run("lock-free-no-card-takes-user-lock-before-api_keys-without-blocking-key-share", func(t *testing.T) {
		w := e.world(sc)
		require.False(t, e.userRowLockedWhileBlockedAtKey(w, "KEY SHARE", apply(e.cmd(sc, w, true))),
			"UPDATE users must not conflict with foreign-key checks")
		w = e.world(sc)
		require.True(t, e.userRowLockedWhileBlockedAtKey(w, "NO KEY UPDATE", apply(e.cmd(sc, w, true))),
			"users row must already be locked while waiting for the api key: lock order users -> api_keys")
		require.Equal(t, "49.00000000", e.state(w).Balance)
	})
	t.Run("lock-free-with-active-card-uses-locked-flow", func(t *testing.T) {
		w := e.world(sc)
		e.makeCard(w, false, 0)
		require.True(t, e.userRowLockedWhileBlockedAtKey(w, "KEY SHARE", apply(e.cmd(sc, w, true))))
	})
	t.Run("lock-free-with-obligation-uses-locked-flow", func(t *testing.T) {
		isc := sc
		isc.inflight = true
		w := e.world(isc)
		require.True(t, e.userRowLockedWhileBlockedAtKey(w, "KEY SHARE", apply(e.prepare(isc, w, true))))
	})
}

// ---- 有卡回退 ----

// makeCard 给用户建一张 status=active 的卡；expired=true 时 expires_at 在过去（惰性过期的「假 active」）。
// dailyLimit>0 时设日限额。
func (e *lfEnv) makeCard(w lfWorld, expired bool, dailyLimit float64) *service.UserSubscription {
	e.t.Helper()
	group := mustCreateGroup(e.t, e.client, &service.Group{
		Name: "lf-" + uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeSubscription,
	})
	now := time.Now()
	dayStart, weekStart, monthStart := timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now)
	daily, weekly, monthly := 3.0, 70.0, 300.0
	if dailyLimit > 0 {
		daily = dailyLimit
	}
	exp := now.Add(48 * time.Hour)
	if expired {
		exp = now.Add(-time.Hour)
	}
	return mustCreateSubscription(e.t, e.client, &service.UserSubscription{
		UserID: w.user.ID, GroupID: group.ID, ExpiresAt: exp,
		DailyLimitUSD: &daily, WeeklyLimitUSD: &weekly, MonthlyLimitUSD: &monthly,
		DailyUsageUSD: 1, WeeklyUsageUSD: 1, MonthlyUsageUSD: 1,
		DailyWindowStart: &dayStart, WeeklyWindowStart: &weekStart, MonthlyWindowStart: &monthStart,
	})
}

func TestLockFreeBilling_ActiveCardFallsBackToLockedFlow(t *testing.T) {
	e := newLFEnv(t)
	cases := []struct {
		name       string
		expired    bool
		dailyLimit float64
		official   float64
		wantSub    bool
	}{
		{"card-covers-fully", false, 0, 1, true},
		{"card-partially-covers-rest-from-wallet", false, 0, 5, true},
		{"card-window-already-full-all-from-wallet", false, 1, 5, true},
		{"active-but-past-expires_at-lazily-expired", true, 0, 5, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sc := lfScenario{balance: 100, official: tc.official, mult: 1, keyQuotaCost: tc.official, rateCost: tc.official}
			var outs [2]lfOutcome
			for i, lockFree := range []bool{false, true} {
				w := e.world(sc)
				e.makeCard(w, tc.expired, tc.dailyLimit)
				res, err := e.repo.Apply(e.ctx, e.cmd(sc, w, lockFree))
				require.NoError(t, err)
				outs[i] = lfOutcome{Res: snapshotResult(res), State: e.state(w)}
			}
			// 每个 world 各建一个分组，分组 ID 必然不同：只比较个数，不比较 ID。
			require.Len(t, outs[1].Res.Depleted, len(outs[0].Res.Depleted))
			depleted := outs[1].Res.Depleted
			outs[0].Res.Depleted, outs[1].Res.Depleted = nil, nil
			require.Equal(t, outs[0], outs[1])
			outs[1].Res.Depleted = depleted
			require.Equal(t, tc.wantSub, outs[1].Res.HasSubscription)
			require.Len(t, outs[1].State.Cards, 1)
			if tc.expired {
				require.Equal(t, "expired", outs[1].State.Cards[0].Status)
				require.NotEmpty(t, outs[1].Res.Depleted, "lazily expiring a card must report its group for cache invalidation")
			}
		})
	}
}

// ---- 用户不存在 / 已软删 ----

func TestLockFreeBilling_UserNotFoundOrSoftDeleted(t *testing.T) {
	e := newLFEnv(t)
	sc := lfScenario{balance: 10, keyQuota: 100, official: 2, mult: 1, keyQuotaCost: 2, rateCost: 2}
	for _, mode := range []string{"soft-deleted", "never-existed"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			var outs [2]lfOutcome
			for i, lockFree := range []bool{false, true} {
				w := e.world(sc)
				c := e.cmd(sc, w, lockFree)
				if mode == "soft-deleted" {
					_, err := e.db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, w.user.ID)
					require.NoError(t, err)
				} else {
					c.UserID = 987654321
				}
				res, err := e.repo.Apply(e.ctx, c)
				require.ErrorIs(t, err, service.ErrUserNotFound)
				require.Nil(t, res)
				outs[i] = lfOutcome{Err: err.Error(), State: e.state(w)}
				require.Equal(t, 0, outs[i].State.Dedup, "dedup claim must roll back with the transaction")
				require.Equal(t, "0.00000000", outs[i].State.KeyQuotaUsed, "api key counters must roll back too")
			}
			require.Equal(t, outs[0], outs[1])
		})
	}
}

// ---- 并发不丢失 ----

func decimalText(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	require.NoError(t, err)
	return d
}

func TestLockFreeBilling_ConcurrentNoLostUpdatesNoDeadlock(t *testing.T) {
	e := newLFEnv(t)
	const n = 100
	sc := lfScenario{balance: 1000, keyQuota: 100000, rateLimit: 100000, acctType: service.AccountTypeAPIKey,
		acctExtra: map[string]any{"quota_limit": 100000.0, "quota_daily_limit": 100000.0},
		official:  0.123456789, mult: 1.3, keyQuotaCost: 0.05, rateCost: 0.05, acctCost: 0.07}
	w := e.world(sc)

	start := make(chan struct{})
	errs := make(chan error, n)
	applied := int32(0)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := e.cmd(sc, w, true) // 每个请求独立 request_id、独立 cmd（Normalize 会改 cmd）
			<-start
			res, err := e.repo.Apply(e.ctx, c)
			if err == nil && res.Applied {
				atomic.AddInt32(&applied, 1)
			}
			errs <- err
		}()
	}
	close(start)
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(90 * time.Second):
		t.Fatal("concurrent lock-free settlements did not finish: possible deadlock")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, n, applied)

	perDebit := decimal.NewFromFloat(service.QuantizeUsageBillingAmount(0.123456789 * 1.3))
	st := e.state(w)
	require.True(t, decimal.NewFromInt(1000).Sub(perDebit.Mul(decimal.NewFromInt(n))).Equal(decimalText(t, st.Balance)),
		"balance %s, want %s", st.Balance, decimal.NewFromInt(1000).Sub(perDebit.Mul(decimal.NewFromInt(n))))
	require.True(t, decimal.NewFromFloat(0.05).Mul(decimal.NewFromInt(n)).Equal(decimalText(t, st.KeyQuotaUsed)), "quota_used %s", st.KeyQuotaUsed)
	require.True(t, decimal.NewFromFloat(0.05).Mul(decimal.NewFromInt(n)).Equal(decimalText(t, st.Usage5h)), "usage_5h %s", st.Usage5h)
	require.True(t, decimal.NewFromFloat(0.07).Mul(decimal.NewFromInt(n)).Equal(decimalText(t, st.AcctQuotaUsed)), "account quota_used %s", st.AcctQuotaUsed)
	require.Equal(t, n, st.Dedup)
}

// ---- 幂等 ----

func TestLockFreeBilling_IdempotentSequentialAndConcurrent(t *testing.T) {
	e := newLFEnv(t)
	sc := lfScenario{balance: 100, keyQuota: 1000, official: 2, mult: 1.5, keyQuotaCost: 3, rateCost: 3}

	t.Run("sequential", func(t *testing.T) {
		w := e.world(sc)
		c := e.cmd(sc, w, true)
		first, err := e.repo.Apply(e.ctx, c)
		require.NoError(t, err)
		require.True(t, first.Applied)
		for i := 0; i < 3; i++ {
			dup := *c
			again, err := e.repo.Apply(e.ctx, &dup)
			require.NoError(t, err)
			require.False(t, again.Applied)
		}
		st := e.state(w)
		require.Equal(t, "97.00000000", st.Balance)
		require.Equal(t, "3.00000000", st.KeyQuotaUsed)
		require.Equal(t, 1, st.Dedup)
	})

	t.Run("concurrent-same-request-id", func(t *testing.T) {
		w := e.world(sc)
		base := e.cmd(sc, w, true)
		const n = 30
		var wg sync.WaitGroup
		start := make(chan struct{})
		var appliedCount int32
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			dup := *base
			go func() {
				defer wg.Done()
				<-start
				res, err := e.repo.Apply(e.ctx, &dup)
				if err == nil && res.Applied {
					atomic.AddInt32(&appliedCount, 1)
				}
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		require.EqualValues(t, 1, appliedCount, "exactly one concurrent duplicate may win the dedup claim")
		st := e.state(w)
		require.Equal(t, "97.00000000", st.Balance)
		require.Equal(t, "3.00000000", st.KeyQuotaUsed)
		require.Equal(t, 1, st.Dedup)
	})
}

// ---- 与其他锁 users 行的操作混合并发 ----

func TestLockFreeBilling_MixedWithReservationsAndAdminBalanceChanges(t *testing.T) {
	e := newLFEnv(t)
	sc := lfScenario{balance: 1000, keyQuota: 100000, official: 0.5, mult: 1, keyQuotaCost: 0.5, rateCost: 0.5,
		acctType: service.AccountTypeAPIKey, acctExtra: map[string]any{"quota_limit": 100000.0}, acctCost: 0.5}
	w := e.world(sc)
	// 第二把公共 key（同一用户），和 user 1 下挂多把 key 的线上形态一致。
	key2 := mustCreateApiKey(t, e.client, &service.APIKey{UserID: w.user.ID, Key: "sk-lf-" + uuid.NewString(), Name: "lf2", Quota: 100000})
	w2 := lfWorld{user: w.user, key: key2, acct: w.acct}

	const settles, reserves, adminOps = 60, 40, 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, settles+reserves+adminOps)
	for i := 0; i < settles; i++ {
		wg.Add(1)
		world := w
		if i%2 == 1 {
			world = w2
		}
		go func() {
			defer wg.Done()
			c := e.cmd(sc, world, true)
			<-start
			_, err := e.repo.Apply(e.ctx, c)
			errs <- err
		}()
	}
	for i := 0; i < reserves; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := uuid.NewString()
			<-start
			if _, err := e.repo.ReserveBillingInflight(e.ctx, w.user.ID, owner, 0.01, false, time.Minute); err != nil {
				errs <- err
				return
			}
			errs <- e.repo.ReleaseBillingInflight(e.ctx, w.user.ID, owner)
		}()
	}
	for i := 0; i < adminOps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.db.Exec(`UPDATE users SET balance = balance + 1 WHERE id=$1`, w.user.ID)
			errs <- err
		}()
	}
	close(start)
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(90 * time.Second):
		t.Fatal("mixed workload did not finish: possible deadlock")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	st := e.state(w)
	want := decimal.NewFromInt(1000).Sub(decimal.NewFromFloat(0.5).Mul(decimal.NewFromInt(settles))).Add(decimal.NewFromInt(adminOps))
	require.True(t, want.Equal(decimalText(t, st.Balance)), "balance %s, want %s", st.Balance, want)
	var q1, q2 string
	require.NoError(t, e.db.QueryRow(`SELECT quota_used::text FROM api_keys WHERE id=$1`, w.key.ID).Scan(&q1))
	require.NoError(t, e.db.QueryRow(`SELECT quota_used::text FROM api_keys WHERE id=$1`, key2.ID).Scan(&q2))
	require.True(t, decimalText(t, q1).Add(decimalText(t, q2)).Equal(decimal.NewFromFloat(0.5).Mul(decimal.NewFromInt(settles))))
}

// ---- 特权短路径与带 InflightObligationID 的加锁结算同时并发 ----

// user 1 的无价模型/估价失败请求带 InflightObligationID，走原加锁路径（users → api_keys → accounts）；
// 特权短路径锁序与它一致。各 50 路并发，要求无死锁、无错误、余额和各计数器精确。
func TestLockFreeBilling_ConcurrentWithObligationLockedSettlements(t *testing.T) {
	e := newLFEnv(t)
	sc := lfScenario{balance: 1000, keyQuota: 100000, rateLimit: 100000, official: 0.5, mult: 1,
		keyQuotaCost: 0.5, rateCost: 0.5, acctType: service.AccountTypeAPIKey,
		acctExtra: map[string]any{"quota_limit": 100000.0}, acctCost: 0.5}
	w := e.world(sc)
	key2 := mustCreateApiKey(t, e.client, &service.APIKey{UserID: w.user.ID, Key: "sk-lf-" + uuid.NewString(), Name: "lf2", Quota: 100000})
	w2 := lfWorld{user: w.user, key: key2, acct: w.acct}

	const each = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2*each)
	var applied int32
	run := func(c *service.UsageBillingCommand) {
		defer wg.Done()
		<-start
		res, err := e.repo.Apply(e.ctx, c)
		if err == nil && res.Applied {
			atomic.AddInt32(&applied, 1)
		}
		errs <- err
	}
	for i := 0; i < each; i++ {
		world := w
		if i%2 == 1 {
			world = w2
		}
		wg.Add(1)
		go run(e.cmd(sc, world, true))
	}
	for i := 0; i < each; i++ {
		world := w
		if i%2 == 1 {
			world = w2
		}
		// 一半带特权标记（应回退加锁流程），一半不带，都带预占 ID。
		isc := sc
		isc.inflight = true
		c := e.prepare(isc, world, i%4 < 2)
		wg.Add(1)
		go run(c)
	}
	close(start)
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(90 * time.Second):
		t.Fatal("lock-free and obligation-locked settlements did not finish: possible deadlock")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 2*each, applied)

	st := e.state(w)
	total := decimal.NewFromFloat(0.5).Mul(decimal.NewFromInt(2 * each))
	require.True(t, decimal.NewFromInt(1000).Sub(total).Equal(decimalText(t, st.Balance)), "balance %s", st.Balance)
	var q1, q2 string
	require.NoError(t, e.db.QueryRow(`SELECT quota_used::text FROM api_keys WHERE id=$1`, w.key.ID).Scan(&q1))
	require.NoError(t, e.db.QueryRow(`SELECT quota_used::text FROM api_keys WHERE id=$1`, key2.ID).Scan(&q2))
	require.True(t, decimalText(t, q1).Add(decimalText(t, q2)).Equal(total), "key quota %s + %s", q1, q2)
	require.True(t, decimalText(t, st.AcctQuotaUsed).Equal(total), "account quota %s", st.AcctQuotaUsed)
	var pending int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND phase<>'settled' AND amount>0`, w.user.ID).Scan(&pending))
	require.Zero(t, pending, "every obligation-carrying settlement must consume its lease")
}
