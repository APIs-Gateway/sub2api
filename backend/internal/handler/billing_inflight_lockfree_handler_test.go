//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 入口层测试：余额 >= billing.inflight_reservation.lock_free_min_balance 时 OpenAI(Responses / Chat Completions)
// 与 Gateway(Claude Messages) 入口正常放行且不调用预占 repo；余额低于阈值照常预占；余额不足仍被原有的
// 计费准入（BillingCacheService.CheckBillingEligibility，billing_cache_service.go checkBalanceEligibility）拒绝。
//
// 拒绝发生在 handler 里 CheckBillingEligibility 之后、选号与 reserveBillingInflightHTTP 之前，
// 所以跳过预占既不会改变拒绝结果，也不会让被拒的请求走到上游。

const lockFreeHandlerThreshold = 500000.0

// ---- 假仓储 ----

// lockFreeHandlerBillingRepo 同时是 UsageBillingRepository（Apply）和 BillingInflightRepository（预占），
// 记录每一种调用，用于断言「跳过时预占 repo 零调用」。
type lockFreeHandlerBillingRepo struct {
	service.UsageBillingRepository

	mu                                            sync.Mutex
	reserves, resizes, stages, finishes, releases int
	reserveExclusive                              []bool
	reserveAmounts                                []float64
	cmds                                          chan *service.UsageBillingCommand
}

func newLockFreeHandlerBillingRepo() *lockFreeHandlerBillingRepo {
	return &lockFreeHandlerBillingRepo{cmds: make(chan *service.UsageBillingCommand, 16)}
}

func (r *lockFreeHandlerBillingRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	cp := *cmd
	r.cmds <- &cp
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

func (r *lockFreeHandlerBillingRepo) ReserveBillingInflight(_ context.Context, _ int64, _ string, amount float64, exclusive bool, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserves++
	r.reserveAmounts = append(r.reserveAmounts, amount)
	r.reserveExclusive = append(r.reserveExclusive, exclusive)
	return true, nil
}

func (r *lockFreeHandlerBillingRepo) ResizeBillingInflight(context.Context, int64, string, string, float64, bool, time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resizes++
	return true, nil
}

func (r *lockFreeHandlerBillingRepo) RenewBillingInflight(context.Context, int64, string, time.Duration) (bool, error) {
	return true, nil
}

func (r *lockFreeHandlerBillingRepo) ReleaseBillingInflight(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releases++
	return nil
}

func (r *lockFreeHandlerBillingRepo) StageBillingInflight(context.Context, int64, string, string, *service.UsageBillingCommand, time.Duration) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages++
	return "obligation", nil
}

func (r *lockFreeHandlerBillingRepo) FinishBillingInflightAttempt(context.Context, int64, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes++
	return nil
}

type lockFreeCounts struct{ reserves, resizes, stages, finishes, releases int }

func (r *lockFreeHandlerBillingRepo) counts() lockFreeCounts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return lockFreeCounts{r.reserves, r.resizes, r.stages, r.finishes, r.releases}
}

func (r *lockFreeHandlerBillingRepo) waitCommand(t *testing.T) *service.UsageBillingCommand {
	t.Helper()
	select {
	case cmd := <-r.cmds:
		return cmd
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the settlement command (usage billing Apply)")
		return nil
	}
}

func (r *lockFreeHandlerBillingRepo) requireNoMoreCommands(t *testing.T) {
	t.Helper()
	select {
	case cmd := <-r.cmds:
		t.Fatalf("unexpected extra settlement command: %+v", cmd)
	case <-time.After(150 * time.Millisecond):
	}
}

func lockFreeWaitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lockFreeUserRepo 给计费准入（BillingCacheService.GetUserBalance）提供「新鲜」余额，独立于 API Key 鉴权快照里的 User.Balance。
type lockFreeUserRepo struct {
	service.UserRepository
	balance float64
}

func (r *lockFreeUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive, Balance: r.balance}, nil
}

func lockFreeConfig(threshold float64) *config.Config {
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	cfg.Billing.InflightReservation.DefaultMaxOutputTokens = 8192
	cfg.Billing.InflightReservation.LockFreeMinBalance = threshold
	return cfg
}

// ---- OpenAI 入口（Responses / Chat Completions）----

type lockFreeScenario struct {
	threshold       float64
	snapshotBalance float64 // API Key 鉴权快照里的 User.Balance（预占跳过判断读它）
	freshBalance    float64 // 计费准入读的余额（缓存/DB）
	// failFirst 为 true 时账号 11 返回 520（触发 failover），账号 21 成功。
	failFirst bool
	// onDo 在上游收到请求时调用（模拟客户端取消等）。
	onDo func()
}

type lockFreeOpenAIHarness struct {
	router    *gin.Engine
	billing   *lockFreeHandlerBillingRepo
	upstream  *chainRespUpstream
	usageLogs chan *service.UsageLog
}

func newLockFreeOpenAIHarness(t *testing.T, s lockFreeScenario) *lockFreeOpenAIHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := lockFreeConfig(s.threshold)

	group := chainRespGroup(1, 1, 0)
	accounts := []service.Account{keepaliveMismatchAccount(11, 1, nil)}
	replies := map[int64]chainRespReply{}
	if s.failFirst {
		accounts = append(accounts, keepaliveMismatchAccount(21, 2, nil))
		replies[11] = chainRespUpstreamError()
	}
	if s.onDo != nil {
		reply := replies[11]
		if reply.body == "" {
			reply = chainRespReply{body: keepaliveMismatchResponsesSSE(chainRespModel, "served")}
		}
		reply.onDo = s.onDo
		replies[11] = reply
	}
	h := &lockFreeOpenAIHarness{
		billing:   newLockFreeHandlerBillingRepo(),
		usageLogs: make(chan *service.UsageLog, 16),
	}
	h.upstream = &chainRespUpstream{replies: replies, gate: make(chan struct{})}

	billingCache := service.NewBillingCacheService(nil, &lockFreeUserRepo{balance: s.freshBalance}, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	concurrency := service.NewConcurrencyService(cache)
	gateway := service.NewOpenAIGatewayService(
		&chainRespAccountRepo{schedulable: map[int64][]service.Account{1: accounts}},
		&openAIWSUsageHandlerUsageLogRepoStub{created: h.usageLogs},
		h.billing,
		nil, nil, nil, nil,
		cfg,
		nil, concurrency,
		service.NewBillingService(cfg, nil),
		nil,
		billingCache,
		h.upstream,
		&service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	handler := NewOpenAIGatewayHandler(gateway, concurrency, billingCache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), newUsageRecordTestPool(t), nil, nil, nil, cfg)
	handler.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
	handler.maxAccountSwitches = 3

	groupID := group.ID
	apiKey := &service.APIKey{
		ID:      1931,
		UserID:  chainRespUserID,
		GroupID: &groupID,
		Group:   group,
		User:    &service.User{ID: chainRespUserID, Status: service.StatusActive, Balance: s.snapshotBalance},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: chainRespUserID, Concurrency: 1})
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "req-lockfree"))
		c.Next()
	})
	router.POST("/openai/v1/responses", handler.Responses)
	router.POST("/openai/v1/chat/completions", handler.ChatCompletions)
	h.router = router
	return h
}

const (
	lockFreeResponsesBody = `{"model":"gpt-5.4","stream":true,"input":"hello"}`
	lockFreeChatBody      = `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hello"}]}`
)

var lockFreeOpenAIRoutes = []struct{ name, path, body string }{
	{"responses", "/openai/v1/responses", lockFreeResponsesBody},
	{"chat_completions", "/openai/v1/chat/completions", lockFreeChatBody},
}

func (h *lockFreeOpenAIHarness) serve(ctx context.Context, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// 覆盖点 3：高余额 -> 放行、零预占调用，结算照常扣费且命令不带 inflight 标识。
func TestLockFreeHandler_OpenAIHighBalanceSkipsReservationAndStillSettles(t *testing.T) {
	for _, route := range lockFreeOpenAIRoutes {
		t.Run(route.name, func(t *testing.T) {
			h := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228})
			rec := h.serve(context.Background(), route.path, route.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{11}, h.upstream.accountCalls())

			cmd := h.billing.waitCommand(t)
			require.Greater(t, cmd.BalanceCost, 0.0, "必须真实扣费")
			require.Empty(t, cmd.InflightObligationID, "没有 lease，结算命令不带 obligation")
			require.Equal(t, int64(11), cmd.AccountID)
			select {
			case log := <-h.usageLogs:
				require.InDelta(t, log.ActualCost, cmd.BalanceCost, 1e-12)
			case <-time.After(5 * time.Second):
				t.Fatal("usage log not written")
			}
			h.billing.requireNoMoreCommands(t)
			require.Equal(t, lockFreeCounts{}, h.billing.counts(), "跳过预占：预占 repo（预占/续约/登记/释放）零调用")
		})
	}
}

// 覆盖点 3：余额低于阈值 / 阈值关闭 -> 照常预占，结算带 obligation 并释放 lease。扣费金额与跳过时一致。
func TestLockFreeHandler_OpenAIBelowThresholdOrDisabledStillReserves(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		balance   float64
	}{
		{"just_below_threshold", lockFreeHandlerThreshold, lockFreeHandlerThreshold - 0.01},
		{"threshold_disabled", 0, 991228},
	}
	for _, route := range lockFreeOpenAIRoutes {
		skipped := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228})
		require.Equal(t, http.StatusOK, skipped.serve(context.Background(), route.path, route.body).Code)
		skippedCmd := skipped.billing.waitCommand(t)

		for _, tc := range cases {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				h := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: tc.threshold, snapshotBalance: tc.balance, freshBalance: tc.balance})
				rec := h.serve(context.Background(), route.path, route.body)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				cmd := h.billing.waitCommand(t)
				lockFreeWaitUntil(t, "lease release after settlement", func() bool { return h.billing.counts().releases == 1 })

				got := h.billing.counts()
				require.Equal(t, lockFreeCounts{reserves: 1, stages: 1, finishes: got.finishes, releases: 1}, got, "基线：恰好预占一次、登记一次、释放一次")
				require.NotEmpty(t, cmd.InflightObligationID)
				h.billing.mu.Lock()
				require.False(t, h.billing.reserveExclusive[0], "已知价格的模型不应是 exclusive 预占")
				require.Greater(t, h.billing.reserveAmounts[0], 0.0)
				h.billing.mu.Unlock()

				want := *skippedCmd
				want.InflightObligationID = cmd.InflightObligationID
				// 请求 ID 由上游响应决定，同一脚本下一致；金额/倍率/token 必须与跳过时完全一致。
				require.Equal(t, want.BalanceCost, cmd.BalanceCost)
				require.Equal(t, want.OfficialCost, cmd.OfficialCost)
				require.Equal(t, want.RateMultiplier, cmd.RateMultiplier)
				require.Equal(t, want.InputTokens, cmd.InputTokens)
				require.Equal(t, want.OutputTokens, cmd.OutputTokens)
			})
		}
	}
}

// 覆盖点 3：余额不足（<=0）的用户仍被原有计费准入拒绝；拒绝发生在选号/预占之前，所以不论阈值开不开，
// 状态码与响应都一致、预占 repo 零调用、上游零调用、不产生扣费。
func TestLockFreeHandler_OpenAIInsufficientBalanceStillRejectedByEligibility(t *testing.T) {
	cases := []struct {
		name            string
		snapshotBalance float64
		freshBalance    float64
	}{
		{"zero_balance", 0, 0},
		{"negative_balance", -3, -3},
		// 鉴权快照滞后：快照里还是高余额，但准入读的新鲜余额已经用光。准入读新鲜值，所以仍被拒绝。
		{"stale_high_snapshot_but_fresh_balance_exhausted", 991228, 0},
	}
	for _, route := range lockFreeOpenAIRoutes {
		for _, tc := range cases {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				withThreshold := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: tc.snapshotBalance, freshBalance: tc.freshBalance})
				recOn := withThreshold.serve(context.Background(), route.path, route.body)
				baseline := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: 0, snapshotBalance: tc.snapshotBalance, freshBalance: tc.freshBalance})
				recOff := baseline.serve(context.Background(), route.path, route.body)

				require.GreaterOrEqual(t, recOn.Code, 400, recOn.Body.String())
				require.Equal(t, http.StatusForbidden, recOn.Code, "余额不足由 billingErrorDetails 映射成 403")
				require.Contains(t, strings.ToLower(recOn.Body.String()), "insufficient")
				require.Equal(t, recOff.Code, recOn.Code, "开关不改变拒绝结果")
				require.Equal(t, recOff.Body.String(), recOn.Body.String())
				for _, h := range []*lockFreeOpenAIHarness{withThreshold, baseline} {
					require.Empty(t, h.upstream.accountCalls(), "被拒请求不会走到上游")
					require.Equal(t, lockFreeCounts{}, h.billing.counts(), "被拒请求不碰预占 repo")
					h.billing.requireNoMoreCommands(t)
				}
			})
		}
	}
}

// 覆盖点 2/3：failover 换号。高余额时两次尝试都跳过预占，只结算成功的账号一次；对照（阈值关）是 Resize 同一 lease。
func TestLockFreeHandler_OpenAIFailoverWithoutLeaseSettlesWinnerOnce(t *testing.T) {
	for _, route := range lockFreeOpenAIRoutes {
		t.Run(route.name, func(t *testing.T) {
			skipped := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228, failFirst: true})
			rec := skipped.serve(context.Background(), route.path, route.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{11, 21}, skipped.upstream.accountCalls(), "第一个账号失败后换号")
			cmd := skipped.billing.waitCommand(t)
			require.Equal(t, int64(21), cmd.AccountID, "只结算成功的账号")
			require.Greater(t, cmd.BalanceCost, 0.0)
			skipped.billing.requireNoMoreCommands(t)
			require.Equal(t, lockFreeCounts{}, skipped.billing.counts(), "两次尝试都不碰预占 repo，也没有泄漏的 lease")

			leased := newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: 0, snapshotBalance: 991228, freshBalance: 991228, failFirst: true})
			require.Equal(t, http.StatusOK, leased.serve(context.Background(), route.path, route.body).Code)
			leasedCmd := leased.billing.waitCommand(t)
			lockFreeWaitUntil(t, "known cost staged", func() bool { return leased.billing.counts().stages == 1 })
			time.Sleep(150 * time.Millisecond)
			got := leased.billing.counts()
			require.Equal(t, 1, got.reserves, "基线：同一逻辑请求一个 lease")
			require.Equal(t, 1, got.resizes, "基线：换号 Resize 同一 lease")
			// 基线（改动前就是如此）：520 不是「确定未计费」证明，第一次尝试按未知结果保留，lease 留到 TTL 才过期，不会提前释放。
			// 跳过预占的请求没有这份占用，也就不存在这段滞留。
			require.Zero(t, got.releases, "基线：failover 过的 lease 保留到 TTL")
			require.Equal(t, int64(21), leasedCmd.AccountID)
			require.Equal(t, leasedCmd.BalanceCost, cmd.BalanceCost, "金额与有 lease 时一致")
		})
	}
}

// 覆盖点 2：客户端在上游处理期间取消。对照有 lease 与无 lease 两种模式下结算命令数一致；
// 无 lease 时预占 repo 零调用、不 panic。
func TestLockFreeHandler_OpenAIClientCancelMidRequestBehavesLikeLeasedRequest(t *testing.T) {
	for _, route := range lockFreeOpenAIRoutes {
		t.Run(route.name, func(t *testing.T) {
			run := func(threshold float64) (commands int, h *lockFreeOpenAIHarness) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h = newLockFreeOpenAIHarness(t, lockFreeScenario{threshold: threshold, snapshotBalance: 991228, freshBalance: 991228, onDo: cancel})
				require.NotPanics(t, func() { h.serve(ctx, route.path, route.body) })
				for {
					select {
					case <-h.billing.cmds:
						commands++
						continue
					case <-time.After(300 * time.Millisecond):
					}
					return commands, h
				}
			}
			leasedCommands, leased := run(0)
			skippedCommands, skipped := run(lockFreeHandlerThreshold)
			require.Equal(t, leasedCommands, skippedCommands, "取消后的结算次数与有 lease 时一致（不多扣、不漏扣）")
			require.LessOrEqual(t, skippedCommands, 1)
			require.Equal(t, 1, leased.billing.counts().reserves, "基线：有 lease 时发生了预占")
			require.Equal(t, lockFreeCounts{}, skipped.billing.counts(), "无 lease：取消路径上预占 repo 零调用")
		})
	}
}

// ---- Gateway（Claude Messages）入口 ----

type lockFreeAnthropicUpstream struct {
	mu       sync.Mutex
	calls    []int64
	failures int32 // 前 N 次调用返回 500（触发 failover）
	n        int32
	onDo     func()
}

func (u *lockFreeAnthropicUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	u.mu.Unlock()
	if u.onDo != nil {
		u.onDo()
	}
	if atomic.AddInt32(&u.n, 1) <= u.failures {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"rotate account"}}`)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-lockfree"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg_ok","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":50,"output_tokens":30}}`)),
		Request:    req,
	}, nil
}

func (u *lockFreeAnthropicUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, c)
}

func (u *lockFreeAnthropicUpstream) accountCalls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...)
}

type lockFreeMessagesHarness struct {
	handler  *GatewayHandler
	billing  *lockFreeHandlerBillingRepo
	upstream *lockFreeAnthropicUpstream
	group    *service.Group
	apiKey   *service.APIKey
	cleanup  func()
}

func newLockFreeMessagesHarness(t *testing.T, s lockFreeScenario, failures int32) *lockFreeMessagesHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := lockFreeConfig(s.threshold)
	groupID := int64(9161)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := func(id int64) *service.Account {
		return &service.Account{
			ID: id, Name: "anthropic-lockfree", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "test-key", "pool_mode": true},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Concurrency: 1, Priority: int(id), Status: service.StatusActive, Schedulable: true,
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
		}
	}
	accounts := []*service.Account{account(9261), account(9262)}
	upstream := &lockFreeAnthropicUpstream{failures: failures, onDo: s.onDo}
	billing := newLockFreeHandlerBillingRepo()

	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	accountRepo := &forceCacheBillingAccountRepo{}
	usageLogs := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 16)}
	billingCache := service.NewBillingCacheService(nil, &lockFreeUserRepo{balance: s.freshBalance}, nil, nil, nil, nil, cfg, nil, nil)
	gw := service.NewGatewayService(
		accountRepo, &fakeGroupRepo{group: group}, usageLogs, billing,
		nil, nil, nil,
		&forceCacheBillingGatewayCache{accountID: 9261},
		cfg, schedulerSnapshot, nil,
		service.NewBillingService(cfg, nil),
		service.NewRateLimitService(accountRepo, nil, cfg, nil, nil),
		billingCache,
		nil, upstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{
		gatewayService:           gw,
		billingCacheService:      billingCache,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:       3,
		maxAccountSwitchesGemini: 3,
		usageRecordWorkerPool:    newUsageRecordTestPool(t),
		cfg:                      cfg,
	}
	apiKey := &service.APIKey{
		ID: 9361, UserID: 9461, GroupID: &groupID, Status: service.StatusActive, Group: group,
		User: &service.User{ID: 9461, Concurrency: 10, Balance: s.snapshotBalance, Status: service.StatusActive},
	}
	return &lockFreeMessagesHarness{handler: h, billing: billing, upstream: upstream, group: group, apiKey: apiKey, cleanup: billingCache.Stop}
}

func (h *lockFreeMessagesHarness) serve(ctx context.Context) *httptest.ResponseRecorder {
	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(context.WithValue(ctx, ctxkey.Group, h.group))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), h.apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: h.apiKey.UserID, Concurrency: 10})
	h.handler.Messages(c)
	return rec
}

// 覆盖点 3：Gateway(Claude messages) 高余额放行、零预占调用，结算照常。
func TestLockFreeHandler_MessagesHighBalanceSkipsReservationAndStillSettles(t *testing.T) {
	h := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228}, 0)
	defer h.cleanup()
	rec := h.serve(context.Background())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cmd := h.billing.waitCommand(t)
	require.Greater(t, cmd.BalanceCost, 0.0)
	require.Empty(t, cmd.InflightObligationID)
	h.billing.requireNoMoreCommands(t)
	require.Equal(t, lockFreeCounts{}, h.billing.counts(), "跳过预占：预占 repo 零调用")
}

// 覆盖点 3：Gateway 低于阈值 / 阈值关闭照常预占，金额与跳过时一致。
func TestLockFreeHandler_MessagesBelowThresholdOrDisabledStillReserves(t *testing.T) {
	skipped := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228}, 0)
	defer skipped.cleanup()
	require.Equal(t, http.StatusOK, skipped.serve(context.Background()).Code)
	skippedCmd := skipped.billing.waitCommand(t)

	for _, tc := range []struct {
		name      string
		threshold float64
		balance   float64
	}{
		{"just_below_threshold", lockFreeHandlerThreshold, lockFreeHandlerThreshold - 0.01},
		{"threshold_disabled", 0, 991228},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: tc.threshold, snapshotBalance: tc.balance, freshBalance: tc.balance}, 0)
			defer h.cleanup()
			require.Equal(t, http.StatusOK, h.serve(context.Background()).Code)
			cmd := h.billing.waitCommand(t)
			lockFreeWaitUntil(t, "lease release after settlement", func() bool { return h.billing.counts().releases == 1 })
			got := h.billing.counts()
			require.Equal(t, 1, got.reserves)
			require.Equal(t, 1, got.stages)
			require.Equal(t, 1, got.releases)
			require.NotEmpty(t, cmd.InflightObligationID)
			require.Equal(t, skippedCmd.BalanceCost, cmd.BalanceCost, "金额与跳过预占时一致")
			require.Equal(t, skippedCmd.OfficialCost, cmd.OfficialCost)
		})
	}
}

// 覆盖点 3：Gateway 余额不足仍被计费准入拒绝（含鉴权快照滞后的情况），与阈值开关无关。
func TestLockFreeHandler_MessagesInsufficientBalanceStillRejectedByEligibility(t *testing.T) {
	for _, tc := range []struct {
		name            string
		snapshotBalance float64
		freshBalance    float64
	}{
		{"zero_balance", 0, 0},
		{"negative_balance", -3, -3},
		{"stale_high_snapshot_but_fresh_balance_exhausted", 991228, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			on := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: tc.snapshotBalance, freshBalance: tc.freshBalance}, 0)
			defer on.cleanup()
			off := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: 0, snapshotBalance: tc.snapshotBalance, freshBalance: tc.freshBalance}, 0)
			defer off.cleanup()
			recOn, recOff := on.serve(context.Background()), off.serve(context.Background())
			require.Equal(t, http.StatusForbidden, recOn.Code, recOn.Body.String())
			require.Equal(t, recOff.Code, recOn.Code)
			require.Equal(t, recOff.Body.String(), recOn.Body.String())
			for _, h := range []*lockFreeMessagesHarness{on, off} {
				require.Empty(t, h.upstream.accountCalls())
				require.Equal(t, lockFreeCounts{}, h.billing.counts())
				h.billing.requireNoMoreCommands(t)
			}
		})
	}
}

// 覆盖点 2：Gateway failover 换号，无 lease 时只结算成功的账号一次。
func TestLockFreeHandler_MessagesFailoverWithoutLeaseSettlesWinnerOnce(t *testing.T) {
	h := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: lockFreeHandlerThreshold, snapshotBalance: 991228, freshBalance: 991228}, 1)
	defer h.cleanup()
	rec := h.serve(context.Background())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, h.upstream.accountCalls(), 2, "第一次尝试失败后换号")
	cmd := h.billing.waitCommand(t)
	require.Equal(t, h.upstream.accountCalls()[1], cmd.AccountID)
	h.billing.requireNoMoreCommands(t)
	require.Equal(t, lockFreeCounts{}, h.billing.counts())
}

// 覆盖点 2：Gateway 客户端在上游处理期间取消，有/无 lease 的结算次数一致，无 lease 时预占 repo 零调用、不 panic。
func TestLockFreeHandler_MessagesClientCancelMidRequestBehavesLikeLeasedRequest(t *testing.T) {
	run := func(threshold float64) (int, *lockFreeMessagesHarness) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h := newLockFreeMessagesHarness(t, lockFreeScenario{threshold: threshold, snapshotBalance: 991228, freshBalance: 991228, onDo: cancel}, 0)
		t.Cleanup(h.cleanup)
		require.NotPanics(t, func() { h.serve(ctx) })
		commands := 0
		for {
			select {
			case <-h.billing.cmds:
				commands++
				continue
			case <-time.After(300 * time.Millisecond):
			}
			return commands, h
		}
	}
	leasedCommands, leased := run(0)
	skippedCommands, skipped := run(lockFreeHandlerThreshold)
	require.Equal(t, leasedCommands, skippedCommands)
	require.LessOrEqual(t, skippedCommands, 1)
	require.Equal(t, 1, leased.billing.counts().reserves)
	require.Equal(t, lockFreeCounts{}, skipped.billing.counts())
}

// ---- 同一 HTTP 请求内两次尝试：第一次高余额跳过（无 lease），failover 第二次换了低余额 User 建 lease ----

func newLockFreeInflightGateway(t *testing.T, threshold float64) (*service.OpenAIGatewayService, *lockFreeHandlerBillingRepo) {
	t.Helper()
	cfg := lockFreeConfig(threshold)
	billing := newLockFreeHandlerBillingRepo()
	gateway := service.NewOpenAIGatewayService(
		nil, nil, billing, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, &service.BillingCacheService{}, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return gateway, billing
}

func lockFreeAttemptRequest(userID int64, balance float64, account int64) service.BillingInflightRequest {
	group := chainRespGroup(1, 1, 0)
	groupID := group.ID
	key := &service.APIKey{ID: 1931, UserID: userID, GroupID: &groupID, Group: group, User: &service.User{ID: userID, Status: service.StatusActive, Balance: balance}}
	return service.BillingInflightRequest{
		APIKey:  key,
		Account: &service.Account{ID: account, Platform: service.PlatformOpenAI},
		Model:   chainRespModel,
		Body:    []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}
}

func lockFreeNewGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c
}

func TestLockFreeHandler_FailoverSkipThenLeasedAttemptIsReleasedExactlyOnce(t *testing.T) {
	respond := func(t *testing.T) func(int, string, string) {
		return func(status int, code, message string) {
			t.Fatalf("unexpected admission rejection: %d %s %s", status, code, message)
		}
	}

	t.Run("second_attempt_settles", func(t *testing.T) {
		gateway, billing := newLockFreeInflightGateway(t, lockFreeHandlerThreshold)
		c := lockFreeNewGinContext()
		defer finishBillingInflightHTTP(c) // 入口的 defer，HandlerDone 幂等

		// 第一次尝试：高余额 -> 跳过，ctx 里没有 lease。
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(1, 991228, 11), nil, respond(t)))
		require.Nil(t, service.BillingInflightLeaseFromContext(c.Request.Context()))
		service.MarkBillingInflightAttemptNoCharge(c.Request.Context()) // 无 lease 时 no-op
		require.Equal(t, lockFreeCounts{}, billing.counts())

		// 第二次尝试：failover 后换成低余额用户 -> 建 lease 并放进请求 ctx。
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(2, 100, 21), nil, respond(t)))
		lease := service.BillingInflightLeaseFromContext(c.Request.Context())
		require.NotNil(t, lease)
		require.Equal(t, 1, billing.counts().reserves)

		// 成功后提交 usage 任务，handler 返回，worker 结算。
		attach, finish := service.AcquireBillingInflightTask(c.Request.Context())
		finishBillingInflightHTTP(c)
		require.Zero(t, billing.counts().releases, "任务还没结算，lease 不能提前释放")
		workerCtx := attach(context.Background())
		service.CompleteBillingInflightTask(workerCtx)
		finish(true)
		got := billing.counts()
		require.Equal(t, 1, got.releases, "lease 恰好释放一次，不泄漏")
		require.Zero(t, got.resizes)
		finishBillingInflightHTTP(c) // 入口 defer 再来一次：幂等
		require.Equal(t, 1, billing.counts().releases)
	})

	t.Run("second_attempt_refused_with_no_charge_proof", func(t *testing.T) {
		gateway, billing := newLockFreeInflightGateway(t, lockFreeHandlerThreshold)
		c := lockFreeNewGinContext()
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(1, 991228, 11), nil, respond(t)))
		require.Nil(t, service.BillingInflightLeaseFromContext(c.Request.Context()))
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(2, 100, 21), nil, respond(t)))
		require.NotNil(t, service.BillingInflightLeaseFromContext(c.Request.Context()))

		service.MarkBillingInflightAttemptNoCharge(c.Request.Context()) // 上游明确拒绝、未计费
		require.NotPanics(t, func() { finishBillingInflightHTTP(c); finishBillingInflightHTTP(c) })
		got := billing.counts()
		require.Equal(t, 1, got.releases, "确定未计费 -> 释放，且只释放一次")
		require.Equal(t, 1, got.reserves)
	})

	t.Run("second_attempt_unknown_outcome_keeps_bounded_ttl_hold", func(t *testing.T) {
		gateway, billing := newLockFreeInflightGateway(t, lockFreeHandlerThreshold)
		c := lockFreeNewGinContext()
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(1, 991228, 11), nil, respond(t)))
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(2, 100, 21), nil, respond(t)))
		require.NotNil(t, service.BillingInflightLeaseFromContext(c.Request.Context()))
		require.NotPanics(t, func() { finishBillingInflightHTTP(c) })
		require.Zero(t, billing.counts().releases, "已派发但结果未知：与改动前一致，保留到 TTL 而不是立即释放")
	})

	t.Run("reverse_order_leased_first_then_high_balance_attempt_reuses_lease", func(t *testing.T) {
		gateway, billing := newLockFreeInflightGateway(t, lockFreeHandlerThreshold)
		c := lockFreeNewGinContext()
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(2, 100, 11), nil, respond(t)))
		first := service.BillingInflightLeaseFromContext(c.Request.Context())
		require.NotNil(t, first)
		service.MarkBillingInflightAttemptNoCharge(c.Request.Context())
		require.True(t, reserveBillingInflightHTTP(c, gateway, lockFreeAttemptRequest(1, 991228, 21), nil, respond(t)))
		require.Same(t, first, service.BillingInflightLeaseFromContext(c.Request.Context()), "已有 lease 的请求第二次尝试继续 Resize 同一个 lease")
		require.Equal(t, 1, billing.counts().reserves)
		require.Equal(t, 1, billing.counts().resizes)
		service.MarkBillingInflightAttemptNoCharge(c.Request.Context())
		finishBillingInflightHTTP(c)
		require.Equal(t, 1, billing.counts().releases)
	})
}
