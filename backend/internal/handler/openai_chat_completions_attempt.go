package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// /v1/chat/completions 的「选号 → 等槽 → Forward → failover」单跳逻辑。
//
// 与 openai_responses_attempt.go 同一个形状：无链请求只调用一次（args.info == nil，args.key 是原始 Key），
// 逻辑与引入回退链之前 ChatCompletions 里内联的循环逐行一致；有链请求每一跳调用一次
// （args.info 非 nil，args.key 是该跳的影子 Key），在原有出口上多出三件事：
//   - 原来会直接写 503 / 429 / failover 耗尽错误的出口，在非末跳改为返回 FallbackWorthy 且不写响应（错误交给 runner 暂存）；
//   - 选号 ctx 打上「补试整组饱和」标记、等槽按 hop 策略短等并最多重选一次（openAIHopSlotPolicy）；
//   - 本跳的换号预算取 min(现有值, MaxPerHopSwitches)，并受每请求总尝试次数约束。
//
// 与旧「稳定优先」的关系（旧机制只覆盖 chat/completions，下线属于 PR5）：有链请求每一跳都按「未开启稳定优先」选号
// （StablePriorityIntent 取零值，调度器直接走原始选号），所以一个请求不会既按链回退、又按旧机制沿分组指针爬。
// 无链请求（开关关闭、没配链、链长 < 2、解析出错、forced 用户没有管理员隐藏链）仍然完全走旧机制。

// chatChannelPlan 是某个分组下的渠道映射结果（每跳重算：渠道配置按分组生效）。
type chatChannelPlan struct {
	mapping      service.ChannelMappingResult
	forwardModel string
}

// openAIChatRun 是一次 /v1/chat/completions 请求在选号 / 转发阶段需要的请求级状态
// （只读，除 sessionHash 的初值外不被每跳修改）。
type openAIChatRun struct {
	h                  *OpenAIGatewayHandler
	c                  *gin.Context
	reqLog             *zap.Logger
	subject            middleware2.AuthSubject
	subscription       *service.UserSubscription
	body               []byte
	reqModel           string
	reqStream          bool
	requestPlatform    string
	sessionHash        string
	promptCacheKey     string
	routingStart       time.Time
	streamStarted      *bool
	maxAccountSwitches int
}

// chatHopArgs 是一次 attempt 调用的入参。
type chatHopArgs struct {
	// key 是这一跳使用的 Key：无链为原始 Key，有链为影子 Key（Group / GroupID 是这一跳的分组）。
	key *service.APIKey
	// info 非 nil 表示有链；nil 表示无链（原路径）。
	info    *service.HopInfo
	channel chatChannelPlan
	// output / settings 仅有链时使用。
	output   *service.OutputTracker
	settings service.GroupFallbackSettings
}

// streamNow 返回此刻写错误应按流式还是 JSON：响应头已按流式提交（含只写过心跳 / ping 的情形）都要走流式收尾。
// 闭包里调用它，保证读取的是「写错误那一刻」的状态，而不是创建闭包时的状态。
// 无链时 output 为 nil，等价于原来直接读 streamStarted。
func (r *openAIChatRun) streamNow(output *service.OutputTracker) bool {
	return *r.streamStarted || output.HeartbeatOnly()
}

func (r *openAIChatRun) attempt(args chatHopArgs) service.HopResult {
	h := r.h
	c := r.c
	reqLog := r.reqLog
	key := args.key
	info := args.info
	chained := info != nil
	channelMapping := args.channel.mapping
	forwardModel := args.channel.forwardModel

	maxAccountSwitches := r.maxAccountSwitches
	var policy *openAIHopSlotPolicy
	if chained {
		if info.MaxSwitches > 0 && info.MaxSwitches < maxAccountSwitches {
			maxAccountSwitches = info.MaxSwitches
		}
		policy = newOpenAIHopSlotPolicy(*info, args.settings, time.Now())
	}

	// 每一跳都从干净的状态开始：失败账号排除集合、换号计数、同账号重试计数、failover 状态都不跨跳继承。
	sessionHash := r.sessionHash
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	var clientPolicy openAIClientRestrictionSelection
	attempts := 0

	stream := func() bool { return r.streamNow(args.output) }
	finish := func(res service.HopResult) service.HopResult {
		res.Attempts = attempts
		res.UpstreamAttempted = attempts > 0
		return res
	}
	terminal := func() service.HopResult {
		return finish(service.HopResult{Outcome: service.HopOutcomeTerminal})
	}
	// fail 落地一次失败。无链：直接写，与原来一致。有链：按分类决定——
	// 不可回退（含真实内容已输出）立即写；可回退且需延迟（非末跳）把写错误的闭包交给 runner 暂存；
	// 可回退的末跳立即写并标记已写。facts 只在有链时才求值，无链请求不产生额外查询。
	fail := func(facts func() service.HopFailure, write func()) service.HopResult {
		if !chained {
			write()
			return service.HopResult{}
		}
		f := facts()
		f.OutputCommitted = args.output.Committed()
		res := service.ClassifyHopFailure(f)
		switch {
		case res.Outcome != service.HopOutcomeFallbackWorthy:
			write()
		case info.DeferFinalError:
			res.WriteFinalError = write
		default:
			write()
			res.ErrorWritten = true
		}
		return finish(res)
	}
	// 重选后没号 / 探测预算用尽 / 没号的诊断事实，见 openAINoAccountFacts。chat 没有图片意图和压缩请求，不存在能力受限。
	noAccountFacts := func(selectionErr error) func() service.HopFailure {
		return func() service.HopFailure {
			return h.openAINoAccountFacts(c, key, r.reqModel, policy, selectionErr, false)
		}
	}
	// failoverExhaustedBy 在 failover 失败后收尾。noGroupEvidence 为 true 表示这次停下并不能说明整组有问题
	// （例如只试了一个号就按 429 闸门停下）：照常可以回退，但不产生熔断信号。
	failoverExhaustedBy := func(failoverErr *service.UpstreamFailoverError, noGroupEvidence bool) service.HopResult {
		return fail(func() service.HopFailure {
			return service.HopFailure{
				Kind:                   service.HopFailureFailoverExhausted,
				LastStatus:             failoverErr.StatusCode,
				RequestScopedTransient: failoverErr.RequestScopedTransient || noGroupEvidence,
			}
		}, func() {
			h.handleFailoverExhausted(c, failoverErr, stream())
		})
	}
	failoverExhausted := func(failoverErr *service.UpstreamFailoverError) service.HopResult {
		return failoverExhaustedBy(failoverErr, clientPolicy.policyExcluded)
	}
	// attemptsExhausted：有链时受每请求总尝试次数约束（AttemptsRemaining 为 0 视为未设置）。
	attemptsExhausted := func() bool {
		return chained && info.AttemptsRemaining > 0 && attempts >= info.AttemptsRemaining
	}

	for {
		if failoverClientGone(c) {
			return terminal()
		}
		reqLog.Debug("openai_chat_completions.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		// 有链：每一跳都不参与旧的稳定优先（零值意图，调度器走原始选号），回退完全由链决定。
		// 无链：与引入回退链之前完全一致。
		stableIntent := service.StablePriorityIntent{}
		if !chained {
			stableIntent = service.StablePriorityIntent{Enabled: key.StablePriorityEnabled || (key.User != nil && key.User.StablePriorityEnabled)}
		}
		modelAvailabilityDiagnoser := service.ModelAvailabilityDiagnoser(h.gatewayService)
		if openAIStablePriorityCanFallback(key, stableIntent) {
			// A no-account result may have traversed fallback groups. Diagnosing
			// only the home group could turn a temporarily unavailable fallback
			// model into a false 404, so keep the established 503 response.
			modelAvailabilityDiagnoser = nil
		}
		selectAccount := func() (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
			if clientPolicy.pending && clientPolicy.pinnedGroupID != nil {
				selection, decision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
					policy.selectionContext(c.Request.Context()), clientPolicy.pinnedGroupID, "", sessionHash,
					forwardModel, failedAccountIDs, service.OpenAIUpstreamTransportAny,
					service.OpenAIEndpointCapabilityChatCompletions, false, true, r.requestPlatform,
				)
				clientPolicy.preserveStableDecision(&decision)
				return selection, decision, err
			}
			return h.gatewayService.SelectAccountWithSchedulerStable(
				policy.selectionContext(c.Request.Context()),
				key.Group,
				key.GroupID,
				"",
				sessionHash,
				forwardModel,
				failedAccountIDs,
				service.OpenAIUpstreamTransportAny,
				service.OpenAIEndpointCapabilityChatCompletions,
				false,
				true,
				stableIntent,
				r.requestPlatform,
			)
		}
		selection, scheduleDecision, err := selectAccount()
		if err != nil {
			if failoverClientGone(c) {
				reqLog.Info("openai_chat_completions.account_select_aborted_client_disconnected", zap.Error(err))
				return terminal()
			}
			reqLog.Warn("openai_chat_completions.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if lastFailoverErr == nil {
				if clientPolicy.rejectExhausted(h, c, err, stream()) {
					return terminal()
				}
				return fail(noAccountFacts(err), func() {
					h.respondNoAccountError(c, modelAvailabilityDiagnoser, key, r.reqModel, r.reqModel, service.PlatformOpenAI, "Service temporarily unavailable", err, noAccountCapacityMarkIfNoAvailable, openAINoAccountResponseStreaming, stream())
				})
			}
			return failoverExhausted(lastFailoverErr)
		}
		if selection == nil || selection.Account == nil {
			if lastFailoverErr != nil {
				return failoverExhausted(lastFailoverErr)
			}
			if clientPolicy.rejectExhausted(h, c, nil, stream()) {
				return terminal()
			}
			return fail(noAccountFacts(nil), func() {
				h.respondNoAccountError(c, modelAvailabilityDiagnoser, key, r.reqModel, r.reqModel, service.PlatformOpenAI, "No available accounts", nil, noAccountCapacityMarkAlways, openAINoAccountResponseStreaming, stream())
			})
		}
		if failoverClientGone(c) {
			releaseOpenAIClientPolicySelection(selection)
			return terminal()
		}
		if clientPolicy.exclude(h.gatewayService, c, selection, failedAccountIDs) {
			clientPolicy.pinStableGroup(key.GroupID, scheduleDecision)
			reqLog.Debug("openai_chat_completions.account_client_incompatible", zap.Int64("account_id", selection.Account.ID))
			continue
		}
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai_chat_completions.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		if scheduleDecision.StablePriorityFallback || scheduleDecision.StablePriorityReverted {
			reqLog.Info("openai_chat_completions.stable_priority",
				zap.Bool("fallback", scheduleDecision.StablePriorityFallback),
				zap.Bool("reverted", scheduleDecision.StablePriorityReverted),
				zap.String("state", scheduleDecision.StablePriorityState),
				zap.Int64p("home_group", key.GroupID),
				zap.Int64("served_group", scheduleDecision.StableServedGroupID),
				zap.Int64("account_id", account.ID),
			)
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		// 稳定优先兜底到 served 组时，须统一按"实际服务组"处理：
		//  1) 粘性会话绑定的 groupID 须与调度器读取侧一致，否则后续请求换命名空间、粘性失效、在兜底账号间抖动；
		//  2) 渠道映射须按 served 组重算，否则转发体与计费模型字段会沿用 home 组映射而出错。
		// 有链时 StableServedGroupID 恒为 0（上面没有开启稳定优先），这里不会生效；粘性绑定用的是本跳的分组。
		stickyGroupID := key.GroupID
		effectiveMapping := channelMapping
		homeGroupID := int64(0)
		if key.GroupID != nil {
			homeGroupID = *key.GroupID
		}
		if served := scheduleDecision.StableServedGroupID; served > 0 && served != homeGroupID {
			servedGroupID := served
			stickyGroupID = &servedGroupID
			effectiveMapping, _ = h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), &servedGroupID, r.reqModel)
		}
		// decision 必须来自刚才同一次 Select，groupID 必须是本跳的分组（粘性绑定以分组为键）。
		accountReleaseFunc, slotStatus := h.acquireResponsesAccountSlotForHop(c, stickyGroupID, sessionHash, selection, scheduleDecision, policy, r.reqStream, r.streamStarted, reqLog)
		if slotStatus == accountSlotRetrySelection {
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}
		if slotStatus == accountSlotBusyDeferred {
			// 繁忙：没有写任何响应，把「最近一次等槽失败」的原样错误交给 runner 暂存。
			// 只有最后一次失败确实是等槽失败时才会走到这里（重选之后的没号 / failover 耗尽有各自的出口）。
			return fail(func() service.HopFailure {
				return service.HopFailure{Kind: service.HopFailureBusyTimeout}
			}, func() {
				h.writeDeferredAccountSlotFailure(c, policy, stream())
			})
		}
		if slotStatus != accountSlotAcquired {
			// 等槽阶段已经写出了响应（Redis 错误、排队已满 / 超时的原样错误等）。
			return terminal()
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(r.routingStart).Milliseconds())
		forwardStart := time.Now()

		forwardBody := r.body
		if effectiveMapping.Mapped {
			forwardBody = h.gatewayService.ReplaceModelInBody(r.body, effectiveMapping.MappedModel)
		}
		// 在途计费预占按这一跳的有效 Key（有链时是影子 Key）估价：同一个请求的多次尝试 / 多跳复用并重设同一张预占单。
		// 预占被拒时本跳没有真正调用上游（attempts 不增加），直接按终止收尾：与无链一致，不退回分组层 RPM 计数
		// （ReleaseHopGroupRPMIfNotServed 只对跳过和「未写错误的可回退失败」退回）。
		// 心跳字节（SSE 注释行）不算内容交付：与 Responses 入口同口径取扣除心跳后的 Size。
		if !reserveBillingInflightHTTP(c, h.gatewayService, service.BillingInflightRequest{APIKey: key, Account: account, Model: r.reqModel, Body: forwardBody, ChannelUsageFields: effectiveMapping.ToUsageFields(r.reqModel, ""), StableDecision: &scheduleDecision}, accountReleaseFunc, func(status int, code, message string) {
			h.handleStreamingAwareError(c, status, code, message, stream())
		}) {
			return terminal()
		}
		attempts++
		writerSizeBeforeForward := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardAsChatCompletions(c.Request.Context(), c, account, forwardBody, r.promptCacheKey, "")
		}()
		cyberBlockKeyChat := ""
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockKeyChat = service.CyberSessionBlockKey(key.ID, c, r.body)
		}
		requestPayloadHash := service.HashUsageRequestPayload(r.body)
		h.recordCyberPolicyIfMarked(c, key, account, r.subscription, r.reqModel, err != nil, cyberBlockKeyChat, effectiveMapping.ToUsageFields(r.reqModel, ""), requestPayloadHash, scheduleDecision)
		// 上游模型不一致：先读 B（成功路径 RecordUsage 透传），再记审计行并清标（下一次尝试可重新打标）。
		upstreamResponseModel := ""
		if mark := service.GetOpsUpstreamModelMismatch(c); mark != nil {
			upstreamResponseModel = mark.ResponseModel
		}
		h.recordUpstreamModelMismatchIfMarked(c, key, account, r.subscription, r.reqModel, effectiveMapping.ToUsageFields(r.reqModel, ""), requestPayloadHash, r.body)

		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := resolveOpenAIUpstreamEndpoint(c, account)
		// 稳定优先方案 Y：兜底时按实际服务档位组倍率计费（normal 态为 0，不影响正常计费；有链时恒为 0，计价由影子 Key 的分组决定）。
		stableServedGroupID := scheduleDecision.StableServedGroupID
		stableServedRate := scheduleDecision.StableServedRateMultiplier
		stableServedImageIndependent := scheduleDecision.StableServedImageRateIndependent
		stableServedImageRate := scheduleDecision.StableServedImageRateMultiplier
		stableServedImagePrice1K := scheduleDecision.StableServedImagePrice1K
		stableServedImagePrice2K := scheduleDecision.StableServedImagePrice2K
		stableServedImagePrice4K := scheduleDecision.StableServedImagePrice4K

		cyberBlocked := service.GetOpsCyberPolicy(c) != nil
		submitChatUsage := func(result *service.OpenAIForwardResult) {
			h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
				if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
					Result:                           result,
					APIKey:                           key,
					User:                             key.User,
					Account:                          account,
					Subscription:                     r.subscription,
					InboundEndpoint:                  inboundEndpoint,
					UpstreamEndpoint:                 upstreamEndpoint,
					UserAgent:                        userAgent,
					IPAddress:                        clientIP,
					APIKeyService:                    h.apiKeyService,
					ChannelUsageFields:               effectiveMapping.ToUsageFields(r.reqModel, result.UpstreamModel),
					CyberBlocked:                     cyberBlocked,
					UpstreamResponseModel:            upstreamResponseModel,
					StableServedGroupID:              stableServedGroupID,
					StableServedRateMultiplier:       stableServedRate,
					StableServedImageRateIndependent: stableServedImageIndependent,
					StableServedImageRateMultiplier:  stableServedImageRate,
					StableServedImagePrice1K:         stableServedImagePrice1K,
					StableServedImagePrice2K:         stableServedImagePrice2K,
					StableServedImagePrice4K:         stableServedImagePrice4K,
				}); err != nil {
					logger.L().With(
						zap.String("component", "handler.openai_gateway.chat_completions"),
						zap.Int64("user_id", r.subject.UserID),
						zap.Int64("api_key_id", key.ID),
						zap.Any("group_id", key.GroupID),
						zap.String("model", r.reqModel),
						zap.Int64("account_id", account.ID),
					).Error("openai_chat_completions.record_usage_failed", zap.Error(err))
				}
			})
		}

		if err != nil {
			if result == nil && service.IsGrokContentPolicyRejectionError(err) {
				service.MarkBillingInflightAttemptNoCharge(c.Request.Context())
			}
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai_chat_completions.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					if result == nil && service.IsBillingInflightNoChargeError(err) && service.OpenAICompactKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward {
						service.MarkBillingInflightAttemptNoCharge(c.Request.Context())
					}
					if failoverClientGone(c) {
						reqLog.Info("openai_chat_completions.failover_aborted_client_disconnected",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						return terminal()
					}
					if !openAIForwardMayFailover(c, writerSizeBeforeForward, failoverErr) {
						// 真实内容已经写给客户端：不能换号也不能换组，原样收尾（outputCommitted）。
						args.output.MarkContentStarted()
						h.handleFailoverExhausted(c, failoverErr, true)
						return terminal()
					}
					// 与 Responses 入口一致：只写过心跳或 SafeToFailoverAfterWrite 的 failover 已提交
					// 200 SSE，耗尽时走流内 error 事件。
					if failoverErr.SafeToFailoverAfterWrite && c.Writer.Written() {
						*r.streamStarted = true
					}
					if openAIForwardWroteKeepaliveOnly(c, writerSizeBeforeForward) {
						*r.streamStarted = true
					}
					// Pool mode: retry on the same account
					if retry, canceled := waitPoolModeSameAccountRetry(c, reqLog, "openai_chat_completions.pool_mode_same_account_retry", account, failoverErr, sameAccountRetryCount); canceled {
						return terminal()
					} else if retry {
						if attemptsExhausted() {
							return failoverExhausted(failoverErr)
						}
						continue
					}
					if failoverErr.StatusCode == http.StatusTooManyRequests && !service.ShouldSwitchAccountOn429(account.ID) {
						// 弱 429：该账号近 30 秒的 429 占比不够，系统故意不换号，组内其它账号一个都没试过。
						// 这不是「整组耗尽」的证据：有链时照常回退到下一跳，但不计入熔断，免得繁忙分组被零星 429 误开。
						return failoverExhaustedBy(failoverErr, true)
					}
					h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
					h.gatewayService.RecordOpenAIAccountSwitch()
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					if switchCount >= maxAccountSwitches {
						return failoverExhausted(failoverErr)
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
						return failoverExhausted(failoverErr)
					}
					if attemptsExhausted() {
						return failoverExhausted(failoverErr)
					}
					reqLog.Warn("openai_chat_completions.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				// A partial stream may carry metered usage even when the terminal
				// event or upstream read fails. Cyber policy already records its
				// own usage above; a failover never reaches this branch.
				if result != nil && service.GetOpsCyberPolicy(c) == nil &&
					(result.PartialOutputDelivered || result.Usage != (service.OpenAIUsage{})) {
					submitChatUsage(result)
				}
				clientGone := (result != nil && result.ClientDisconnect) || failoverClientGone(c)
				if clientGone {
					return terminal()
				}
				h.gatewayService.ReportOpenAIAccountScheduleError(account.ID, err)
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureOpenAIStreamReadErrorResponse(c, err, stream())
					if !wroteFallback {
						wroteFallback = h.ensureForwardErrorResponse(c, stream())
					}
				}
				reqLog.Warn("openai_chat_completions.forward_failed",
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				)
				return terminal()
			}
		}
		if result != nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, account.GetMappedModel(r.reqModel))
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, account.GetMappedModel(r.reqModel))
		}

		submitChatUsage(result)
		reqLog.Debug("openai_chat_completions.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return finish(service.HopResult{Outcome: service.HopOutcomeDone})
	}
}

// runChatChain 是有链请求的逐跳执行：骨架（groupChainEntry）负责影子 Key、分组层 RPM、输出状态、指标；
// 这里只提供 chat/completions 自己的静态资格检查与单跳逻辑。
func (h *OpenAIGatewayHandler) runChatChain(
	c *gin.Context,
	plan *groupChainPlan,
	run *openAIChatRun,
	apiKey *service.APIKey,
	firstHopTicket *service.GroupRPMTicket,
	channelPlanFor func(groupID *int64) chatChannelPlan,
	applyForwardModel func(chatChannelPlan),
	reqModel string,
	reqStream bool,
	reqLog *zap.Logger,
	streamStarted *bool,
) {
	entry := newGroupChainEntry(plan, h.billingCacheService, apiKey, firstHopTicket, reqLog, reqModel, reqStream, func(hop service.ChainHop) string {
		groupID := hop.GroupID
		return channelPlanFor(&groupID).forwardModel
	})
	res := entry.run(c, chainHopFuncs{
		Eligible: func(hopKey *service.APIKey, info service.HopInfo) bool {
			// 静态资格（设计 3.4）：主分组那一跳与原来一致不额外检查；其余各跳（含管理员 head）模型必须在该分组开放、
			// 且有价格（未配价格的模型会按零成本放行，兜底分组不能白送）。chat/completions 没有分组级的入口闸。
			if info.Hop.RouteSource == service.RouteSourcePrimary {
				return true
			}
			return h.openAIHopStaticEligible(c.Request.Context(), hopKey, info, reqModel, channelPlanFor(hopKey.GroupID).mapping, true)
		},
		Attempt: func(_ context.Context, hopKey *service.APIKey, info service.HopInfo) service.HopResult {
			hopChannel := channelPlanFor(hopKey.GroupID)
			// 每跳重写选号用的模型：渠道映射按分组生效，上一跳的映射不能带到下一跳的选号与模型降级判定里。
			applyForwardModel(hopChannel)
			return run.attempt(chatHopArgs{
				key:      hopKey,
				info:     &info,
				channel:  hopChannel,
				output:   plan.output,
				settings: plan.settings,
			})
		},
		WriteRPMExceeded: func(err error) {
			h.writeBillingError(c, err, *streamStarted || plan.output.HeartbeatOnly())
		},
		WriteUnresolved: func() {
			markOpsRoutingCapacityLimited(c)
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", *streamStarted || plan.output.HeartbeatOnly())
		},
	})
	if hop, ok := entry.servedHop(res); ok && apiKey.GroupID != nil && hop.GroupID != *apiKey.GroupID {
		// 实际服务的分组不是主分组：把它补记到安全审计事件上（设计 Q18）；没有配置记录器时是空操作。
		h.recordSecurityAuditServedGroup(c, reqLog, hop.GroupID)
	}
}
