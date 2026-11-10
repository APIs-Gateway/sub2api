//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNoAutoUnsched_RepositoryAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })
	service.SetNoAutoUnschedConfigIDs(nil)

	group := mustCreateGroup(t, client, &service.Group{Name: "nostop-group", Platform: service.PlatformOpenAI})
	mk := func(name string, extra map[string]any) *service.Account {
		acc := mustCreateAccount(t, client, &service.Account{
			Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: extra,
		})
		mustBindAccountToGroup(t, client, acc.ID, group.ID, 1)
		return acc
	}
	flagged := mk("nostop-flagged", map[string]any{service.AccountExtraKeyNoAutoUnsched: true})
	listed := mk("nostop-listed", nil)
	plain := mk("nostop-plain", nil)
	service.SetNoAutoUnschedConfigIDs([]int64{listed.ID})

	future := time.Now().Add(time.Hour)
	for _, acc := range []*service.Account{flagged, listed, plain} {
		// 直接写库：模拟旧版本/其它实例/开关打开前已经落下的冷却窗口。
		_, err := client.Account.UpdateOneID(acc.ID).
			SetRateLimitResetAt(future).SetOverloadUntil(future).SetTempUnschedulableUntil(future).Save(ctx)
		require.NoError(t, err)
	}

	ids := func(accounts []service.Account) []int64 {
		out := make([]int64, 0, len(accounts))
		for _, a := range accounts {
			out = append(out, a.ID)
		}
		return out
	}

	// 读侧：pool 里始终有带标记的账号，普通账号被三类窗口剔除。
	byGroup, err := repo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, service.PlatformOpenAI)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{flagged.ID, listed.ID}, ids(byGroup))
	byGroupMulti, err := repo.ListSchedulableByGroupIDAndPlatforms(ctx, group.ID, []string{service.PlatformOpenAI})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{flagged.ID, listed.ID}, ids(byGroupMulti))
	byGroupAll, err := repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{flagged.ID, listed.ID}, ids(byGroupAll))
	byPlatform, err := repo.ListSchedulableByPlatform(ctx, service.PlatformOpenAI)
	require.NoError(t, err)
	require.Subset(t, ids(byPlatform), []int64{flagged.ID, listed.ID})
	require.NotContains(t, ids(byPlatform), plain.ID)
	all, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	require.Subset(t, ids(all), []int64{flagged.ID, listed.ID})
	require.NotContains(t, ids(all), plain.ID)

	// status=error（只由失败处理自动写入，同时置 schedulable=false）对带标记账号视为自动停调度而放行；
	// 手动停用（status=disabled / status=active 下 schedulable=false）与过期仍然生效；
	// 不带标记的 plain 账号（已被冷却窗口剔除）不受影响。
	_, err = client.Account.UpdateOneID(flagged.ID).SetStatus(service.StatusError).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.UpdateOneID(listed.ID).SetStatus(service.StatusDisabled).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	assertPool := func(want ...int64) {
		t.Helper()
		byGroup, err := repo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, service.PlatformOpenAI)
		require.NoError(t, err)
		require.ElementsMatch(t, want, ids(byGroup))
		byGroupAll, err := repo.ListSchedulableByGroupID(ctx, group.ID)
		require.NoError(t, err)
		require.ElementsMatch(t, want, ids(byGroupAll))
		byPlatform, err := repo.ListSchedulableByPlatform(ctx, service.PlatformOpenAI)
		require.NoError(t, err)
		require.Subset(t, ids(byPlatform), want)
		require.NotContains(t, ids(byPlatform), plain.ID)
	}
	assertPool(flagged.ID)

	// 手动关调度（status=active, schedulable=false）：带标记账号同样选不中。
	_, err = client.Account.UpdateOneID(listed.ID).SetStatus(service.StatusActive).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	assertPool(flagged.ID)
	require.NoError(t, repo.SetSchedulable(ctx, flagged.ID, false))
	_, err = client.Account.UpdateOneID(flagged.ID).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	assertPool()

	// 过期自动暂停不是「因出错自动产生」，照常生效；status=error 叠加过期也选不中。
	_, err = client.Account.UpdateOneID(flagged.ID).SetStatus(service.StatusActive).SetSchedulable(true).
		SetExpiresAt(time.Now().Add(-time.Hour)).SetAutoPauseOnExpired(true).Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.UpdateOneID(listed.ID).SetStatus(service.StatusError).SetSchedulable(false).
		SetExpiresAt(time.Now().Add(-time.Hour)).SetAutoPauseOnExpired(true).Save(ctx)
	require.NoError(t, err)
	assertPool()

	// 恢复：只剩三类冷却窗口，两个带标记账号都在候选池里。
	_, err = client.Account.UpdateOneID(flagged.ID).SetStatus(service.StatusActive).SetSchedulable(true).ClearExpiresAt().Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.UpdateOneID(listed.ID).SetStatus(service.StatusActive).SetSchedulable(true).ClearExpiresAt().Save(ctx)
	require.NoError(t, err)
	assertPool(flagged.ID, listed.ID)

	// 关掉开关（flagged 去掉 extra 标记）后立刻恢复原口径：窗口内的账号不再出现；status=error 同理。
	_, err = client.Account.UpdateOneID(flagged.ID).SetExtra(map[string]any{}).Save(ctx)
	require.NoError(t, err)
	assertPool(listed.ID)
	_, err = client.Account.UpdateOneID(flagged.ID).SetExtra(map[string]any{service.AccountExtraKeyNoAutoUnsched: true}).
		SetStatus(service.StatusError).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	assertPool(flagged.ID, listed.ID)
	_, err = client.Account.UpdateOneID(flagged.ID).SetExtra(map[string]any{}).Save(ctx)
	require.NoError(t, err)
	assertPool(listed.ID)
	_, err = client.Account.UpdateOneID(flagged.ID).SetExtra(map[string]any{service.AccountExtraKeyNoAutoUnsched: true}).
		SetStatus(service.StatusActive).SetSchedulable(true).Save(ctx)
	require.NoError(t, err)

	// 分组页统计口径与调度一致：两个带标记账号算「可用」，不算「限流中」。
	groupRepo := &groupRepository{sql: tx}
	counts, err := groupRepo.loadAccountCounts(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.EqualValues(t, 3, counts[group.ID].Total)
	require.EqualValues(t, 2, counts[group.ID].Active)
	require.EqualValues(t, 1, counts[group.ID].RateLimited)

	// 写侧：干净账号上做自动冷却/置错——带标记的不落库，普通的照常落库。
	fresh := func(name string, extra map[string]any) *service.Account {
		return mk(name, extra)
	}
	f1 := fresh("nostop-fresh-flagged", map[string]any{service.AccountExtraKeyNoAutoUnsched: true})
	f2 := fresh("nostop-fresh-listed", nil)
	service.SetNoAutoUnschedConfigIDs([]int64{f2.ID})
	f3 := fresh("nostop-fresh-plain", nil)

	until := time.Now().Add(30 * time.Minute)
	for _, id := range []int64{f1.ID, f2.ID, f3.ID} {
		require.NoError(t, repo.SetRateLimited(ctx, id, until))
		require.NoError(t, repo.SetOverloaded(ctx, id, until))
		require.NoError(t, repo.SetTempUnschedulable(ctx, id, until, "temp"))
		require.NoError(t, repo.SetModelRateLimit(ctx, id, "gpt-6-luna", until, "model"))
		require.NoError(t, repo.SetError(ctx, id, "boom"))
	}
	for _, id := range []int64{f1.ID, f2.ID} {
		got, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Nil(t, got.RateLimitResetAt)
		require.Nil(t, got.OverloadUntil)
		require.Nil(t, got.TempUnschedulableUntil)
		require.Equal(t, service.StatusActive, got.Status)
		require.True(t, got.Schedulable)
		require.Empty(t, got.Extra["model_rate_limits"])
	}
	got, err := repo.GetByID(ctx, f3.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RateLimitResetAt)
	require.NotNil(t, got.OverloadUntil)
	require.NotNil(t, got.TempUnschedulableUntil)
	require.Equal(t, service.StatusError, got.Status)
	require.False(t, got.Schedulable)
	require.NotEmpty(t, got.Extra["model_rate_limits"])

	// 管理员手动置错不受豁免影响。
	require.NoError(t, repo.SetError(service.WithManualAccountState(ctx), f1.ID, "manual"))
	got, err = repo.GetByID(ctx, f1.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusError, got.Status)
	require.False(t, got.Schedulable)
}
