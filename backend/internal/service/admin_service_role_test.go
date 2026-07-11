//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminServiceCreateUserRole(t *testing.T) {
	tests := []struct {
		name string
		role string
		want string
	}{
		{name: "defaults to user", want: RoleUser},
		{name: "allows admin", role: RoleAdmin, want: RoleAdmin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &userRepoStub{nextID: 10}
			svc := &adminServiceImpl{userRepo: repo}

			user, err := svc.CreateUser(context.Background(), &CreateUserInput{
				Email:    "role@example.com",
				Password: "strong-password",
				Role:     tt.role,
			})

			require.NoError(t, err)
			require.Equal(t, tt.want, user.Role)
			require.Len(t, repo.created, 1)
			require.Equal(t, tt.want, repo.created[0].Role)
		})
	}
}

func TestAdminServiceCreateUserRejectsInvalidRole(t *testing.T) {
	repo := &userRepoStub{}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.CreateUser(context.Background(), &CreateUserInput{
		Email:    "role@example.com",
		Password: "strong-password",
		Role:     "owner",
	})

	require.EqualError(t, err, `invalid user role: "owner"`)
	require.Empty(t, repo.created)
}

func TestAdminServiceUpdateUserRoleInvalidatesAuthCache(t *testing.T) {
	repo := &userRepoStub{user: &User{ID: 42, Email: "role@example.com", Role: RoleUser}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		authCacheInvalidator: invalidator,
	}

	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleAdmin})

	require.NoError(t, err)
	require.Equal(t, RoleAdmin, updated.Role)
	require.Equal(t, []int64{42}, invalidator.userIDs)
}

func TestAdminServiceUpdateUserRejectsInvalidRole(t *testing.T) {
	repo := &userRepoStub{user: &User{ID: 42, Email: "role@example.com", Role: RoleUser}}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: "owner"})

	require.EqualError(t, err, `invalid user role: "owner"`)
	require.Empty(t, repo.updated)
}
