//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClientRestrictedSelectionState_ReleaseClearsOwnershipBeforeCallback(t *testing.T) {
	calls := 0
	selection := &service.AccountSelectionResult{Acquired: true}
	selection.ReleaseFunc = func() {
		calls++
		require.False(t, selection.Acquired)
		require.Nil(t, selection.ReleaseFunc)
		releaseOpenAIClientPolicySelection(selection)
	}
	releaseOpenAIClientPolicySelection(selection)
	releaseOpenAIClientPolicySelection(selection)
	require.Equal(t, 1, calls)
}

func TestClientRestrictedSelectionState_CompatibleBusyAndBudgetAreNotAllRestricted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	h := &OpenAIGatewayHandler{}
	s := &openAIClientRestrictionSelection{pending: true, policyExcluded: true, lastAccountID: 11}
	require.False(t, s.rejectExhausted(h, c, service.NewOpenAISelectionBudgetExhaustedErrorForTest("gpt-5.4"), false))
	require.False(t, s.rejectExhausted(h, c, errors.New("snapshot unavailable"), false))
	require.False(t, c.Writer.Written())
	s.exclude(&service.OpenAIGatewayService{}, c, &service.AccountSelectionResult{Account: &service.Account{ID: 12, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}, map[int64]struct{}{})
	require.False(t, s.pending)
	require.True(t, s.policyExcluded, "compatible selection clears immediate denial but not durable whole-group evidence")
	require.False(t, s.rejectExhausted(h, c, service.ErrNoAvailableAccounts, false))
}

func TestClientRestrictedSelectionState_StableDecisionKeepsNewAccountAndServedPricing(t *testing.T) {
	home := int64(1)
	price := 3.0
	s := &openAIClientRestrictionSelection{}
	s.pinStableGroup(&home, service.OpenAIAccountScheduleDecision{
		StablePriorityState: service.StablePriorityModeFallback, StablePriorityFallback: true, StablePriorityReverted: true,
		StableServedGroupID: 2, StableServedRateMultiplier: 2.5,
		StableServedImageRateIndependent: true, StableServedImageRateMultiplier: 4,
		StableServedImagePrice1K: &price, StableServedImagePrice2K: &price, StableServedImagePrice4K: &price,
	})
	require.EqualValues(t, 2, *s.pinnedGroupID)
	d := service.OpenAIAccountScheduleDecision{SelectedAccountID: 22, Layer: "actual-new-selection", CandidateCount: 4}
	s.preserveStableDecision(&d)
	require.EqualValues(t, 22, d.SelectedAccountID)
	require.Equal(t, "actual-new-selection", d.Layer)
	require.Equal(t, 4, d.CandidateCount)
	require.Equal(t, service.StablePriorityModeFallback, d.StablePriorityState)
	require.True(t, d.StablePriorityFallback)
	require.True(t, d.StablePriorityReverted)
	require.EqualValues(t, 2, d.StableServedGroupID)
	require.Equal(t, 2.5, d.StableServedRateMultiplier)
	require.True(t, d.StableServedImageRateIndependent)
	require.Equal(t, 4.0, d.StableServedImageRateMultiplier)
	require.Equal(t, &price, d.StableServedImagePrice1K)
	require.Equal(t, &price, d.StableServedImagePrice2K)
	require.Equal(t, &price, d.StableServedImagePrice4K)
}

func TestClientRestrictedSelectionState_NilSelectionDoesNotCreateOwnershipOrPolicyEvidence(t *testing.T) {
	// Defensive empty results have no owned callback to release and must not be
	// classified as an incompatible account or alter the caller's exclusions.
	releaseOpenAIClientPolicySelection(nil)
	s := &openAIClientRestrictionSelection{}
	excluded := map[int64]struct{}{99: {}}
	selection := &service.AccountSelectionResult{Acquired: true}
	require.False(t, s.exclude(nil, nil, selection, excluded))
	require.False(t, s.exclude(&service.OpenAIGatewayService{}, nil, nil, excluded))
	require.False(t, s.exclude(&service.OpenAIGatewayService{}, nil, selection, excluded))
	require.True(t, selection.Acquired)
	require.Equal(t, map[int64]struct{}{99: {}}, excluded)
	require.False(t, s.pending)
	require.False(t, s.policyExcluded)
}
