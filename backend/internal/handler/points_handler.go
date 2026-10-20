package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// PointsHandler 邀请返利积分制（issue #11）用户端接口。
type PointsHandler struct {
	pointsService    *service.PointsService
	affiliateService *service.AffiliateService
}

func NewPointsHandler(pointsService *service.PointsService, affiliateService *service.AffiliateService) *PointsHandler {
	return &PointsHandler{pointsService: pointsService, affiliateService: affiliateService}
}

// GetOverview GET /api/v1/user/points/overview
// 合并：积分账户（可用/冻结/历史）+ 邀请信息（邀请码/被邀请人/有效返现率）+ 公开规则。
func (h *PointsHandler) GetOverview(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	ctx := c.Request.Context()
	account, err := h.pointsService.GetUserPoints(ctx, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	detail, err := h.affiliateService.GetAffiliateDetail(ctx, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"account":        account,
		"affiliate":      detail,
		"effective_rate": h.pointsService.EffectiveRateForUser(ctx, subject.UserID),
		"config":         h.pointsService.PublicConfig(ctx),
	})
}

// ListLedger GET /api/v1/user/points/ledger
func (h *PointsHandler) ListLedger(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.pointsService.ListUserLedger(c.Request.Context(), subject.UserID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

// ListPlans GET /api/v1/user/points/plans —— 可用积分兑换的套餐 + 积分价。
func (h *PointsHandler) ListPlans(c *gin.Context) {
	plans, err := h.pointsService.ListRedeemablePlans(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"plans": plans})
}

type pointsRedeemBalanceRequest struct {
	Points int64 `json:"points" binding:"required"`
}

// RedeemBalance POST /api/v1/user/points/redeem-balance
func (h *PointsHandler) RedeemBalance(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req pointsRedeemBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	balance, err := h.pointsService.RedeemToBalance(c.Request.Context(), subject.UserID, req.Points)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"balance": balance})
}

type pointsRedeemPlanRequest struct {
	DailyAmountUSD float64 `json:"daily_amount_usd" binding:"required"`
	ValidityDays   int     `json:"validity_days" binding:"required"`
	// 幂等键（客户端每次兑换生成的 exchange_id）：防双击/重试/网络重发二次扣分。空则不去重。
	IdempotencyKey string `json:"idempotency_key"`
}

// RedeemPlan POST /api/v1/user/points/redeem-plan
func (h *PointsHandler) RedeemPlan(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req pointsRedeemPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	sub, err := h.pointsService.RedeemToPlan(c.Request.Context(), subject.UserID, req.DailyAmountUSD, req.ValidityDays, req.IdempotencyKey)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"subscription": sub})
}

// ListWithdrawals GET /api/v1/user/points/withdrawals
func (h *PointsHandler) ListWithdrawals(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	items, err := h.pointsService.ListUserWithdrawals(c.Request.Context(), subject.UserID, 50)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"withdrawals": items})
}

type pointsCreateWithdrawalRequest struct {
	Points              int64  `json:"points" binding:"required"`
	PayoutMethod        string `json:"payout_method" binding:"required"`
	PayoutAlipayAccount string `json:"payout_alipay_account"`
	PayoutAlipayName    string `json:"payout_alipay_name"`
	PayoutUSDTChain     string `json:"payout_usdt_chain"`
	PayoutUSDTAddress   string `json:"payout_usdt_address"`
}

// CreateWithdrawal POST /api/v1/user/points/withdrawals
func (h *PointsHandler) CreateWithdrawal(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req pointsCreateWithdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	w, err := h.pointsService.CreateWithdrawal(c.Request.Context(), subject.UserID, req.Points, req.PayoutMethod, req.PayoutAlipayAccount, req.PayoutAlipayName, req.PayoutUSDTChain, req.PayoutUSDTAddress)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, w)
}
