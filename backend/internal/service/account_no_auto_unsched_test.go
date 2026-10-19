//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type noAutoUnschedSettingRepoStub struct {
	SettingRepository
	streamTimeout string
}

func (r *noAutoUnschedSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyStreamTimeoutSettings {
		return r.streamTimeout, nil
	}
	return "", ErrSettingNotFound
}

func newNoAutoUnschedRateLimitService(t *testing.T, action string, exemptIDs ...int64) (*RateLimitService, *rateLimitAccountRepoStub) {
	t.Helper()
	cfg := &config.Config{Gateway: config.GatewayConfig{NoAutoUnschedAccountIDs: exemptIDs}}
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, cfg, nil, nil)
	svc.SetSettingService(NewSettingService(&noAutoUnschedSettingRepoStub{
		streamTimeout: `{"enabled":true,"action":"` + action + `","temp_unsched_minutes":5,"threshold_count":1,"threshold_window_minutes":10}`,
	}, cfg))
	return svc, repo
}

func TestHandleStreamTimeout_NoAutoUnschedAccountStaysSchedulable(t *testing.T) {
	svc, repo := newNoAutoUnschedRateLimitService(t, StreamTimeoutActionTempUnsched, 3230)

	require.False(t, svc.HandleStreamTimeout(context.Background(), &Account{ID: 3230, Platform: PlatformOpenAI}, "gpt-6-luna"))
	require.Zero(t, repo.tempCalls)

	// 名单外的账号照常暂停。
	require.True(t, svc.HandleStreamTimeout(context.Background(), &Account{ID: 3231, Platform: PlatformOpenAI}, "gpt-6-luna"))
	require.Equal(t, 1, repo.tempCalls)
}

func TestHandleStreamTimeout_NoAutoUnschedAccountSkipsErrorAction(t *testing.T) {
	svc, repo := newNoAutoUnschedRateLimitService(t, StreamTimeoutActionError, 3230)

	require.False(t, svc.HandleStreamTimeout(context.Background(), &Account{ID: 3230, Platform: PlatformOpenAI}, "gpt-6-luna"))
	require.Zero(t, repo.setErrorCalls)
}

func TestHandleOpenAIAccountUpstreamError_NoAutoUnschedAccountSkipsModelCooldown(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{NoAutoUnschedAccountIDs: []int64{3230}}}
	svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: NewRateLimitService(&rateLimitAccountRepoStub{}, nil, cfg, nil, nil)}
	exempt := &Account{ID: 3230, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	other := &Account{ID: 3231, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"service unavailable"}}`)

	for i := 0; i < 3; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), exempt, http.StatusServiceUnavailable, http.Header{}, body, "gpt-6-luna")
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), other, http.StatusServiceUnavailable, http.Header{}, body, "gpt-6-luna")
	}

	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(exempt, "gpt-6-luna"))
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(other, "gpt-6-luna"))
}

func TestAccountNoAutoUnsched_NilSafe(t *testing.T) {
	require.False(t, accountNoAutoUnsched(nil, &Account{ID: 1}))
	require.False(t, accountNoAutoUnsched(&config.Config{}, nil))
	require.False(t, accountNoAutoUnsched(&config.Config{}, &Account{ID: 1}))
}
