package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// /v1/responses 的「选号 → 等槽 → Forward → failover」单跳逻辑。
//
// 无链请求只调用一次（args.info == nil，args.key 是原始 Key），逻辑与引入回退链之前 Responses 里内联的循环逐行一致；
// 有链请求每一跳调用一次（args.info 非 nil，args.key 是该跳的影子 Key），在原有出口上多出三件事：
//   - 原来会直接写 503 / 429 / failover 耗尽错误的出口，在非末跳改为返回 FallbackWorthy 且不写响应（错误交给 runner 暂存）；
//   - 选号 ctx 打上「补试整组饱和」标记、等槽按 hop 策略短等并最多重选一次（openAIHopSlotPolicy）；
//   - 本跳的换号预算取 min(现有值, MaxPerHopSwitches)，并受每请求总尝试次数约束。

// responsesChannelPlan 是某个分组下的渠道映射结果（每跳重算：渠道配置按分组生效）。
type responsesChannelPlan struct {
	mapping      service.ChannelMappingResult
	forwardBody  []byte
	forwardModel string
}

// openAIResponsesRun 是一次 /v1/responses 请求在选号 / 转发阶段需要的请求级状态（只读，除 sessionHash 的初值外不被每跳修改）。
type openAIResponsesRun struct {
	h                  *OpenAIGatewayHandler
	c                  *gin.Context
	reqLog             *zap.Logger
	subject            middleware2.AuthSubject
	subscription       *service.UserSubscription
	body               []byte
	sessionHashBody    []byte
	reqModel           string
	reqStream          bool
	previousResponseID string
	imageIntent        bool
	legacyCompact      bool
	nativeV2           bool
	requireCompact     bool
	requestPlatform    string
	sessionHash        string
	routingStart       time.Time
	streamStarted      *bool
	maxAccountSwitches int
}

// responsesHopArgs 是一次 attempt 调用的入参。
type responsesHopArgs struct {
	// key 是这一跳使用的 Key：无链为原始 Key，有链为影子 Key（Group / GroupID 是这一跳的分组）。
	key *service.APIKey
	// info 非 nil 表示有链；nil 表示无链（原路径）。
	info    *service.HopInfo
	channel responsesChannelPlan
	// output / settings 仅有链时使用。
	output   *service.OutputTracker
	settings service.GroupFallbackSettings
}

// streamNow 返回此刻写错误应按流式还是 JSON：响应头已按流式提交（含只写过心跳 / ping 的情形）都要走流式收尾。
// 闭包里调用它，保证读取的是「写错误那一刻」的状态，而不是创建闭包时的状态。
func (r *openAIResponsesRun) streamNow(output *service.OutputTracker) bool {
	return *r.streamStarted || output.HeartbeatOnly()
}

func (r *openAIResponsesRun) attempt(args responsesHopArgs) service.HopResult {
	h := r.h
	c := r.c
	reqLog := r.reqLog
	key := args.key
	info := args.info
	chained := info != nil
	channelMapping := args.channel.mapping
	forwardBody := args.channel.forwardBody
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
	firstOutputTimeoutSwitchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	var passthroughFailoverState openAIPassthroughFailoverState
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
	// 重选后没号 / 探测预算用尽 / 没号的诊断事实，见 noAccountFacts。
	noAccountFacts := func(selectionErr error) func() service.HopFailure {
		return func() service.HopFailure {
			return r.noAccountFacts(key, policy, selectionErr)
		}
	}
	failoverExhausted := func(failoverErr *service.UpstreamFailoverError) service.HopResult {
		return fail(func() service.HopFailure {
			return service.HopFailure{
				Kind:                   service.HopFailureFailoverExhausted,
				LastStatus:             failoverErr.StatusCode,
				RequestScopedTransient: failoverErr.RequestScopedTransient,
			}
		}, func() {
			h.handleFailoverExhausted(c, failoverErr, stream())
		})
	}
	// attemptsExhausted：有链时受每请求总尝试次数约束（AttemptsRemaining 为 0 视为未设置）。
	attemptsExhausted := func() bool {
		return chained && info.AttemptsRemaining > 0 && attempts >= info.AttemptsRemaining
	}

	for {
		if failoverClientGone(c) {
			return terminal()
		}
		// Select account supporting the requested model
		reqLog.Debug("openai.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			policy.selectionContext(c.Request.Context()),
			key.GroupID,
			r.previousResponseID,
			sessionHash,
			forwardModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			openAIResponsesRequiredCapabilityForRequest(r.imageIntent, r.nativeV2 || r.legacyCompact, r.requestPlatform),
			r.requireCompact,
			!r.imageIntent,
			r.requestPlatform,
		)
		if err != nil {
			if failoverClientGone(c) {
				reqLog.Info("openai.account_select_aborted_client_disconnected", zap.Error(err))
				return terminal()
			}
			reqLog.Warn("openai.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if lastFailoverErr == nil {
				// 仅 legacy 压缩端点才把选号失败解释成「无账号支持 /responses/compact」；
				// 原生 v2 的选号失败属于普通无可用账号，不应套用该文案。
				if r.legacyCompact && errors.Is(err, service.ErrNoAvailableCompactAccounts) {
					return fail(func() service.HopFailure {
						// 能力被阻断（没有账号支持 compact），不是容量问题，不计熔断。
						return service.HopFailure{Kind: service.HopFailureNoAccount, PoolHasAccounts: true, ModelSupported: true, CapabilityBlocked: true}
					}, func() {
						markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
						h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "compact_not_supported", "No available accounts support /responses/compact", stream())
					})
				}
				return fail(noAccountFacts(err), func() {
					h.respondNoAccountError(c, h.gatewayService, key, r.reqModel, r.reqModel, service.PlatformOpenAI, "Service temporarily unavailable", err, noAccountCapacityMarkIfNoAvailable, openAINoAccountResponseStreaming, stream())
				})
			}
			return failoverExhausted(lastFailoverErr)
		}
		if selection == nil || selection.Account == nil {
			return fail(noAccountFacts(nil), func() {
				h.respondNoAccountError(c, h.gatewayService, key, r.reqModel, r.reqModel, service.PlatformOpenAI, "No available accounts", nil, noAccountCapacityMarkAlways, openAINoAccountResponseStreaming, stream())
			})
		}
		if r.previousResponseID != "" && selection != nil && selection.Account != nil {
			reqLog.Debug("openai.account_selected_with_previous_response_id", zap.Int64("account_id", selection.Account.ID))
		}
		reqLog.Debug("openai.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_previous_hit", scheduleDecision.StickyPreviousHit),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		// decision 必须来自刚才同一次 Select，groupID 必须是本跳的分组（粘性绑定以分组为键）。
		accountReleaseFunc, slotStatus := h.acquireResponsesAccountSlotForHop(c, key.GroupID, sessionHash, selection, scheduleDecision, policy, r.reqStream, r.streamStarted, reqLog)
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

		// Forward request
		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(r.routingStart).Milliseconds())
		forwardStart := time.Now()
		writerSizeBeforeForward := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
		// 跨 passthrough 边界的 failover：从 Kiro 等透传账号切到 Bedrock 等非透传账号前，
		// 从不可变的 canonical forwardBody 派生本次尝试 body 并整块剔除上游私有的加密
		// reasoning item（含耦合的 id/summary），避免非透传上游 400 拒绝 Kiro reasoning 形态。
		attemptBody := h.deriveOpenAIForwardAttemptBody(reqLog, forwardBody, account, &passthroughFailoverState)
		// 在途计费预占按这一跳的有效 Key（有链时是影子 Key）估价：同一个请求的多次尝试 / 多跳复用并重设同一张预占单。
		// 预占被拒时本跳没有真正调用上游（attempts 不增加），分组层 RPM 凭据由 ReleaseHopGroupRPMIfNotServed 退回。
		if !reserveBillingInflightHTTP(c, h.gatewayService, service.BillingInflightRequest{APIKey: key, Account: account, Model: r.reqModel, Body: attemptBody, ChannelUsageFields: channelMapping.ToUsageFields(r.reqModel, "")}, accountReleaseFunc, func(status int, code, message string) {
			h.handleStreamingAwareError(c, status, code, message, stream())
		}) {
			return terminal()
		}
		attempts++
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.Forward(c.Request.Context(), c, account, attemptBody)
		}()
		cyberBlockKeyHTTP := ""
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockKeyHTTP = service.CyberSessionBlockKey(key.ID, c, r.sessionHashBody)
		}
		requestPayloadHash := service.HashUsageRequestPayload(r.body)
		h.recordCyberPolicyIfMarked(c, key, account, r.subscription, r.reqModel, err != nil, cyberBlockKeyHTTP, channelMapping.ToUsageFields(r.reqModel, ""), requestPayloadHash)
		// 上游模型不一致：先读 B（成功路径 RecordUsage 透传），再记审计行并清标（下一次尝试可重新打标）。
		upstreamResponseModel := ""
		if mark := service.GetOpsUpstreamModelMismatch(c); mark != nil {
			upstreamResponseModel = mark.ResponseModel
		}
		h.recordUpstreamModelMismatchIfMarked(c, key, account, r.subscription, r.reqModel, channelMapping.ToUsageFields(r.reqModel, ""), requestPayloadHash, r.body)
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
		// #5148 对齐：错误路径返回的部分 result（流中断前上游已计量的 usage）也要
		// 正常入账；failover 错误由上方 failover 分支 continue/return，不会调用本闭包
		//（流式路径已写出后 result 可能非 nil），不会重复计费。
		submitResponsesUsage := func(res *service.OpenAIForwardResult) {
			if res == nil {
				return
			}
			userAgent := c.GetHeader("User-Agent")
			clientIP := ip.GetClientIP(c)
			inboundEndpoint := GetInboundEndpoint(c)
			upstreamEndpoint := resolveOpenAIUpstreamEndpoint(c, account)
			cyberBlocked := service.GetOpsCyberPolicy(c) != nil
			h.submitOpenAIUsageRecordTask(c.Request.Context(), res, func(ctx context.Context) {
				if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
					Result:                res,
					APIKey:                key,
					User:                  key.User,
					Account:               account,
					Subscription:          r.subscription,
					InboundEndpoint:       inboundEndpoint,
					UpstreamEndpoint:      upstreamEndpoint,
					UserAgent:             userAgent,
					IPAddress:             clientIP,
					RequestPayloadHash:    requestPayloadHash,
					APIKeyService:         h.apiKeyService,
					ChannelUsageFields:    channelMapping.ToUsageFields(r.reqModel, res.UpstreamModel),
					CyberBlocked:          cyberBlocked,
					UpstreamResponseModel: upstreamResponseModel,
				}); err != nil {
					logger.L().With(
						zap.String("component", "handler.openai_gateway.responses"),
						zap.Int64("user_id", r.subject.UserID),
						zap.Int64("api_key_id", key.ID),
						zap.Any("group_id", key.GroupID),
						zap.String("model", r.reqModel),
						zap.Int64("account_id", account.ID),
					).Error("openai.record_usage_failed", zap.Error(err))
				}
			})
		}
		if err != nil {
			if result == nil && service.IsGrokContentPolicyRejectionError(err) {
				service.MarkBillingInflightAttemptNoCharge(c.Request.Context())
			}
			// Client went away: record the observed usage and stop; never report
			// the cancellation as an upstream failure. (Upstream spells this as two
			// identical branches; merged here with the same evaluation order.)
			if (result != nil && result.ClientDisconnect) || failoverClientGone(c) {
				reqLog.Info("openai.client_disconnected",
					zap.Int64("account_id", account.ID),
					zap.Error(err),
				)
				submitResponsesUsage(result)
				return terminal()
			}
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai.forward_partial_error_with_image_result",
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
						reqLog.Info("openai.failover_aborted_client_disconnected",
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
					if failoverErr.SafeToFailoverAfterWrite && c.Writer.Written() {
						*r.streamStarted = true
					}
					// 只写过 SSE 心跳字节（Size 扣除心跳后仍等于转发前）也已把响应头提交为 200：
					// 后续耗尽必须在同一连接内以 response.failed 收尾，不能再写 JSON 错误体。
					if openAIForwardWroteKeepaliveOnly(c, writerSizeBeforeForward) {
						*r.streamStarted = true
					}
					// 池模式：同账号重试
					if retry, canceled := waitPoolModeSameAccountRetry(c, reqLog, "openai.pool_mode_same_account_retry", account, failoverErr, sameAccountRetryCount); canceled {
						return terminal()
					} else if retry {
						if attemptsExhausted() {
							return failoverExhausted(failoverErr)
						}
						continue
					}
					if failoverErr.StatusCode == http.StatusTooManyRequests && !service.ShouldSwitchAccountOn429(account.ID) {
						return failoverExhausted(failoverErr)
					}
					if openAIFirstOutputFailoverExhausted(failoverErr, &firstOutputTimeoutSwitchCount) {
						return failoverExhausted(failoverErr)
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
					reqLog.With(appendOpenAIProxyLogFields(account)...).Warn("openai.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				h.gatewayService.ReportOpenAIAccountScheduleError(account.ID, err)
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureForwardErrorResponse(c, *r.streamStarted)
				}
				fields := []zap.Field{
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				}
				submitResponsesUsage(result)
				if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
					reqLog.Warn("openai.forward_failed", fields...)
					return terminal()
				}
				reqLog.Error("openai.forward_failed", fields...)
				return terminal()
			}
		}
		if result != nil {
			if account.Type == service.AccountTypeOAuth {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, account.GetMappedModel(r.reqModel))
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, account.GetMappedModel(r.reqModel))
		}

		// 使用量记录通过有界 worker 池提交，避免请求热路径创建无界 goroutine。
		submitResponsesUsage(result)
		reqLog.Debug("openai.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return finish(service.HopResult{Outcome: service.HopOutcomeDone})
	}
}

// noAccountFacts 把一次「没有拿到号」的选号失败描述成 ClassifyHopFailure 的输入（仅有链时调用）。
//   - 本跳已经组内重选过一次（policy.ReselectUsed）：忙号被排除后剩下的是「没号」，本质是繁忙，按繁忙分类、不计熔断（规则 1）；
//   - 选号探测预算用尽（IsOpenAISelectionBudgetExhausted，大分组 DB 复核预算耗尽）：同样是繁忙，不计熔断（S-3）；
//   - 不是「没有可用账号」类的错误（仓储 / 快照故障等）：不可回退，按原逻辑写 503；
//   - 其余：用与 resolveNoAccountError 同源的诊断给出「组内有账号 / 支持该模型」，并把图片意图、压缩请求这类
//     能力受限的请求标成 CapabilityBlocked，避免把「没有账号具备这项能力」记成分组故障。
func (r *openAIResponsesRun) noAccountFacts(key *service.APIKey, policy *openAIHopSlotPolicy, selectionErr error) service.HopFailure {
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
		CapabilityBlocked: r.imageIntent || r.legacyCompact || r.nativeV2 || errors.Is(selectionErr, service.ErrNoAvailableCompactAccounts),
	}
	if r.h != nil && r.h.gatewayService != nil && key != nil {
		diag := r.h.gatewayService.DiagnoseModelAvailabilityForPlatform(r.c.Request.Context(), key.GroupID, r.reqModel, service.PlatformFromAPIKey(key))
		f.PoolHasAccounts = diag.HasAccountsInPool
		f.ModelSupported = diag.HasModelSupport
	}
	return f
}

// writeBillingError 按 billingErrorDetails 写出计费 / RPM 类错误（含 Retry-After），streamStarted 决定用流式还是 JSON 格式。
// 入口准入与逐跳的分组层 RPM 超限共用这一个出口。
func (h *OpenAIGatewayHandler) writeBillingError(c *gin.Context, err error, streamStarted bool) {
	status, code, message, retryAfter := billingErrorDetails(err)
	if retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
	}
	h.handleStreamingAwareError(c, status, code, message, streamStarted)
}
