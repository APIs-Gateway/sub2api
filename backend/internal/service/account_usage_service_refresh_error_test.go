package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// Model ClearError's persisted effect so this regression catches both the write
// and mutations of the account returned by the repository.
type usageErrorAccountRepo struct {
	stubOpenAIAccountRepo
	clearCalls int
}

func (r *usageErrorAccountRepo) ClearError(ctx context.Context, id int64) error {
	r.clearCalls++
	account, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	account.Status = StatusActive
	account.ErrorMessage = ""
	return nil
}

type usageErrorLogRepo struct {
	UsageLogRepository
}

func (usageErrorLogRepo) GetAccountWindowStats(context.Context, int64, time.Time) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{}, nil
}

func (usageErrorLogRepo) GetAccountTodayStats(context.Context, int64) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{}, nil
}

func TestAccountUsageService_OpenAIQueriesPreserveRefreshError(t *testing.T) {
	for _, scenario := range []string{"cached_expired_access", "cached_valid_access_invalid_refresh", "failed_probe_missing_access", "probe_throttled"} {
		for _, query := range []string{"usage", "forced_usage", "today", "today_batch"} {
			t.Run(scenario+"/"+query, func(t *testing.T) {
				const message = "Token refresh failed (non-retryable): refresh_token_invalidated"
				account := Account{
					ID: 7358, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Status: StatusError, ErrorMessage: message,
					Credentials: map[string]any{"refresh_token": "invalid-refresh-token"},
				}
				cache := NewUsageCache()
				if strings.HasPrefix(scenario, "cached_") {
					account.Credentials["access_token"] = "cached-access-token"
					expiresAt := time.Now().Add(-time.Hour)
					if scenario == "cached_valid_access_invalid_refresh" {
						expiresAt = time.Now().Add(time.Hour)
					}
					account.Credentials["expires_at"] = expiresAt.Format(time.RFC3339)
					account.Extra = map[string]any{"codex_5h_used_percent": 18.0, "codex_7d_used_percent": 34.0}
				}
				if scenario == "probe_throttled" {
					cache.openAIProbeCache.Store(account.ID, time.Now())
				}
				// Forced requests take the probe path, but fail locally without
				// credentials; no external network is needed for this regression.
				if query == "forced_usage" {
					delete(account.Credentials, "access_token")
				}
				repo := &usageErrorAccountRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
				svc := &AccountUsageService{accountRepo: repo, usageLogRepo: usageErrorLogRepo{}, cache: cache}
				ctx := context.Background()
				switch query {
				case "usage", "forced_usage":
					usage, err := svc.GetUsage(ctx, account.ID, query == "forced_usage")
					if err != nil || usage == nil {
						t.Fatalf("GetUsage: usage=%v err=%v", usage, err)
					}
				case "today":
					if _, err := svc.GetTodayStats(ctx, account.ID); err != nil {
						t.Fatal(err)
					}
				case "today_batch":
					if _, err := svc.GetTodayStatsBatch(ctx, []int64{account.ID}); err != nil {
						t.Fatal(err)
					}
				}
				stored, err := repo.GetByID(ctx, account.ID)
				if err != nil {
					t.Fatal(err)
				}
				if repo.clearCalls != 0 || stored.Status != StatusError || stored.ErrorMessage != message {
					t.Fatalf("query erased refresh error: clearCalls=%d status=%q error=%q", repo.clearCalls, stored.Status, stored.ErrorMessage)
				}
			})
		}
	}
}
