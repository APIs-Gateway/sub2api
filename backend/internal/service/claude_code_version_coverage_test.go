//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// 后台循环：首次同步因刚同步过而跳过，之后由 ticker 驱动 runOnce 推进版本；Stop 能正常收尾。
func TestClaudeCodeVersionSyncStartTickerDrivesSync(t *testing.T) {
	repo := newClaudeCodeVersionSyncSettingRepoStub(map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "2.1.279",
	})
	repo.updatedAt = time.Now()
	github := &claudeCodeVersionSyncGitHubStub{latest: &GitHubRelease{TagName: "v2.1.280"}}
	svc := NewClaudeCodeVersionSyncService(repo, &SettingService{}, github, 20*time.Millisecond)

	svc.Start()
	require.Eventually(t, func() bool {
		writes := repo.syncedWrites()
		return len(writes) == 1 && writes[0] == "2.1.280"
	}, 5*time.Second, 10*time.Millisecond)
	svc.Stop()
	svc.Stop() // 幂等

	var nilSvc *ClaudeCodeVersionSyncService
	nilSvc.Stop()
}

func TestProvideClaudeCodeVersionSyncServiceWithoutDependencies(t *testing.T) {
	svc := ProvideClaudeCodeVersionSyncService(nil, nil, nil)
	require.NotNil(t, svc)
	require.Equal(t, claudeCodeVersionSyncInterval, svc.interval)
	svc.Stop()
}

func TestClaudeCodeVersionSyncedWithinIntervalEdgeCases(t *testing.T) {
	repo := newClaudeCodeVersionSyncSettingRepoStub(map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "2.1.280",
	})
	repo.updatedAt = time.Now()
	require.False(t, NewClaudeCodeVersionSyncService(repo, &SettingService{}, nil, 0).syncedWithinInterval(),
		"非正间隔不视为刚同步过")

	invalid := newClaudeCodeVersionSyncSettingRepoStub(map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "not-a-version",
	})
	invalid.updatedAt = time.Now()
	require.False(t, NewClaudeCodeVersionSyncService(invalid, &SettingService{}, nil, time.Hour).syncedWithinInterval(),
		"已存值非法时启动同步照常执行")
}

func TestClaudeCodeVersionSyncPersistFailureKeepsCache(t *testing.T) {
	repo := newClaudeCodeVersionSyncSettingRepoStub(nil)
	repo.setErr = errors.New("写入失败")
	github := &claudeCodeVersionSyncGitHubStub{latest: &GitHubRelease{TagName: "v2.1.280"}}

	newClaudeCodeVersionSyncService(repo, github).runOnce()

	require.Empty(t, repo.syncedWrites())
	_, ok := repo.values[SettingKeyClaudeCodeClientVersionSynced]
	require.False(t, ok)
}

func TestClaudeCodeVersionSyncNoStableReleaseKeepsValue(t *testing.T) {
	repo := newClaudeCodeVersionSyncSettingRepoStub(map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "2.1.279",
	})
	github := &claudeCodeVersionSyncGitHubStub{
		releases: []*GitHubRelease{{TagName: "v2.1.290-beta.1", Prerelease: true}, {TagName: "v2.1.291", Draft: true}},
	}

	newClaudeCodeVersionSyncService(repo, github).runOnce()

	require.Equal(t, 1, github.latestCalls)
	require.Equal(t, 1, github.calls)
	require.Empty(t, repo.syncedWrites())
	require.Equal(t, "2.1.279", repo.values[SettingKeyClaudeCodeClientVersionSynced])
}

type claudeCodeVersionErrRepoStub struct {
	SettingRepository
	calls int
}

func (r *claudeCodeVersionErrRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	r.calls++
	return nil, errors.New("数据库不可用")
}

func TestGetClaudeCodeClientVersionFallbacksAndCache(t *testing.T) {
	ctx := context.Background()

	var nilSvc *SettingService
	require.Equal(t, claude.CLIVersion(), nilSvc.GetClaudeCodeClientVersion(ctx))
	nilSvc.InvalidateClaudeCodeClientVersionCache()
	require.Equal(t, claude.CLIVersion(), (&SettingService{}).GetClaudeCodeClientVersion(ctx))

	// 读库失败：回退基线，并在短 TTL 内命中错误缓存而不反复打库。
	errRepo := &claudeCodeVersionErrRepoStub{}
	errSvc := NewSettingService(errRepo, &config.Config{})
	require.Equal(t, claude.CLIVersion(), errSvc.GetClaudeCodeClientVersion(ctx))
	require.Equal(t, claude.CLIVersion(), errSvc.GetClaudeCodeClientVersion(ctx))
	require.Equal(t, 1, errRepo.calls)

	// 正常值命中缓存：底层变化在失效前不可见，失效后立即回源。
	repo := &authSourceDefaultsRepoStub{values: map[string]string{
		SettingKeyClaudeCodeClientVersionSynced: "2.1.281",
	}}
	svc := NewSettingService(repo, &config.Config{})
	require.Equal(t, "2.1.281", svc.GetClaudeCodeClientVersion(ctx))
	repo.values[SettingKeyClaudeCodeClientVersionSynced] = "2.1.282"
	require.Equal(t, "2.1.281", svc.GetClaudeCodeClientVersion(ctx))
	svc.InvalidateClaudeCodeClientVersionCache()
	require.Equal(t, "2.1.282", svc.GetClaudeCodeClientVersion(ctx))
}
