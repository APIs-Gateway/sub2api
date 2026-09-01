package service

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpsCleanupPlan(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		days         int
		wantOK       bool
		wantTruncate bool
		wantCutoff   time.Time
	}{
		{name: "negative skips", days: -1, wantOK: false},
		{name: "zero truncates", days: 0, wantOK: true, wantTruncate: true},
		{name: "positive yields past cutoff", days: 7, wantOK: true, wantCutoff: now.AddDate(0, 0, -7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cutoff, truncate, ok := opsCleanupPlan(now, tc.days)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if truncate != tc.wantTruncate {
				t.Fatalf("truncate = %v, want %v", truncate, tc.wantTruncate)
			}
			if !tc.wantTruncate && !cutoff.Equal(tc.wantCutoff) {
				t.Fatalf("cutoff = %v, want %v", cutoff, tc.wantCutoff)
			}
		})
	}
}

func TestIsMissingRelationError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not missing", err: nil, want: false},
		{name: "match relation does not exist", err: fakeErr(`pq: relation "ops_error_logs" does not exist`), want: true},
		{name: "match case-insensitive", err: fakeErr(`ERROR: Relation "x" Does Not Exist`), want: true},
		{name: "non-matching error", err: fakeErr("connection refused"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRelationError(tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

type opsCleanupRepoStub struct {
	OpsRepository
	heartbeatCalls int
	sawSuccess     bool
	lastError      string
}

func (r *opsCleanupRepoStub) UpsertJobHeartbeat(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
	r.heartbeatCalls++
	if input.LastSuccessAt != nil {
		r.sawSuccess = true
	}
	if input.LastError != nil {
		r.lastError = *input.LastError
	}
	return nil
}

// upstream sync (#5030): 清理任务成功日志改走结构化 info 级别。这里不关心
// 日志本身的格式（logger 包已单独测试），只验证 runScheduled 在 leader lock
// 与全部清理目标都被跳过（retention < 0）时，确实一路跑到"成功"分支并记了
// 一次心跳——即触发了那条 info 日志所在的代码块，而不是提前从某个错误分支返回。
func TestOpsCleanupRunScheduledReachesSuccessLogging(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &opsCleanupRepoStub{}
	svc := &OpsCleanupService{
		db:      db,
		opsRepo: repo,
		cfg: &config.Config{
			RunMode: config.RunModeSimple,
			Ops: config.OpsConfig{
				Cleanup: config.OpsCleanupConfig{
					ErrorLogRetentionDays:      -1,
					MinuteMetricsRetentionDays: -1,
					HourlyMetricsRetentionDays: -1,
				},
			},
		},
	}

	svc.runScheduled()

	require.Equal(t, 1, repo.heartbeatCalls)
	require.True(t, repo.sawSuccess, "expected the success heartbeat, got error heartbeat: %q", repo.lastError)
	require.NoError(t, mock.ExpectationsWereMet())
}
