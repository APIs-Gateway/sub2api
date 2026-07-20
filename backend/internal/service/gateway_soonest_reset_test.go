//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func accWithWindowEnd(id int64, end *time.Time) accountWithLoad {
	return accountWithLoad{
		account: &Account{
			ID:               id,
			Schedulable:      true,
			Status:           StatusActive,
			SessionWindowEnd: end,
		},
		loadInfo: &AccountLoadInfo{AccountID: id},
	}
}

func TestFilterBySoonestReset_PicksSoonestFutureWindow(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(24 * time.Hour)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, testTimePtr(later)),
		accWithWindowEnd(2, testTimePtr(soon)),
		accWithWindowEnd(3, testTimePtr(later)),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 1)
	require.Equal(t, int64(2), got[0].account.ID, "重置时间最早的账号被选中")
}

func TestFilterBySoonestReset_IgnoresNilAndExpiredWindows(t *testing.T) {
	now := time.Now()
	expired := now.Add(-1 * time.Hour)
	active := now.Add(2 * time.Hour)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, nil),                  // 无活跃窗口
		accWithWindowEnd(2, testTimePtr(expired)), // 已过期，视为无活跃窗口
		accWithWindowEnd(3, testTimePtr(active)),  // 唯一活跃窗口
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 1)
	require.Equal(t, int64(3), got[0].account.ID, "仅保留拥有未来重置时间的账号")
}

func TestFilterBySoonestReset_NoActiveWindowReturnsAll(t *testing.T) {
	now := time.Now()
	expired := now.Add(-30 * time.Minute)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, nil),
		accWithWindowEnd(2, testTimePtr(expired)),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 2, "没有任何账号拥有活跃窗口时，返回原集合不做过滤")
}

func TestFilterBySoonestReset_TiedSoonestKeepsAll(t *testing.T) {
	now := time.Now()
	end := now.Add(90 * time.Minute)
	accounts := []accountWithLoad{
		accWithWindowEnd(1, testTimePtr(end)),
		accWithWindowEnd(2, testTimePtr(end)),
		accWithWindowEnd(3, testTimePtr(now.Add(5*time.Hour))),
	}
	got := filterBySoonestReset(accounts)
	require.Len(t, got, 2, "并列最早重置的账号都保留，交由后续 LRU 决定")
	ids := map[int64]bool{got[0].account.ID: true, got[1].account.ID: true}
	require.True(t, ids[1] && ids[2])
}

func TestFilterBySoonestReset_SingleOrEmptyUnchanged(t *testing.T) {
	require.Empty(t, filterBySoonestReset(nil))
	single := []accountWithLoad{accWithWindowEnd(1, nil)}
	require.Len(t, filterBySoonestReset(single), 1)
}

func TestSelectAccountWithLoadAwareness_PreferSoonestReset(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(20 * time.Hour)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &later},
			{ID: 2, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &soon},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.PreferSoonestReset = true
	concurrencyCache := &mockConcurrencyCache{}
	svc := &GatewayService{
		accountRepo:        repo,
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(2), result.Account.ID)
}
