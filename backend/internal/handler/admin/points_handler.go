package admin

import (
	"log/slog"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// PointsHandler 邀请返利积分制（issue #11）后台接口：配置、提现审核队列、流水查询。
type PointsHandler struct {
	pointsService *service.PointsService
}

func NewPointsHandler(pointsService *service.PointsService) *PointsHandler {
	return &PointsHandler{pointsService: pointsService}
}

// GetSettings GET /api/v1/admin/points/settings
func (h *PointsHandler) GetSettings(c *gin.Context) {
	response.Success(c, h.pointsService.AdminGetSettings(c.Request.Context()))
}

// UpdateSettings PUT /api/v1/admin/points/settings
func (h *PointsHandler) UpdateSettings(c *gin.Context) {
	var req service.PointsSettingsInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	before := h.pointsService.AdminGetSettings(c.Request.Context())
	settings, err := h.pointsService.AdminUpdateSettings(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// money-safety（spec §2.1/§7）：peg 是积分↔余额的锚，变更会重估全部存量积分价值，必须留痕。
	if before != nil && before.Peg != settings.Peg {
		subject, _ := middleware.GetAuthSubjectFromContext(c)
		slog.Warn("points peg changed",
			"audit", true,
			"user_id", subject.UserID,
			"peg_before", before.Peg,
			"peg_after", settings.Peg,
		)
	}
	response.Success(c, settings)
}

// ListWithdrawals GET /api/v1/admin/points/withdrawals?status=&search=
func (h *PointsHandler) ListWithdrawals(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.pointsService.AdminListWithdrawals(c.Request.Context(), service.PointsWithdrawalFilter{
		Status:   c.Query("status"),
		Search:   c.Query("search"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

type reviewWithdrawalRequest struct {
	Note        string `json:"note"`
	PayoutProof string `json:"payout_proof"`
}

// ApproveWithdrawal POST /api/v1/admin/points/withdrawals/:id/approve —— 标记已打款。
func (h *PointsHandler) ApproveWithdrawal(c *gin.Context) {
	h.review(c, true)
}

// RejectWithdrawal POST /api/v1/admin/points/withdrawals/:id/reject —— 驳回并退回积分。
func (h *PointsHandler) RejectWithdrawal(c *gin.Context) {
	h.review(c, false)
}

func (h *PointsHandler) review(c *gin.Context, approve bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Admin not authenticated")
		return
	}
	var req reviewWithdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// note/proof 可选，绑定失败容忍空体
		req = reviewWithdrawalRequest{}
	}
	w, err := h.pointsService.AdminReviewWithdrawal(c.Request.Context(), id, subject.UserID, approve, req.Note, req.PayoutProof)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, w)
}

// ListLedger GET /api/v1/admin/points/ledger?kind=&search=
func (h *PointsHandler) ListLedger(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.pointsService.AdminListLedger(c.Request.Context(), service.PointsLedgerFilter{
		Kind:     c.Query("kind"),
		Search:   c.Query("search"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}
