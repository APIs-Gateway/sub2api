package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// priceQuoter 是 PricingQuoteHandler 对 service.PriceQuoter 的最小依赖，便于测试时替换。
type priceQuoter interface {
	Quote(ctx context.Context, req service.QuoteRequest) (*service.Quote, error)
	OfficialReference(model string) service.QuoteOfficialReference
}

// PricingQuoteHandler 提供只读的价格报价接口：返回任意「模型 × 分组（× 用户）」
// 按当前配置计费时的最终价格，与网关计费同源（见 service.PriceQuoter）。
type PricingQuoteHandler struct {
	quoter priceQuoter
}

// NewPricingQuoteHandler 创建价格报价 handler。
func NewPricingQuoteHandler(quoter *service.PriceQuoter) *PricingQuoteHandler {
	return &PricingQuoteHandler{quoter: quoter}
}

// Quote 返回「模型 × 分组」的报价。
// GET /api/v1/admin/pricing/quote?model=gpt-5.5&group_id=3&user_id=12&served_group_id=4&service_tier=priority&at=2026-10-01T02:00:00Z
//
// model、group_id 必填；user_id（查用户专属倍率）、served_group_id（稳定优先兜底服务组）、
// service_tier（priority/fast/flex/...）、at（RFC3339 计费时点，影响 DeepSeek 峰谷）可选。
func (h *PricingQuoteHandler) Quote(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", "model parameter is required").
			WithMetadata(map[string]string{"param": "model"}))
		return
	}
	groupID, ok := parseQuoteIDParam(c, "group_id", true)
	if !ok {
		return
	}
	servedGroupID, ok := parseQuoteIDParam(c, "served_group_id", false)
	if !ok {
		return
	}
	userID, ok := parseQuoteIDParam(c, "user_id", false)
	if !ok {
		return
	}

	var at time.Time
	if raw := strings.TrimSpace(c.Query("at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "at must be an RFC3339 timestamp").
				WithMetadata(map[string]string{"param": "at"}))
			return
		}
		at = parsed
	}

	quote, err := h.quoter.Quote(c.Request.Context(), service.QuoteRequest{
		Model:         model,
		GroupID:       groupID,
		ServedGroupID: servedGroupID,
		UserID:        userID,
		ServiceTier:   strings.TrimSpace(c.Query("service_tier")),
		At:            at,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, quote)
}

// parseQuoteIDParam 解析正整数 ID 查询参数。缺失时 required=true 报 400，否则返回 0。
// 解析失败时已写入 400 响应，调用方直接返回。
func parseQuoteIDParam(c *gin.Context, name string, required bool) (int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		if required {
			response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", name+" parameter is required").
				WithMetadata(map[string]string{"param": name}))
			return 0, false
		}
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", name+" must be a positive integer").
			WithMetadata(map[string]string{"param": name}))
		return 0, false
	}
	return value, true
}
