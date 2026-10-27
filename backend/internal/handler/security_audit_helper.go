package handler

import (
	"crypto/sha256"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const securityAuditCompletedContextKey = "sub2api.security_audit.completed"

// securityAuditWSTurnContextKey / securityAuditWSDedupeContextKey 支撑同一 WS
// turn 内的审计去重（见 runSecurityAudit）：账号 failover 重试、bridge 循环重放
// 等路径可能对同一 turn 的同一份 payload 重复触发 BeforeRequest，若不去重会让
// coordinator.Check 被多次调用，产生重复的审计成本/记录。仅当结果是
// DecisionAllow 时才缓存，避免误缓存一次失败/拦截判定进而放行后续重复请求。
const securityAuditWSTurnContextKey = "sub2api.security_audit.ws_turn"
const securityAuditWSDedupeContextKey = "sub2api.security_audit.ws_dedupe"

type securityAuditWSDedupeEntry struct {
	stage    string
	turn     int
	bodyHash [sha256.Size]byte
	decision securityaudit.Decision
}

// isSecurityAuditWebSocketStage 判断 stage 是否为 WS 多轮场景（首轮/后续轮），
// 与 checkSecurityAuditStage 调用点使用的 "first_turn" / "subsequent_turn" 字面量对齐。
func isSecurityAuditWebSocketStage(stage string) bool {
	switch strings.TrimSpace(stage) {
	case "first_turn", "subsequent_turn":
		return true
	default:
		return false
	}
}

// securityAuditWSTurn 读取由 ResponsesWebSocket 的 BeforeRequest 钩子写入的当前 turn 号。
func securityAuditWSTurn(c *gin.Context) (int, bool) {
	turn, exists := c.Get(securityAuditWSTurnContextKey)
	if !exists {
		return 0, false
	}
	turnNo, ok := turn.(int)
	return turnNo, ok
}

func (h *GatewayHandler) checkSecurityAudit(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte) *securityaudit.Decision {
	if h == nil {
		return nil
	}
	return runSecurityAudit(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, protocol, model, body, "http")
}

func (h *OpenAIGatewayHandler) checkSecurityAudit(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte) *securityaudit.Decision {
	if h == nil {
		return nil
	}
	return runSecurityAudit(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, protocol, model, body, "http")
}

func (h *OpenAIGatewayHandler) checkSecurityAuditStage(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string) *securityaudit.Decision {
	if h == nil {
		return nil
	}
	return runSecurityAudit(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, protocol, model, body, stage)
}

// checkSecurityAuditForChain / checkSecurityAuditStageForChain 是有回退链时的审计入口：
// 按链上所有存活跳的分组集合审一次（并集，设计 3.4 第 0 项）。chain 为空时与无链版本完全相同。
// 审计拦截是终止条件，调用方不得继续尝试下一跳。
func (h *GatewayHandler) checkSecurityAuditForChain(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, chain []service.ChainHop) *securityaudit.Decision {
	if h == nil {
		return nil
	}
	return runSecurityAuditForChain(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, protocol, model, body, "http", chain)
}

func (h *OpenAIGatewayHandler) checkSecurityAuditForChain(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, chain []service.ChainHop) *securityaudit.Decision {
	return h.checkSecurityAuditStageForChain(c, reqLog, apiKey, subject, protocol, model, body, "http", chain)
}

func (h *OpenAIGatewayHandler) checkSecurityAuditStageForChain(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string, chain []service.ChainHop) *securityaudit.Decision {
	if h == nil {
		return nil
	}
	return runSecurityAuditForChain(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, protocol, model, body, stage, chain)
}

// recordSecurityAuditServedGroup 在请求结束后补记实际服务的分组（设计 Q18）。
// 审计事件里的分组始终是主分组（触发审计的那一跳另记在只给管理端的 ScopeGroup* 字段）；这里把实际服务的分组按 request_id 补记上去。
// coordinator 未配置 recorder 时是空操作；失败只打日志，不影响响应。
func recordSecurityAuditServedGroup(c *gin.Context, reqLog *zap.Logger, coordinator *securityaudit.Coordinator, servedGroupID int64) {
	if c == nil || c.Request == nil || coordinator == nil || servedGroupID <= 0 {
		return
	}
	requestID := contentModerationRequestID(c.Request.Context())
	if err := coordinator.RecordServedGroup(c.Request.Context(), requestID, servedGroupID); err != nil && reqLog != nil {
		reqLog.Warn("security_audit.record_served_group_failed", zap.String("request_id", requestID), zap.Int64("served_group_id", servedGroupID), zap.Error(err))
	}
}

func (h *GatewayHandler) recordSecurityAuditServedGroup(c *gin.Context, reqLog *zap.Logger, servedGroupID int64) {
	if h == nil {
		return
	}
	recordSecurityAuditServedGroup(c, reqLog, h.securityAuditCoordinator, servedGroupID)
}

func (h *OpenAIGatewayHandler) recordSecurityAuditServedGroup(c *gin.Context, reqLog *zap.Logger, servedGroupID int64) {
	if h == nil {
		return
	}
	recordSecurityAuditServedGroup(c, reqLog, h.securityAuditCoordinator, servedGroupID)
}

func runSecurityAudit(c *gin.Context, reqLog *zap.Logger, coordinator *securityaudit.Coordinator, legacy *service.ContentModerationService, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string) *securityaudit.Decision {
	return runSecurityAuditForChain(c, reqLog, coordinator, legacy, apiKey, subject, protocol, model, body, stage, nil)
}

// runSecurityAuditForChain 是 runSecurityAudit 的实现；chain 为空时（无链）行为与改动前逐行一致。
func runSecurityAuditForChain(c *gin.Context, reqLog *zap.Logger, coordinator *securityaudit.Coordinator, legacy *service.ContentModerationService, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string, chain []service.ChainHop) *securityaudit.Decision {
	if c == nil || c.Request == nil {
		return nil
	}
	if completed, exists := c.Get(securityAuditCompletedContextKey); exists && completed == true && strings.TrimSpace(stage) == "http" {
		return nil
	}
	if coordinator == nil {
		legacyDecision := runContentModerationForChain(c, reqLog, legacy, apiKey, subject, protocol, model, body, chain)
		if legacyDecision == nil {
			return nil
		}
		decision := securityaudit.Decision{Kind: securityaudit.DecisionAllow, HTTPStatus: http.StatusOK, AllowNextStage: true}
		decision.Legacy = &securityaudit.LegacyDecision{
			Allowed: legacyDecision.Allowed, Blocked: legacyDecision.Blocked, Flagged: legacyDecision.Flagged,
			Message: legacyDecision.Message, StatusCode: legacyDecision.StatusCode,
			ErrorCode: "content_policy_violation", Action: legacyDecision.Action,
		}
		if legacyDecision.Blocked {
			decision.Kind, decision.HTTPStatus, decision.ErrorCode, decision.ClientMessage, decision.AllowNextStage = securityaudit.DecisionBlock, contentModerationStatus(legacyDecision), "content_policy_violation", legacyDecision.Message, false
		}
		if decision.AllowNextStage {
			c.Set(securityAuditCompletedContextKey, true)
		}
		return &decision
	}
	request := buildSecurityAuditRequest(c, apiKey, subject, protocol, model, body, stage)
	if len(chain) > 0 {
		request.ChainGroups = chainSecurityAuditGroups(chain)
		if reqLog != nil {
			reqLog.Info("security_audit.chain_scope", zap.String("request_id", request.RequestID), zap.Int("chain_groups", len(request.ChainGroups)))
		}
	}
	if isSecurityAuditWebSocketStage(request.Stage) {
		if turnNo, ok := securityAuditWSTurn(c); ok {
			bodyHash := sha256.Sum256(body)
			if cached, exists := c.Get(securityAuditWSDedupeContextKey); exists {
				if entry, ok := cached.(securityAuditWSDedupeEntry); ok &&
					entry.stage == request.Stage && entry.turn == turnNo && entry.bodyHash == bodyHash {
					decision := entry.decision
					logSecurityAuditDone(reqLog, request, decision, true)
					return &decision
				}
			}
			logSecurityAuditStart(reqLog, request, len(body), false)
			decision := coordinator.Check(c.Request.Context(), request)
			if decision.Kind == securityaudit.DecisionAllow {
				c.Set(securityAuditWSDedupeContextKey, securityAuditWSDedupeEntry{
					stage: request.Stage, turn: turnNo, bodyHash: bodyHash, decision: decision,
				})
			}
			logSecurityAuditDone(reqLog, request, decision, false)
			return &decision
		}
	}
	logSecurityAuditStart(reqLog, request, len(body), false)
	decision := coordinator.Check(c.Request.Context(), request)
	if decision.AllowNextStage {
		c.Set(securityAuditCompletedContextKey, true)
	}
	logSecurityAuditDone(reqLog, request, decision, false)
	return &decision
}

// logSecurityAuditStart/logSecurityAuditDone 集中审计请求的开始/结束日志，供 HTTP
// 阶段与 WS 多轮阶段共用。cached 标记这次 decision 是否来自 runSecurityAudit 里的
// (stage,turn,body) 去重缓存（见上方 securityAuditWSDedupeEntry）。引入 WS 去重
// 分支之前，WS 阶段（first_turn/subsequent_turn）无论命中缓存还是真正调用
// coordinator.Check，都会在下面这两段日志之前直接 return，导致所有 WS 审计请求
// 完全没有 gateway_check_start/done 记录，审计追踪出现空洞。补上 cached 字段后，
// 无论走缓存还是走真实审计引擎，都能在日志里区分并留痕。
func logSecurityAuditStart(reqLog *zap.Logger, request securityaudit.Request, bodyBytes int, cached bool) {
	if reqLog == nil {
		return
	}
	reqLog.Info("security_audit.gateway_check_start",
		zap.String("request_id", request.RequestID), zap.Int64("user_id", request.UserID),
		zap.Int64("api_key_id", request.APIKeyID), zap.Int64p("group_id", request.GroupID),
		zap.String("endpoint", request.Endpoint), zap.String("provider", request.Provider),
		zap.String("protocol", request.Protocol), zap.String("model", request.Model), zap.String("stage", request.Stage),
		zap.Int("body_bytes", bodyBytes), zap.Bool("cached", cached))
}

func logSecurityAuditDone(reqLog *zap.Logger, request securityaudit.Request, decision securityaudit.Decision, cached bool) {
	if reqLog == nil {
		return
	}
	reqLog.Info("security_audit.gateway_check_done",
		zap.String("request_id", request.RequestID), zap.String("decision", string(decision.Kind)),
		zap.String("error_code", decision.ErrorCode), zap.Bool("allow_next_stage", decision.AllowNextStage),
		zap.String("stage", request.Stage), zap.Bool("cached", cached))
}

func buildSecurityAuditRequest(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string) securityaudit.Request {
	legacy := buildContentModerationInput(c, apiKey, subject, protocol, model, body)
	request := securityaudit.Request{
		RequestID: legacy.RequestID, UserID: legacy.UserID, UserEmail: legacy.UserEmail,
		APIKeyID: legacy.APIKeyID, APIKeyName: legacy.APIKeyName, GroupID: cloneSecurityAuditGroupID(legacy.GroupID),
		GroupName: legacy.GroupName, Provider: legacy.Provider, Endpoint: legacy.Endpoint,
		Protocol: legacy.Protocol, Model: legacy.Model, Body: body, Stage: strings.TrimSpace(stage),
	}
	if apiKey != nil && apiKey.User != nil {
		request.Username = apiKey.User.Username
		if request.UserEmail == "" {
			request.UserEmail = apiKey.User.Email
		}
	}
	if request.Stage == "" {
		request.Stage = "http"
	}
	return request
}

func securityAuditStatus(decision *securityaudit.Decision) int {
	if decision == nil || decision.HTTPStatus < 400 || decision.HTTPStatus > 599 {
		return http.StatusForbidden
	}
	return decision.HTTPStatus
}

func securityAuditErrorCode(decision *securityaudit.Decision) string {
	if decision == nil || strings.TrimSpace(decision.ErrorCode) == "" {
		return "content_policy_violation"
	}
	return decision.ErrorCode
}

func securityAuditMessage(decision *securityaudit.Decision) string {
	if decision == nil {
		return "Request blocked by content policy"
	}
	if decision.Legacy != nil && decision.Legacy.Blocked && strings.TrimSpace(decision.Legacy.Message) != "" {
		return decision.Legacy.Message
	}
	if strings.TrimSpace(decision.ClientMessage) != "" {
		return decision.ClientMessage
	}
	return "Request blocked by content policy"
}

// chainSecurityAuditGroups 提取链上各跳的分组（含主分组），保持链的顺序。
func chainSecurityAuditGroups(chain []service.ChainHop) []securityaudit.ChainGroup {
	groups := chainModerationGroups(chain)
	if len(groups) == 0 {
		return nil
	}
	out := make([]securityaudit.ChainGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, securityaudit.ChainGroup{ID: g.ID, Name: g.Name})
	}
	return out
}

func cloneSecurityAuditGroupID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
