//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestShouldCooldownOpenAITransientUpstreamError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{"500 is transient", http.StatusInternalServerError, nil, true},
		{"502 is transient", http.StatusBadGateway, nil, true},
		{"503 is transient", http.StatusServiceUnavailable, nil, true},
		{"504 is transient", http.StatusGatewayTimeout, nil, true},
		{"cloudflare 524 is transient", 524, nil, true},
		{"400 processing error is transient", http.StatusBadRequest, []byte(`{"error":{"message":"An error occurred while processing your request. You can retry your request, and if the issue persists, please contact us through our help center at help.openai.com and include the request ID"}}`), true},
		{"400 without a known marker is not transient", http.StatusBadRequest, []byte(`{"error":{"message":"invalid request"}}`), false},
		{"404 is not transient", http.StatusNotFound, nil, false},
		{"401 is not transient", http.StatusUnauthorized, nil, false},
		{"429 is not transient", http.StatusTooManyRequests, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldCooldownOpenAITransientUpstreamError(tc.statusCode, tc.body))
		})
	}
}

func TestHandleOpenAIAccountUpstreamError_TransientCooldownIsModelScopedNotAccountWide(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 91101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"service unavailable"}}`)

	// First transient failure on a model: recorded, but not yet cooled down.
	shouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	require.False(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	// Second consecutive transient failure on the same model: model-scoped
	// cooldown kicks in, but the account itself must stay schedulable for
	// every other model — this is the whole point of the upstream fix.
	shouldDisable = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	require.False(t, shouldDisable)
	require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestHandleOpenAIAccountUpstreamError_TransientCooldownSkipsOAuthAccounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 91102, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"message":"service unavailable"}}`)

	for i := 0; i < 3; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	}

	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
}

func TestOpenAIAccountScheduler_SkipsAccountBlockedForRequestedModel(t *testing.T) {
	now := time.Now()
	account := &Account{ID: 91103, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(128)}
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now)
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now.Add(time.Millisecond))
	scheduler := &defaultOpenAIAccountScheduler{service: svc}

	require.False(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5"}))
	require.True(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.6-sol"}))
}

func TestCanonicalOpenAIAccountSchedulingModel_ResolvesMappedModel(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.5-alias": "gpt-5.5",
			},
		},
	}

	require.Equal(t, "gpt-5.5", canonicalOpenAIAccountSchedulingModel(account, "gpt-5.5-alias"))
	require.Equal(t, "gpt-5.6", canonicalOpenAIAccountSchedulingModel(account, "gpt-5.6"))
	require.Equal(t, "", canonicalOpenAIAccountSchedulingModel(account, ""))
	require.Equal(t, "gpt-5.5", canonicalOpenAIAccountSchedulingModel(nil, "gpt-5.5"))
}

func TestIsOpenAIAccountRequestRuntimeBlocked_NilSafe(t *testing.T) {
	var svc *OpenAIGatewayService
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(nil, "gpt-5.5"))

	svc = &OpenAIGatewayService{}
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(nil, "gpt-5.5"))
}

func TestGetOpenAIAccountModelTransientState_NilReceiver(t *testing.T) {
	var svc *OpenAIGatewayService
	require.Nil(t, svc.getOpenAIAccountModelTransientState())
}

func TestCanonicalOpenAIAccountSchedulingModel_EmptyMappedValueFallsBackToOriginal(t *testing.T) {
	// An explicit-but-empty model_mapping entry (matched=true, mappedModel="")
	// must not turn into an empty transient-cooldown key; fall back to the
	// original requested model instead.
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.5": "",
			},
		},
	}

	require.Equal(t, "gpt-5.5", canonicalOpenAIAccountSchedulingModel(account, "gpt-5.5"))
}

func TestRecordOpenAIAccountModelTransientFailure_NilSafe(t *testing.T) {
	var svc *OpenAIGatewayService
	require.Zero(t, svc.recordOpenAIAccountModelTransientFailure(&Account{ID: 1}, "gpt-5.5", time.Now()).FailureStreak)

	svc = &OpenAIGatewayService{}
	require.Zero(t, svc.recordOpenAIAccountModelTransientFailure(nil, "gpt-5.5", time.Now()).FailureStreak)
}

func TestReportOpenAIAccountScheduleResult_SuccessClearsModelTransientStreakSoNonConsecutiveFailuresDoNotEscalate(t *testing.T) {
	// Regression for the fork gap where recordSuccess was never wired into
	// the production success-reporting path: a single transient failure
	// followed by a success (e.g. many healthy requests in between) must not
	// linger in the failure window and get aggregated with a later,
	// unrelated failure into an unwarranted cooldown escalation.
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 91105, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	now := time.Now()

	// t=0: first transient failure on this model — recorded, streak=1, no cooldown yet.
	decision := svc.recordOpenAIAccountModelTransientFailure(account, "gpt-5.5", now)
	require.Equal(t, 1, decision.FailureStreak)
	require.Zero(t, decision.Cooldown)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))

	// A later request against the same model succeeds. The production call
	// site (handler layer) reports this via ReportOpenAIAccountScheduleResult
	// with the requested model, which must clear the tracked failure streak.
	svc.ReportOpenAIAccountScheduleResult(account.ID, true, nil, "gpt-5.5")

	// t=40s: a second, non-consecutive transient failure within the same 60s
	// window must restart the streak at 1 (not escalate to 2 and trigger the
	// short cooldown), because the intervening success cleared the entry.
	decision = svc.recordOpenAIAccountModelTransientFailure(account, "gpt-5.5", now.Add(40*time.Second))
	require.Equal(t, 1, decision.FailureStreak)
	require.Zero(t, decision.Cooldown)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
}

func TestReportOpenAIAccountScheduleResult_SuccessWithoutModelDoesNotTouchTransientState(t *testing.T) {
	// The model parameter is a variadic for backward compatibility with the
	// ~27 existing call sites; omitting it (as failure-reporting call sites
	// do, and as any call site not passing a model still may) must be a
	// pure no-op against the transient tracker rather than panicking or
	// clearing an unrelated entry.
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 91106, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	now := time.Now()

	svc.recordOpenAIAccountModelTransientFailure(account, "gpt-5.5", now)
	svc.recordOpenAIAccountModelTransientFailure(account, "gpt-5.5", now.Add(time.Second))
	require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))

	svc.ReportOpenAIAccountScheduleResult(account.ID, true, nil)

	require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
}

func TestRecordOpenAIAccountModelTransientFailure_NilStateAfterOnceFired(t *testing.T) {
	// Simulate the lazy-init sync.Once having already fired without ever
	// assigning openaiModelTransient (defensive scenario the code guards
	// against explicitly), so getOpenAIAccountModelTransientState returns nil
	// even though s itself is non-nil.
	svc := &OpenAIGatewayService{}
	svc.openaiModelTransientOnce.Do(func() {})
	require.Nil(t, svc.getOpenAIAccountModelTransientState())

	account := &Account{ID: 91104}
	decision := svc.recordOpenAIAccountModelTransientFailure(account, "gpt-5.5", time.Now())
	require.Zero(t, decision.FailureStreak)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
}
