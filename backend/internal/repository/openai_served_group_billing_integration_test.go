//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Key 级回退链（PR2a 第 4b 段）：影子 Key 走真实 OpenAIGatewayService.RecordUsage + 真实 PG 计费仓储，
// 验证三件事：usage_logs.group_id 始终是主分组；served_group_id / served_route_source 只在换了分组时写；
// 费用和余额扣减按实际服务分组的倍率结算。service 层的同类测试用的是仓储替身，看不到真实表的落库形状。

type servedBillingRow struct {
	groupID      sql.NullInt64
	servedGroup  sql.NullInt64
	servedSource sql.NullInt64
	totalCost    float64
	actualCost   float64
	rate         float64
}

// latestServedBillingRow 等到该 Key 的用量行数达到 wantCount，返回最新一行。
func latestServedBillingRow(t *testing.T, apiKeyID int64, wantCount int) servedBillingRow {
	t.Helper()
	db := inflightTestDB(t)
	require.Eventually(t, func() bool {
		var n int
		return db.QueryRow(`SELECT count(*) FROM usage_logs WHERE api_key_id=$1`, apiKeyID).Scan(&n) == nil && n == wantCount
	}, 10*time.Second, 20*time.Millisecond, "usage_logs 行数应为 %d", wantCount)
	var row servedBillingRow
	require.NoError(t, db.QueryRow(
		`SELECT group_id, served_group_id, served_route_source, total_cost, actual_cost, rate_multiplier FROM usage_logs WHERE api_key_id=$1 ORDER BY id DESC LIMIT 1`,
		apiKeyID,
	).Scan(&row.groupID, &row.servedGroup, &row.servedSource, &row.totalCost, &row.actualCost, &row.rate))
	return row
}

func userBalance(t *testing.T, userID int64) float64 {
	t.Helper()
	var balance float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
	return balance
}

func TestOpenAIRecordUsage_ServedGroupBillsByServedRateAndKeepsHomeGroupID(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformOpenAI, "", "application/json")
	client, db := inflightTestEntClient(t), inflightTestDB(t)
	_, err := db.Exec(`UPDATE users SET balance=100 WHERE id=$1`, f.user.ID)
	require.NoError(t, err)

	home := f.key.Group
	servedGroup := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 2})
	servedGroup.Hydrated = true
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})

	record := func(t *testing.T, key *service.APIKey) {
		t.Helper()
		err := f.openAIService.RecordUsage(context.Background(), &service.OpenAIRecordUsageInput{
			Result: &service.OpenAIForwardResult{
				RequestID: "resp_served_billing_" + uuid.NewString(),
				Usage:     service.OpenAIUsage{InputTokens: 1000, OutputTokens: 200},
				Model:     "gpt-5.1",
				Duration:  time.Second,
			},
			APIKey:  key,
			User:    f.user,
			Account: account,
		})
		require.NoError(t, err)
	}
	hopKey := func(group *service.Group, source string) *service.APIKey {
		return userhandler.NewServedAPIKey(f.key, service.ChainHop{GroupID: group.ID, Group: group, RouteSource: source})
	}

	// 对照：第一跳（服务分组就是主分组）。两个 served 列保持 NULL，与引入回退链之前一致。
	balanceBefore := userBalance(t, f.user.ID)
	record(t, hopKey(home, service.RouteSourcePrimary))
	primary := latestServedBillingRow(t, f.key.ID, 1)
	require.True(t, primary.groupID.Valid)
	require.Equal(t, home.ID, primary.groupID.Int64)
	require.False(t, primary.servedGroup.Valid, "第一跳不写 served_group_id")
	require.False(t, primary.servedSource.Valid, "第一跳不写 served_route_source")
	require.Positive(t, primary.actualCost, "有价模型照常计费")
	require.InDelta(t, 1, primary.rate, 1e-9)
	balanceAfterPrimary := userBalance(t, f.user.ID)
	require.InDelta(t, primary.actualCost, balanceBefore-balanceAfterPrimary, 1e-7)

	// 用户链回退到倍率 2 的分组：group_id 仍是主分组，served 列写服务分组与来源，费用按服务分组倍率。
	record(t, hopKey(servedGroup, service.RouteSourceUser))
	userHop := latestServedBillingRow(t, f.key.ID, 2)
	require.Equal(t, home.ID, userHop.groupID.Int64, "group_id 始终是主分组")
	require.True(t, userHop.servedGroup.Valid)
	require.Equal(t, servedGroup.ID, userHop.servedGroup.Int64)
	require.True(t, userHop.servedSource.Valid)
	require.EqualValues(t, service.ServedRouteSourceUserChain, userHop.servedSource.Int64)
	require.InDelta(t, 2, userHop.rate, 1e-9)
	require.InDelta(t, primary.totalCost, userHop.totalCost, 1e-9, "同样的 token：标价相同")
	require.InDelta(t, 2*primary.actualCost, userHop.actualCost, 1e-7, "实收费用按服务分组倍率")
	balanceAfterUser := userBalance(t, f.user.ID)
	require.InDelta(t, userHop.actualCost, balanceAfterPrimary-balanceAfterUser, 1e-7, "余额按服务分组的实收费用扣减")

	// 管理员链：来源值不同，其余同上。
	record(t, hopKey(servedGroup, service.RouteSourceAdmin))
	adminHop := latestServedBillingRow(t, f.key.ID, 3)
	require.Equal(t, home.ID, adminHop.groupID.Int64)
	require.Equal(t, servedGroup.ID, adminHop.servedGroup.Int64)
	require.EqualValues(t, service.ServedRouteSourceAdminChain, adminHop.servedSource.Int64)
	require.InDelta(t, 2*primary.actualCost, adminHop.actualCost, 1e-7)
	require.InDelta(t, adminHop.actualCost, balanceAfterUser-userBalance(t, f.user.ID), 1e-7)

	// 三次结算各占一个去重键：没有重复结算，也没有漏记。
	var dedup int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
	require.Equal(t, 3, dedup)
}
