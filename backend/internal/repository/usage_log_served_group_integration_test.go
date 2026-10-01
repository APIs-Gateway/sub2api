//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// usage_logs.served_group_id / served_route_source：老写入路径保持 NULL，非 NULL 往返一致。
func TestUsageLogRepo_ServedColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "served-roundtrip@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-served-roundtrip", Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-served-roundtrip"})

	// PR1 的写入路径不设置 served 字段 => 两列必须为 NULL
	plain := &service.UsageLog{UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "gpt-5", InputTokens: 1}
	_, err := repo.Create(ctx, plain)
	require.NoError(t, err)
	var gid, src *int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT served_group_id, served_route_source FROM usage_logs WHERE id = $1`, plain.ID).Scan(&gid, &src))
	require.Nil(t, gid)
	require.Nil(t, src)
	got, err := repo.GetByID(ctx, plain.ID)
	require.NoError(t, err)
	require.Nil(t, got.ServedGroupID)
	require.Nil(t, got.ServedRouteSource)

	// 非 NULL 往返
	servedGroup := int64(4321)
	source := service.ServedRouteSourceUserChain
	withServed := &service.UsageLog{
		UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "gpt-5", InputTokens: 1,
		ServedGroupID: &servedGroup, ServedRouteSource: &source,
	}
	_, err = repo.Create(ctx, withServed)
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, withServed.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ServedGroupID)
	require.Equal(t, servedGroup, *got.ServedGroupID)
	require.NotNil(t, got.ServedRouteSource)
	require.Equal(t, source, *got.ServedRouteSource)
}
