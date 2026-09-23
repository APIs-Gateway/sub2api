//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// failingModelRateLimitRepo makes SetModelRateLimit fail so the
// capability-loss handler's repository-error branch is exercised.
type failingModelRateLimitRepo struct {
	accountRepoStub
	calls int
}

func (r *failingModelRateLimitRepo) SetModelRateLimit(_ context.Context, _ int64, _ string, _ time.Time, _ ...string) error {
	r.calls++
	return errors.New("db unavailable")
}

var openAIImageCapabilityLossTestBody = []byte(`{"error":{"message":"Tool choice 'image_generation' not found in 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`)

func TestOpenAIImagesSelfBuiltRequestMarker(t *testing.T) {
	// The helpers tolerate a nil context; use a typed variable rather than a
	// literal nil so staticcheck (SA1012) stays quiet.
	var nilCtx context.Context
	require.False(t, isOpenAIImagesSelfBuiltRequest(nilCtx))
	require.False(t, isOpenAIImagesSelfBuiltRequest(context.Background()))
	require.True(t, isOpenAIImagesSelfBuiltRequest(withOpenAIImagesSelfBuiltRequest(context.Background())))
	require.True(t, isOpenAIImagesSelfBuiltRequest(withOpenAIImagesSelfBuiltRequest(nilCtx)))
}

func TestRateLimitServiceHandleOpenAIImageCapabilityLoss_NilGuards(t *testing.T) {
	account := &Account{ID: 301, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	var nilSvc *RateLimitService
	require.False(t, nilSvc.HandleOpenAIImageCapabilityLoss(context.Background(), account, http.StatusBadRequest, openAIImageCapabilityLossTestBody))
	require.False(t, (&RateLimitService{accountRepo: &modelNotFoundAccountRepoStub{}}).HandleOpenAIImageCapabilityLoss(context.Background(), nil, http.StatusBadRequest, openAIImageCapabilityLossTestBody))
	require.False(t, (&RateLimitService{}).HandleOpenAIImageCapabilityLoss(context.Background(), account, http.StatusBadRequest, openAIImageCapabilityLossTestBody))
}

func TestRateLimitServiceHandleOpenAIImageCapabilityLoss_RepoErrorStillHandled(t *testing.T) {
	repo := &failingModelRateLimitRepo{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 302, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	handled := svc.HandleOpenAIImageCapabilityLoss(context.Background(), account, http.StatusBadRequest, openAIImageCapabilityLossTestBody)

	require.True(t, handled, "a capability loss is still recognised even if persisting the cooldown fails")
	require.Equal(t, 1, repo.calls)
}

func TestOpenAIGatewayServiceHandleUpstreamError_SelfBuiltCapabilityLossWithoutRateLimitService(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 303, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := withOpenAIImagesSelfBuiltRequest(context.Background())

	disabled := svc.handleOpenAIAccountUpstreamError(ctx, account, http.StatusBadRequest, http.Header{}, openAIImageCapabilityLossTestBody, "gpt-image-2")

	require.False(t, disabled)
	_, wholeAccountBlocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, wholeAccountBlocked)
}

func TestOpenAIGatewayServiceHandleUpstreamError_SelfBuiltCapabilityLossCoolsImageScope(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := &Account{ID: 304, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := withOpenAIImagesSelfBuiltRequest(context.Background())

	disabled := svc.handleOpenAIAccountUpstreamError(ctx, account, http.StatusBadRequest, http.Header{}, openAIImageCapabilityLossTestBody, "gpt-image-2")

	require.False(t, disabled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, openAIImageGenerationRateLimitKey, repo.modelRateLimitCalls[0].scope)
	require.Equal(t, openAIImageCapabilityLossReason, repo.modelRateLimitCalls[0].reason)
}

func TestShouldCoolOpenAIImagesToolForError_NilAndStructured(t *testing.T) {
	require.False(t, shouldCoolOpenAIImagesToolForError(nil))
	require.True(t, shouldCoolOpenAIImagesToolForError(&OpenAIImagesUpstreamError{Code: "image_generation_unavailable"}))
	require.False(t, shouldCoolOpenAIImagesToolForError(&OpenAIImagesUpstreamError{Code: "image_generation_unavailable", SynthesizedFromModelText: true}))
}
