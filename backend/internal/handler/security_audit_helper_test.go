package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type stagePromptEngine struct {
	mu     sync.Mutex
	stages []string
}

func (e *stagePromptEngine) EffectiveMode() securityaudit.Mode { return securityaudit.ModeBlocking }

func (e *stagePromptEngine) Enqueue(context.Context, securityaudit.Request) error { return nil }

func (e *stagePromptEngine) Evaluate(_ context.Context, req securityaudit.Request) (*securityaudit.PromptDecision, error) {
	e.mu.Lock()
	e.stages = append(e.stages, req.Stage)
	e.mu.Unlock()
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}

func (e *stagePromptEngine) snapshotStages() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.stages...)
}

// turnCountingPromptEngine 用于验证 runSecurityAudit 在同一 WS turn 内对重复
// payload 的去重行为：decisions 按调用顺序逐个返回，用尽后回落到 Allow。
type turnCountingPromptEngine struct {
	mu        sync.Mutex
	evaluates int
	decisions []*securityaudit.PromptDecision
}

func (e *turnCountingPromptEngine) EffectiveMode() securityaudit.Mode {
	return securityaudit.ModeBlocking
}

func (e *turnCountingPromptEngine) Enqueue(context.Context, securityaudit.Request) error { return nil }

func (e *turnCountingPromptEngine) Evaluate(_ context.Context, _ securityaudit.Request) (*securityaudit.PromptDecision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := e.evaluates
	e.evaluates++
	if idx < len(e.decisions) {
		return e.decisions[idx], nil
	}
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}

func (e *turnCountingPromptEngine) evaluateCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.evaluates
}

func TestSecurityAuditWebSocketStagesAreNotSuppressedByHTTPCompletionMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	groupID := int64(3)
	apiKey := &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID}
	subject := middleware2.AuthSubject{UserID: 7}
	engine := &stagePromptEngine{}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	body := []byte(`{"model":"gpt-test","input":[{"role":"user","content":"check me"}]}`)

	first := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", body, "first_turn")
	second := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", body, "subsequent_turn")

	require.True(t, first.AllowNextStage)
	require.True(t, second.AllowNextStage)
	require.Equal(t, []string{"first_turn", "subsequent_turn"}, engine.snapshotStages())
}

func TestIsSecurityAuditWebSocketStage(t *testing.T) {
	require.True(t, isSecurityAuditWebSocketStage("first_turn"))
	require.True(t, isSecurityAuditWebSocketStage("subsequent_turn"))
	require.False(t, isSecurityAuditWebSocketStage("http"))
	require.False(t, isSecurityAuditWebSocketStage(""))
}

func newSecurityAuditDedupeTestContext() (*gin.Context, *service.APIKey, middleware2.AuthSubject) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	groupID := int64(3)
	apiKey := &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID}
	subject := middleware2.AuthSubject{UserID: 7}
	return ctx, apiKey, subject
}

// TestRunSecurityAuditDeduplicatesRepeatedPayloadWithinSameWebSocketTurn 验证
// 重复场景确实被去重：账号 failover / bridge 重放等路径可能对同一 turn 的相同
// payload 重复调用 runSecurityAudit，去重后底层 coordinator 只应被真正调用一次；
// turn 号变化后必须重新审计。
func TestRunSecurityAuditDeduplicatesRepeatedPayloadWithinSameWebSocketTurn(t *testing.T) {
	ctx, apiKey, subject := newSecurityAuditDedupeTestContext()
	engine := &turnCountingPromptEngine{}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	payload := []byte(`{"type":"response.create","response":{"input":"same turn"}}`)

	ctx.Set(securityAuditWSTurnContextKey, 2)
	first := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	second := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.True(t, first.AllowNextStage)
	require.True(t, second.AllowNextStage)
	require.Equal(t, 1, engine.evaluateCount(), "repeated payload within the same turn must not re-trigger the audit engine")

	entry, exists := ctx.Get(securityAuditWSDedupeContextKey)
	require.True(t, exists)
	require.IsType(t, securityAuditWSDedupeEntry{}, entry)

	// 新 turn 到来时必须重新审计，不能被上一轮的缓存覆盖。
	ctx.Set(securityAuditWSTurnContextKey, 3)
	third := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.True(t, third.AllowNextStage)
	require.Equal(t, 2, engine.evaluateCount(), "a new turn number must trigger a fresh audit")
}

// TestRunSecurityAuditDoesNotCacheBlockedWebSocketDecision 验证被拦截的判定不会
// 被误缓存，否则重试同一份被拦截的 payload 会被错误放行。
func TestRunSecurityAuditDoesNotCacheBlockedWebSocketDecision(t *testing.T) {
	ctx, apiKey, subject := newSecurityAuditDedupeTestContext()
	engine := &turnCountingPromptEngine{
		decisions: []*securityaudit.PromptDecision{
			{Kind: securityaudit.DecisionBlock, AllowNextStage: false},
			{Kind: securityaudit.DecisionAllow, AllowNextStage: true},
		},
	}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	payload := []byte(`{"type":"response.create","response":{"input":"blocked retry"}}`)

	ctx.Set(securityAuditWSTurnContextKey, 2)
	first := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.Equal(t, securityaudit.DecisionBlock, first.Kind)
	require.False(t, first.AllowNextStage)

	_, cachedAfterBlock := ctx.Get(securityAuditWSDedupeContextKey)
	require.False(t, cachedAfterBlock, "a blocked decision must not be cached")

	second := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.Equal(t, securityaudit.DecisionAllow, second.Kind)
	require.Equal(t, 2, engine.evaluateCount(), "retrying after a blocked decision must re-run the audit engine")
}

// TestRunSecurityAuditDoesNotCacheFlaggedWebSocketDecision 验证 Flag（允许通过但
// 标记待人工复核）同样不缓存，避免同一份被标记的内容被静默复用为"已审计通过"。
func TestRunSecurityAuditDoesNotCacheFlaggedWebSocketDecision(t *testing.T) {
	ctx, apiKey, subject := newSecurityAuditDedupeTestContext()
	engine := &turnCountingPromptEngine{
		decisions: []*securityaudit.PromptDecision{
			{Kind: securityaudit.DecisionFlag, AllowNextStage: true},
			{Kind: securityaudit.DecisionAllow, AllowNextStage: true},
		},
	}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	payload := []byte(`{"type":"response.create","response":{"input":"flagged retry"}}`)

	ctx.Set(securityAuditWSTurnContextKey, 2)
	first := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.Equal(t, securityaudit.DecisionFlag, first.Kind)
	require.True(t, first.AllowNextStage)

	_, cachedAfterFlag := ctx.Get(securityAuditWSDedupeContextKey)
	require.False(t, cachedAfterFlag, "a flagged decision must not be cached")

	second := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.Equal(t, securityaudit.DecisionAllow, second.Kind)
	require.Equal(t, 2, engine.evaluateCount(), "retrying after a flagged decision must re-run the audit engine")
}

// TestRunSecurityAuditLogsWebSocketChecksAndCacheHits 是 upstream #5511
// （fix(security-audit): restore websocket audit logs，本批次 item 6 / upstream
// #5234 的后续修正）在 fork 手写去重实现上的等价验证：修复前，
// isSecurityAuditWebSocketStage 分支内无论是命中 (stage,turn,body) 去重缓存
// 直接复用上次结果，还是缓存未命中真正调用 coordinator.Check，两条路径都会在
// 走到下方 gateway_check_start/done 日志之前直接 return，导致所有 WS 多轮安全
// 审计请求完全没有审计日志留痕。这个测试验证：第一次调用产生 start(cached=false)
// + done(cached=false)；同一 turn 内的重复 payload 命中缓存后，仍必须产生
// done(cached=true) 记录（且不再重复产生 start），同时底层审计引擎只被真正调用一次。
func TestRunSecurityAuditLogsWebSocketChecksAndCacheHits(t *testing.T) {
	ctx, apiKey, subject := newSecurityAuditDedupeTestContext()
	engine := &turnCountingPromptEngine{}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	core, logs := observer.New(zap.InfoLevel)
	reqLog := zap.New(core)
	payload := []byte(`{"type":"response.create","response":{"input":"same turn"}}`)

	ctx.Set(securityAuditWSTurnContextKey, 2)
	first := runSecurityAudit(ctx, reqLog, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	second := runSecurityAudit(ctx, reqLog, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", payload, "subsequent_turn")
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.True(t, first.AllowNextStage)
	require.True(t, second.AllowNextStage)
	require.Equal(t, 1, engine.evaluateCount(), "cache hit must not re-trigger the audit engine")

	startLogs := logs.FilterMessage("security_audit.gateway_check_start").All()
	require.Len(t, startLogs, 1, "cache hit must not produce a second start log")
	require.Equal(t, false, startLogs[0].ContextMap()["cached"])

	doneLogs := logs.FilterMessage("security_audit.gateway_check_done").All()
	require.Len(t, doneLogs, 2, "both the fresh check and the cache hit must produce a done log")
	require.Equal(t, false, doneLogs[0].ContextMap()["cached"])
	require.Equal(t, true, doneLogs[1].ContextMap()["cached"])
	require.Equal(t, "allow", doneLogs[1].ContextMap()["decision"])
	require.Equal(t, "subsequent_turn", doneLogs[1].ContextMap()["stage"])
}

const securityauditProtocolResponses = "openai_responses"
