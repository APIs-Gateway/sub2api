package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ResponsesInputTokens retains authentication, moderation, eligibility and
// routing policy, but never reserves funds or enters generation/usage recording.
func (h *OpenAIGatewayHandler) ResponsesInputTokens(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	setOpenAIClientTransportHTTP(c)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	platform := apiKey.Group.Platform
	if platform != service.PlatformOpenAI && platform != service.PlatformGrok {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Input token counting is not supported for this platform")
		return
	}
	h.applyOpenAIForcedAccountRouting(c, apiKey)
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(c, "handler.openai_gateway.input_tokens", zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", apiKey.ID))
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
		} else {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		}
		return
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object")
		return
	}
	model := gjson.GetBytes(body, "model")
	if _, err := service.ResponsesInputTokensModel(body); err != nil || model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	body = service.TrimRequestBodyModel(body)
	requestedModel := strings.TrimSpace(model.String())
	setOpsRequestContext(c, requestedModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))
	plan := h.resolveOpenAIChainPlan(c, reqLog, apiKey)
	var decision *securityaudit.Decision
	if plan == nil {
		decision = h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, requestedModel, body)
	} else {
		decision = h.checkSecurityAuditForChain(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, requestedModel, body, plan.Hops())
	}
	if decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	var ticket *service.GroupRPMTicket
	if plan == nil {
		err = h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey))
	} else {
		ticket, err = h.billingCacheService.CheckBillingEligibilityForChain(c.Request.Context(), apiKey.User, apiKey, plan.FirstHop().Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey))
	}
	writeBillingError := func(err error) {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
	}
	if err != nil {
		writeBillingError(err)
		return
	}
	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	if h.rejectIfCyberSessionBlocked(c, apiKey, body, requestedModel, cyberBlockFormatResponses) {
		return
	}
	channelFor := func(key *service.APIKey) responsesChannelPlan {
		mapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), key.GroupID, requestedModel)
		return responsesChannelPlan{mapping: mapping, forwardBody: openAIModelMappedBody(body, mapping.Mapped, mapping.MappedModel, h.gatewayService.ReplaceModelInBody), forwardModel: openAIChannelForwardModel(mapping, requestedModel)}
	}
	attempt := func(key *service.APIKey, info *service.HopInfo) service.HopResult {
		channel := channelFor(key)
		c.Request = c.Request.WithContext(service.WithOpenAIForwardModel(c.Request.Context(), channel.forwardModel, false))
		var policy *openAIHopSlotPolicy
		if info != nil {
			policy = newOpenAIHopSlotPolicy(*info, plan.settings, time.Now())
		}
		fail := func(facts service.HopFailure, write func()) service.HopResult {
			if info == nil {
				write()
				return service.HopResult{Outcome: service.HopOutcomeTerminal}
			}
			result := service.ClassifyHopFailure(facts)
			if result.Outcome == service.HopOutcomeFallbackWorthy && info.DeferFinalError {
				result.WriteFinalError = write
			} else {
				write()
				result.ErrorWritten = true
			}
			return result
		}
		excluded := make(map[int64]struct{})
		for {
			if failoverClientGone(c) {
				return service.HopResult{Outcome: service.HopOutcomeTerminal}
			}
			selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(policy.selectionContext(c.Request.Context()), key.GroupID,
				strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()), sessionHash, channel.forwardModel, excluded,
				service.OpenAIUpstreamTransportAny, service.OpenAIEndpointCapabilityChatCompletions, false, false, openAICompatibleRequestPlatform(key))
			if err != nil || selection == nil || selection.Account == nil {
				facts := service.HopFailure{}
				if info != nil {
					facts = h.openAINoAccountFacts(c, key, requestedModel, policy, err, false)
				}
				return fail(facts, func() {
					h.respondNoAccountError(c, h.gatewayService, key, requestedModel, channel.forwardModel, openAICompatibleRequestPlatform(key), "Service temporarily unavailable", err, noAccountCapacityMarkIfNoAvailable, openAINoAccountResponseStreaming, false)
				})
			}
			account := selection.Account
			setOpsSelectedAccount(c, account.ID, account.Platform)
			release, status := h.acquireResponsesAccountSlotForHop(c, key.GroupID, sessionHash, selection, scheduleDecision, policy, false, &streamStarted, reqLog)
			if status == accountSlotRetrySelection {
				excluded[account.ID] = struct{}{}
				continue
			}
			if status == accountSlotBusyDeferred {
				return fail(service.HopFailure{Kind: service.HopFailureBusyTimeout}, func() { h.writeDeferredAccountSlotFailure(c, policy, false) })
			}
			if status != accountSlotAcquired {
				return service.HopResult{Outcome: service.HopOutcomeTerminal}
			}
			err = func() error {
				if release != nil {
					defer release()
				}
				return h.gatewayService.ForwardResponsesInputTokens(c.Request.Context(), c, account, channel.forwardBody)
			}()
			result := service.HopResult{Outcome: service.HopOutcomeDone, UpstreamAttempted: service.ResponsesInputTokensAttemptedUpstream(c)}
			if result.UpstreamAttempted {
				result.Attempts = 1
			}
			if err != nil {
				reqLog.Warn("openai.input_tokens_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				result.Outcome = service.HopOutcomeTerminal
			}
			return result
		}
	}
	if plan == nil {
		attempt(apiKey, nil)
		return
	}
	entry := newGroupChainEntry(plan, h.billingCacheService, apiKey, ticket, reqLog, requestedModel, false, nil)
	result := entry.run(c, chainHopFuncs{
		Eligible: func(key *service.APIKey, info service.HopInfo) bool {
			return h.openAIHopStaticEligible(c.Request.Context(), key, info, requestedModel, channelFor(key).mapping, false)
		},
		Attempt: func(_ context.Context, key *service.APIKey, info service.HopInfo) service.HopResult {
			return attempt(key, &info)
		},
		WriteRPMExceeded: writeBillingError,
		WriteUnresolved: func() {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable")
		},
	})
	if hop, ok := entry.servedHop(result); ok && apiKey.GroupID != nil && hop.GroupID != *apiKey.GroupID {
		h.recordSecurityAuditServedGroup(c, reqLog, hop.GroupID)
	}
}
