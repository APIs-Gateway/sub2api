package securityaudit

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type LegacyModerationAdapter struct {
	service *service.ContentModerationService
}

func NewLegacyModerationAdapter(svc *service.ContentModerationService) LegacyEngine {
	return &LegacyModerationAdapter{service: svc}
}

func (a *LegacyModerationAdapter) Check(ctx context.Context, req Request) (*LegacyDecision, error) {
	if a == nil || a.service == nil {
		return nil, nil
	}
	decision, err := a.service.Check(ctx, service.ContentModerationCheckInput{
		RequestID: req.RequestID, UserID: req.UserID, UserEmail: req.UserEmail,
		APIKeyID: req.APIKeyID, APIKeyName: req.APIKeyName, GroupID: cloneInt64Ptr(req.GroupID),
		GroupName: req.GroupName, Endpoint: req.Endpoint, Provider: req.Provider,
		Model: req.Model, Protocol: req.Protocol, Body: req.Body,
		ChainGroups: legacyChainGroups(req.ChainGroups),
	})
	if err != nil || decision == nil {
		return nil, err
	}
	return &LegacyDecision{
		Allowed: decision.Allowed, Blocked: decision.Blocked, Flagged: decision.Flagged,
		Message: decision.Message, StatusCode: decision.StatusCode,
		ErrorCode: "content_policy_violation", Action: decision.Action,
	}, nil
}

// legacyChainGroups 把回退链分组转给内容审核；无链返回 nil（审核按原输入分组判定）。
func legacyChainGroups(groups []ChainGroup) []service.ContentModerationChainGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]service.ContentModerationChainGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, service.ContentModerationChainGroup{ID: g.ID, Name: g.Name})
	}
	return out
}
