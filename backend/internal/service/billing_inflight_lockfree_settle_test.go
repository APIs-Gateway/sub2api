//go:build unit

package service

import (
	"context"
	"math"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖「特权用户跳过计费预占（nil lease）」之后的结算与失败路径：
// 无 lease 的请求照常结算、金额与有 lease 时一致、失败/取消/换号不 panic 不泄漏不多扣。
// 测试只依赖「跳过时 applyInflightEstimate 返回 nil lease」这一行为，不依赖跳过条件的内部判断。

// lockFreeSettleUserID 是这些测试里请求所属用户；privileged 为 true 时它在特权名单里。
const lockFreeSettleUserID = int64(601)

type lockFreeSettleRepo struct {
	inflightCaptureRepo

	cmu      sync.Mutex
	cmds     []UsageBillingCommand
	reserves int
	resizes  int
	admits   int // 只读准入次数（特权用户跳过预占时）
}

// AdmitBillingLockFree 满足 BillingLockFreeAdmissionReader：这些测试里余额充足，恒放行。
func (r *lockFreeSettleRepo) AdmitBillingLockFree(context.Context, int64) (bool, error) {
	r.cmu.Lock()
	r.admits++
	r.cmu.Unlock()
	return true, nil
}

func (r *lockFreeSettleRepo) ReserveBillingInflight(ctx context.Context, userID int64, owner string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	r.cmu.Lock()
	r.reserves++
	r.cmu.Unlock()
	return r.inflightCaptureRepo.ReserveBillingInflight(ctx, userID, owner, amount, exclusive, ttl)
}

func (r *lockFreeSettleRepo) ResizeBillingInflight(ctx context.Context, userID int64, owner, attempt string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	r.cmu.Lock()
	r.resizes++
	r.cmu.Unlock()
	return r.inflightCaptureRepo.ResizeBillingInflight(ctx, userID, owner, attempt, amount, exclusive, ttl)
}

func (r *lockFreeSettleRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	r.cmds = append(r.cmds, *cmd)
	return &UsageBillingApplyResult{Applied: true}, nil
}

func (r *lockFreeSettleRepo) applied() []UsageBillingCommand {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return append([]UsageBillingCommand(nil), r.cmds...)
}

func (r *lockFreeSettleRepo) reserveCalls() (reserves, resizes int) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return r.reserves, r.resizes
}

// inflightTouched 返回预占 repo 被碰过的总次数（预占、续约之外的全部写入口）。
func (r *lockFreeSettleRepo) inflightTouched() int {
	reserves, resizes := r.reserveCalls()
	r.inflightCaptureRepo.mu.Lock()
	defer r.inflightCaptureRepo.mu.Unlock()
	return reserves + resizes + len(r.stages) + len(r.finishes) + r.releases
}

type lockFreeSettleKind struct {
	name string
	// record 走真实的结算入口（RecordUsage），返回写入的 usage_log。
	record func(t *testing.T, ctx context.Context, repo *lockFreeSettleRepo, requestID string, accountID int64) *UsageLog
}

func lockFreeSettleKinds() []lockFreeSettleKind {
	return []lockFreeSettleKind{
		{name: "gateway_claude", record: func(t *testing.T, ctx context.Context, repo *lockFreeSettleRepo, requestID string, accountID int64) *UsageLog {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			err := svc.RecordUsage(ctx, &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: requestID,
					Usage:     ClaudeUsage{InputTokens: 1200, OutputTokens: 600},
					Model:     "claude-sonnet-4",
					Duration:  time.Second,
				},
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: 601},
				Account: &Account{ID: accountID},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			return usageRepo.lastLog
		}},
		{name: "openai", record: func(t *testing.T, ctx context.Context, repo *lockFreeSettleRepo, requestID string, accountID int64) *UsageLog {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: requestID,
					Usage:     OpenAIUsage{InputTokens: 1200, OutputTokens: 600},
					Model:     "gpt-5",
					Duration:  time.Second,
				},
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: 601},
				Account: &Account{ID: accountID},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			return usageRepo.lastLog
		}},
	}
}

// lockFreeAdmit 复刻 handler 的准入：applyInflightEstimate -> reserveBillingInflightHTTP 把非 nil lease 放进请求 ctx 并 MarkDispatched。
func lockFreeAdmit(t *testing.T, ctx context.Context, repo *lockFreeSettleRepo, privileged bool, amount float64) (context.Context, *BillingInflightLease) {
	t.Helper()
	cfg := inflightTestConfig()
	request := BillingInflightRequest{APIKey: &APIKey{User: &User{ID: lockFreeSettleUserID, Balance: 991228}}}
	lease, err := applyInflightEstimate(ctx, repo, cfg, lockFreeSettleResolver(t, privileged), request, amount, false)
	require.NoError(t, err)
	if lease != nil {
		ctx = WithBillingInflightLease(ctx, lease)
		lease.MarkDispatched()
	}
	return ctx, lease
}

// lockFreeSettleResolver：privileged 为 true 时名单里有 lockFreeSettleUserID；否则返回 nil 解析器（无特权）。
func lockFreeSettleResolver(t *testing.T, privileged bool) *BillingLockFreeUsers {
	t.Helper()
	if !privileged {
		return nil
	}
	return lockFreeTestResolver(t, lockFreeSettleUserID)
}

type lockFreeSettleOutcome struct {
	cmd *UsageBillingCommand
	log *UsageLog
}

// lockFreeSettleOne 复刻一个成功请求从准入到结算的完整生命周期：
// 准入 -> 提交 usage 任务（AcquireBillingInflightTask）-> handler 返回（HandlerDone）-> worker 执行 RecordUsage -> finish(true)。
func lockFreeSettleOne(t *testing.T, kind lockFreeSettleKind, repo *lockFreeSettleRepo, privileged bool, cancelBeforeRecord bool) lockFreeSettleOutcome {
	t.Helper()
	ctx, lease := lockFreeAdmit(t, context.Background(), repo, privileged, 0.5)
	attach, finish := AcquireBillingInflightTask(ctx)
	lease.HandlerDone() // lease 为 nil 时必须是 no-op
	workerCtx := attach(context.Background())
	if cancelBeforeRecord {
		var cancel context.CancelFunc
		workerCtx, cancel = context.WithCancel(workerCtx)
		cancel() // 客户端已断开：结算必须走脱离请求的计费 ctx
	}
	log := kind.record(t, workerCtx, repo, "req-lockfree-settle", 701)
	finish(true)
	applied := repo.applied()
	require.Len(t, applied, 1, "每个请求恰好结算一次，不能少扣也不能重复扣")
	cmd := applied[0]
	return lockFreeSettleOutcome{cmd: &cmd, log: log}
}

func requireNoInflightTraffic(t *testing.T, repo *lockFreeSettleRepo, msg string) {
	t.Helper()
	require.Zero(t, repo.inflightTouched(), msg)
	repo.inflightCaptureRepo.mu.Lock()
	defer repo.inflightCaptureRepo.mu.Unlock()
	require.Empty(t, repo.owner, msg)
}

// 覆盖点 1：无 lease（跳过预占）的请求照常结算，金额与有 lease 时一致。
func TestLockFreeSettle_SettlementAmountMatchesLeasedRequest(t *testing.T) {
	for _, kind := range lockFreeSettleKinds() {
		for _, cancelled := range []bool{false, true} {
			name := kind.name
			if cancelled {
				name += "/client_cancelled"
			}
			t.Run(name, func(t *testing.T) {
				leasedRepo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
				// 无特权 = 等同改动前（上游原版）的行为：建 lease、写 obligation。
				leased := lockFreeSettleOne(t, kind, leasedRepo, false, cancelled)
				reserves, _ := leasedRepo.reserveCalls()
				require.Equal(t, 1, reserves, "基线：未跳过时恰好预占一次")
				leasedRepo.inflightCaptureRepo.mu.Lock()
				require.Len(t, leasedRepo.stages, 1, "基线：有 lease 时结算前登记一次已知成本")
				require.Equal(t, 1, leasedRepo.releases, "基线：结算成功后释放 lease")
				leasedRepo.inflightCaptureRepo.mu.Unlock()
				require.Equal(t, "obligation", leased.cmd.InflightObligationID)

				skippedRepo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
				skipped := lockFreeSettleOne(t, kind, skippedRepo, true, cancelled)
				requireNoInflightTraffic(t, skippedRepo, "跳过预占的请求在结算全程不得碰预占 repo（不预占/不登记/不释放）")
				require.Empty(t, skipped.cmd.InflightObligationID, "没有 lease 就没有 obligation，结算命令不带 inflight 标识")

				// 扣费命令：除 InflightObligationID 外逐字段一致。
				require.Greater(t, skipped.cmd.BalanceCost, 0.0, "必须真的扣了费，而不是 0 元通过")
				want := *leased.cmd
				want.InflightObligationID = ""
				require.Equal(t, want, *skipped.cmd, "有/无 lease 的扣费命令（金额、倍率、token、账号、请求 ID）必须完全一致")

				// usage 记录：金额一致，且与扣费命令一致。
				require.Equal(t, leased.log.TotalCost, skipped.log.TotalCost)
				require.Equal(t, leased.log.ActualCost, skipped.log.ActualCost)
				require.InDelta(t, skipped.log.ActualCost, skipped.cmd.BalanceCost, 1e-12)
				require.Equal(t, leased.log.InputTokens, skipped.log.InputTokens)
				require.Equal(t, leased.log.OutputTokens, skipped.log.OutputTokens)
				require.Equal(t, leased.log.RequestID, skipped.log.RequestID)
			})
		}
	}
}

// 覆盖点 1/2：nil lease 下全部生命周期钩子都是安全的 no-op，且不碰 repo、不改 ctx。
func TestLockFreeSettle_NilLeaseLifecycleHooksAreSafeNoOps(t *testing.T) {
	repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
	ctx, lease := lockFreeAdmit(t, context.Background(), repo, true, 1.5)
	require.Nil(t, lease)
	require.Nil(t, BillingInflightLeaseFromContext(ctx))

	require.NotPanics(t, func() {
		lease.MarkDispatched()
		require.NoError(t, lease.Resize(ctx, 3, false))
		lease.HandlerDone()
		lease.HandlerDone()

		attach, finish := AcquireBillingInflightTask(ctx)
		require.Equal(t, ctx, attach(ctx), "没有 lease 时 attach 不改 ctx")
		finish(true)
		finish(false)
		finish(true)

		MarkBillingInflightAttemptNoCharge(ctx)
		CompleteBillingInflightTask(ctx)
		CompleteBillingInflightTask(nil)

		cmd := &UsageBillingCommand{BalanceCost: 1, OfficialCost: 1}
		stageBillingInflight(ctx, cmd)
		require.Empty(t, cmd.InflightObligationID)
	})
	requireNoInflightTraffic(t, repo, "nil lease 的钩子不得产生任何预占 repo 写入")
	require.Empty(t, repo.applied(), "钩子本身不结算、不扣费")
}

// 覆盖点 2：上游失败。无 lease 时没有可泄漏的占用，也没有多余扣费；有 lease 的对照行为是释放/保留 TTL。
func TestLockFreeSettle_UpstreamFailureWithoutLeaseLeavesNothingBehind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	refusal := []byte(`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	cases := []struct {
		name string
		// run 返回上游是否给出了「确定未计费」证明。
		run func(t *testing.T, ctx context.Context) bool
	}{
		{"provider_refusal_with_no_charge_proof", func(t *testing.T, ctx context.Context) bool {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			return markBillingInflightProviderRefusal(c, 401, refusal, nil)
		}},
		{"transport_failure_without_proof", func(t *testing.T, ctx context.Context) bool {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			return markBillingInflightProviderRefusal(c, 502, []byte("<html>bad gateway</html>"), nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 对照：有 lease。
			leasedRepo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			ctx, lease := lockFreeAdmit(t, context.Background(), leasedRepo, false, 0.5)
			require.NotNil(t, lease)
			proof := tc.run(t, ctx)
			lease.HandlerDone()
			if proof {
				require.Equal(t, 1, func() int {
					leasedRepo.inflightCaptureRepo.mu.Lock()
					defer leasedRepo.inflightCaptureRepo.mu.Unlock()
					return leasedRepo.releases
				}(), "基线：有确定未计费证明时 lease 被释放")
			}

			// 被测：跳过预占。
			skippedRepo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			ctx, lease = lockFreeAdmit(t, context.Background(), skippedRepo, true, 0.5)
			require.Nil(t, lease)
			require.NotPanics(t, func() {
				require.Equal(t, proof, tc.run(t, ctx), "失败分类不依赖 lease 是否存在")
				lease.HandlerDone()
			})
			requireNoInflightTraffic(t, skippedRepo, "无 lease 的上游失败不得产生任何预占 repo 写入（nothing to leak）")
			require.Empty(t, skippedRepo.applied(), "失败请求没有结算，不得产生扣费命令")
			require.Empty(t, leasedRepo.applied())
		})
	}
}

// 覆盖点 2：failover 换号。每次尝试重新走 applyInflightEstimate；
// 无 lease 时每次都是 nil，且最终只结算最后成功的那一个账号，一次。
func TestLockFreeSettle_FailoverRetriesWithoutLeaseSettleOnlyWinningAttempt(t *testing.T) {
	for _, kind := range lockFreeSettleKinds() {
		t.Run(kind.name, func(t *testing.T) {
			run := func(privileged bool) *lockFreeSettleRepo {
				repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
				cfg := inflightTestConfig()
				resolver := lockFreeSettleResolver(t, privileged)
				user := &User{ID: lockFreeSettleUserID, Balance: 991228}
				ctx := context.Background()
				for _, accountID := range []int64{701, 702, 703} {
					request := BillingInflightRequest{APIKey: &APIKey{User: user}, Account: &Account{ID: accountID}}
					lease, err := applyInflightEstimate(ctx, repo, cfg, resolver, request, 0.5, false)
					require.NoError(t, err)
					if lease != nil {
						ctx = WithBillingInflightLease(ctx, lease)
						lease.MarkDispatched()
					}
					if accountID != 703 {
						// 账号 701/702 在输出前被上游拒绝：handler 标记「确定未计费」后换号。
						require.NotPanics(t, func() { MarkBillingInflightAttemptNoCharge(ctx) })
					}
				}
				attach, finish := AcquireBillingInflightTask(ctx)
				BillingInflightLeaseFromContext(ctx).HandlerDone()
				kind.record(t, attach(context.Background()), repo, "req-lockfree-failover", 703)
				finish(true)
				return repo
			}

			leased := run(false)
			reserves, resizes := leased.reserveCalls()
			require.Equal(t, 1, reserves, "基线：同一逻辑请求只占一个 lease")
			require.Equal(t, 2, resizes, "基线：换号用 Resize，不新开 lease")
			leased.inflightCaptureRepo.mu.Lock()
			require.Equal(t, 1, leased.releases, "基线：结算后 lease 恰好释放一次")
			leased.inflightCaptureRepo.mu.Unlock()
			require.Len(t, leased.applied(), 1)

			skipped := run(true)
			requireNoInflightTraffic(t, skipped, "三次尝试都跳过预占，repo 不被触碰")
			applied := skipped.applied()
			require.Len(t, applied, 1, "失败的尝试不得产生扣费，只结算成功的那一次")
			require.Equal(t, int64(703), applied[0].AccountID)
			require.Equal(t, leased.applied()[0].BalanceCost, applied[0].BalanceCost, "金额与有 lease 时一致")
			require.Greater(t, applied[0].BalanceCost, 0.0)
		})
	}
}

// 覆盖点 2：结算失败（repo.Apply 报错）时，无 lease 的请求不 panic，也不重复扣费。
func TestLockFreeSettle_SettlementErrorWithoutLeaseDoesNotPanicOrDoubleCharge(t *testing.T) {
	repo := &lockFreeSettleErrRepo{lockFreeSettleRepo: lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}}
	ctx, lease := lockFreeAdmit(t, context.Background(), &repo.lockFreeSettleRepo, true, 0.5)
	require.Nil(t, lease)
	attach, finish := AcquireBillingInflightTask(ctx)
	lease.HandlerDone()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NotPanics(t, func() {
		err := svc.RecordUsage(attach(context.Background()), &OpenAIRecordUsageInput{
			Result:  &OpenAIForwardResult{RequestID: "req-apply-fails", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 50}, Model: "gpt-5", Duration: time.Second},
			APIKey:  &APIKey{ID: 501, Quota: 100},
			User:    &User{ID: 601},
			Account: &Account{ID: 701},
		})
		require.Error(t, err, "结算失败必须向上返回，而不是被 nil lease 吞掉")
		finish(false)
	})
	require.Equal(t, 1, repo.attempts, "只尝试扣费一次，不自动重试成重复扣费")
	requireNoInflightTraffic(t, &repo.lockFreeSettleRepo, "无 lease 时结算失败也不触碰预占 repo")
}

type lockFreeSettleErrRepo struct {
	lockFreeSettleRepo
	attempts int
}

func (r *lockFreeSettleErrRepo) Apply(context.Context, *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.attempts++
	return nil, context.DeadlineExceeded
}

// 跳过边界：只有「在特权名单里」才跳过；余额、名单为空、解析器为 nil、名单里是别人都照常预占；cfg 为 nil 不 panic。
func TestLockFreeSettle_SkipBoundaryConditions(t *testing.T) {
	cases := []struct {
		name      string
		resolver  *BillingLockFreeUsers
		userID    int64
		balance   float64
		wantLease bool
	}{
		{"privileged_skips", lockFreeTestResolver(t, 601), 601, 991228, false},
		{"privileged_skips_even_with_zero_balance", lockFreeTestResolver(t, 601), 601, 0, false},
		{"privileged_skips_even_with_negative_balance", lockFreeTestResolver(t, 601), 601, -5, false},
		{"unprivileged_high_balance_reserves", lockFreeTestResolver(t, 999), 601, 991228, true},
		{"unprivileged_zero_balance_reserves", lockFreeTestResolver(t, 999), 601, 0, true},
		{"empty_list_reserves", lockFreeTestResolver(t), 601, 991228, true},
		{"nil_resolver_reserves", nil, 601, 991228, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			request := BillingInflightRequest{APIKey: &APIKey{User: &User{ID: tc.userID, Balance: tc.balance}}}
			lease, err := applyInflightEstimate(context.Background(), repo, inflightTestConfig(), tc.resolver, request, 1.5, false)
			require.NoError(t, err)
			if tc.wantLease {
				require.NotNil(t, lease)
				defer lease.HandlerDone()
				reserves, _ := repo.reserveCalls()
				require.Equal(t, 1, reserves)
			} else {
				require.Nil(t, lease)
				requireNoInflightTraffic(t, repo, "")
			}
		})
	}

	for name, resolver := range map[string]*BillingLockFreeUsers{"nil_resolver": nil, "privileged": lockFreeTestResolver(t, 1)} {
		t.Run("nil_config_does_not_panic/"+name, func(t *testing.T) {
			repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			request := BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 1, Balance: 991228}}}
			require.NotPanics(t, func() {
				lease, err := applyInflightEstimate(context.Background(), repo, nil, resolver, request, 1, false)
				require.NoError(t, err)
				require.Nil(t, lease) // 没有配置就没有预占能力（与改动前一致）
			})
		})
	}
}

// 已有 lease 时（同一请求第二次尝试，或第二次尝试换了特权用户）不能把 lease 丢掉：继续 Resize 同一个 lease。
func TestLockFreeSettle_ExistingLeaseIsResizedNotDroppedOnPrivilegedRetry(t *testing.T) {
	repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
	ctx, lease := lockFreeAdmit(t, context.Background(), repo, false, 1) // 无特权：建 lease
	require.NotNil(t, lease)
	MarkBillingInflightAttemptNoCharge(ctx)

	cfg := inflightTestConfig()
	retry := BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 999, Balance: 991228}}}
	got, err := applyInflightEstimate(ctx, repo, cfg, lockFreeTestResolver(t, 999), retry, 2, false)
	require.NoError(t, err)
	require.Same(t, lease, got)
	reserves, resizes := repo.reserveCalls()
	require.Equal(t, 1, reserves, "没有第二个 lease")
	require.Equal(t, 1, resizes)

	attach, finish := AcquireBillingInflightTask(ctx)
	lease.HandlerDone()
	stageCtx := attach(context.Background())
	stageBillingInflight(stageCtx, &UsageBillingCommand{BalanceCost: 1, OfficialCost: 1})
	CompleteBillingInflightTask(stageCtx)
	finish(true)
	repo.inflightCaptureRepo.mu.Lock()
	defer repo.inflightCaptureRepo.mu.Unlock()
	require.Equal(t, 1, repo.releases, "该 lease 仍被正常释放，不泄漏")
}

// 未知价格（exclusive）与金额非有限的请求，特权用户也仍必须走预占：这类请求没有可信的成本上界。
func TestLockFreeSettle_ExclusiveAndNonFiniteEstimatesStillReserveForPrivilegedUser(t *testing.T) {
	cases := []struct {
		name      string
		amount    float64
		exclusive bool
	}{
		{"unknown_price_exclusive", 0, true},
		{"nan_amount", math.NaN(), false},
		{"inf_amount", math.Inf(1), false},
		{"negative_amount", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			cfg := inflightTestConfig()
			request := BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 601, Balance: 991228}}}
			lease, err := applyInflightEstimate(context.Background(), repo, cfg, lockFreeTestResolver(t, 601), request, tc.amount, tc.exclusive)
			require.NoError(t, err)
			require.NotNil(t, lease, "特权用户也不能跳过没有可信成本上界的请求")
			defer lease.HandlerDone()
			reserves, _ := repo.reserveCalls()
			require.Equal(t, 1, reserves)
			repo.inflightCaptureRepo.mu.Lock()
			defer repo.inflightCaptureRepo.mu.Unlock()
			require.True(t, repo.exclusive)
		})
	}
}

// 并发：大量跳过预占的请求同时进入、结算，-race 下无数据竞争、repo 零写入、每个请求恰好结算一次。
func TestLockFreeSettle_ConcurrentSkippedRequestsSettleExactlyOnce(t *testing.T) {
	repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
	kinds := lockFreeSettleKinds()
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := kinds[i%len(kinds)]
			ctx, lease := lockFreeAdmit(t, context.Background(), repo, true, 0.5)
			if lease != nil {
				t.Errorf("request %d unexpectedly reserved", i)
				return
			}
			attach, finish := AcquireBillingInflightTask(ctx)
			lease.HandlerDone()
			kind.record(t, attach(context.Background()), repo, "req-concurrent-"+string(rune('a'+i%26))+string(rune('A'+i/26)), 701)
			finish(true)
		}(i)
	}
	wg.Wait()
	requireNoInflightTraffic(t, repo, "并发下依然不碰预占 repo")
	require.Len(t, repo.applied(), n, "每个请求恰好一次扣费命令")
}
