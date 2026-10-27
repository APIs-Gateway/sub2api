package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetUnpricedUsage 返回近 24 小时或 7 天按（分组、模型）汇总的「未定价用量」：有用量、分组倍率大于 0，
// 但 total_cost 与 actual_cost 都为 0 的 usage_logs 行（上游模型不一致拦截留下的审计行除外，那类行不计费是有意的）。
// 只读，只给管理员；返回里带分组名与模型名，不含请求内容、用户或密钥信息。
// GET /api/v1/admin/ops/unpriced-usage?window=24h|7d&platform=&group_id=&limit=
func (h *OpsHandler) GetUnpricedUsage(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}

	window := strings.TrimSpace(c.Query("window"))
	switch window {
	case "", service.OpsUnpricedBillingWindow24h, service.OpsUnpricedBillingWindow7d:
	default:
		response.BadRequest(c, "Invalid window")
		return
	}

	groupID, err := parseOptionalPositiveID(c, "group_id")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			response.BadRequest(c, "Invalid limit")
			return
		}
		limit = n
	}

	report, err := h.opsService.GetUnpricedBillingReport(
		c.Request.Context(),
		window,
		strings.TrimSpace(c.Query("platform")),
		groupID,
		limit,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, report)
}
