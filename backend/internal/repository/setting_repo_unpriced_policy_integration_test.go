//go:build integration

package repository

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// billing_unpriced_policy 是 W6 受保护键：通用 Set 被 W6GenericWriteGuard 拒绝，专用写入能真正写进去（W6 PR7b-2a）。
func TestSettingRepository_BillingUnpricedPolicyDedicatedWrite(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewSettingRepository(client).(*settingRepository)
	key := service.SettingKeyBillingUnpricedPolicy

	previous, previousErr := repo.GetValue(ctx, key)
	t.Cleanup(func() {
		// 直接 SQL 还原：通用 Set/Delete 会被守卫拒绝。
		if previousErr == nil {
			_, _ = integrationDB.ExecContext(ctx, "UPDATE settings SET value = $2 WHERE key = $1", key, previous)
		} else {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", key)
		}
	})

	// 通用写入仍然拦这个键（Set 与 SetMultiple 都是）。
	err := repo.Set(ctx, key, service.BillingUnpricedPolicyBlockAllowlist)
	require.Error(t, err)
	require.Equal(t, service.ReasonSettingKeyProtected, infraerrors.Reason(err))
	err = repo.SetMultiple(ctx, map[string]string{key: service.BillingUnpricedPolicyBlockAllowlist})
	require.Error(t, err)
	require.Equal(t, service.ReasonSettingKeyProtected, infraerrors.Reason(err))

	// 经 SettingService 走专用写入：成功落库，读回来是 block_allowlist；再写 observe 能覆盖。
	svc := service.NewSettingService(repo, nil)
	require.NoError(t, svc.SetBillingUnpricedPolicy(ctx, service.BillingUnpricedPolicyBlockAllowlist))
	got, err := repo.GetValue(ctx, key)
	require.NoError(t, err)
	require.Equal(t, service.BillingUnpricedPolicyBlockAllowlist, got)
	require.Equal(t, service.BillingUnpricedPolicyBlockAllowlist, svc.BillingUnpricedPolicy(ctx))
	require.NoError(t, svc.SetBillingUnpricedPolicy(ctx, service.BillingUnpricedPolicyObserve))
	got, err = repo.GetValue(ctx, key)
	require.NoError(t, err)
	require.Equal(t, service.BillingUnpricedPolicyObserve, got)

	// 非法值：400，不落库。
	require.Error(t, svc.SetBillingUnpricedPolicy(ctx, "block"))
	got, _ = repo.GetValue(ctx, key)
	require.Equal(t, service.BillingUnpricedPolicyObserve, got)
}
