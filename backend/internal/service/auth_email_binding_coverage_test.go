//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type emailAvailabilityRepoStub struct {
	mockUserRepo
	getByEmailErr error
	aliasErr      error
}

func (r *emailAvailabilityRepoStub) GetByEmail(context.Context, string) (*User, error) {
	return nil, r.getByEmailErr
}

func (r *emailAvailabilityRepoStub) ExistsByEmailAlias(context.Context, string) (bool, error) {
	return false, r.aliasErr
}

func TestEnsureEmailIdentityAvailableForUserMapsLookupFailures(t *testing.T) {
	ctx := context.Background()
	svc := &AuthService{}
	require.ErrorIs(t, svc.ensureEmailIdentityAvailableForUser(ctx, nil, "owner@gmail.com"), ErrUserNotFound)

	svc.userRepo = &emailAvailabilityRepoStub{getByEmailErr: errors.New("lookup failed")}
	require.ErrorIs(
		t,
		svc.ensureEmailIdentityAvailableForUser(ctx, &User{ID: 1, Email: "current@example.com"}, "owner@gmail.com"),
		ErrServiceUnavailable,
	)

	svc.userRepo = &emailAvailabilityRepoStub{getByEmailErr: ErrUserNotFound, aliasErr: errors.New("alias lookup failed")}
	require.ErrorIs(
		t,
		svc.ensureEmailIdentityAvailableForUser(ctx, &User{ID: 1, Email: "current@example.com"}, "owner@gmail.com"),
		ErrServiceUnavailable,
	)
}
