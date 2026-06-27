package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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
	h.stampMonthlyOverdraftRemaining(c.Request.Context(), subject.UserID, out)
	response.Success(c, out)
}

// stampMonthlyOverdraftRemaining 给一批订阅 DTO 填「用户级本月剩余透支次数」（per-user，全卡同值），
// 供前端在本月已满 5 次时提前置灰透支按钮。读不到（如未配 entClient / 出错）则不填，前端按 null
// 处理（不前置拦截，交服务端兜底校验）。
func (h *SubscriptionHandler) stampMonthlyOverdraftRemaining(ctx context.Context, userID int64, out []dto.UserSubscription) {
	if len(out) == 0 {
		return
	}
	remaining, err := h.subscriptionService.MonthlyOverdraftRemaining(ctx, userID)
	if err != nil {
		return
	}
	for i := range out {
		r := remaining
		out[i].MonthlyOverdraftRemaining = &r
	}
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
	h.stampMonthlyOverdraftRemaining(c.Request.Context(), subject.UserID, out)
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

// SetOverdraftDays is the retired per-day overdraft toggle. The three-window model uses
// POST /api/v1/subscriptions/overdraft as an explicit user action; there is no on/off switch.
func (h *SubscriptionHandler) SetOverdraftDays(c *gin.Context) {
	if _, ok := middleware2.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	subID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || subID <= 0 {
		response.BadRequest(c, "invalid subscription id")
		return
	}
	response.ErrorFrom(c, infraerrors.New(
		http.StatusGone,
		"SUBSCRIPTION_OVERDRAFT_TOGGLE_RETIRED",
		"subscription overdraft toggle is retired; use POST /api/v1/subscriptions/overdraft when daily quota is exhausted",
	))
}

// PricingBounds 返回自定义购买区间（每日额度 D / 有效天数 T / 单价 u 的允许范围），供购买页设滑块/校验。
// GET /api/v1/subscriptions/pricing
func (h *SubscriptionHandler) PricingBounds(c *gin.Context) {
	response.Success(c, h.subscriptionService.PricingBounds())
}

// Quote 自定义购买实时报价（规格第 2/3 节）：按 D+T 算 售价 P=D×T×u(D) + 派生周/月封顶。
// 金额完全由后端公式决定、不信前端（下单走同一公式冻结进订单快照）。
// POST /api/v1/subscriptions/quote  body: { daily_amount_usd, validity_days }
func (h *SubscriptionHandler) Quote(c *gin.Context) {
	var req struct {
		DailyAmountUSD float64 `json:"daily_amount_usd"`
		ValidityDays   int     `json:"validity_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	res, err := h.subscriptionService.QuoteSubscription(req.DailyAmountUSD, req.ValidityDays)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// Overdraft 用户手动透支「借一天」（规格第 8 节）：清空今日已用额度（刷新当日额度）+ expires_at 提前 1 天
// + 用户级月度计数 +1。仅解日上限，周/月封顶仍生效；每用户每自然月最多 5 次。
// POST /api/v1/subscriptions/overdraft
func (h *SubscriptionHandler) Overdraft(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	// 幂等（spec §并发与幂等）：持久化去重——同键重放返回首次结果，绝不重复 expires_at−1 /
	// month_overdraft++（光靠「借后 daily_usage=0」只能挡瞬时连点，挡不住「首次成功→当日再次撞满
	// →同键重放」二次借天，故必须落幂等记录）。
	// key 来源兼容两路：优先 Idempotency-Key 头（标准）；缺失时回填 body.idempotency_key
	// （前端历史契约），保证两条路径都进持久化去重。
	if c.GetHeader("Idempotency-Key") == "" {
		var body struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := c.ShouldBindJSON(&body); err == nil && body.IdempotencyKey != "" {
			c.Request.Header.Set("Idempotency-Key", body.IdempotencyKey)
		}
	}
	executeUserIdempotentJSON(
		c,
		"user.subscriptions.overdraft",
		gin.H{"user_id": subject.UserID},
		service.DefaultWriteIdempotencyTTL(),
		func(ctx context.Context) (any, error) {
			return h.subscriptionService.ManualOverdraft(ctx, subject.UserID)
		},
	)
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

		// Add group name if preloaded（仅取名字；限额已不挂 group）。
		if sub.Group != nil {
			item.GroupName = sub.Group.Name
		}
		// 三窗口限额挂卡、不挂 group（新模型）：summary 也按卡级限额返回，避免回旧 group 值/空值。
		if sub.DailyLimitUSD != nil {
			item.DailyLimitUSD = *sub.DailyLimitUSD
		}
		if sub.WeeklyLimitUSD != nil {
			item.WeeklyLimitUSD = *sub.WeeklyLimitUSD
		}
		if sub.MonthlyLimitUSD != nil {
			item.MonthlyLimitUSD = *sub.MonthlyLimitUSD
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
	PlanID int64 `json:"plan_id" binding:"required,gt=0"`
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
