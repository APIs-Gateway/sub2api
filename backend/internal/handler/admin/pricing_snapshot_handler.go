package admin

import (
	"context"
	"errors"
	"io"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// pricingSnapshotAdmin 是 PricingSnapshotHandler 对 service.PricingSnapshotAdminService 的最小依赖，便于测试时替换。
type pricingSnapshotAdmin interface {
	Overview(ctx context.Context) (*service.SnapshotOverview, error)
	Pin(ctx context.Context, adminID int64) (*service.PricingSnapshotPinResult, error)
	FetchCandidate(ctx context.Context, adminID int64) (*service.PricingSnapshotFetchResult, error)
	Preview(ctx context.Context, candidateID int64, holdModels []string) (*service.SnapshotApprovalPlan, error)
	Approve(ctx context.Context, candidateID int64, holdModels []string, planHash string, approverID int64) (*service.PricingSnapshotMeta, error)
	Reject(ctx context.Context, candidateID int64) error
}

// PricingSnapshotHandler 是价格快照页（W6 设计 6.2）的管理接口：固定当前价格、拉取候选、预览差异、批准、拒绝。
type PricingSnapshotHandler struct {
	svc pricingSnapshotAdmin
}

// NewPricingSnapshotHandler 创建价格快照 handler。
func NewPricingSnapshotHandler(svc *service.PricingSnapshotAdminService) *PricingSnapshotHandler {
	return &PricingSnapshotHandler{svc: svc}
}

type pricingSnapshotConfirmRequest struct {
	Confirm bool `json:"confirm"`
}

type pricingSnapshotPreviewRequest struct {
	HoldModels []string `json:"hold_models"`
}

type pricingSnapshotApproveRequest struct {
	HoldModels []string `json:"hold_models"`
	PlanHash   string   `json:"plan_hash"`
	Confirm    bool     `json:"confirm"`
}

// Overview 返回当前模式、生效快照、待批准候选（红点）与历史。
// GET /api/v1/admin/pricing/snapshots
func (h *PricingSnapshotHandler) Overview(c *gin.Context) {
	out, err := h.svc.Overview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, out)
}

// Pin 是启动引导：把线上此刻正在计费的价格数据导入为生效快照并切到 pinned 模式。需要 {"confirm": true}。
// POST /api/v1/admin/pricing/snapshots/pin
func (h *PricingSnapshotHandler) Pin(c *gin.Context) {
	adminID, ok := pricingSnapshotAdminID(c)
	if !ok {
		return
	}
	var req pricingSnapshotConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm {
		response.ErrorFrom(c, infraerrors.BadRequest("CONFIRMATION_REQUIRED", "confirm must be true"))
		return
	}
	res, err := h.svc.Pin(c.Request.Context(), adminID)
	if err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, gin.H{"snapshot": res.Snapshot, "created": res.Created})
}

// Fetch 拉取远程价格为候选快照（不影响账单）。
// POST /api/v1/admin/pricing/snapshots/fetch
func (h *PricingSnapshotHandler) Fetch(c *gin.Context) {
	adminID, ok := pricingSnapshotAdminID(c)
	if !ok {
		return
	}
	res, err := h.svc.FetchCandidate(c.Request.Context(), adminID)
	if err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, gin.H{"unchanged": res.Unchanged, "created": res.Created, "candidate": res.Candidate, "active": res.Active})
}

// Preview 返回候选对生效快照的差异、实际变价的名字、无价风险和 plan_hash（批准时带回作二次确认）。
// POST /api/v1/admin/pricing/snapshots/:id/preview  {"hold_models": ["model-a"]}
func (h *PricingSnapshotHandler) Preview(c *gin.Context) {
	id, ok := parsePricingSnapshotID(c)
	if !ok {
		return
	}
	var req pricingSnapshotPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_REQUEST", "invalid request body"))
		return
	}
	plan, err := h.svc.Preview(c.Request.Context(), id, req.HoldModels)
	if err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, plan)
}

// Approve 批准候选：生成 merged 快照并切换为生效快照。需要预览时拿到的 plan_hash 与 {"confirm": true}。
// POST /api/v1/admin/pricing/snapshots/:id/approve  {"hold_models": [], "plan_hash": "...", "confirm": true}
func (h *PricingSnapshotHandler) Approve(c *gin.Context) {
	id, ok := parsePricingSnapshotID(c)
	if !ok {
		return
	}
	adminID, ok := pricingSnapshotAdminID(c)
	if !ok {
		return
	}
	var req pricingSnapshotApproveRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm || req.PlanHash == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("CONFIRMATION_REQUIRED", "plan_hash and confirm=true are required; preview first"))
		return
	}
	meta, err := h.svc.Approve(c.Request.Context(), id, req.HoldModels, req.PlanHash, adminID)
	if err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, gin.H{"snapshot": meta})
}

// Reject 拒绝一个候选。
// POST /api/v1/admin/pricing/snapshots/:id/reject
func (h *PricingSnapshotHandler) Reject(c *gin.Context) {
	id, ok := parsePricingSnapshotID(c)
	if !ok {
		return
	}
	if err := h.svc.Reject(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, mapPricingSnapshotError(err))
		return
	}
	response.Success(c, gin.H{"id": id, "status": service.PricingSnapshotStatusRejected})
}

func pricingSnapshotAdminID(c *gin.Context) (int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return 0, false
	}
	return subject.UserID, true
}

func parsePricingSnapshotID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "id must be a positive integer").
			WithMetadata(map[string]string{"param": "id"}))
		return 0, false
	}
	return id, true
}

// mapPricingSnapshotError 把快照流程的哨兵错误翻译成带稳定 reason 的 HTTP 错误；其余错误原样返回（500）。
func mapPricingSnapshotError(err error) error {
	switch {
	case errors.Is(err, service.ErrPricingSnapshotNotFound):
		return infraerrors.NotFound("PRICING_SNAPSHOT_NOT_FOUND", "pricing snapshot not found")
	case errors.Is(err, service.ErrPricingNotPinned):
		return infraerrors.Conflict("PRICING_NOT_PINNED", "pricing is not pinned: pin the current pricing first")
	case errors.Is(err, service.ErrPricingAlreadyPinned):
		return infraerrors.Conflict("PRICING_ALREADY_PINNED", "pricing is already pinned")
	case errors.Is(err, service.ErrPricingBootstrapMismatch):
		return infraerrors.Conflict("PRICING_BOOTSTRAP_MISMATCH", "the price file differs from the data in use; retry shortly")
	case errors.Is(err, service.ErrPricingSnapshotNotCandidate):
		return infraerrors.Conflict("PRICING_SNAPSHOT_NOT_CANDIDATE", "the snapshot is not a pending candidate")
	case errors.Is(err, service.ErrPricingSnapshotPlanMismatch):
		return infraerrors.Conflict("PRICING_SNAPSHOT_PLAN_CHANGED", "the plan changed since the preview; preview again")
	case errors.Is(err, service.ErrPricingSnapshotBaselineChanged), errors.Is(err, service.ErrPricingSnapshotConflict):
		return infraerrors.Conflict("PRICING_SNAPSHOT_BASELINE_CHANGED", "the active snapshot changed; preview again")
	case errors.Is(err, service.ErrPricingSnapshotNothingToApprove):
		return infraerrors.BadRequest("PRICING_SNAPSHOT_NOTHING_TO_APPROVE", "nothing to approve")
	case errors.Is(err, service.ErrPricingSnapshotUnknownHold):
		return infraerrors.BadRequest("PRICING_SNAPSHOT_UNKNOWN_HOLD", err.Error())
	case errors.Is(err, service.ErrPricingSnapshotExposureUnavailable):
		return infraerrors.InternalServer("PRICING_SNAPSHOT_EXPOSURE_UNAVAILABLE", "save-time validation is not configured; approval is refused")
	default:
		return err
	}
}
