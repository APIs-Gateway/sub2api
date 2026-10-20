//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rateLimit429AccountRepoStub struct {
	mockAccountRepoForGemini
	rateLimitCalls     int
	lastRateLimitID    int64
	lastRateLimitReset time.Time
	setRateLimitedErr  error
	tempUnschedCalls   int
	lastTempUnschedID  int64
	lastTempReason     string
	tempUnschedErr     error
}

func (r *rateLimit429AccountRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return r.setRateLimitedErr
}

func (r *rateLimit429AccountRepoStub) SetTempUnschedulable(_ context.Context, id int64, _ time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempReason = reason
	return r.tempUnschedErr
}

func TestGetRateLimit429CooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 5, settings.CooldownSeconds)
}

func TestGetRateLimit429CooldownSettings_ReadsFromDB(t *testing.T) {
	repo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	repo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 12, settings.CooldownSeconds)
}

func TestSetRateLimit429CooldownSettings_EnabledRejectsOutOfRange(t *testing.T) {
	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	for _, seconds := range []int{0, -1, 7201, 99999} {
		err := svc.SetRateLimit429CooldownSettings(context.Background(), &RateLimit429CooldownSettings{
			Enabled: true, CooldownSeconds: seconds,
		})
		require.Error(t, err, "should reject enabled=true + cooldown_seconds=%d", seconds)
		require.Contains(t, err.Error(), "cooldown_seconds must be between 1-7200")
	}
}

func TestHandle429_FallbackBelowThresholdSkipsLocalMark(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Zero(t, accountRepo.rateLimitCalls)
	require.False(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_RetryAfterBypassesThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 56, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{"Retry-After": []string{"9"}}

	recordUpstream429Attempt(account.ID)
	before := time.Now()
	svc.handle429(context.Background(), account, headers, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, int64(56), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(9*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(9*time.Second)))
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_RetryAfterSetRateLimitedErrorStillSeedsSwitch(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{setRateLimitedErr: errors.New("db unavailable")}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 60, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{"Retry-After": []string{"9"}}

	recordUpstream429Attempt(account.ID)
	svc.handle429(context.Background(), account, headers, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, account.ID, accountRepo.lastRateLimitID)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_ResetHeaderParseFailureBelowThresholdSkipsFallback(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 61, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", "not-a-unix-time")

	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	svc.handle429(context.Background(), account, headers, []byte(`{"error":{"message":"slow down"}}`))

	require.Zero(t, accountRepo.rateLimitCalls)
	require.False(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_AnthropicWindowBypassesThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 63, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt.Unix(), 10))

	recordUpstream429Attempt(account.ID)
	svc.handle429(context.Background(), account, headers, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, resetAt, accountRepo.lastRateLimitReset)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_GeminiBodyResetBypassesThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 64, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	recordUpstream429Attempt(account.ID)
	svc.handle429(context.Background(), account, http.Header{}, buildGeminiRateLimitBody("7s"))

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, account.ID, accountRepo.lastRateLimitID)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_UnifiedResetBypassesThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 65, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(resetAt.Unix(), 10))

	recordUpstream429Attempt(account.ID)
	svc.handle429(context.Background(), account, headers, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, resetAt, accountRepo.lastRateLimitReset)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestParseUpstreamRetryAfterResetTime(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	require.Nil(t, parseUpstreamRetryAfterResetTime(nil, now))
	require.Nil(t, parseUpstreamRetryAfterResetTime(http.Header{"Retry-After": []string{"0"}}, now))
	require.Nil(t, parseUpstreamRetryAfterResetTime(http.Header{"Retry-After": []string{"-1"}}, now))
	require.Nil(t, parseUpstreamRetryAfterResetTime(http.Header{"Retry-After": []string{"not a date"}}, now))
	require.Nil(t, parseUpstreamRetryAfterResetTime(http.Header{"Retry-After": []string{now.Add(-time.Minute).Format(http.TimeFormat)}}, now))

	got := parseUpstreamRetryAfterResetTime(http.Header{"Retry-After": []string{now.Add(2 * time.Minute).Format(http.TimeFormat)}}, now)
	require.NotNil(t, got)
	require.Equal(t, now.Add(2*time.Minute).Unix(), got.Unix())
}

func TestGatewayRetry429SideEffects(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	rateLimitService := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	gatewayService := &GatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 62, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"4"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)),
	}
	gatewayService.handleRetryExhaustedSideEffects(context.Background(), resp, account)

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.True(t, ShouldSwitchAccountOn429(account.ID))

	var nilGateway *GatewayService
	nilGateway.handleRetryable429SideEffects(context.Background(), account, http.StatusTooManyRequests, nil, nil)
	gatewayService.handleRetryable429SideEffects(context.Background(), nil, http.StatusTooManyRequests, nil, nil)
	gatewayService.handleRetryable429SideEffects(context.Background(), account, http.StatusInternalServerError, nil, nil)
	gatewayService.handleRetryable429SideEffects(context.Background(), account, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"5"}}, nil, "gpt-4.1")

	require.Equal(t, 2, accountRepo.rateLimitCalls)
}

func TestHandleUpstreamError_429IgnoresCustomErrorCodeSkip(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       59,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusInternalServerError)},
		},
	}
	headers := http.Header{"Retry-After": []string{"6"}}

	recordUpstream429Attempt(account.ID)
	disabled := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.False(t, disabled)
	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, account.ID, accountRepo.lastRateLimitID)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_FallbackUsesDBSecondsAfterThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}
	require.Zero(t, accountRepo.rateLimitCalls)

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, int64(42), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(12*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(12*time.Second)))
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandleUpstreamError_PoolMode429SeedsThresholdDecision(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       54,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	for i := 0; i < upstream429MinAttempts-1; i++ {
		recordUpstream429Attempt(account.ID)
		require.False(t, svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)))
	}
	require.False(t, ShouldSwitchAccountOn429(account.ID))

	recordUpstream429Attempt(account.ID)
	require.False(t, svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)))
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandleUpstreamError_PoolMode429TempUnschedWaitsForThreshold(t *testing.T) {
	body := []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	newPoolAccount := func(id int64) *Account {
		return &Account{
			ID:       id,
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusTooManyRequests),
						"keywords":         []any{"slow down"},
						"duration_minutes": float64(10),
					},
				},
			},
		}
	}

	resetUpstream429TrackerForTest()
	repo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	lowRatioAccount := newPoolAccount(57)
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(lowRatioAccount.ID)
	}

	disabled := svc.HandleUpstreamError(context.Background(), lowRatioAccount, http.StatusTooManyRequests, http.Header{}, body)

	require.False(t, disabled)
	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, ShouldSwitchAccountOn429(lowRatioAccount.ID))

	resetUpstream429TrackerForTest()
	repo = &rateLimit429AccountRepoStub{}
	svc = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	thresholdAccount := newPoolAccount(58)
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(thresholdAccount.ID)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		disabled = svc.HandleUpstreamError(context.Background(), thresholdAccount, http.StatusTooManyRequests, http.Header{}, body)
		require.False(t, disabled)
		require.Zero(t, repo.tempUnschedCalls)
	}

	disabled = svc.HandleUpstreamError(context.Background(), thresholdAccount, http.StatusTooManyRequests, http.Header{}, body)

	require.True(t, disabled)
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, thresholdAccount.ID, repo.lastTempUnschedID)
	require.True(t, ShouldSwitchAccountOn429(thresholdAccount.ID))
}

func TestUpstream429StrongDecisionIsNotDowngradedByWeakSignal(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountID := int64(47)

	require.True(t, recordUpstream429AndShouldSwitch(accountID, true))
	require.True(t, recordUpstream429AndShouldSwitch(accountID, false))
	require.True(t, ShouldSwitchAccountOn429(accountID))
}

func TestUpstream429RatioDecisionSurvivesSuccessfulAttemptsUntilTTL(t *testing.T) {
	tracker := newUpstream429Tracker()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	accountID := int64(53)

	for i := 0; i < upstream429MinAttempts; i++ {
		tracker.record(accountID, upstream429Attempt)
	}
	for i := 0; i < upstream429MinAttempts/2; i++ {
		require.Equal(t, i == upstream429MinAttempts/2-1, tracker.record429(accountID, false))
	}
	require.True(t, tracker.shouldSwitch(accountID))

	for i := 0; i < upstream429MinAttempts; i++ {
		tracker.record(accountID, upstream429Attempt)
	}
	require.True(t, tracker.shouldSwitch(accountID))

	now = now.Add(upstream429SwitchDecisionTTL + time.Millisecond)
	require.False(t, tracker.shouldSwitch(accountID))
}

func TestUpstream429RatioUsesRecentEvents(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountID := int64(48)

	for i := 0; i < upstream429MaxEventsPerAccount; i++ {
		recordUpstream429Attempt(accountID)
	}
	for i := 0; i < upstream429MinAttempts/2; i++ {
		recordUpstream429Attempt(accountID)
		require.False(t, recordUpstream429AndShouldSwitch(accountID, false))
	}

	allowed := false
	for i := upstream429MinAttempts / 2; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(accountID)
		allowed = recordUpstream429AndShouldSwitch(accountID, false)
		if allowed {
			break
		}
	}
	require.True(t, allowed)
	require.True(t, ShouldSwitchAccountOn429(accountID))
}

func TestUpstream429RatioUsesRecentAttemptsNotEvents(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountID := int64(52)

	for i := 0; i < upstream429MinAttempts+2; i++ {
		recordUpstream429Attempt(accountID)
	}
	for i := 0; i < upstream429MinAttempts-2; i++ {
		recordUpstream429Attempt(accountID)
		require.False(t, recordUpstream429AndShouldSwitch(accountID, false))
	}
	require.False(t, ShouldSwitchAccountOn429(accountID))

	recordUpstream429Attempt(accountID)
	require.False(t, recordUpstream429AndShouldSwitch(accountID, false))
	recordUpstream429Attempt(accountID)
	require.True(t, recordUpstream429AndShouldSwitch(accountID, false))
	require.True(t, ShouldSwitchAccountOn429(accountID))
}

func TestSelectionWaitPlanDoesNotRecord429Attempt(t *testing.T) {
	resetUpstream429TrackerForTest()
	gatewayService := &GatewayService{}
	account := &Account{ID: 49, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	selection, err := gatewayService.newSelectionResult(context.Background(), account, false, nil, &AccountWaitPlan{
		AccountID:      account.ID,
		MaxConcurrency: 1,
		Timeout:        time.Second,
		MaxWaiting:     1,
	})
	require.NoError(t, err)
	require.Equal(t, account.ID, selection.Account.ID)

	attempts, hits := countUpstream429EventsForTest(account.ID)
	require.Zero(t, attempts)
	require.Zero(t, hits)
}

func TestOpenAIForwardRecords429AttemptForThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	gin.SetMode(gin.TestMode)
	accountRepo := &rateLimit429AccountRepoStub{}
	rateLimitService := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)

	responses := make([]*http.Response, 0, upstream429MinAttempts)
	for i := 0; i < upstream429MinAttempts; i++ {
		responses = append(responses, &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)),
		})
	}
	upstream := &httpUpstreamRecorder{responses: responses}
	gatewayService := &OpenAIGatewayService{rateLimitService: rateLimitService, httpUpstream: upstream}
	account := openAIFailoverCachedBodyTestAccount(50, "account-a", nil)
	body := []byte(`{"model":"gpt-4.1","stream":false,"instructions":"threshold-test","input":"hello"}`)

	for i := 0; i < upstream429MinAttempts; i++ {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		_, err := gatewayService.Forward(context.Background(), c, account, body)
		require.Error(t, err)
		var failoverErr *UpstreamFailoverError
		require.True(t, errors.As(err, &failoverErr))
	}

	attempts, hits := countUpstream429EventsForTest(account.ID)
	require.Equal(t, upstream429MinAttempts, attempts)
	require.Equal(t, upstream429MinAttempts, hits)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestGeminiForwardCountsFinalWeak429Once(t *testing.T) {
	resetUpstream429TrackerForTest()
	gin.SetMode(gin.TestMode)

	responses := make([]*http.Response, 0, geminiMaxRetries)
	for i := 0; i < geminiMaxRetries; i++ {
		responses = append(responses, &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`)),
		})
	}
	upstream := &httpUpstreamRecorder{responses: responses}
	svc := &GeminiMessagesCompatService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       52,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
		},
		Concurrency: 1,
	}
	body := []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	result, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Nil(t, result)

	attempts, hits := countUpstream429EventsForTest(account.ID)
	require.Equal(t, geminiMaxRetries, attempts)
	require.Equal(t, geminiMaxRetries, hits)
	require.False(t, ShouldSwitchAccountOn429(account.ID))
}

func TestSelectionAndLocalGeminiParseDoNotRecordUpstream429Attempt(t *testing.T) {
	resetUpstream429TrackerForTest()
	gin.SetMode(gin.TestMode)
	gatewayService := &GatewayService{}
	geminiService := &GeminiMessagesCompatService{}
	account := &Account{ID: 51, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	selection, err := gatewayService.newSelectionResult(context.Background(), account, true, nil, nil)
	require.NoError(t, err)
	require.Equal(t, account.ID, selection.Account.ID)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	_, err = geminiService.Forward(context.Background(), c, account, []byte(`{}`))
	require.Error(t, err)

	attempts, hits := countUpstream429EventsForTest(account.ID)
	require.Zero(t, attempts)
	require.Zero(t, hits)
}

func TestHandle429_AnthropicNoResetBelowThresholdSkipsLocalMark(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)

	account := &Account{ID: 45, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Zero(t, accountRepo.rateLimitCalls)
	require.False(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_AnthropicNoResetUsesFallbackAfterThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)

	account := &Account{ID: 46, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}
	require.Zero(t, accountRepo.rateLimitCalls)

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, int64(46), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(5*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(5*time.Second)))
	require.True(t, ShouldSwitchAccountOn429(account.ID))
}

func TestHandle429_FallbackDisabledSkipsLocalMark(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	for i := 0; i < upstream429MinAttempts/2; i++ {
		svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}

	require.Zero(t, accountRepo.rateLimitCalls)
}

func TestHandle429_FallbackUsesDefaultSecondsWhenSettingServiceMissingAfterThreshold(t *testing.T) {
	resetUpstream429TrackerForTest()
	accountRepo := &rateLimit429AccountRepoStub{}
	cfg := &config.Config{}
	svc := NewRateLimitService(accountRepo, nil, cfg, nil, nil)

	account := &Account{ID: 44, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"message":"slow down"}}`))
	}
	require.Zero(t, accountRepo.rateLimitCalls)

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitCalls)
	require.Equal(t, int64(44), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(5*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(5*time.Second)))
}
