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

// pricingGroupSummaryReader 是 PricingMatrixHandler 读取分组批量摘要的最小依赖（由矩阵派生服务实现，便于测试时替换）。
type pricingGroupSummaryReader interface {
	GroupSummaries(ctx context.Context, ids []int64) (*service.GroupPricingSummaryResult, error)
}

// GroupSummaries 批量返回分组的价格阶段、配置 revision 与成本核算规则摘要，矩阵页一次拿全，避免对每个分组各请求一次。
// ids 是逗号分隔的分组 id（最多 service.MaxGroupPricingSummaries 个）；不传表示全部未删除分组，同样最多这么多个
// （超出时 truncated 为 true）。只读，机器令牌可调用。
// GET /api/v1/admin/pricing-matrix/groups/summary?ids=1,2,3
func (h *PricingMatrixHandler) GroupSummaries(c *gin.Context) {
	ids, ok := parseGroupSummaryIDs(c)
	if !ok {
		return
	}
	res, err := h.summaries.GroupSummaries(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// parseGroupSummaryIDs 解析 ?ids=1,2,3；失败时已写入 400 响应。空白项忽略，出现非正整数就整体拒绝。
func parseGroupSummaryIDs(c *gin.Context) ([]int64, bool) {
	raw := strings.TrimSpace(c.Query("ids"))
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "ids must be comma separated positive integers").
				WithMetadata(map[string]string{"param": "ids"}))
			return nil, false
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "ids must not be empty when given").
			WithMetadata(map[string]string{"param": "ids"}))
		return nil, false
	}
	return ids, true
}
