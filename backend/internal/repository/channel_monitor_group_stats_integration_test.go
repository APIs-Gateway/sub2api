//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestChannelMonitorGroupStatsAggregator_ExclusionRules 验证聚合任务的排除规则。
// 在集成测试环境中插入混合信号的 usage_logs / ops_error_logs，运行聚合，验证计数。
//
// 排除规则：
//  1. 用户 4xx（status_code 400–499）
//  2. is_business_limited = true
//  3. nginx 伪造响应：output_tokens IN (86, 116) 且 upstream_model_mismatch = true 且 total_cost = 0
//  4. 探测 key（C1 阶段为空列表，C2 阶段从配置读取）
//  5. 流中失败（ops_error_logs 里 status_code = 200）
func TestChannelMonitorGroupStatsAggregator_ExclusionRules(t *testing.T) {
	db := integrationDB
	ctx := context.Background()

	// 准备测试数据：创建分组、用户、API key。
	tx := testEntTx(t)
	defer tx.Rollback()

	group := tx.Group.Create().
		SetName("test-group-stats").
		SaveX(ctx)

	user := tx.User.Create().
		SetEmail("stats-test@example.com").
		SetPasswordHash("hash").
		SaveX(ctx)

	apiKey := tx.APIKey.Create().
		SetUserID(user.ID).
		SetKey("sk-test-stats").
		SetName("test-stats-key").
		SaveX(ctx)

	account := tx.Account.Create().
		SetName("test-account").
		SetPlatform("openai").
		SetType("pool").
		SaveX(ctx)

	// 时间桶：20 分钟前的完整桶。
	now := time.Now().UTC().Truncate(time.Minute)
	bucketStart := now.Add(-20 * time.Minute)
	// 对齐到 10 分钟边界
	bucketStart = bucketStart.Add(-time.Duration(bucketStart.Minute()%10) * time.Minute)
	bucketEnd := bucketStart.Add(10 * time.Minute)

	// 插入 usage_logs 行（成功计数来源）。
	// 5 条正常成功。
	for i := 0; i < 5; i++ {
		tx.UsageLog.Create().
			SetRequestID("req-normal-"+string(rune('a'+i))).
			SetUserID(user.ID).
			SetGroupID(group.ID).
			SetAPIKeyID(apiKey.ID).
			SetAccountID(account.ID).
			SetModel("gpt-5.6-sol").
			SetInputTokens(100).
			SetOutputTokens(200).
			SetTotalCost(0.001).
			SetCreatedAt(bucketStart.Add(time.Duration(i) * time.Minute)).
			SaveX(ctx)
	}

	// 1 条 nginx 伪造响应（output_tokens=86, upstream_model_mismatch=true, total_cost=0）。
	// 应被排除。
	tx.UsageLog.Create().
		SetRequestID("req-fake-86").
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetAPIKeyID(apiKey.ID).
		SetAccountID(account.ID).
		SetModel("gpt-5.6-luna").
		SetUpstreamModel("gpt-5.6-luna").
		SetUpstreamModelMismatch(true).
		SetInputTokens(100).
		SetOutputTokens(86).
		SetTotalCost(0).
		SetCreatedAt(bucketStart.Add(6 * time.Minute)).
		SaveX(ctx)

	// 1 条 output_tokens=116 但 upstream_model_mismatch=false（正常请求，不应排除）。
	tx.UsageLog.Create().
		SetRequestID("req-normal-116").
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetAPIKeyID(apiKey.ID).
		SetAccountID(account.ID).
		SetModel("gpt-5.6-terra").
		SetInputTokens(150).
		SetOutputTokens(116).
		SetUpstreamModelMismatch(false).
		SetTotalCost(0.002).
		SetCreatedAt(bucketStart.Add(7 * time.Minute)).
		SaveX(ctx)

	require.NoError(t, tx.Commit())

	// 插入 ops_error_logs 行（失败计数来源）。
	// ops_error_logs 没有 ent schema，使用原始 SQL。
	tx2, err := db.Begin()
	require.NoError(t, err)
	defer tx2.Rollback()

	// 2 条 5xx 失败（应计入 error_5xx_count）。
	for i := 0; i < 2; i++ {
		_, err := tx2.Exec(`
			INSERT INTO ops_error_logs
				(user_id, group_id, api_key_id, account_id, model, status_code, upstream_status_code,
				 error_phase, error_type, is_business_limited, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, user.ID, group.ID, apiKey.ID, account.ID, "gpt-5.6-sol", 502, 502, "upstream_call", "upstream_error", false,
			bucketStart.Add(time.Duration(i+1)*time.Minute))
		require.NoError(t, err)
	}

	// 1 条 4xx（应被排除）。
	_, err = tx2.Exec(`
		INSERT INTO ops_error_logs
			(user_id, group_id, api_key_id, account_id, model, status_code, upstream_status_code,
			 error_phase, error_type, is_business_limited, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, user.ID, group.ID, apiKey.ID, account.ID, "gpt-5.6-sol", 400, 0, "validation", "validation_error", false,
		bucketStart.Add(3*time.Minute))
	require.NoError(t, err)

	// 1 条 is_business_limited=true（应被排除）。
	_, err = tx2.Exec(`
		INSERT INTO ops_error_logs
			(user_id, group_id, api_key_id, account_id, model, status_code, upstream_status_code,
			 error_phase, error_type, is_business_limited, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, user.ID, group.ID, apiKey.ID, account.ID, "gpt-5.6-sol", 503, 0, "rate_limit", "rate_limit", true,
		bucketStart.Add(4*time.Minute))
	require.NoError(t, err)

	// 1 条 status_code=200（流中失败，应被排除）。
	_, err = tx2.Exec(`
		INSERT INTO ops_error_logs
			(user_id, group_id, api_key_id, account_id, model, status_code, upstream_status_code,
			 error_phase, error_type, is_business_limited, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, user.ID, group.ID, apiKey.ID, account.ID, "gpt-5.6-sol", 200, 200, "stream", "stream_error", false,
		bucketStart.Add(5*time.Minute))
	require.NoError(t, err)

	require.NoError(t, tx2.Commit())

	// 运行聚合任务。
	aggregator := service.NewChannelMonitorGroupStatsAggregationTask(db, []int64{})
	affected, err := aggregator.UpsertGroupStats(ctx, bucketStart, bucketEnd)
	require.NoError(t, err)
	assert.Greater(t, affected, int64(0), "should have upserted at least one row")

	// 验证聚合结果。
	// 预期：success_count = 5 (正常) + 1 (output_tokens=116 但 mismatch=false) = 6
	//       error_5xx_count = 2 (5xx 且不被排除)
	//       total_count = 6 + 2 = 8
	//       success_rate = 6 / 8 = 0.75
	tx3 := testEntTx(t)
	defer tx3.Rollback()

	stats := tx3.ChannelMonitorGroupStats.Query().
		Where(func(s *sql.Selector) {
			s.Where(sql.And(
				sql.EQ("group_id", group.ID),
				sql.EQ("bucket_start", bucketStart),
			))
		}).
		AllX(ctx)

	require.Len(t, stats, 1, "should have exactly one stats row for the bucket")
	stat := stats[0]

	assert.Equal(t, group.ID, stat.GroupID, "group_id should match")
	assert.Equal(t, bucketStart.Unix(), stat.BucketStart.Unix(), "bucket_start should match")
	assert.Equal(t, int16(10), stat.BucketMinutes, "bucket_minutes should be 10")
	assert.Equal(t, 6, stat.SuccessCount, "success_count should exclude nginx fake response")
	assert.Equal(t, 2, stat.Error5xxCount, "error_5xx_count should exclude 4xx, is_business_limited, status_code=200")
	assert.Equal(t, 8, stat.TotalCount, "total_count should be success + error")
	require.NotNil(t, stat.SuccessRate, "success_rate should not be nil when total > 0")
	assert.InDelta(t, 0.75, *stat.SuccessRate, 0.001, "success_rate should be 6/8 = 0.75")
}

// TestChannelMonitorGroupStatsAggregator_EmptyBucket 验证空桶不写入任何行。
func TestChannelMonitorGroupStatsAggregator_EmptyBucket(t *testing.T) {
	db := integrationDB
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Minute)
	bucketStart := now.Add(-60 * time.Minute)
	// 对齐到 10 分钟边界
	bucketStart = bucketStart.Add(-time.Duration(bucketStart.Minute()%10) * time.Minute)
	bucketEnd := bucketStart.Add(10 * time.Minute)

	aggregator := service.NewChannelMonitorGroupStatsAggregationTask(db, []int64{})
	affected, err := aggregator.UpsertGroupStats(ctx, bucketStart, bucketEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(0), affected, "empty bucket should not write any rows")
}
