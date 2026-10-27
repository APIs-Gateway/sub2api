package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var replayCmdNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestParsePricingReplayArgs_Defaults(t *testing.T) {
	var errOut bytes.Buffer
	a, err := parsePricingReplayArgs(nil, &errOut, replayCmdNow)
	require.NoError(t, err)
	require.Equal(t, 30, a.days)
	require.True(t, a.until.Equal(replayCmdNow))
	require.Empty(t, a.groups)
	require.Equal(t, replayMatrixDerived, a.matrixSource)
	require.Equal(t, service.DefaultPricingReplayBatchSize, a.batchSize)
	require.Equal(t, repository.DefaultPricingReplayStatementTimeout, a.statementTimeout)
	require.Equal(t, repository.DefaultPricingReplayLockTimeout, a.lockTimeout)
	require.False(t, a.strict)
	require.False(t, a.record)
	require.GreaterOrEqual(t, a.workers, 1)
}

func TestParsePricingReplayArgs_AllFlags(t *testing.T) {
	var errOut bytes.Buffer
	a, err := parsePricingReplayArgs([]string{
		"--days", "7", "--until", "2026-10-01T00:00:00Z", "--groups", "3, 1,2", "--out-dir", "/tmp/x",
		"--matrix-source", "stored", "--pricing-file", "p.json", "--workers", "2", "--batch-size", "100",
		"--statement-timeout", "30s", "--lock-timeout", "1s", "--max-diff-rows", "5", "--sample", "3", "--strict", "--record",
	}, &errOut, replayCmdNow)
	require.NoError(t, err)
	require.Equal(t, 7, a.days)
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), a.until.UTC())
	require.Equal(t, []int64{3, 1, 2}, a.groups)
	require.Equal(t, "/tmp/x", a.outDir)
	require.Equal(t, replayMatrixStored, a.matrixSource)
	require.Equal(t, "p.json", a.pricingFile)
	require.Equal(t, 2, a.workers)
	require.Equal(t, 100, a.batchSize)
	require.Equal(t, 30*time.Second, a.statementTimeout)
	require.Equal(t, time.Second, a.lockTimeout)
	require.Equal(t, 5, a.maxDiffRows)
	require.Equal(t, 3, a.sample)
	require.True(t, a.strict)
	require.True(t, a.record)
}

func TestParsePricingReplayArgs_Invalid(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":     {"--nope"},
		"positional":       {"extra"},
		"days zero":        {"--days", "0"},
		"days too big":     {"--days", "366"},
		"bad source":       {"--matrix-source", "x"},
		"workers zero":     {"--workers", "0"},
		"workers too many": {"--workers", "65"},
		"batch zero":       {"--batch-size", "0"},
		"batch too big":    {"--batch-size", "50001"},
		"stmt zero":        {"--statement-timeout", "0s"},
		"stmt too long":    {"--statement-timeout", "31m"},
		"lock zero":        {"--lock-timeout", "0s"},
		"lock too long":    {"--lock-timeout", "2m"},
		"bad group":        {"--groups", "1,x"},
		"negative group":   {"--groups", "-1"},
		"bad until":        {"--until", "yesterday"},
	} {
		var errOut bytes.Buffer
		_, err := parsePricingReplayArgs(args, &errOut, replayCmdNow)
		require.Error(t, err, name)
	}
}

func TestParsePricingReplayGroups(t *testing.T) {
	ids, err := parsePricingReplayGroups("  ")
	require.NoError(t, err)
	require.Nil(t, ids)
	ids, err = parsePricingReplayGroups("1,,2 ,")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, ids)
	_, err = parsePricingReplayGroups("0")
	require.Error(t, err)
}

func TestResolvePricingReplayFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}

	got, err := resolvePricingReplayFile("explicit.json", cfg)
	require.NoError(t, err)
	require.Equal(t, "explicit.json", got)

	_, err = resolvePricingReplayFile("", cfg)
	require.Error(t, err)

	fallback := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(fallback, []byte("{}"), 0o600))
	cfg.Pricing.FallbackFile = fallback
	cfg.Pricing.DataDir = filepath.Join(dir, "missing")
	got, err = resolvePricingReplayFile("", cfg)
	require.NoError(t, err)
	require.Equal(t, fallback, got)

	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	local := filepath.Join(dataDir, "model_pricing.json")
	require.NoError(t, os.WriteFile(local, []byte("{}"), 0o600))
	cfg.Pricing.DataDir = dataDir
	got, err = resolvePricingReplayFile("", cfg)
	require.NoError(t, err)
	require.Equal(t, local, got, "the service data dir wins over the fallback file")
}

func TestRunPricingReplayCommand_RejectsBadArgs(t *testing.T) {
	var out bytes.Buffer
	err := runPricingReplayCommand([]string{"--days", "0"}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage: pricing-replay")
}

func TestPrintPricingReplayResult(t *testing.T) {
	s := &service.PricingReplaySummary{
		RowsInWindow: 10, RowsSelected: 8, RowsReplayed: 7, RowsUncovered: 1, RowsErrored: 0,
		ElapsedSeconds: 2, RowsPerSecond: 3.5, DiffCSVRows: 2,
		Diffs: []service.PricingReplayDiffCount{{Kind: "cost", Class: "translation", Count: 2}},
	}
	s.Verdict = service.PricingReplayVerdict{Pass: false, TranslationDiffs: 2, Reasons: []string{"translation diffs"}}
	var out bytes.Buffer
	printPricingReplayResult(&out, s, "dir/base")
	text := out.String()
	for _, want := range []string{"in_window=10", "kind=cost class=translation", "pass=false", "not passed: translation diffs", "dir/base-summary.json", "dir/base-diffs.csv (2 rows)"} {
		require.True(t, strings.Contains(text, want), want)
	}
}

// 数据库连不上时命令在核对只读会话那一步就退出：配置、时区、日志降级与 DSN 拼装都走过，且没有任何文件产出。
func TestRunPricingReplayCommand_DatabaseUnreachable(t *testing.T) {
	logger.InitBootstrap() // main() 在分派子命令之前已经做过这一步
	t.Setenv("DATABASE_HOST", "127.0.0.1")
	t.Setenv("DATABASE_PORT", "1")
	t.Setenv("DATABASE_USER", "replay")
	t.Setenv("DATABASE_PASSWORD", "x")
	t.Setenv("DATABASE_DBNAME", "replay_test")
	t.Setenv("DATABASE_SSLMODE", "disable")
	dir := t.TempDir()
	var out bytes.Buffer
	err := runPricingReplayCommand([]string{"--out-dir", dir, "--statement-timeout", "5s"}, &out)
	require.Error(t, err)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}
