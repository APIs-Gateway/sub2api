//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsIngressRejectRepositoryUpsertMergesAndListFilters(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE ops_ingress_reject_aggregates RESTART IDENTITY")
	require.NoError(t, err)

	repo := &opsRepository{db: integrationDB}
	bucket := time.Now().UTC().Truncate(time.Minute)
	userID := int64(101)

	first := &service.OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: "invalid_api_key", RouteFamily: "messages", Protocol: "anthropic",
		ClientIP: "203.0.113.0/24", UserID: &userID, RequestCount: 3,
		FirstSeen: bucket, LastSeen: bucket.Add(10 * time.Second),
	}
	second := &service.OpsIngressRejectAggregate{
		// Same dimensions as `first`: the second flush from a live aggregator must
		// merge into the same row instead of producing a duplicate.
		BucketStart: bucket, RejectReason: "invalid_api_key", RouteFamily: "messages", Protocol: "anthropic",
		ClientIP: "203.0.113.0/24", UserID: &userID, RequestCount: 2,
		FirstSeen: bucket.Add(-5 * time.Second), LastSeen: bucket.Add(20 * time.Second),
	}
	other := &service.OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: "ip_restricted", RouteFamily: "codex", Protocol: "openai",
		ClientIP: "198.51.100.0/24", RequestCount: 1, FirstSeen: bucket, LastSeen: bucket,
	}

	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{first}))
	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{second, other}))

	all, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{PageSize: 50})
	require.NoError(t, err)
	require.Equal(t, 2, all.Total)

	filtered, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{
		RejectReason: "invalid_api_key", UserID: &userID, PageSize: 50,
	})
	require.NoError(t, err)
	require.Equal(t, 1, filtered.Total)
	require.Len(t, filtered.Items, 1)
	merged := filtered.Items[0]
	require.Equal(t, int64(5), merged.RequestCount, "merged row must sum request_count across both flushes")
	require.WithinDuration(t, bucket.Add(-5*time.Second), merged.FirstSeen, time.Second, "first_seen must widen to the earliest flush")
	require.WithinDuration(t, bucket.Add(20*time.Second), merged.LastSeen, time.Second, "last_seen must widen to the latest flush")
	require.Equal(t, userID, *merged.UserID)
	require.Nil(t, merged.APIKeyID)
}
