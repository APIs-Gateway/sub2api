package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// OpenAI 兼容入口（/v1/responses、/v1/chat/completions）接入回退链时共用的两段判定。
// 抽到这里是为了让各入口的「哪些跳要跳过」「没号算不算熔断」只有一份口径，不会各写各的而慢慢跑偏。

// openAIHopStaticEligible 是回退链每跳的静态资格检查中与入口无关的部分（设计 3.4 第 4、5 项）：
// 主分组那一跳与原来一致，不额外检查（无链等价的前提）；其余各跳（含管理员 head）模型必须在该分组开放，
// 且有价格（未配价格的模型会按零成本放行，兜底分组不能白送）。checkPrice 为 false 时跳过取价检查
// （例如 Responses 的生图请求按图片价计费，不走这条取价链）。
//
// 取价检查在 service 层带「非结算调用」标记，不会计入无价计数（计数只属于真正写用量行的结算）。
func (h *OpenAIGatewayHandler) openAIHopStaticEligible(ctx context.Context, hopKey *service.APIKey, info service.HopInfo, reqModel string, mapping service.ChannelMappingResult, checkPrice bool) bool {
	if info.Hop.RouteSource == service.RouteSourcePrimary {
		return true
	}
	if !h.gatewayService.IsModelOpenForGroup(ctx, info.Hop.GroupID, reqModel) {
		return false
	}
	if checkPrice && !h.gatewayService.IsModelPricedForGroup(ctx, hopKey, reqModel, mapping) {
		return false
	}
	return true
}

// openAINoAccountFacts 把一次「没有拿到号」的选号失败描述成 ClassifyHopFailure 的输入（仅有链时调用）。
//   - 本跳已经组内重选过一次（policy.ReselectUsed）：忙号被排除后剩下的是「没号」，本质是繁忙，按繁忙分类、不计熔断（规则 1）；
//   - 选号探测预算用尽（IsOpenAISelectionBudgetExhausted，大分组 DB 复核预算耗尽）：同样是繁忙，不计熔断（S-3）；
//   - 不是「没有可用账号」类的错误（仓储 / 快照故障等）：不可回退，按原逻辑写 503；
//   - 其余：用与 resolveNoAccountError 同源的诊断给出「组内有账号 / 支持该模型」，并把 capabilityBlocked
//     （图片意图、压缩请求这类能力受限的请求）标成 CapabilityBlocked，避免把「没有账号具备这项能力」记成分组故障。
func (h *OpenAIGatewayHandler) openAINoAccountFacts(c *gin.Context, key *service.APIKey, reqModel string, policy *openAIHopSlotPolicy, selectionErr error, capabilityBlocked bool) service.HopFailure {
	if policy.ReselectUsed() || service.IsOpenAISelectionBudgetExhausted(selectionErr) {
		return service.HopFailure{Kind: service.HopFailureBusyTimeout}
	}
	if selectionErr != nil && !errors.Is(selectionErr, service.ErrNoAvailableAccounts) && !errors.Is(selectionErr, service.ErrNoAvailableCompactAccounts) {
		return service.HopFailure{Kind: service.HopFailureOther}
	}
	f := service.HopFailure{
		Kind:              service.HopFailureNoAccount,
		PoolHasAccounts:   true,
		ModelSupported:    true,
		CapabilityBlocked: capabilityBlocked || errors.Is(selectionErr, service.ErrNoAvailableCompactAccounts),
	}
	if h != nil && h.gatewayService != nil && key != nil {
		diag := h.gatewayService.DiagnoseModelAvailabilityForPlatform(c.Request.Context(), key.GroupID, reqModel, service.PlatformFromAPIKey(key))
		f.PoolHasAccounts = diag.HasAccountsInPool
		f.ModelSupported = diag.HasModelSupport
	}
	return f
}
