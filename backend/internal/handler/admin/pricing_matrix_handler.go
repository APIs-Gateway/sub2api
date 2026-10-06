package admin

import (
	"context"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// pricingDeriveViewer 是 PricingMatrixHandler 对 service.PricingDerivationService 的最小依赖，便于测试时替换。
type pricingDeriveViewer interface {
	ViewGroup(ctx context.Context, groupID int64) (*service.GroupDeriveView, error)
	ViewChannel(ctx context.Context, channelID int64) ([]service.GroupDeriveView, error)
	Stats() service.PricingDeriveStats
}

// modelCatalogLister 是 PricingMatrixHandler 对 service.ModelCatalogService 的最小依赖。
type modelCatalogLister interface {
	List(ctx context.Context, filter service.ModelCatalogFilter) ([]service.ModelCatalogEntry, error)
}

// pricingStageOperator 是 PricingMatrixHandler 对 service.PricingStageService 的最小依赖（W6 PR5）。
type pricingStageOperator interface {
	Switch(ctx context.Context, req service.PricingStageSwitchRequest) (*service.PricingStageSwitchResult, error)
	ShadowStats() service.PricingShadowStats
	MatrixSnapshotStats() service.MatrixSnapshotStats
	ShadowSamples(ctx context.Context, groupID int64, limit int) ([]service.PricingShadowSample, error)
}

// PricingMatrixHandler 价格矩阵（W6）的管理接口：查看渠道到矩阵的派生结果与模型目录（只读），
// 以及（PR5）阶段切换与影子比对结果。阶段切换是唯一的写入口，PR7 之前只允许 legacy 与 shadow。
type PricingMatrixHandler struct {
	derive    pricingDeriveViewer
	catalog   modelCatalogLister
	stage     pricingStageOperator
	summaries pricingGroupSummaryReader
}

// NewPricingMatrixHandler 创建价格矩阵 handler。
func NewPricingMatrixHandler(derive *service.PricingDerivationService, catalog *service.ModelCatalogService, stage *service.PricingStageService) *PricingMatrixHandler {
	return &PricingMatrixHandler{derive: derive, catalog: catalog, stage: stage, summaries: derive}
}

// switchPricingStageRequest 阶段切换请求体。
type switchPricingStageRequest struct {
	Stage   string `json:"stage" binding:"required"`
	Confirm bool   `json:"confirm"`
}

// SwitchStage 切换分组的价格体系阶段（登记为 pricing.stage_switch）。PR7 合并之前只允许 legacy 与 shadow。
// PUT /api/v1/admin/pricing-matrix/groups/:id/stage  {"stage": "shadow", "confirm": true}
func (h *PricingMatrixHandler) SwitchStage(c *gin.Context) {
	id, ok := parsePricingMatrixID(c)
	if !ok {
		return
	}
	var req switchPricingStageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	result, err := h.stage.Switch(c.Request.Context(), service.PricingStageSwitchRequest{
		GroupID:    id,
		To:         service.PricingStage(strings.TrimSpace(req.Stage)),
		OperatorID: subject.UserID,
		Confirm:    req.Confirm,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ShadowStats 返回影子比对的进程内计数（指标 pricing_shadow_compared_total、pricing_shadow_diff_total 等）
// 与矩阵快照缓存的计数。多实例各算各的，进程重启后清零。
// GET /api/v1/admin/pricing-matrix/shadow/stats
func (h *PricingMatrixHandler) ShadowStats(c *gin.Context) {
	response.Success(c, gin.H{
		"metrics":  h.stage.ShadowStats(),
		"snapshot": h.stage.MatrixSnapshotStats(),
	})
}

// ShadowDiffs 返回最近的差异样本（pricing_shadow_diffs，保留 14 天的采样）。
// GET /api/v1/admin/pricing-matrix/shadow/diffs?group_id=16&limit=50
func (h *PricingMatrixHandler) ShadowDiffs(c *gin.Context) {
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "group_id must be a positive integer").
				WithMetadata(map[string]string{"param": "group_id"}))
			return
		}
		groupID = v
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "limit must be a positive integer").
				WithMetadata(map[string]string{"param": "limit"}))
			return
		}
		limit = v
	}
	items, err := h.stage.ShadowSamples(c.Request.Context(), groupID, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// ViewGroupDerive 返回分组按渠道当前配置实时派生的结果，并与库里现状对照。
// GET /api/v1/admin/pricing-matrix/groups/:id/derive
func (h *PricingMatrixHandler) ViewGroupDerive(c *gin.Context) {
	id, ok := parsePricingMatrixID(c)
	if !ok {
		return
	}
	view, err := h.derive.ViewGroup(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ViewChannelDerive 返回渠道里每个分组的派生结果与库里现状的对照。
// GET /api/v1/admin/pricing-matrix/channels/:id/derive
func (h *PricingMatrixHandler) ViewChannelDerive(c *gin.Context) {
	id, ok := parsePricingMatrixID(c)
	if !ok {
		return
	}
	views, err := h.derive.ViewChannel(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"channel_id": id, "groups": views})
}

// HookStats 返回派生钩子的进程内计数（运行次数、失败次数、panic 次数）。
// GET /api/v1/admin/pricing-matrix/hook-stats
func (h *PricingMatrixHandler) HookStats(c *gin.Context) {
	response.Success(c, h.derive.Stats())
}

// ListModelCatalog 列出模型目录。
// GET /api/v1/admin/model-catalog?platform=openai&status=active
func (h *PricingMatrixHandler) ListModelCatalog(c *gin.Context) {
	entries, err := h.catalog.List(c.Request.Context(), service.ModelCatalogFilter{
		Platform: strings.TrimSpace(c.Query("platform")),
		Status:   service.ModelCatalogStatus(strings.TrimSpace(c.Query("status"))),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": entries})
}

// parsePricingMatrixID 解析路径里的正整数 :id；失败时已写入 400 响应。
func parsePricingMatrixID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "id must be a positive integer").
			WithMetadata(map[string]string{"param": "id"}))
		return 0, false
	}
	return id, true
}
