//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
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

// roleGuardUserRepoStub 在 rpmUserRepoStub 之上提供可控的管理员计数，
// 用于测试"最后一个管理员不可降级"守卫。
type roleGuardUserRepoStub struct {
	*rpmUserRepoStub
	adminTotal  int64
	listErr     error
	listCalls   int
	lastFilters UserListFilters
}

func (s *roleGuardUserRepoStub) ListWithFilters(_ context.Context, _ pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error) {
	s.listCalls++
	s.lastFilters = filters
	if s.listErr != nil {
		return nil, nil, s.listErr
	}
	return nil, &pagination.PaginationResult{Total: s.adminTotal}, nil
}

func newRoleGuardRepo(user *User, adminTotal int64) *roleGuardUserRepoStub {
	base := &userRepoStub{user: user}
	return &roleGuardUserRepoStub{rpmUserRepoStub: &rpmUserRepoStub{userRepoStub: base}, adminTotal: adminTotal}
}

// captureAuditLogs 把默认 slog 输出重定向到缓冲区，便于断言审计日志字段。
func captureAuditLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestAdminService_UpdateUser_DemoteLastAdminRejected(t *testing.T) {
	repo := newRoleGuardRepo(&User{ID: 42, Email: "a@example.com", Role: RoleAdmin, Status: StatusActive}, 1)
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: &redeemRepoStub{}}

	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleUser, ActorAdminID: 7})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLastAdminDemote)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Contains(t, err.Error(), "last admin")
	require.Nil(t, repo.lastUpdated, "最后一个管理员不应被降级持久化")
	require.Equal(t, 1, repo.listCalls, "降级路径应触发管理员计数")
	require.Equal(t, RoleAdmin, repo.lastFilters.Role)
	require.Equal(t, StatusActive, repo.lastFilters.Status, "只统计启用状态的管理员")
	require.NotNil(t, repo.lastFilters.IncludeSubscriptions)
	require.False(t, *repo.lastFilters.IncludeSubscriptions, "计数不应加载订阅")
}

func TestAdminService_UpdateUser_DemoteLastAdminCountErrorPropagates(t *testing.T) {
	repo := newRoleGuardRepo(&User{ID: 42, Email: "a@example.com", Role: RoleAdmin, Status: StatusActive}, 0)
	repo.listErr = errors.New("db down")
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: &redeemRepoStub{}}

	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleUser})
	require.Error(t, err)
	require.Contains(t, err.Error(), "count admin users")
	require.Nil(t, repo.lastUpdated, "计数失败时应拒绝降级")
}

func TestAdminService_UpdateUser_DemoteAdminAllowedWhenOthersExist(t *testing.T) {
	logs := captureAuditLogs(t)
	repo := newRoleGuardRepo(&User{ID: 42, Email: "a@example.com", Role: RoleAdmin, Status: StatusActive}, 2)
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: invalidator,
	}

	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleUser, ActorAdminID: 7})
	require.NoError(t, err)
	require.Equal(t, RoleUser, updated.Role)
	require.NotNil(t, repo.lastUpdated)
	require.Equal(t, RoleUser, repo.lastUpdated.Role, "存在其他管理员时允许降级")

	out := logs.String()
	require.Contains(t, out, "admin.user_role_changed")
	require.Contains(t, out, "audit=true")
	require.Contains(t, out, "actor_admin_id=7")
	require.Contains(t, out, "target_user_id=42")
	require.Contains(t, out, "old_role=admin")
	require.Contains(t, out, "new_role=user")
}

func TestAdminService_UpdateUser_DemoteInactiveAdminSkipsCount(t *testing.T) {
	repo := newRoleGuardRepo(&User{ID: 42, Email: "a@example.com", Role: RoleAdmin, Status: StatusDisabled}, 1)
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: &authCacheInvalidatorStub{},
	}

	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleUser})
	require.NoError(t, err)
	require.Equal(t, RoleUser, updated.Role)
	require.Equal(t, 0, repo.listCalls, "降级未启用的管理员不减少可用管理员数，无需计数")
}

func TestAdminService_UpdateUser_PromoteDoesNotCountAdmins(t *testing.T) {
	logs := captureAuditLogs(t)
	repo := newRoleGuardRepo(&User{ID: 42, Email: "u@example.com", Role: RoleUser, Status: StatusActive}, 1)
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: &authCacheInvalidatorStub{},
	}

	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleAdmin, ActorAdminID: 9})
	require.NoError(t, err)
	require.Equal(t, RoleAdmin, updated.Role)
	require.Equal(t, 0, repo.listCalls, "升级路径不应触发管理员计数")

	out := logs.String()
	require.Contains(t, out, "admin.user_role_changed")
	require.Contains(t, out, "actor_admin_id=9")
	require.Contains(t, out, "old_role=user")
	require.Contains(t, out, "new_role=admin")
}

func TestAdminService_UpdateUser_NoRoleChangeNoAuditLog(t *testing.T) {
	logs := captureAuditLogs(t)
	repo := newRoleGuardRepo(&User{ID: 42, Email: "u@example.com", Role: RoleAdmin, Status: StatusActive}, 1)
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: &redeemRepoStub{}}

	username := "renamed"
	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: RoleAdmin, Username: &username})
	require.NoError(t, err)
	require.Equal(t, 0, repo.listCalls)
	require.NotContains(t, logs.String(), "admin.user_role_changed")
}

func TestAdminService_CreateUser_AuditsAdminCreation(t *testing.T) {
	logs := captureAuditLogs(t)
	repo := &userRepoStub{nextID: 77}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.CreateUser(context.Background(), &CreateUserInput{
		Email:        "new-admin@example.com",
		Password:     "strong-password",
		Role:         RoleAdmin,
		ActorAdminID: 5,
	})
	require.NoError(t, err)
	out := logs.String()
	require.Contains(t, out, "admin.admin_user_created")
	require.Contains(t, out, "audit=true")
	require.Contains(t, out, "actor_admin_id=5")
	require.Contains(t, out, "target_user_id=77")

	logs.Reset()
	_, err = svc.CreateUser(context.Background(), &CreateUserInput{
		Email:        "plain@example.com",
		Password:     "strong-password",
		ActorAdminID: 5,
	})
	require.NoError(t, err)
	require.NotContains(t, logs.String(), "admin.admin_user_created", "创建普通用户不写管理员审计日志")
}
