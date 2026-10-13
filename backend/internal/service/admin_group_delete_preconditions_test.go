//go:build unit

package service

import (
	"context"
	"database/sql"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newDeletePreconditionsTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:admin_group_delete_preconditions?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// groupRepoStubWithStatus wraps the existing groupRepoStub (from admin_service_delete_test.go)
// and overrides GetByID to return a group with a preset status.
type groupRepoStubWithStatus struct {
	groupRepoStub
	status string
	getErr error
}

func (s *groupRepoStubWithStatus) GetByID(_ context.Context, id int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &Group{ID: id, Name: "test-group", Status: s.status}, nil
}

func (s *groupRepoStubWithStatus) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &Group{ID: id, Name: "test-group", Status: s.status}, nil
}

// countOnlyAPIKeyRepoStub is a minimal stub that returns a preset count for
// CountByGroupID and a no-op for ListKeysByGroupID (called when preconditions pass).
// All other methods are delegated to apiKeyRepoStubForGroupUpdate (panics on unexpected calls).
type countOnlyAPIKeyRepoStub struct {
	apiKeyRepoStubForGroupUpdate
	keyCount int64
}

func (s *countOnlyAPIKeyRepoStub) CountByGroupID(_ context.Context, _ int64) (int64, error) {
	return s.keyCount, nil
}

func (s *countOnlyAPIKeyRepoStub) ListKeysByGroupID(_ context.Context, _ int64) ([]string, error) {
	return nil, nil
}

// TestDeleteGroup_PreconditionNotInactive checks that an active group is rejected
// before any DB writes occur.
func TestDeleteGroup_PreconditionNotInactive(t *testing.T) {
	ctx := context.Background()
	client := newDeletePreconditionsTestClient(t)

	svc := &adminServiceImpl{
		groupRepo:  &groupRepoStubWithStatus{status: StatusActive},
		apiKeyRepo: &countOnlyAPIKeyRepoStub{keyCount: 0},
		entClient:  client,
	}

	err := svc.DeleteGroup(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "GROUP_DELETE_NOT_INACTIVE", infraerrors.Reason(err))
	require.Equal(t, 422, infraerrors.Code(err))
}

// TestDeleteGroup_PreconditionHasKeys checks that a group with API keys is blocked
// with the key count in the error metadata.
func TestDeleteGroup_PreconditionHasKeys(t *testing.T) {
	ctx := context.Background()
	client := newDeletePreconditionsTestClient(t)

	svc := &adminServiceImpl{
		groupRepo:  &groupRepoStubWithStatus{status: "inactive"},
		apiKeyRepo: &countOnlyAPIKeyRepoStub{keyCount: 5},
		entClient:  client,
	}

	err := svc.DeleteGroup(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "GROUP_DELETE_HAS_KEYS", infraerrors.Reason(err))
	require.Equal(t, 422, infraerrors.Code(err))
	appErr := infraerrors.FromError(err)
	require.Equal(t, "5", appErr.Metadata["key_count"])
}

// TestDeleteGroup_PreconditionHasForSalePlans checks that a for-sale plan on the
// group blocks deletion with the plan count in the error metadata.
func TestDeleteGroup_PreconditionHasForSalePlans(t *testing.T) {
	ctx := context.Background()
	client := newDeletePreconditionsTestClient(t)

	_, err := client.SubscriptionPlan.Create().
		SetGroupID(1).
		SetName("Pro Plan").
		SetPrice(29.99).
		SetForSale(true).
		SetDailyAmountUsd(10).
		Save(ctx)
	require.NoError(t, err)

	svc := &adminServiceImpl{
		groupRepo:  &groupRepoStubWithStatus{status: "inactive"},
		apiKeyRepo: &countOnlyAPIKeyRepoStub{keyCount: 0},
		entClient:  client,
	}

	err = svc.DeleteGroup(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "GROUP_DELETE_HAS_PLANS", infraerrors.Reason(err))
	require.Equal(t, 422, infraerrors.Code(err))
	appErr := infraerrors.FromError(err)
	require.Equal(t, "1", appErr.Metadata["plan_count"])
}

// TestDeleteGroup_OffSalePlanDoesNotBlock confirms that a plan with for_sale=false
// does not prevent deletion (proceeds to cascade, which returns nil from the stub).
func TestDeleteGroup_OffSalePlanDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	// Use a separate in-memory DB URI to avoid collisions with the for-sale test above.
	db, err := sql.Open("sqlite", "file:admin_group_delete_offsale?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.SubscriptionPlan.Create().
		SetGroupID(1).
		SetName("Archived Plan").
		SetPrice(9.99).
		SetForSale(false).
		SetDailyAmountUsd(5).
		Save(ctx)
	require.NoError(t, err)

	grpRepo := &groupRepoStubWithStatus{status: "inactive"}
	// Override DeleteCascade so the full path completes without error.
	grpRepo.groupRepoStub.affectedUserIDs = nil
	grpRepo.groupRepoStub.deleteErr = nil

	svc := &adminServiceImpl{
		groupRepo:  grpRepo,
		apiKeyRepo: &countOnlyAPIKeyRepoStub{keyCount: 0},
		entClient:  client,
	}

	err = svc.DeleteGroup(ctx, 1)
	require.NoError(t, err)
}
