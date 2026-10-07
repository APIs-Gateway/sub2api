package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Client policy rejects a selection, not the caller's group or model permission.
// This state is local to one hop and never marks an upstream account unhealthy.
type openAIClientRestrictionSelection struct {
	pending        bool
	policyExcluded bool
	lastAccountID  int64
	lastPlatform   string
	pinnedGroupID  *int64
	stable         service.OpenAIAccountScheduleDecision
}

func releaseOpenAIClientPolicySelection(selection *service.AccountSelectionResult) {
	if selection == nil {
		return
	}
	release := selection.ReleaseFunc
	selection.ReleaseFunc = nil
	selection.Acquired = false
	if release != nil {
		release()
	}
}

func (s *openAIClientRestrictionSelection) exclude(
	gateway *service.OpenAIGatewayService, c *gin.Context,
	selection *service.AccountSelectionResult, excluded map[int64]struct{},
) bool {
	if gateway == nil || selection == nil || selection.Account == nil {
		return false
	}
	result := gateway.DetectOpenAIClientRestriction(c, selection.Account)
	if !result.Enabled || result.Matched {
		// A compatible but busy account is a capacity outcome, not proof that
		// every eligible account rejects the client.
		s.pending = false
		s.pinnedGroupID = nil
		return false
	}
	releaseOpenAIClientPolicySelection(selection)
	excluded[selection.Account.ID] = struct{}{}
	// Durable evidence: accepting a later account clears the immediate policy
	// response state, but does not turn this mixed exclusion set into evidence
	// that the entire upstream group failed. This state resets at each hop.
	s.policyExcluded = true
	s.pending = true
	s.lastAccountID = selection.Account.ID
	s.lastPlatform = selection.Account.Platform
	return true
}

func (s *openAIClientRestrictionSelection) rejectExhausted(
	h *OpenAIGatewayHandler, c *gin.Context, selectionErr error, streamStarted bool,
) bool {
	if !s.pending || service.IsOpenAISelectionBudgetExhausted(selectionErr) ||
		(selectionErr != nil && !errors.Is(selectionErr, service.ErrNoAvailableAccounts) && !errors.Is(selectionErr, service.ErrNoAvailableCompactAccounts)) {
		return false
	}
	setOpsSelectedAccount(c, s.lastAccountID, s.lastPlatform)
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	h.handleStreamingAwareError(c, http.StatusForbidden, "forbidden_error", service.CodexOfficialClientsOnlyMessage, streamStarted)
	return true
}

// The stable selector may already have legitimately chosen a served group.
// A client-policy exclusion must not turn its exhaustion into a new cross-group
// fallback transition. Retry through the normal public selector in that same
// group, preserving its stable billing decision and the original routing ctx.
func (s *openAIClientRestrictionSelection) pinStableGroup(home *int64, decision service.OpenAIAccountScheduleDecision) {
	if s.pinnedGroupID != nil {
		return
	}
	groupID := int64(0)
	if home != nil {
		groupID = *home
	}
	if decision.StableServedGroupID > 0 {
		groupID = decision.StableServedGroupID
	}
	if groupID > 0 {
		s.pinnedGroupID = &groupID
	}
	s.stable = decision
}

func (s *openAIClientRestrictionSelection) preserveStableDecision(decision *service.OpenAIAccountScheduleDecision) {
	decision.StablePriorityState = s.stable.StablePriorityState
	decision.StablePriorityFallback = s.stable.StablePriorityFallback
	decision.StablePriorityReverted = s.stable.StablePriorityReverted
	decision.StableServedGroupID = s.stable.StableServedGroupID
	decision.StableServedRateMultiplier = s.stable.StableServedRateMultiplier
	decision.StableServedImageRateIndependent = s.stable.StableServedImageRateIndependent
	decision.StableServedImageRateMultiplier = s.stable.StableServedImageRateMultiplier
	decision.StableServedImagePrice1K = s.stable.StableServedImagePrice1K
	decision.StableServedImagePrice2K = s.stable.StableServedImagePrice2K
	decision.StableServedImagePrice4K = s.stable.StableServedImagePrice4K
}
