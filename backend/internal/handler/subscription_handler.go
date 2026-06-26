package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SubscriptionSummaryItem represents a subscription item in summary
type SubscriptionSummaryItem struct {
	ID              int64   `json:"id"`
	GroupID         int64   `json:"group_id"`
	GroupName       string  `json:"group_name"`
	Status          string  `json:"status"`
	DailyUsedUSD    float64 `json:"daily_used_usd,omitempty"`
	DailyLimitUSD   float64 `json:"daily_limit_usd,omitempty"`
	WeeklyUsedUSD   float64 `json:"weekly_used_usd,omitempty"`
	WeeklyLimitUSD  float64 `json:"weekly_limit_usd,omitempty"`
	MonthlyUsedUSD  float64 `json:"monthly_used_usd,omitempty"`
	MonthlyLimitUSD float64 `json:"monthly_limit_usd,omitempty"`
	ExpiresAt       *string `json:"expires_at,omitempty"`
}

// SubscriptionProgressInfo represents subscription with progress info
type SubscriptionProgressInfo struct {
	Subscription *dto.UserSubscription         `json:"subscription"`
	Progress     *service.SubscriptionProgress `json:"progress"`
}

// SubscriptionHandler handles user subscription operations
type SubscriptionHandler struct {
	subscriptionService *service.SubscriptionService
}

// NewSubscriptionHandler creates a new user subscription handler
func NewSubscriptionHandler(subscriptionService *service.SubscriptionService) *SubscriptionHandler {
	return &SubscriptionHandler{
		subscriptionService: subscriptionService,
	}
}

// List handles listing current user's subscriptions
// GET /api/v1/subscriptions
func (h *SubscriptionHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.UserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *dto.UserSubscriptionFromService(&subscriptions[i]))
	}
	response.Success(c, out)
}

// GetActive handles getting current user's active subscriptions
// GET /api/v1/subscriptions/active
func (h *SubscriptionHandler) GetActive(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.UserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *dto.UserSubscriptionFromService(&subscriptions[i]))
	}
	response.Success(c, out)
}

// GetProgress handles getting subscription progress for current user
// GET /api/v1/subscriptions/progress
func (h *SubscriptionHandler) GetProgress(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	// Get all active subscriptions with progress
	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result := make([]SubscriptionProgressInfo, 0, len(subscriptions))
	for i := range subscriptions {
		sub := &subscriptions[i]
		progress, err := h.subscriptionService.GetSubscriptionProgress(c.Request.Context(), sub.ID)
		if err != nil {
			// Skip subscriptions with errors
			continue
		}
		result = append(result, SubscriptionProgressInfo{
			Subscription: dto.UserSubscriptionFromService(sub),
			Progress:     progress,
		})
	}

	response.Success(c, result)
}

// SetOverdraftDays lets the current user set the max overdraft days on ONE of their own
// subscription cards. PUT /api/v1/subscriptions/:id/overdraft
// body: { "max_overdraft_days": <int|null> } — null/omitted/negative = off, >=0 = on (0 = only today's accrual).
func (h *SubscriptionHandler) SetOverdraftDays(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	subID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || subID <= 0 {
		response.BadRequest(c, "invalid subscription id")
		return
	}
	var req struct {
		MaxOverdraftDays *int `json:"max_overdraft_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if err := h.subscriptionService.SetSubscriptionOverdraftDays(c.Request.Context(), subject.UserID, subID, req.MaxOverdraftDays); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"id": subID})
}

// GetSummary handles getting a summary of current user's subscription status
// GET /api/v1/subscriptions/summary
func (h *SubscriptionHandler) GetSummary(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	// Get all active subscriptions
	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var totalUsed float64
	items := make([]SubscriptionSummaryItem, 0, len(subscriptions))

	for _, sub := range subscriptions {
		item := SubscriptionSummaryItem{
			ID:             sub.ID,
			GroupID:        sub.GroupID,
			Status:         sub.Status,
			DailyUsedUSD:   sub.DailyUsageUSD,
			WeeklyUsedUSD:  sub.WeeklyUsageUSD,
			MonthlyUsedUSD: sub.MonthlyUsageUSD,
		}

		// Add group info if preloaded
		if sub.Group != nil {
			item.GroupName = sub.Group.Name
			if sub.Group.DailyLimitUSD != nil {
				item.DailyLimitUSD = *sub.Group.DailyLimitUSD
			}
			if sub.Group.WeeklyLimitUSD != nil {
				item.WeeklyLimitUSD = *sub.Group.WeeklyLimitUSD
			}
			if sub.Group.MonthlyLimitUSD != nil {
				item.MonthlyLimitUSD = *sub.Group.MonthlyLimitUSD
			}
		}

		// Format expiration time
		if !sub.ExpiresAt.IsZero() {
			formatted := sub.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
			item.ExpiresAt = &formatted
		}

		// Track total usage (use monthly as the most comprehensive)
		totalUsed += sub.MonthlyUsageUSD

		items = append(items, item)
	}

	summary := struct {
		ActiveCount   int                       `json:"active_count"`
		TotalUsedUSD  float64                   `json:"total_used_usd"`
		Subscriptions []SubscriptionSummaryItem `json:"subscriptions"`
	}{
		ActiveCount:   len(subscriptions),
		TotalUsedUSD:  totalUsed,
		Subscriptions: items,
	}

	response.Success(c, summary)
}

// changePlanRequestBody 续费/转套餐请求体：目标套餐 ID。
type changePlanRequestBody struct {
	PlanID int64 `json:"plan_id"`
}

// Renew 续费当前生效卡（规格第 5 节）：同套餐续 T'、D 不变，从其他余额扣续费价。用户自助。
// POST /api/v1/subscriptions/renew
func (h *SubscriptionHandler) Renew(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var req changePlanRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	res, err := h.subscriptionService.RenewSubscription(c.Request.Context(), subject.UserID, req.PlanID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// ChangePlan 转套餐（规格第 7 节）：旧卡折剩余价值抵新套餐，多退少补；每自然日最多 1 次。用户自助。
// POST /api/v1/subscriptions/change-plan
func (h *SubscriptionHandler) ChangePlan(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var req changePlanRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	res, err := h.subscriptionService.ChangeSubscriptionPlan(c.Request.Context(), subject.UserID, req.PlanID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}
