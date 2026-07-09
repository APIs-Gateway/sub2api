package service

import (
	"context"
	"errors"
	"time"
)

// StablePriorityIntent 描述本次请求是否参与稳定优先（跨分组兜底）。
type StablePriorityIntent struct {
	Enabled bool // 来自 user.StablePriorityEnabled
}

// openAIHomeExhausted 判定一次选择是否表示"该组所有渠道都不可用"。
// 约定：
//   - 返回 ErrNoAvailableAccounts（或其包装）→ 全挂；
//   - 选到账号 → 未全挂；
//   - 无账号但带 WaitPlan（accounts 存在只是繁忙）→ 未全挂，应等待而非兜底；
//   - 无账号且无 WaitPlan → 全挂。
//
// 注意：ctx 取消等非"无账号"错误不视为全挂，直接向上透传、不触发兜底。
func openAIHomeExhausted(sel *AccountSelectionResult, err error) bool {
	if err != nil {
		return errors.Is(err, ErrNoAvailableAccounts)
	}
	if sel == nil {
		return true
	}
	if sel.Account != nil {
		return false
	}
	return sel.WaitPlan == nil
}

// resolveStableChain 沿 home 组的 StablePriorityFallbackGroupID 解析兜底链 [G1,G2,...]。
// 仅纳入 active 的 openai 组；遇环、非 openai、加载失败或达到深度上限即停止。
func (s *OpenAIGatewayService) resolveStableChain(ctx context.Context, home *Group) []*Group {
	if home == nil || home.Platform != PlatformOpenAI || home.StablePriorityFallbackGroupID == nil || s.groupRepo == nil {
		return nil
	}
	chain := make([]*Group, 0, StablePriorityMaxChainDepth)
	visited := map[int64]struct{}{home.ID: {}}
	nextID := *home.StablePriorityFallbackGroupID
	for depth := 0; depth < StablePriorityMaxChainDepth; depth++ {
		if _, seen := visited[nextID]; seen {
			break // 环保护
		}
		visited[nextID] = struct{}{}
		g, err := s.groupRepo.GetByIDLite(ctx, nextID)
		if err != nil || g == nil || g.Platform != PlatformOpenAI {
			break
		}
		if g.Status == StatusActive {
			chain = append(chain, g)
		}
		if g.StablePriorityFallbackGroupID == nil {
			break
		}
		nextID = *g.StablePriorityFallbackGroupID
	}
	return chain
}

// climbStableChain 逐档尝试兜底组，返回第一个"未全挂"的结果及其对应档位组。
// 全档皆挂时返回最后一档的结果与 nil 组。
func (s *OpenAIGatewayService) climbStableChain(
	chain []*Group,
	selectFor func(groupID int64) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error),
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, *Group, error) {
	var (
		lastSel *AccountSelectionResult
		lastDec OpenAIAccountScheduleDecision
		lastErr error
	)
	for _, g := range chain {
		sel, dec, err := selectFor(g.ID)
		if !openAIHomeExhausted(sel, err) {
			return sel, dec, g, err
		}
		lastSel, lastDec, lastErr = sel, dec, err
	}
	return lastSel, lastDec, nil, lastErr
}

// homeGroupHealthy 判断 home(廉价)组当前是否有可调度的 openai 渠道。
// 复用 listSchedulableAccounts（与选择同源的 IsSchedulable 过滤），channel-monitor 开不开都可用。
func (s *OpenAIGatewayService) homeGroupHealthy(ctx context.Context, homeID int64) bool {
	id := homeID
	accounts, err := s.listSchedulableAccounts(ctx, &id)
	if err != nil {
		return false
	}
	for i := range accounts {
		if accounts[i].IsSchedulable() {
			return true
		}
	}
	return false
}

func annotateStableServed(dec *OpenAIAccountScheduleDecision, state string, fallback bool, served *Group, homeID int64) {
	dec.StablePriorityState = state
	dec.StablePriorityFallback = fallback
	if served != nil {
		dec.StableServedGroupID = served.ID
		dec.StableServedRateMultiplier = served.RateMultiplier
		dec.StableServedImageRateIndependent = served.ImageRateIndependent
		dec.StableServedImageRateMultiplier = served.ImageRateMultiplier
		dec.StableServedImagePrice1K = served.ImagePrice1K
		dec.StableServedImagePrice2K = served.ImagePrice2K
		dec.StableServedImagePrice4K = served.ImagePrice4K
	} else {
		dec.StableServedGroupID = homeID
	}
}

// SelectAccountWithSchedulerStable 在 SelectAccountWithScheduler 之上叠加"稳定优先"多档兜底 + 防抖切回。
// homeGroup 为 API Key 所属（廉价）组；intent.Enabled 决定本请求是否参与兜底。
// 未启用 / 无状态存储 / 未配兜底链时，退化为原始选择行为。
func (s *OpenAIGatewayService) SelectAccountWithSchedulerStable(
	ctx context.Context,
	homeGroup *Group,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	intent StablePriorityIntent,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	original := func() (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact)
	}

	// 快速旁路：未启用 / 无状态存储 / 无 home / 未配兜底链（用快照上已带的指针判断，
	// 避免 normal 健康路径上的额外 DB 查询）→ 原始行为。
	if openAIAllGroupsRoutingEnabled(ctx) || !intent.Enabled || s.stableStore == nil || groupID == nil || homeGroup == nil ||
		homeGroup.Platform != PlatformOpenAI || homeGroup.StablePriorityFallbackGroupID == nil {
		return original()
	}

	homeID := *groupID
	nowNano := time.Now().UnixNano()
	state, _ := s.stableStore.Get(ctx, homeID)

	selectFor := func(gid int64) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		id := gid
		// 兜底档位不沿用 home 的 previousResponseID（该响应来自 home，跨组不应命中）。
		prevID := previousResponseID
		if gid != homeID {
			prevID = ""
		}
		return s.selectAccountWithScheduler(ctx, &id, prevID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact)
	}

	if state.InFallback() {
		// 兜底态（异常路径）：解析链并评估能否切回 home。
		chain := s.resolveStableChain(ctx, homeGroup)
		if len(chain) == 0 {
			// 链已被移除/失效 → 回到 home 正常行为。
			sel, dec, err := original()
			annotateStableServed(&dec, StablePriorityModeNormal, false, nil, homeID)
			return sel, dec, err
		}
		healthy := s.homeGroupHealthy(ctx, homeID)
		_, _ = s.stableStore.ObserveHomeHealth(ctx, homeID, healthy, nowNano)
		if healthy {
			if ok, _ := s.stableStore.TryRevert(ctx, homeID, nowNano,
				int64(StablePriorityFallbackMinDwell), int64(StablePriorityRevertStableDuration)); ok {
				sel, dec, err := selectFor(homeID)
				if !openAIHomeExhausted(sel, err) {
					dec.StablePriorityReverted = true
					annotateStableServed(&dec, StablePriorityModeNormal, false, nil, homeID)
					return sel, dec, err
				}
				// 切回后 home 仍选不到 → 重新进入兜底。
				_, _ = s.stableStore.TryEnterFallback(ctx, homeID, nowNano)
			}
		}
		// 维持兜底：从 G1 起逐档爬（跳过 home）。
		served, sdec, sg, serr := s.climbStableChain(chain, selectFor)
		if sg != nil {
			annotateStableServed(&sdec, StablePriorityModeFallback, true, sg, homeID)
			return served, sdec, serr
		}
		// 兜底链也全挂 → 最后再试一次 home。
		sel, dec, err := original()
		annotateStableServed(&dec, StablePriorityModeFallback, false, nil, homeID)
		return sel, dec, err
	}

	// normal 态：先用 home（健康路径不触碰 DB 解析链）。
	sel, dec, err := original()
	if !openAIHomeExhausted(sel, err) {
		annotateStableServed(&dec, StablePriorityModeNormal, false, nil, homeID)
		return sel, dec, err
	}
	// home 全挂（冷路径）→ 解析链、逐档爬、进入兜底态。
	chain := s.resolveStableChain(ctx, homeGroup)
	if len(chain) == 0 {
		annotateStableServed(&dec, StablePriorityModeNormal, false, nil, homeID)
		return sel, dec, err
	}
	served, sdec, sg, serr := s.climbStableChain(chain, selectFor)
	if sg != nil {
		_, _ = s.stableStore.TryEnterFallback(ctx, homeID, nowNano)
		annotateStableServed(&sdec, StablePriorityModeFallback, true, sg, homeID)
		return served, sdec, serr
	}
	// 全档（含 home）皆挂 → 返回 home 的原始结果/错误（与今日行为一致）。
	annotateStableServed(&dec, StablePriorityModeNormal, false, nil, homeID)
	return sel, dec, err
}
