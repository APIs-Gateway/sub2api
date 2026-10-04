package admin

import (
	"context"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
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

// PricingMatrixHandler 价格矩阵（W6）的只读管理接口：查看渠道到矩阵的派生结果与模型目录。
// 全部是 GET，不写任何东西；本 PR 里没有任何计费、调度、准入路径读取这些数据。
type PricingMatrixHandler struct {
	derive  pricingDeriveViewer
	catalog modelCatalogLister
}

// NewPricingMatrixHandler 创建价格矩阵只读 handler。
func NewPricingMatrixHandler(derive *service.PricingDerivationService, catalog *service.ModelCatalogService) *PricingMatrixHandler {
	return &PricingMatrixHandler{derive: derive, catalog: catalog}
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
