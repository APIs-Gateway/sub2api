package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func contentModerationStatus(decision *service.ContentModerationDecision) int {
	if decision == nil || decision.StatusCode < 400 || decision.StatusCode > 599 {
		return http.StatusForbidden
	}
	return decision.StatusCode
}

func contentModerationErrorCode(decision *service.ContentModerationDecision) string {
	return "content_policy_violation"
}

func runContentModeration(c *gin.Context, reqLog *zap.Logger, svc *service.ContentModerationService, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.ContentModerationDecision {
	if svc == nil || c == nil || c.Request == nil {
		return nil
	}
	input := buildContentModerationInput(c, apiKey, subject, protocol, model, body)
	return checkContentModerationInput(c, reqLog, svc, input, body)
}

// runContentModerationForChain 是有回退链时的审核入口：按链上所有存活跳的分组集合审一次（设计 3.4 第 0 项，审查 B1）。
//
//   - chain 必须是入口解析出的那份有效链，后续迭代用的也是同一份；不得在循环里重新解析（B1 不变式）。
//   - 任一跳在审核范围内就审核；用户可见的 GroupID/GroupName 始终是用户 Key 的主分组（会进入发给用户的违规邮件），
//     触发审核的那一跳另记在只给管理端的 ScopeGroupID/ScopeGroupName（审查 BK-1：链上可能有管理员隐藏分组）。
//   - apiKey 必须是用户原 Key（主分组），不能是回退链某一跳的影子 Key（NewServedAPIKey），否则影子 Key 的分组名会被当成主分组。
//   - 拦截是终止条件：调用方拿到 Blocked 的决定后必须直接结束，不得继续尝试下一跳。
//   - chain 为空（无链、开关关闭）时与 runContentModeration 完全相同。
func runContentModerationForChain(c *gin.Context, reqLog *zap.Logger, svc *service.ContentModerationService, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte, chain []service.ChainHop) *service.ContentModerationDecision {
	if len(chain) == 0 {
		return runContentModeration(c, reqLog, svc, apiKey, subject, protocol, model, body)
	}
	if svc == nil || c == nil || c.Request == nil {
		return nil
	}
	input := buildContentModerationInput(c, apiKey, subject, protocol, model, body)
	input.ChainGroups = chainModerationGroups(chain)
	if reqLog != nil {
		reqLog.Info("content_moderation.chain_scope", zap.String("request_id", input.RequestID), zap.Int("chain_groups", len(input.ChainGroups)))
	}
	return checkContentModerationInput(c, reqLog, svc, input, body)
}

// chainModerationGroups 提取链上各跳的分组（含主分组），保持链的顺序；分组缺失的跳忽略。
func chainModerationGroups(chain []service.ChainHop) []service.ContentModerationChainGroup {
	if len(chain) == 0 {
		return nil
	}
	out := make([]service.ContentModerationChainGroup, 0, len(chain))
	for _, hop := range chain {
		id := hop.GroupID
		name := ""
		if hop.Group != nil {
			if id <= 0 {
				id = hop.Group.ID
			}
			name = hop.Group.Name
		}
		if id <= 0 {
			continue
		}
		out = append(out, service.ContentModerationChainGroup{ID: id, Name: name})
	}
	return out
}

// checkContentModerationInput 是 runContentModeration 的后半段（日志 + Check），供有链 / 无链两条入口共用。
func checkContentModerationInput(c *gin.Context, reqLog *zap.Logger, svc *service.ContentModerationService, input service.ContentModerationCheckInput, body []byte) *service.ContentModerationDecision {
	if reqLog != nil {
		reqLog.Info("content_moderation.gateway_check_start",
			zap.String("request_id", input.RequestID),
			zap.Int64("user_id", input.UserID),
			zap.Int64("api_key_id", input.APIKeyID),
			zap.String("api_key_name", input.APIKeyName),
			zap.Int64p("group_id", input.GroupID),
			zap.String("group_name", input.GroupName),
			zap.String("endpoint", input.Endpoint),
			zap.String("provider", input.Provider),
			zap.String("protocol", input.Protocol),
			zap.String("model", input.Model),
			zap.Int("body_bytes", len(body)),
		)
	}
	decision, err := svc.Check(c.Request.Context(), input)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("content_moderation.check_failed", zap.Error(err))
		}
		return nil
	}
	if reqLog != nil && decision != nil {
		reqLog.Info("content_moderation.gateway_check_done",
			zap.String("request_id", input.RequestID),
			zap.Bool("allowed", decision.Allowed),
			zap.Bool("blocked", decision.Blocked),
			zap.Bool("flagged", decision.Flagged),
			zap.String("action", decision.Action),
			zap.Int("status_code", decision.StatusCode),
			zap.String("highest_category", decision.HighestCategory),
			zap.Float64("highest_score", decision.HighestScore),
		)
	}
	return decision
}

func buildContentModerationInput(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) service.ContentModerationCheckInput {
	input := service.ContentModerationCheckInput{
		RequestID: contentModerationRequestID(c.Request.Context()),
		UserID:    subject.UserID,
		Endpoint:  GetInboundEndpoint(c),
		Provider:  contentModerationProvider(apiKey),
		Model:     strings.TrimSpace(model),
		Protocol:  protocol,
		Body:      body,
	}
	if forcedPlatform, ok := middleware2.GetForcePlatformFromContext(c); ok {
		input.Provider = strings.TrimSpace(forcedPlatform)
	}
	if apiKey != nil {
		input.APIKeyID = apiKey.ID
		input.APIKeyName = apiKey.Name
		if apiKey.User != nil {
			input.UserEmail = apiKey.User.Email
		}
		if apiKey.GroupID != nil {
			groupID := *apiKey.GroupID
			input.GroupID = &groupID
		}
		if apiKey.Group != nil {
			input.GroupName = apiKey.Group.Name
		}
		// 防御：传进来的是回退链某一跳的影子 Key 时，它的 GroupID / Group 是被服务那一跳的，可能是管理员隐藏链的分组。
		// 审核 / 审计里用户可见的分组始终是主分组（会进入发给用户的邮件），所以改回 HomeGroupID；
		// 主分组的名字影子 Key 上拿不到，留空（邮件里显示「-」），宁可少一个名字也不能泄露隐藏分组的名字（审查 BK-1 / B5）。
		// 非影子 Key（HomeGroupID 为空）完全不受影响。
		if apiKey.HomeGroupID != nil {
			home := *apiKey.HomeGroupID
			input.GroupID = &home
			input.GroupName = ""
		}
	}
	if input.Endpoint == "" && c.Request != nil && c.Request.URL != nil {
		input.Endpoint = c.Request.URL.Path
	}
	return input
}

func contentModerationProvider(apiKey *service.APIKey) string {
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	return strings.TrimSpace(apiKey.Group.Platform)
}

func contentModerationRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if requestID, ok := ctx.Value(ctxkey.RequestID).(string); ok {
		return strings.TrimSpace(requestID)
	}
	return ""
}
