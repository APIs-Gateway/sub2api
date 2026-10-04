package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ChatCompletions handles OpenAI Chat Completions API requests.
// POST /v1/chat/completions
func (h *OpenAIGatewayHandler) ChatCompletions(c *gin.Context) {
	defer finishBillingInflightHTTP(c)
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	h.applyOpenAIForcedAccountRouting(c, apiKey)

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	body = service.TrimRequestBodyModel(body)
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	reqStream, ok := parseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", invalidStreamFieldTypeMessage)
		return
	}
	if service.IsGPTImageGenerationModel(reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "This model is not supported on the Chat Completions endpoint")
		return
	}

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	setOpsRequestContext(c, reqModel, reqStream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))

	// Key 级回退链：先解析一次有效链，审核 / 审计按链上全部分组的并集审一次（审核拦截即终止，不会去试下一跳），
	// 之后逐跳迭代的就是这一份链。plan 为 nil 表示无链：审核与之后的所有步骤都走原路径。
	plan := h.resolveOpenAIChainPlan(c, reqLog, apiKey)
	var auditDecision *securityaudit.Decision
	if plan != nil {
		auditDecision = h.checkSecurityAuditForChain(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, body, plan.Hops())
	} else {
		auditDecision = h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, body)
	}
	if auditDecision != nil && !auditDecision.AllowNextStage {
		h.openAISecurityAuditError(c, auditDecision)
		return
	}
	if h.rejectIfCyberSessionBlocked(c, apiKey, body, reqModel, cyberBlockFormatChat) {
		return
	}

	// 解析渠道级模型映射。渠道配置按分组生效，所以有链时每一跳各解析一次（见 channelPlanFor），
	// 无链仍在这里解析一次，与原来一致。
	channelCache := make(map[int64]chatChannelPlan, 2)
	channelPlanFor := func(groupID *int64) chatChannelPlan {
		cacheKey := int64(0)
		if groupID != nil {
			cacheKey = *groupID
		}
		if cached, ok := channelCache[cacheKey]; ok {
			return cached
		}
		mapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), groupID, reqModel)
		resolved := chatChannelPlan{mapping: mapping, forwardModel: openAIChannelForwardModel(mapping, reqModel)}
		channelCache[cacheKey] = resolved
		return resolved
	}
	// applyForwardModel 把这一跳选号用的模型写进 ctx（每跳重算，设计 3.4 第 7 项）；
	// 模型降级判定读取的就是这个值，后写的值遮住先写的，不会把上一跳的映射带到下一跳。
	applyForwardModel := func(channel chatChannelPlan) {
		c.Request = c.Request.WithContext(service.WithModelDowngradeSelectionModel(c.Request.Context(), channel.forwardModel))
	}
	var legacyChannel chatChannelPlan
	if plan == nil {
		legacyChannel = channelPlanFor(apiKey.GroupID)
		applyForwardModel(legacyChannel)
	}

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	requestPlatform := openAICompatibleRequestPlatform(apiKey)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 有链：RPM 的顺序与无链一致（先首跳分组层、后用户层，用户层只计一次），首跳分组层的计数凭据交给逐跳循环；
	// 首跳（或用户层）超限直接 429，不回退、不退回计数。
	var billingErr error
	var firstHopTicket *service.GroupRPMTicket
	if plan != nil {
		firstHopTicket, billingErr = h.billingCacheService.CheckBillingEligibilityForChain(c.Request.Context(), apiKey.User, apiKey, plan.FirstHop().Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey))
	} else {
		billingErr = h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey))
	}
	if err := billingErr; err != nil {
		reqLog.Info("openai_chat_completions.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	promptCacheKey := h.gatewayService.ExtractSessionID(c, body)

	run := &openAIChatRun{
		h:                  h,
		c:                  c,
		reqLog:             reqLog,
		subject:            subject,
		subscription:       subscription,
		body:               body,
		reqModel:           reqModel,
		reqStream:          reqStream,
		requestPlatform:    requestPlatform,
		sessionHash:        sessionHash,
		promptCacheKey:     promptCacheKey,
		routingStart:       routingStart,
		streamStarted:      &streamStarted,
		maxAccountSwitches: h.maxAccountSwitches,
	}
	if plan == nil {
		// 无链：单次调用，所有出口都在 attempt 内部直接写响应，与引入回退链之前一致。
		run.attempt(chatHopArgs{key: apiKey, channel: legacyChannel})
		return
	}
	h.runChatChain(c, plan, run, apiKey, firstHopTicket, channelPlanFor, applyForwardModel, reqModel, reqStream, reqLog, &streamStarted)
}

func openAIStablePriorityCanFallback(apiKey *service.APIKey, intent service.StablePriorityIntent) bool {
	return intent.Enabled && apiKey != nil && apiKey.Group != nil && apiKey.Group.StablePriorityFallbackGroupID != nil
}

// resolveOpenAIUpstreamEndpoint returns the actual upstream endpoint for an
// OpenAI account, used by every OpenAI usage-recording site. APIKey accounts
// whose upstream is forced or probed to not support the Responses API are
// served directly via /v1/chat/completions (the raw chat path) regardless of
// the inbound endpoint; everything else goes through the Responses API.
func resolveOpenAIUpstreamEndpoint(c *gin.Context, account *service.Account) string {
	if account != nil && account.Type == service.AccountTypeAPIKey &&
		!openai_compat.ShouldUseResponsesAPI(account.Extra) {
		return "/v1/chat/completions"
	}
	return GetUpstreamEndpoint(c, account.Platform)
}
