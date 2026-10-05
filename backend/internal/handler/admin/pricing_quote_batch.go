package admin

import (
	"context"
	"math"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 一次批量报价的上限：一个平台的矩阵（几十个模型 × 几个到十几个分组）一次请求能取完，
// 又不至于让一个请求在进程里循环几千次。超出由前端分批取。
const (
	quoteBatchMaxGroups = 40
	quoteBatchMaxModels = 60
)

// quoteBatchCell 是「模型 × 分组」格子的精简报价：够矩阵页判断开放状态并显示用户实付价。
// 与 Quote 同源（同一次 PriceQuoter.Quote 的结果），只取展示需要的字段。
type quoteBatchCell struct {
	GroupID int64  `json:"group_id"`
	Model   string `json:"model"`
	// Error 非空表示这个格子没报出价（如分组不存在），其余字段无意义。
	Error               string              `json:"error,omitempty"`
	Access              service.QuoteAccess `json:"access"`
	Priced              bool                `json:"priced"`
	Source              service.QuoteSource `json:"source"`
	BillingMode         string              `json:"billing_mode"`
	GroupMultiplier     float64             `json:"group_multiplier"`
	ExtraMultiplier     *float64            `json:"extra_multiplier,omitempty"`
	EffectiveMultiplier float64             `json:"effective_multiplier"`
	// FinalPerMTok 是用户实付单价（USD / 百万 token，已乘倍率）；按次计费或没有价格时为空。
	FinalPerMTok *service.QuoteUnitPrices `json:"final_per_mtok,omitempty"`
	// PerRequestPrice 是按次计费的用户实付单价（USD / 次，已乘倍率）。
	PerRequestPrice *float64 `json:"per_request_price,omitempty"`
	// PerRequestMin / PerRequestMax：按次计费没有主价、只有区间价时，给出区间价（已乘倍率）的最小与最大值，
	// 此时不返回 PerRequestPrice（否则会被当成 0 元）。
	PerRequestMin *float64 `json:"per_request_min,omitempty"`
	PerRequestMax *float64 `json:"per_request_max,omitempty"`
}

// QuoteBatch 一次返回多个「模型 × 分组」的精简报价，外加每个模型的官方参考价。
// GET /api/v1/admin/pricing/quote-batch?group_ids=3,4&models=gpt-5.5,gpt-5.4-mini
//
// 只读。group_ids 最多 40 个、models 最多 60 个，重复项会去掉；都按默认条件报价
// （不带用户、不带 service tier，计费时点取当前时刻）。group_ids 可以不给，此时只返回模型的官方参考价。
func (h *PricingQuoteHandler) QuoteBatch(c *gin.Context) {
	groupIDs, ok := parseQuoteBatchGroupIDs(c, quoteBatchMaxGroups)
	if !ok {
		return
	}
	models, ok := parseQuoteBatchModels(c, quoteBatchMaxModels)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	refs := make([]service.QuoteOfficialReference, 0, len(models))
	for _, model := range models {
		refs = append(refs, h.quoter.OfficialReference(model))
	}
	cells := make([]quoteBatchCell, 0, len(groupIDs)*len(models))
	for _, groupID := range groupIDs {
		for _, model := range models {
			if err := ctx.Err(); err != nil {
				response.ErrorFrom(c, err)
				return
			}
			cells = append(cells, h.quoteBatchCell(ctx, groupID, model))
		}
	}
	response.Success(c, gin.H{"models": refs, "cells": cells})
}

func (h *PricingQuoteHandler) quoteBatchCell(ctx context.Context, groupID int64, model string) quoteBatchCell {
	cell := quoteBatchCell{GroupID: groupID, Model: model}
	quote, err := h.quoter.Quote(ctx, service.QuoteRequest{Model: model, GroupID: groupID})
	if err != nil || quote == nil {
		cell.Error = "QUOTE_FAILED"
		if err != nil {
			if reason := infraerrors.Reason(err); reason != "" {
				cell.Error = reason
			}
		}
		return cell
	}
	cell.Access = quote.Access
	cell.Priced = quote.Priced
	cell.Source = quote.Source
	cell.BillingMode = quote.BillingMode
	cell.GroupMultiplier = quote.GroupMultiplier
	cell.ExtraMultiplier = quote.ExtraMultiplier
	cell.EffectiveMultiplier = quote.EffectiveMultiplier
	if quote.FinalPrices != nil {
		perMTok := quote.FinalPrices.PerMTok
		cell.FinalPerMTok = &perMTok
	}
	if quote.PerRequest != nil && quote.Priced {
		if quote.PerRequest.DefaultPrice == 0 && len(quote.PerRequest.Tiers) > 0 {
			lo, hi := quote.PerRequest.Tiers[0].Price, quote.PerRequest.Tiers[0].Price
			for _, tier := range quote.PerRequest.Tiers[1:] {
				lo = math.Min(lo, tier.Price)
				hi = math.Max(hi, tier.Price)
			}
			lo *= quote.EffectiveMultiplier
			hi *= quote.EffectiveMultiplier
			cell.PerRequestMin = &lo
			cell.PerRequestMax = &hi
		} else {
			price := quote.PerRequest.DefaultPrice * quote.EffectiveMultiplier
			cell.PerRequestPrice = &price
		}
	}
	return cell
}

// splitQuoteBatchList 按逗号拆分、去空白、去空项与重复项，保持原有顺序。
func splitQuoteBatchList(raw string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, dup := seen[part]; dup {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

// quoteBatchListError 统一的批量参数错误：缺失、非法或超过上限都是 400。
func quoteBatchListError(c *gin.Context, reason, message, param string) {
	response.ErrorFrom(c, infraerrors.BadRequest(reason, message).WithMetadata(map[string]string{"param": param}))
}

func parseQuoteBatchGroupIDs(c *gin.Context, limit int) ([]int64, bool) {
	items := splitQuoteBatchList(c.Query("group_ids"))
	if len(items) > limit {
		quoteBatchListError(c, "INVALID_PARAMETER", "group_ids has too many items", "group_ids")
		return nil, false
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil || id <= 0 {
			quoteBatchListError(c, "INVALID_PARAMETER", "group_ids must be positive integers", "group_ids")
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func parseQuoteBatchModels(c *gin.Context, limit int) ([]string, bool) {
	items := splitQuoteBatchList(c.Query("models"))
	if len(items) == 0 {
		quoteBatchListError(c, "MISSING_PARAMETER", "models parameter is required", "models")
		return nil, false
	}
	if len(items) > limit {
		quoteBatchListError(c, "INVALID_PARAMETER", "models has too many items", "models")
		return nil, false
	}
	return items, true
}
