package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---- 测试替身 ----

// scopedPromptEngine 模拟真实 PromptService 的范围判定：先 ScopeRequest（并集），再 IncludesGroup；范围内一律拦截。
type scopedPromptEngine struct {
	cfg  securityaudit.ActiveConfig
	mu   sync.Mutex
	seen []securityaudit.Request
}

func (e *scopedPromptEngine) EffectiveMode() securityaudit.Mode { return securityaudit.ModeBlocking }

func (e *scopedPromptEngine) Enqueue(context.Context, securityaudit.Request) error { return nil }

func (e *scopedPromptEngine) Evaluate(_ context.Context, req securityaudit.Request) (*securityaudit.PromptDecision, error) {
	req = e.cfg.ScopeRequest(req)
	e.mu.Lock()
	e.seen = append(e.seen, req)
	e.mu.Unlock()
	if !e.cfg.IncludesGroup(req.GroupID) {
		return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
	}
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionBlock, AllowNextStage: false}, nil
}

func (e *scopedPromptEngine) lastGroupID() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.seen) == 0 || e.seen[len(e.seen)-1].GroupID == nil {
		return 0
	}
	return *e.seen[len(e.seen)-1].GroupID
}

type chainModSettingRepo struct {
	values map[string]string
}

func (r *chainModSettingRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	if v, ok := r.values[key]; ok {
		return &service.Setting{Key: key, Value: v}, nil
	}
	return nil, service.ErrSettingNotFound
}

func (r *chainModSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *chainModSettingRepo) Set(context.Context, string, string) error { return nil }

func (r *chainModSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *chainModSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }

func (r *chainModSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

func (r *chainModSettingRepo) Delete(context.Context, string) error { return nil }

type chainModRepo struct{}

func (chainModRepo) CreateLog(context.Context, *service.ContentModerationLog) error { return nil }

func (chainModRepo) ListLogs(context.Context, service.ContentModerationLogFilter) ([]service.ContentModerationLog, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (chainModRepo) CountFlaggedByUserSince(context.Context, int64, time.Time, bool) (int, error) {
	return 0, nil
}

func (chainModRepo) CleanupExpiredLogs(context.Context, time.Time, time.Time) (*service.ContentModerationCleanupResult, error) {
	return nil, nil
}

func (chainModRepo) UpdateLogEmailSent(context.Context, int64, bool) error { return nil }

func newChainModerationService(t *testing.T, groupIDs ...int64) *service.ContentModerationService {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"enabled":               true,
		"mode":                  service.ContentModerationModePreBlock,
		"all_groups":            false,
		"group_ids":             groupIDs,
		"blocked_keywords":      []string{"secret-token"},
		"keyword_blocking_mode": service.ContentModerationKeywordModeKeywordOnly,
		"email_on_hit":          false,
		"auto_ban_enabled":      false,
	})
	require.NoError(t, err)
	return service.NewContentModerationService(
		&chainModSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(raw),
		}},
		chainModRepo{}, nil, nil, nil, nil, nil, nil,
	)
}

func newChainAuditContext() (*gin.Context, *service.APIKey, middleware2.AuthSubject) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	primary := int64(1)
	apiKey := &service.APIKey{ID: 9, UserID: 7, GroupID: &primary, Group: &service.Group{ID: 1, Name: "primary", Platform: service.PlatformOpenAI}}
	return ctx, apiKey, middleware2.AuthSubject{UserID: 7}
}

func chainHops(ids ...int64) []service.ChainHop {
	hops := make([]service.ChainHop, 0, len(ids))
	for i, id := range ids {
		source := service.RouteSourceUser
		if i == 0 {
			source = service.RouteSourcePrimary
		}
		hops = append(hops, service.ChainHop{GroupID: id, Group: &service.Group{ID: id, Name: "g", Platform: service.PlatformOpenAI}, RouteSource: source})
	}
	return hops
}

var chainAuditBody = []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"secret-token"}]}`)

// ---- B1：提示词审计（coordinator + PromptEngine） ----

func TestRunSecurityAuditForChain_FallbackGroupInScopeBlocksRequest(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	engine := &scopedPromptEngine{cfg: securityaudit.ActiveConfig{GroupIDs: []int64{42}}}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	decision := runSecurityAuditForChain(ctx, nil, coordinator, nil, apiKey, subject, "openai_chat_completions", "gpt-test", chainAuditBody, "http", chainHops(1, 42))
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionBlock, decision.Kind, "主分组不在范围、兜底分组在范围，必须拦截")
	require.False(t, decision.AllowNextStage)
	require.EqualValues(t, 42, engine.lastGroupID(), "审计请求里的分组是触发审计的那一跳")
	_, completed := ctx.Get(securityAuditCompletedContextKey)
	require.False(t, completed, "被拦截时不得标记审计已完成")
}

func TestRunSecurityAudit_NoChainUnchanged(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	engine := &scopedPromptEngine{cfg: securityaudit.ActiveConfig{GroupIDs: []int64{42}}}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	// 无链：只看主分组，不在范围内就放行（与现状一致）。
	decision := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, "openai_chat_completions", "gpt-test", chainAuditBody, "http")
	require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
	require.EqualValues(t, 1, engine.lastGroupID())

	// 空链等同于无链。
	ctx2, apiKey2, subject2 := newChainAuditContext()
	decision = runSecurityAuditForChain(ctx2, nil, coordinator, nil, apiKey2, subject2, "openai_chat_completions", "gpt-test", chainAuditBody, "http", nil)
	require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
	require.EqualValues(t, 1, engine.lastGroupID())
}

func TestRunSecurityAuditForChain_NoGroupInScopeAllows(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	engine := &scopedPromptEngine{cfg: securityaudit.ActiveConfig{GroupIDs: []int64{42}}}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	decision := runSecurityAuditForChain(ctx, nil, coordinator, nil, apiKey, subject, "openai_chat_completions", "gpt-test", chainAuditBody, "http", chainHops(1, 2, 3))
	require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
	require.True(t, decision.AllowNextStage)
}

// ---- B1：内容审核（经 coordinator 的 legacy 适配器，以及无 coordinator 的旧路径） ----

func TestRunSecurityAuditForChain_ModerationViaCoordinatorBlocksFallbackScope(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	moderation := newChainModerationService(t, 42)
	coordinator := securityaudit.NewCoordinator(securityaudit.NewLegacyModerationAdapter(moderation), nil)

	decision := runSecurityAuditForChain(ctx, nil, coordinator, moderation, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody, "http", chainHops(1, 42))
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionBlock, decision.Kind)

	// 同一个配置、无链：主分组不在范围，放行（行为不变）。
	ctx2, apiKey2, subject2 := newChainAuditContext()
	decision = runSecurityAudit(ctx2, nil, coordinator, moderation, apiKey2, subject2, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody, "http")
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
}

func TestRunSecurityAuditForChain_ModerationLegacyPathBlocksFallbackScope(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	moderation := newChainModerationService(t, 42)

	decision := runSecurityAuditForChain(ctx, nil, nil, moderation, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody, "http", chainHops(1, 42))
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionBlock, decision.Kind)
	require.False(t, decision.AllowNextStage)

	ctx2, apiKey2, subject2 := newChainAuditContext()
	decision = runSecurityAudit(ctx2, nil, nil, moderation, apiKey2, subject2, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody, "http")
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
}

func TestRunContentModerationForChain_EmptyChainIsLegacyBehavior(t *testing.T) {
	ctx, apiKey, subject := newChainAuditContext()
	moderation := newChainModerationService(t, 1)

	byChain := runContentModerationForChain(ctx, nil, moderation, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody, nil)
	plain := runContentModeration(ctx, nil, moderation, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "gpt-test", chainAuditBody)
	require.NotNil(t, byChain)
	require.NotNil(t, plain)
	require.Equal(t, plain.Blocked, byChain.Blocked)
	require.True(t, byChain.Blocked)
}

// ---- 入口方法包装 ----

func TestHandlerCheckSecurityAuditForChainWrappers(t *testing.T) {
	engine := &scopedPromptEngine{cfg: securityaudit.ActiveConfig{GroupIDs: []int64{42}}}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	ctx, apiKey, subject := newChainAuditContext()
	oh := &OpenAIGatewayHandler{securityAuditCoordinator: coordinator}
	decision := oh.checkSecurityAuditForChain(ctx, nil, apiKey, subject, "openai_chat_completions", "gpt-test", chainAuditBody, chainHops(1, 42))
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionBlock, decision.Kind)

	ctx2, apiKey2, subject2 := newChainAuditContext()
	gh := &GatewayHandler{securityAuditCoordinator: coordinator}
	decision = gh.checkSecurityAuditForChain(ctx2, nil, apiKey2, subject2, "openai_chat_completions", "gpt-test", chainAuditBody, chainHops(1, 42))
	require.NotNil(t, decision)
	require.Equal(t, securityaudit.DecisionBlock, decision.Kind)

	var nilOH *OpenAIGatewayHandler
	require.Nil(t, nilOH.checkSecurityAuditForChain(ctx, nil, apiKey, subject, "p", "m", chainAuditBody, chainHops(1)))
	var nilGH *GatewayHandler
	require.Nil(t, nilGH.checkSecurityAuditForChain(ctx, nil, apiKey, subject, "p", "m", chainAuditBody, chainHops(1)))
}

// 补记 served 分组：未配置 recorder 时是空操作；配置后按 request_id 转交。
type handlerServedRecorder struct {
	calls   int
	groupID int64
}

func (r *handlerServedRecorder) RecordServedGroup(_ context.Context, _ string, servedGroupID int64) error {
	r.calls++
	r.groupID = servedGroupID
	return nil
}

func TestRecordSecurityAuditServedGroup(t *testing.T) {
	ctx, _, _ := newChainAuditContext()
	coordinator := securityaudit.NewCoordinator(nil, nil)
	oh := &OpenAIGatewayHandler{securityAuditCoordinator: coordinator}
	gh := &GatewayHandler{securityAuditCoordinator: coordinator}

	require.NotPanics(t, func() { oh.recordSecurityAuditServedGroup(ctx, nil, 42) })
	require.NotPanics(t, func() { gh.recordSecurityAuditServedGroup(ctx, nil, 42) })

	rec := &handlerServedRecorder{}
	coordinator.SetServedGroupRecorder(rec)
	// 没有 request_id 时不调用。
	oh.recordSecurityAuditServedGroup(ctx, nil, 42)
	require.Equal(t, 0, rec.calls)

	// 有 request_id 时按 request_id 转交。
	ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), ctxkey.RequestID, "req-1"))
	oh.recordSecurityAuditServedGroup(ctx, nil, 42)
	require.Equal(t, 1, rec.calls)
	require.EqualValues(t, 42, rec.groupID)

	var nilOH *OpenAIGatewayHandler
	require.NotPanics(t, func() { nilOH.recordSecurityAuditServedGroup(ctx, nil, 42) })
}

// ---- B1 不变式：实际服务的分组 ∈ 审核时使用的集合 ----

func TestChainServedGroupIsWithinAuditedSet(t *testing.T) {
	chain := chainHops(1, 2, 3, 4)
	audited := map[int64]bool{}
	for _, g := range chainModerationGroups(chain) {
		audited[g.ID] = true
	}
	require.Len(t, audited, 4)

	for servedIdx := 0; servedIdx < len(chain); servedIdx++ {
		var served []int64
		runner := &service.GroupChainRunner{}
		res := runner.Run(context.Background(), service.ChainRunInput{Chain: chain, Model: "gpt-test", UserID: 7}, func(_ context.Context, info service.HopInfo) service.HopResult {
			served = append(served, info.Hop.GroupID)
			if info.Index == servedIdx {
				return service.HopResult{Outcome: service.HopOutcomeDone, Attempts: 1}
			}
			return service.HopResult{
				Outcome:         service.HopOutcomeFallbackWorthy,
				Reason:          service.FallbackReasonNoAccount,
				Attempts:        1,
				WriteFinalError: func() {},
			}
		})
		require.Equal(t, service.ChainRunServed, res.Status)
		require.Equal(t, servedIdx, res.ServedIndex)
		for _, id := range served {
			require.True(t, audited[id], "尝试过的分组 %d 必须属于审核时使用的集合", id)
		}
		require.Equal(t, chain[servedIdx].GroupID, served[len(served)-1])
	}
}

func TestChainModerationGroupsSkipsInvalidAndKeepsOrder(t *testing.T) {
	require.Nil(t, chainModerationGroups(nil))
	groups := chainModerationGroups([]service.ChainHop{
		{GroupID: 5, Group: &service.Group{ID: 5, Name: "five"}},
		{GroupID: 0, Group: nil},
		{GroupID: 0, Group: &service.Group{ID: 9, Name: "nine"}},
		{GroupID: 7},
	})
	require.Equal(t, []service.ContentModerationChainGroup{{ID: 5, Name: "five"}, {ID: 9, Name: "nine"}, {ID: 7}}, groups)
	require.Nil(t, chainSecurityAuditGroups(nil))
	require.Equal(t, []securityaudit.ChainGroup{{ID: 5, Name: "five"}, {ID: 9, Name: "nine"}, {ID: 7}}, chainSecurityAuditGroups([]service.ChainHop{
		{GroupID: 5, Group: &service.Group{ID: 5, Name: "five"}},
		{GroupID: 0, Group: &service.Group{ID: 9, Name: "nine"}},
		{GroupID: 7},
	}))
}

// ---- B2 / N2：分组层 RPM 的首跳 / 非首跳处理 ----

type hopRPMCache struct {
	counts []int
	incr   int32
	decr   int32
}

func (c *hopRPMCache) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	idx := int(atomic.AddInt32(&c.incr, 1)) - 1
	if idx < len(c.counts) {
		return c.counts[idx], nil
	}
	return 1, nil
}
func (c *hopRPMCache) IncrementUserRPM(context.Context, int64) (int, error)       { return 1, nil }
func (c *hopRPMCache) GetUserGroupRPM(context.Context, int64, int64) (int, error) { return 0, nil }
func (c *hopRPMCache) GetUserRPM(context.Context, int64) (int, error)             { return 0, nil }
func (c *hopRPMCache) DecrementUserGroupRPM(context.Context, int64, int64) error {
	atomic.AddInt32(&c.decr, 1)
	return nil
}

type hopRateRepo struct {
	service.UserGroupRateRepository
	override *int
}

func (r *hopRateRepo) GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error) {
	return r.override, nil
}

func newHopRPMBilling(t *testing.T, cache service.UserRPMCache) *service.BillingCacheService {
	t.Helper()
	svc := service.NewBillingCacheService(nil, nil, nil, nil, cache, &hopRateRepo{}, &config.Config{}, nil, nil)
	t.Cleanup(svc.Stop)
	return svc
}

func hopWithRPMLimit(id int64, limit int) service.ChainHop {
	return service.ChainHop{GroupID: id, Group: &service.Group{ID: id, RPMLimit: limit}}
}

func TestCheckHopGroupRPM_FirstHopExceededRejectsWithoutRelease(t *testing.T) {
	cache := &hopRPMCache{counts: []int{2}}
	billing := newHopRPMBilling(t, cache)

	verdict, ticket := CheckHopGroupRPM(context.Background(), billing, &service.User{ID: 1}, hopWithRPMLimit(10, 1), 0)
	require.Equal(t, GroupRPMReject, verdict, "首跳超限：429，不回退")
	require.NotNil(t, ticket)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.decr), "首跳超限保持计数不变，与无链现状一致")
}

func TestCheckHopGroupRPM_NonFirstHopExceededSkipsAndReleases(t *testing.T) {
	cache := &hopRPMCache{counts: []int{2}}
	billing := newHopRPMBilling(t, cache)

	verdict, ticket := CheckHopGroupRPM(context.Background(), billing, &service.User{ID: 1}, hopWithRPMLimit(20, 1), 1)
	require.Equal(t, GroupRPMSkip, verdict, "非首跳超限：跳过")
	require.Nil(t, ticket)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.decr), "被跳过的这一跳不应占用 RPM 额度")
}

func TestCheckHopGroupRPM_ProceedAndReleaseByOutcome(t *testing.T) {
	for _, tc := range []struct {
		outcome      service.HopOutcome
		wantReleased int32
	}{
		{service.HopOutcomeDone, 0},
		{service.HopOutcomeTerminal, 0},
		{service.HopOutcomeFallbackWorthy, 1},
		{service.HopOutcomeSkipped, 1},
	} {
		cache := &hopRPMCache{counts: []int{1}}
		billing := newHopRPMBilling(t, cache)
		verdict, ticket := CheckHopGroupRPM(context.Background(), billing, &service.User{ID: 1}, hopWithRPMLimit(10, 5), 0)
		require.Equal(t, GroupRPMProceed, verdict)
		require.NotNil(t, ticket)
		ReleaseHopGroupRPMIfNotServed(context.Background(), ticket, tc.outcome)
		require.Equal(t, tc.wantReleased, atomic.LoadInt32(&cache.decr), "outcome=%v", tc.outcome)
	}
}

func TestCheckHopGroupRPM_NoLimitOrNilInputsProceed(t *testing.T) {
	cache := &hopRPMCache{}
	billing := newHopRPMBilling(t, cache)

	verdict, ticket := CheckHopGroupRPM(context.Background(), billing, &service.User{ID: 1}, hopWithRPMLimit(10, 0), 0)
	require.Equal(t, GroupRPMProceed, verdict)
	require.Nil(t, ticket)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.incr), "没有分组限额不计数")

	verdict, ticket = CheckHopGroupRPM(context.Background(), nil, &service.User{ID: 1}, hopWithRPMLimit(10, 1), 0)
	require.Equal(t, GroupRPMProceed, verdict)
	require.Nil(t, ticket)
	verdict, _ = CheckHopGroupRPM(context.Background(), billing, nil, hopWithRPMLimit(10, 1), 0)
	require.Equal(t, GroupRPMProceed, verdict)
	verdict, _ = CheckHopGroupRPM(context.Background(), billing, &service.User{ID: 1}, service.ChainHop{GroupID: 3}, 0)
	require.Equal(t, GroupRPMProceed, verdict)
	require.NotPanics(t, func() { ReleaseHopGroupRPMIfNotServed(context.Background(), nil, service.HopOutcomeFallbackWorthy) })
}
