//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// /v1/responses 接入 Key 级分组回退链的端到端测试：真实的 OpenAIGatewayService + OpenAIGatewayHandler（standard 模式，
// 账号按分组划分，分组层 RPM 与计费准入生效），上游、账号仓储、链解析、熔断器、设置都是假实现。
//
// 覆盖：开关关闭 / Key 没链 / 链长 < 2 / 解析出错 / forced 用户时与无链路径逐字节一致（对照 groupFallback=nil）；
// B1 不变式（链只解析一次，服务分组 ∈ 审计的分组集合）；served 计费列；分组层 RPM 首跳 429 不回退、非首跳超限跳过；
// 繁忙与没号的熔断计数；只写过心跳可以回退并按流式收尾，真实内容已输出不回退；静态资格；forced 路由与管理员链的关系。

const (
	chainRespModel    = "gpt-5.4"
	chainRespUserID   = int64(7001)
	chainRespBodyJSON = `{"model":"gpt-5.4","stream":true,"input":"hello"}`
)

// ---- 假上游 ----

type chainRespReply struct {
	status      int
	body        string
	contentType string
	// gated 为 true 时 body 要等到网关第一次 Flush（心跳真正写出）之后才放出。
	gated bool
}

// chainRespUpstreamError 是上游容量类失败（5xx，触发 failover）。
func chainRespUpstreamError() chainRespReply {
	return chainRespReply{status: 520, body: "<html>520: unknown error</html>", contentType: "text/html"}
}

type chainRespUpstream struct {
	mu      sync.Mutex
	calls   []int64
	replies map[int64]chainRespReply
	gate    <-chan struct{}
}

func (u *chainRespUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	reply, ok := u.replies[accountID]
	u.mu.Unlock()
	if !ok {
		reply = chainRespReply{body: keepaliveMismatchResponsesSSE(chainRespModel, fmt.Sprintf("served-by-%d", accountID))}
	}
	status := reply.status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := reply.contentType
	if contentType == "" {
		contentType = "text/event-stream"
	}
	var body io.ReadCloser = io.NopCloser(strings.NewReader(reply.body))
	if reply.gated {
		body = &keepaliveMismatchGatedBody{gate: u.gate, reader: strings.NewReader(reply.body)}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       body,
	}, nil
}

func (u *chainRespUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *chainRespUpstream) accountCalls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...)
}

// ---- 假仓储 ----

// chainRespAccountRepo 按分组划分账号：schedulable 是各分组里可调度的账号；configured 是诊断「组内有没有账号」用的账号池
// （默认等于 schedulable，可单独指定来模拟「池里有账号但此刻都不可调度」）。
type chainRespAccountRepo struct {
	service.AccountRepository
	schedulable map[int64][]service.Account
	configured  map[int64][]service.Account
}

// chainRespStamp 给账号补上所属分组（真实仓储返回的账号带分组关系，调度里有按分组的归属复核）。
func chainRespStamp(accounts []service.Account, groupID int64, platform string) []service.Account {
	out := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		if platform != "" && (account.Platform != platform || !account.IsSchedulable()) {
			continue
		}
		account.GroupIDs = []int64{groupID}
		out = append(out, account)
	}
	return out
}

func (r *chainRespAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	pools := []map[int64][]service.Account{r.schedulable, r.configured}
	for _, pool := range pools {
		for groupID, accounts := range pool {
			for _, account := range chainRespStamp(accounts, groupID, "") {
				if account.ID == id {
					found := account
					return &found, nil
				}
			}
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r *chainRespAccountRepo) listAll(platform string) []service.Account {
	var out []service.Account
	for groupID, accounts := range r.schedulable {
		out = append(out, chainRespStamp(accounts, groupID, platform)...)
	}
	return out
}

func (r *chainRespAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	return chainRespStamp(r.schedulable[groupID], groupID, platform), nil
}

func (r *chainRespAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.listAll(platform), nil
}

func (r *chainRespAccountRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]service.Account, error) {
	return nil, nil
}

func (r *chainRespAccountRepo) ListByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.listAll(platform), nil
}

func (r *chainRespAccountRepo) ListByGroup(_ context.Context, groupID int64) ([]service.Account, error) {
	if accounts, ok := r.configured[groupID]; ok {
		return chainRespStamp(accounts, groupID, ""), nil
	}
	return chainRespStamp(r.schedulable[groupID], groupID, ""), nil
}

// chainRespUserRepo 只提供计费准入读取余额用的 GetByID。
type chainRespUserRepo struct {
	service.UserRepository
}

func (r *chainRespUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive, Balance: 100}, nil
}

func chainRespAccount(id int64) service.Account {
	return keepaliveMismatchAccount(id, 1, nil)
}

// ---- 测试装置 ----

type chainRespOptions struct {
	// primary 是 Key 的主分组；hops 是 ResolveEffectiveChain 返回的链（可为空）。
	primary *service.Group
	hops    []service.ChainHop
	// hasRoutes 对应 APIKey.HasGroupRoutes（鉴权快照里「这个 Key 配了链」）；switchOn 是全局开关；
	// noRuntime 表示 handler 没有接入回退链（groupFallback == nil），用来当作对照基线。
	hasRoutes  bool
	switchOn   bool
	noRuntime  bool
	resolveErr error
	settings   func(*service.GroupFallbackSettings)

	schedulable map[int64][]service.Account
	configured  map[int64][]service.Account
	replies     map[int64]chainRespReply
	// busy 里的账号永远拿不到并发槽。
	busy      map[int64]bool
	rpm       *hopRPMCache
	breaker   *chainRespBreaker
	keepalive int
	forced    []config.OpenAIForcedAccountRoute
	// audit 非 nil 时启用提示词审计，值是审计范围里的分组 ID（命中则拦截）。
	audit []int64
	// stableStore / stableKey 用于旧「稳定优先」与回退链的共存测试（只覆盖 chat/completions）：
	// stableKey 为 true 时 Key 开着稳定优先，stableStore 是（会记录调用的）稳定优先状态存储。
	stableStore service.StablePriorityStateStore
	stableKey   bool
}

// chainRespBase 是默认场景：主分组 1（倍率 1）有账号 11，兜底分组 2（倍率 2）有账号 21，链为 [1, 2]，开关打开。
func chainRespBase() chainRespOptions {
	g1 := chainRespGroup(1, 1, 0)
	g2 := chainRespGroup(2, 2, 0)
	return chainRespOptions{
		primary:   g1,
		hops:      chainRespHops(g1, g2),
		hasRoutes: true,
		switchOn:  true,
		schedulable: map[int64][]service.Account{
			1: {chainRespAccount(11)},
			2: {chainRespAccount(21)},
		},
		breaker: &chainRespBreaker{},
	}
}

// chainRespWith 与 chainRespBase 相同，但链换成给定的分组（第一个是主分组），账号由调用方自己指定。
func chainRespWith(groups ...*service.Group) chainRespOptions {
	o := chainRespBase()
	o.primary = groups[0]
	o.hops = chainRespHops(groups...)
	o.schedulable = map[int64][]service.Account{}
	return o
}

type chainRespHarness struct {
	handler   *OpenAIGatewayHandler
	router    *gin.Engine
	routes    *chainRespRoutes
	settings  *chainRespSettings
	upstream  *chainRespUpstream
	rpm       *hopRPMCache
	breaker   *chainRespBreaker
	audit     *scopedPromptEngine
	served    *handlerServedRecorder
	usageLogs chan *service.UsageLog
	flushed   chan struct{}
	// keys 是最近一次请求结束时 gin.Context.Keys 的快照（读取 ops 标记用）。
	keys map[string]any
}

func newChainRespHarness(t *testing.T, o chainRespOptions) *chainRespHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Gateway.StreamKeepaliveInterval = o.keepalive
	cfg.Gateway.OpenAIForcedAccountRoutes = o.forced

	hs := &chainRespHarness{
		usageLogs: make(chan *service.UsageLog, 8),
		flushed:   make(chan struct{}),
		rpm:       o.rpm,
		breaker:   o.breaker,
		keys:      map[string]any{},
	}
	hs.upstream = &chainRespUpstream{replies: o.replies, gate: hs.flushed}

	var rpmCache service.UserRPMCache
	if o.rpm != nil {
		rpmCache = o.rpm
	}
	billingCache := service.NewBillingCacheService(nil, &chainRespUserRepo{}, nil, nil, rpmCache, &hopRateRepo{}, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)

	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: hs.usageLogs}
	billingRepo := &openAIWSPrewarmUsageBillingRepoStub{commands: make(chan *service.UsageBillingCommand, 16)}
	// 调度器与入口共用同一个并发服务：busy 账号拿不到槽，调度器才会返回等槽计划，进入 hop 的短等 / 重选逻辑。
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
			return !o.busy[accountID], nil
		},
	}
	concurrency := service.NewConcurrencyService(cache)
	gateway := service.NewOpenAIGatewayService(
		&chainRespAccountRepo{schedulable: o.schedulable, configured: o.configured},
		usageRepo,
		billingRepo,
		nil, nil, nil, nil,
		cfg,
		nil, concurrency,
		service.NewBillingService(cfg, nil),
		nil,
		billingCache,
		hs.upstream,
		&service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, o.stableStore, nil,
	)

	h := NewOpenAIGatewayHandler(gateway, concurrency, billingCache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
	h.maxAccountSwitches = 3

	settings := service.DefaultGroupFallbackSettings()
	settings.Enabled = o.switchOn
	settings.BusyWaitMS = 30
	settings.StickyWaitMS = 30
	if o.settings != nil {
		o.settings(&settings)
	}
	hs.settings = &chainRespSettings{settings: settings}
	hs.routes = &chainRespRoutes{hops: o.hops, err: o.resolveErr}
	if !o.noRuntime {
		var breaker service.GroupChainBreakerGate
		if o.breaker != nil {
			breaker = o.breaker
		}
		h.groupFallback = newGroupFallbackRuntime(hs.routes, hs.settings, breaker)
	}
	if o.audit != nil {
		hs.audit = &scopedPromptEngine{cfg: securityaudit.ActiveConfig{GroupIDs: o.audit}}
		coordinator := securityaudit.NewCoordinator(nil, hs.audit)
		hs.served = &handlerServedRecorder{}
		coordinator.SetServedGroupRecorder(hs.served)
		h.securityAuditCoordinator = coordinator
	}
	hs.handler = h

	primaryID := o.primary.ID
	apiKey := &service.APIKey{
		ID:             1930,
		UserID:         chainRespUserID,
		GroupID:        &primaryID,
		Group:          o.primary,
		HasGroupRoutes: o.hasRoutes,
		User:           &service.User{ID: chainRespUserID, Status: service.StatusActive, Balance: 100},

		StablePriorityEnabled: o.stableKey,
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: chainRespUserID, Concurrency: 1})
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "req-chain-resp"))
		c.Writer = &keepaliveMismatchFlushSignalWriter{ResponseWriter: c.Writer, flushed: hs.flushed}
		c.Next()
		for k, v := range c.Keys {
			hs.keys[k] = v
		}
	})
	router.POST("/openai/v1/responses", h.Responses)
	router.POST("/openai/v1/chat/completions", h.ChatCompletions)
	hs.router = router
	return hs
}

func (hs *chainRespHarness) serve(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.router.ServeHTTP(rec, req)
	return rec
}

// waitUsage 收集 n 条异步落库的 usage 行。
func (hs *chainRespHarness) waitUsage(t *testing.T, n int) []*service.UsageLog {
	t.Helper()
	logs := make([]*service.UsageLog, 0, n)
	for len(logs) < n {
		select {
		case log := <-hs.usageLogs:
			logs = append(logs, log)
		case <-time.After(3 * time.Second):
			t.Fatalf("等待第 %d/%d 条 usage 行超时", len(logs)+1, n)
		}
	}
	return logs
}

type chainRespOutcome struct {
	status int
	body   string
	calls  []int64
	usage  *service.UsageLog
}

// runChainRespCase 发一次流式 Responses 请求；服务成功时等到那条 usage 行。
func runChainRespCase(t *testing.T, o chainRespOptions, body string) (chainRespOutcome, *chainRespHarness) {
	t.Helper()
	hs := newChainRespHarness(t, o)
	rec := hs.serve(body)
	out := chainRespOutcome{status: rec.Code, body: rec.Body.String(), calls: hs.upstream.accountCalls()}
	if rec.Code == http.StatusOK && len(out.calls) > 0 {
		out.usage = hs.waitUsage(t, 1)[0]
	}
	return out, hs
}

func requireNoServedColumns(t *testing.T, usage *service.UsageLog, home int64) {
	t.Helper()
	require.NotNil(t, usage.GroupID)
	require.Equal(t, home, *usage.GroupID)
	require.Nil(t, usage.ServedGroupID, "与引入回退链之前的写入完全一致")
	require.Nil(t, usage.ServedRouteSource)
}

// ---- 无链路径逐字节一致 ----

func TestResponsesChain_GatedRequestsBehaveLikeLegacy(t *testing.T) {
	scenarios := []struct {
		name   string
		mutate func(*chainRespOptions)
	}{
		{"primary group serves", func(*chainRespOptions) {}},
		{"primary group has no accounts", func(o *chainRespOptions) { o.schedulable[1] = nil }},
		{"primary group upstream failure", func(o *chainRespOptions) { o.replies = map[int64]chainRespReply{11: chainRespUpstreamError()} }},
	}
	gates := []struct {
		name         string
		mutate       func(*chainRespOptions)
		wantResolves int32
	}{
		{"switch off", func(o *chainRespOptions) { o.switchOn = false }, 0},
		{"key without a chain", func(o *chainRespOptions) { o.hasRoutes = false }, 0},
		{"chain shorter than two hops", func(o *chainRespOptions) { o.hops = o.hops[:1] }, 1},
		{"chain resolution error", func(o *chainRespOptions) { o.resolveErr = fmt.Errorf("resolve failed") }, 1},
		{"fallback runtime not wired", func(o *chainRespOptions) { o.noRuntime = true }, 0},
	}
	for _, sc := range scenarios {
		for _, gate := range gates {
			t.Run(sc.name+"/"+gate.name, func(t *testing.T) {
				baselineOpts := chainRespBase()
				sc.mutate(&baselineOpts)
				baselineOpts.noRuntime = true
				baseline, _ := runChainRespCase(t, baselineOpts, chainRespBodyJSON)

				opts := chainRespBase()
				sc.mutate(&opts)
				gate.mutate(&opts)
				got, hs := runChainRespCase(t, opts, chainRespBodyJSON)

				require.Equal(t, baseline.status, got.status)
				require.Equal(t, baseline.body, got.body, "响应体与无链路径完全一致")
				require.Equal(t, baseline.calls, got.calls, "上游调用序列一致，兜底分组的账号 21 从未被碰")
				require.EqualValues(t, gate.wantResolves, hs.routes.calls.Load())
				require.Empty(t, opts.breaker.admitted(), "不进熔断")
				require.Empty(t, opts.breaker.failedGroups())
				if baseline.usage == nil {
					require.Nil(t, got.usage)
					return
				}
				require.NotNil(t, got.usage)
				require.Equal(t, baseline.usage.AccountID, got.usage.AccountID)
				require.InDelta(t, baseline.usage.RateMultiplier, got.usage.RateMultiplier, 1e-12)
				requireNoServedColumns(t, got.usage, 1)
			})
		}
	}
}

func TestResponsesChain_LegacyPathServesFromPrimaryGroupOnly(t *testing.T) {
	// 对照基线自身的健全性：主分组有号时就是账号 11 服务，计费倍率是主分组的 1。
	o := chainRespBase()
	o.noRuntime = true
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)
	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{11}, got.calls)
	require.Contains(t, got.body, "served-by-11")
	require.InDelta(t, 1.0, got.usage.RateMultiplier, 1e-12)
	requireNoServedColumns(t, got.usage, 1)
}

// ---- 兜底服务与 served 计费 ----

func TestResponsesChain_NoAccountFallsBackAndBillsHomeGroupWithServedColumns(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	o.audit = []int64{9999} // 审计范围不含链上任何分组：放行，但要看到审计时用的分组集合
	got, hs := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls)
	require.Contains(t, got.body, "served-by-21")
	require.NotContains(t, got.body, "response.failed")

	// served 计费：group_id 是主分组，served_group_id / served_route_source 记实际服务的分组与来源，倍率按服务分组。
	require.NotNil(t, got.usage.GroupID)
	require.EqualValues(t, 1, *got.usage.GroupID)
	require.NotNil(t, got.usage.ServedGroupID)
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.NotNil(t, got.usage.ServedRouteSource)
	require.EqualValues(t, service.ServedRouteSourceUserChain, *got.usage.ServedRouteSource)
	require.InDelta(t, 2.0, got.usage.RateMultiplier, 1e-9, "定价按服务分组（倍率 2）")
	require.EqualValues(t, 21, got.usage.AccountID)

	// B1：链只解析一次；审计看到的是链上全部分组的并集，服务分组属于其中。
	require.EqualValues(t, 1, hs.routes.calls.Load())
	hs.audit.mu.Lock()
	seen := append([]securityaudit.Request(nil), hs.audit.seen...)
	hs.audit.mu.Unlock()
	require.Len(t, seen, 1)
	audited := map[int64]bool{}
	for _, g := range seen[0].ChainGroups {
		audited[g.ID] = true
	}
	require.True(t, audited[1] && audited[2])
	require.True(t, audited[*got.usage.ServedGroupID], "服务分组必须属于审计时使用的集合")
	require.EqualValues(t, 1, *seen[0].GroupID, "用户可见的分组仍是主分组")

	// 审计事件补记服务分组；ops 事件记录服务分组；池里没有账号不计熔断，只有非末跳进熔断。
	require.Equal(t, 1, hs.served.calls)
	require.EqualValues(t, 2, hs.served.groupID)
	require.EqualValues(t, 2, hs.keys[service.OpsServedGroupIDKey])
	require.Empty(t, o.breaker.failedGroups())
	require.Equal(t, []int64{1}, o.breaker.admitted())
}

func TestResponsesChain_FailoverExhaustedFallsBackAndConfirmsBreakerFailure(t *testing.T) {
	o := chainRespBase()
	o.replies = map[int64]chainRespReply{11: chainRespUpstreamError()}
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{11, 21}, got.calls)
	require.Contains(t, got.body, "served-by-21")
	require.NotContains(t, got.body, "unknown error", "主分组的上游错误不能泄漏给兜底成功的请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.Equal(t, []int64{1}, o.breaker.failedGroups(), "5xx 暂记，下一跳成功后才计入主分组的熔断")
}

// 弱 429：账号近 30 秒的 429 占比不够，429 闸门故意不换号，组内其它账号一个都没试过。
// 这不是整组耗尽的证据：请求照常回退到下一个分组，但熔断器不能有任何记录（BK-1）。
func TestResponsesChain_Weak429OnFirstHopFallsBackWithoutBreakerCount(t *testing.T) {
	o := chainRespBase()
	// 429 闸门是进程级状态，账号号段与其它用例错开，避免互相影响。
	o.schedulable = map[int64][]service.Account{
		1: {chainRespAccount(1101)},
		2: {chainRespAccount(2101)},
	}
	o.replies = map[int64]chainRespReply{
		1101: {status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error","message":"slow down"}}`, contentType: "application/json"},
	}
	require.False(t, service.ShouldSwitchAccountOn429(1101), "前提：这个账号没有任何放行换号的 429 判定")
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{1101, 2101}, got.calls, "主分组只试了一个号就被 429 闸门停下，回退到下一个分组")
	require.Contains(t, got.body, "served-by-2101")
	require.NotContains(t, got.body, "slow down", "主分组的上游错误不能泄漏给兜底成功的请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID, "计费 served 列是下一跳")
	require.EqualValues(t, 1, *got.usage.GroupID, "group_id 仍是主分组")
	require.Empty(t, o.breaker.failedGroups(), "弱 429 不是整组耗尽的证据：不计熔断")
}

func TestResponsesChain_AllHopsFailEndsWithLastHopOriginalError(t *testing.T) {
	o := chainRespBase()
	o.replies = map[int64]chainRespReply{11: chainRespUpstreamError(), 21: chainRespUpstreamError()}
	got, hs := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, []int64{11, 21}, got.calls)
	require.GreaterOrEqual(t, got.status, 500)
	require.Nil(t, got.usage)
	require.Empty(t, o.breaker.failedGroups(), "下一跳也失败：主分组暂记的 5xx 作废")
	require.EqualValues(t, 1, hs.routes.calls.Load())

	// 最终错误与「只有最后一跳那个分组、没有回退链」时一模一样，不暴露路由细节。
	baselineOpts := chainRespBase()
	baselineOpts.noRuntime = true
	baselineOpts.schedulable = map[int64][]service.Account{1: {chainRespAccount(21)}}
	baselineOpts.replies = map[int64]chainRespReply{21: chainRespUpstreamError()}
	baseline, _ := runChainRespCase(t, baselineOpts, chainRespBodyJSON)
	require.Equal(t, baseline.status, got.status)
	require.Equal(t, baseline.body, got.body)
}

func TestResponsesChain_AuditHitOnFallbackGroupBlocksBeforeAnyAttempt(t *testing.T) {
	o := chainRespBase()
	o.audit = []int64{2} // 只有兜底分组在审计范围内
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_error")
	require.Empty(t, hs.upstream.accountCalls(), "审计拦截即终止，不会去试任何一跳")
	require.EqualValues(t, 1, hs.routes.calls.Load(), "链在审计之前只解析一次")
	require.Equal(t, 0, hs.served.calls)
}

// ---- 分组层 RPM ----

func TestResponsesChain_FirstHopRPMExceededReturns429WithoutFallback(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 1), chainRespGroup(2, 2, 0))
	o.schedulable = map[int64][]service.Account{1: {chainRespAccount(11)}, 2: {chainRespAccount(21)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Empty(t, hs.upstream.accountCalls(), "首跳超限：429，不回退")
	require.EqualValues(t, 0, hs.rpm.decr, "首跳超限保持计数，与无链现状一致")
	require.Empty(t, o.breaker.admitted())
}

func TestResponsesChain_NonFirstHopRPMExceededIsSkipped(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 0), chainRespGroup(2, 2, 1), chainRespGroup(3, 3, 0))
	o.schedulable = map[int64][]service.Account{2: {chainRespAccount(21)}, 3: {chainRespAccount(31)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	got, hs := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{31}, got.calls, "第 2 个分组超限被跳过，由第 3 个分组服务")
	require.EqualValues(t, 3, *got.usage.ServedGroupID)
	require.EqualValues(t, 1, hs.rpm.decr, "被跳过的这一跳不占用 RPM 额度")
}

func TestResponsesChain_LastHopRPMExceededWritesRateLimitError(t *testing.T) {
	o := chainRespWith(chainRespGroup(1, 1, 0), chainRespGroup(2, 2, 1))
	o.schedulable = map[int64][]service.Account{2: {chainRespAccount(21)}}
	o.rpm = &hopRPMCache{counts: []int{2}}
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.Equal(t, http.StatusTooManyRequests, rec.Code, "末跳超限：429 覆盖主分组暂存的没号错误")
	require.Empty(t, hs.upstream.accountCalls())
	require.EqualValues(t, 1, hs.rpm.decr)
}

// ---- 熔断计数 ----

func TestResponsesChain_BusyHopFallsBackWithoutBreakerCount(t *testing.T) {
	o := chainRespBase()
	o.busy = map[int64]bool{11: true}
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "主分组的号一直拿不到槽：短等后换组，从未向上游发请求")
	require.EqualValues(t, 2, *got.usage.ServedGroupID)
	require.Empty(t, o.breaker.failedGroups(), "繁忙不计熔断")
}

func TestResponsesChain_NoAccountCountsTowardBreakerOnlyWhenPoolHasSchedulableAccounts(t *testing.T) {
	o := chainRespBase()
	o.schedulable[1] = nil
	// 池里配置了可用账号（诊断看得到），但此刻选不出号：这是容量问题，计入熔断。
	o.configured = map[int64][]service.Account{1: {chainRespAccount(11)}}
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls)
	require.Equal(t, []int64{1}, o.breaker.failedGroups())
}

// ---- 输出状态：心跳 / 真实内容 ----

func TestResponsesChain_HeartbeatOnlyThenFailureFallsBackToNextGroup(t *testing.T) {
	o := chainRespBase()
	o.keepalive = 1
	o.replies = map[int64]chainRespReply{
		11: {body: keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak"), gated: true},
	}
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.Equal(t, []int64{11, 21}, hs.upstream.accountCalls())
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, keepaliveMismatchSSEComment), "客户端先收到心跳: %q", body)
	require.Contains(t, body, "served-by-21", "只写过心跳不算内容交付：可以换组")
	require.NotContains(t, body, "leak")
	require.NotContains(t, body, keepaliveMismatchWrongModel)
	require.NotContains(t, body, "response.failed")

	logs := hs.waitUsage(t, 2)
	served := 0
	for _, log := range logs {
		if log.UpstreamModelMismatch {
			require.EqualValues(t, 11, log.AccountID, "被拦截的尝试落一条零计费审计行")
			continue
		}
		served++
		require.EqualValues(t, 21, log.AccountID)
		require.NotNil(t, log.ServedGroupID)
		require.EqualValues(t, 2, *log.ServedGroupID)
	}
	require.Equal(t, 1, served)
}

func TestResponsesChain_HeartbeatOnlyAllGroupsFailEndsInStreamFormat(t *testing.T) {
	o := chainRespBase()
	o.keepalive = 1
	wrong := keepaliveMismatchResponsesSSE(keepaliveMismatchWrongModel, "leak")
	o.replies = map[int64]chainRespReply{11: {body: wrong, gated: true}, 21: {body: wrong, gated: true}}
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.Equal(t, []int64{11, 21}, hs.upstream.accountCalls())
	require.Equal(t, http.StatusOK, rec.Code, "心跳已提交 200，最终错误不能再改状态码写 JSON")
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, keepaliveMismatchSSEComment), "客户端先收到心跳: %q", body)
	require.Contains(t, body, "event: response.failed", "耗尽时在同一 SSE 连接内以流内终止事件收尾")
	require.NotContains(t, body, "leak")
	require.NotContains(t, body, keepaliveMismatchWrongModel)
	hs.waitUsage(t, 2)
}

func TestResponsesChain_RealContentAlreadyWrittenDoesNotFallBack(t *testing.T) {
	o := chainRespBase()
	o.replies = map[int64]chainRespReply{11: {body: keepaliveOutputThenRetryableResponseFailedSSE()}}
	hs := newChainRespHarness(t, o)
	rec := hs.serve(chainRespBodyJSON)

	require.Equal(t, []int64{11}, hs.upstream.accountCalls(), "真实内容已写给客户端：不换组，账号 21 不会被碰")
	body := rec.Body.String()
	require.Contains(t, body, "partial output")
	require.Contains(t, body, "response.failed")
	require.NotContains(t, body, "served-by-21")
	require.Empty(t, o.breaker.failedGroups())
}

// ---- 静态资格 ----

func TestResponsesChain_UnpricedModelSkipsFallbackHop(t *testing.T) {
	const unpriced = `{"model":"pricing-missing-test-model","stream":true,"input":"hello"}`

	o := chainRespBase()
	o.schedulable[1] = nil
	got, hs := runChainRespCase(t, o, unpriced)

	require.NotEqual(t, http.StatusOK, got.status)
	require.Empty(t, got.calls, "没配价格的模型会按零成本放行，兜底分组不能白送：这一跳被跳过")
	require.EqualValues(t, 1, hs.routes.calls.Load())

	// 被跳过之后客户端收到的就是主分组原本的错误。
	baselineOpts := chainRespBase()
	baselineOpts.noRuntime = true
	baselineOpts.schedulable[1] = nil
	baseline, _ := runChainRespCase(t, baselineOpts, unpriced)
	require.Equal(t, baseline.status, got.status)
	require.Equal(t, baseline.body, got.body)
}

// ---- forced 账号路由（设计 6.3） ----

func TestResponsesChain_ForcedUserWithoutHiddenChainKeepsForcedRouting(t *testing.T) {
	o := chainRespBase()
	o.forced = []config.OpenAIForcedAccountRoute{{UserID: chainRespUserID, AccountID: 21}}
	got, hs := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{21}, got.calls, "forced 路由照旧生效")
	requireNoServedColumns(t, got.usage, 1)
	require.Empty(t, o.breaker.admitted(), "链层对 forced 用户短路：不进逐跳循环")
	require.EqualValues(t, 1, hs.routes.calls.Load())
}

func TestResponsesChain_ForcedUserWithHiddenHeadYieldsToTheChain(t *testing.T) {
	g1 := chainRespGroup(1, 1, 0)
	g9 := chainRespGroup(9, 3, 0)
	o := chainRespBase()
	o.primary = g1
	o.hops = []service.ChainHop{
		{GroupID: 9, Group: g9, RouteSource: service.RouteSourceAdmin},
		{GroupID: 1, Group: g1, RouteSource: service.RouteSourcePrimary},
	}
	o.schedulable = map[int64][]service.Account{9: {chainRespAccount(91)}, 1: {chainRespAccount(11)}}
	o.forced = []config.OpenAIForcedAccountRoute{{UserID: chainRespUserID, AccountID: 11}}
	got, _ := runChainRespCase(t, o, chainRespBodyJSON)

	require.Equal(t, http.StatusOK, got.status)
	require.Equal(t, []int64{91}, got.calls, "有管理员隐藏链：forced 路由让位，第 0 跳走标准调度")
	require.EqualValues(t, 1, *got.usage.GroupID, "group_id 仍是用户 Key 的主分组")
	require.NotNil(t, got.usage.ServedGroupID)
	require.EqualValues(t, 9, *got.usage.ServedGroupID)
	require.NotNil(t, got.usage.ServedRouteSource)
	require.EqualValues(t, service.ServedRouteSourceAdminChain, *got.usage.ServedRouteSource)
	require.InDelta(t, 3.0, got.usage.RateMultiplier, 1e-9)
}
