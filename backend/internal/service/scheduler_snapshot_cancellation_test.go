//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type schedulerCancellationCache struct {
	SchedulerCache
	cancel        context.CancelFunc
	tokenCaptures int
}

func (c *schedulerCancellationCache) GetSnapshot(ctx context.Context, _ SchedulerBucket) ([]*Account, bool, error) {
	c.cancel()
	return nil, false, ctx.Err()
}

func (c *schedulerCancellationCache) CaptureBucketWriteToken(ctx context.Context, _ SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.tokenCaptures++
	return SchedulerBucketWriteToken{}, ctx.Err()
}

func (c *schedulerCancellationCache) GetAccount(ctx context.Context, _ int64) (*Account, error) {
	c.cancel()
	return nil, ctx.Err()
}

type schedulerCancellationAccountRepo struct {
	AccountRepository
	listCalls    int
	getByIDCalls int
}

func (r *schedulerCancellationAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, _ string) ([]Account, error) {
	r.listCalls++
	return nil, ctx.Err()
}

func (r *schedulerCancellationAccountRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	r.getByIDCalls++
	return nil, ctx.Err()
}

type schedulerCancellationBoundaryCache struct {
	SchedulerCache
	cancel         context.CancelFunc
	cancelOnToken  bool
	published      int
	snapshotReads  int
	tokenCaptures  int
}

func (c *schedulerCancellationBoundaryCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.snapshotReads++
	return nil, false, nil
}

func (c *schedulerCancellationBoundaryCache) CaptureBucketWriteToken(ctx context.Context, _ SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.tokenCaptures++
	if c.cancelOnToken {
		c.cancel()
		return SchedulerBucketWriteToken{}, ctx.Err()
	}
	return SchedulerBucketWriteToken{}, nil
}

func (c *schedulerCancellationBoundaryCache) SetSnapshot(context.Context, SchedulerBucket, SchedulerBucketWriteToken, []Account) error {
	c.published++
	return nil
}

type schedulerCancellationBoundaryRepo struct {
	AccountRepository
	cancel    context.CancelFunc
	listCalls int
}

func (r *schedulerCancellationBoundaryRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	r.listCalls++
	r.cancel()
	return []Account{}, nil
}

type schedulerCancellationGetAccountRepo struct {
	AccountRepository
	cancel       context.CancelFunc
	getByIDCalls int
}

func (r *schedulerCancellationGetAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	r.getByIDCalls++
	r.cancel()
	return &Account{ID: 42}, nil
}

func TestSchedulerSnapshotListStopsAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, accounts)
	require.False(t, useMixed)
	require.Zero(t, cache.tokenCaptures, "canceled requests must not capture a cache publish token")
	require.Zero(t, repo.listCalls, "canceled requests must not fall back to the database")
}

func TestSchedulerSnapshotGetAccountStopsAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	account, err := svc.GetAccount(ctx, 42)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, account)
	require.Zero(t, repo.getByIDCalls, "canceled requests must not fall back to the database")
}

func TestSchedulerSnapshotListStopsBeforeCacheForCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cache := &schedulerCancellationBoundaryCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, accounts)
	require.False(t, useMixed)
	require.Zero(t, cache.snapshotReads)
	require.Zero(t, repo.listCalls)
}

func TestSchedulerSnapshotListStopsAfterTokenCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationBoundaryCache{cancel: cancel, cancelOnToken: true}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, accounts)
	require.False(t, useMixed)
	require.Equal(t, 1, cache.tokenCaptures)
	require.Zero(t, repo.listCalls, "canceled requests must not fall back after capturing a token")
}

func TestSchedulerSnapshotListStopsBeforePublishAfterDBCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationBoundaryCache{cancel: cancel}
	repo := &schedulerCancellationBoundaryRepo{cancel: cancel}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, accounts)
	require.False(t, useMixed)
	require.Equal(t, 1, repo.listCalls)
	require.Zero(t, cache.published, "canceled requests must not publish a snapshot")
}

func TestSchedulerSnapshotGetAccountStopsBeforeCacheForCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cache := &schedulerCancellationCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	account, err := svc.GetAccount(ctx, 42)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, account)
	require.Zero(t, repo.getByIDCalls)
}

func TestSchedulerSnapshotGetAccountDropsResultAfterDBCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	repo := &schedulerCancellationGetAccountRepo{cancel: cancel}
	svc := NewSchedulerSnapshotService(nil, nil, repo, nil, nil)

	account, err := svc.GetAccount(ctx, 42)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, account)
	require.Equal(t, 1, repo.getByIDCalls)
}
