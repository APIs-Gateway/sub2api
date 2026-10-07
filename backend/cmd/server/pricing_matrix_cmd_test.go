package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestParsePricingMatrixDeriveArgs_DefaultsToDryRun(t *testing.T) {
	var errOut bytes.Buffer
	a, err := parsePricingMatrixDeriveArgs(nil, &errOut)
	require.NoError(t, err)
	require.False(t, a.apply)
	require.Zero(t, a.channelID)
	require.Zero(t, a.groupID)
	require.Equal(t, 2*time.Minute, a.channelTimeout)
}

func TestParsePricingMatrixDeriveArgs_AllFlags(t *testing.T) {
	var errOut bytes.Buffer
	a, err := parsePricingMatrixDeriveArgs([]string{"--apply", "--channel", "7",
		"--channel-timeout", "30s", "--statement-timeout", "5s", "--lock-timeout", "1s"}, &errOut)
	require.NoError(t, err)
	require.True(t, a.apply)
	require.Equal(t, int64(7), a.channelID)
	require.Equal(t, 30*time.Second, a.channelTimeout)
	require.Equal(t, 5*time.Second, a.statementTimeout)
	require.Equal(t, time.Second, a.lockTimeout)

	// --pricing-file 只能用于 dry-run，不能与 --apply 同用。
	p, err := parsePricingMatrixDeriveArgs([]string{"--pricing-file", "p.json"}, &errOut)
	require.NoError(t, err)
	require.False(t, p.apply)
	require.Equal(t, "p.json", p.pricingFile)

	g, err := parsePricingMatrixDeriveArgs([]string{"--group", "9"}, &errOut)
	require.NoError(t, err)
	require.Equal(t, int64(9), g.groupID)
}

func TestParsePricingMatrixDeriveArgs_PricingFileWithApplyRejected(t *testing.T) {
	var errOut bytes.Buffer
	_, err := parsePricingMatrixDeriveArgs([]string{"--pricing-file", "p.json", "--apply"}, &errOut)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--pricing-file cannot be combined with --apply")
}

func TestParsePricingMatrixDeriveArgs_Invalid(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":         {"--nope"},
		"positional":           {"extra"},
		"channel and group":    {"--channel", "1", "--group", "2"},
		"negative channel":     {"--channel", "-1"},
		"zero channel timeout": {"--channel-timeout", "0s"},
		"huge statement":       {"--statement-timeout", "1h"},
		"huge lock":            {"--lock-timeout", "5m"},
	} {
		t.Run(name, func(t *testing.T) {
			var errOut bytes.Buffer
			_, err := parsePricingMatrixDeriveArgs(args, &errOut)
			require.Error(t, err)
		})
	}
}

func TestRunPricingMatrixCommand_RejectsUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	require.Error(t, runPricingMatrixCommand(nil, &out))
	require.Error(t, runPricingMatrixCommand([]string{"seed"}, &out))
}

// dry-run 的连接是只读会话，apply 只带超时。
func TestPricingMatrixDeriveDSN(t *testing.T) {
	a := pricingMatrixDeriveArgs{statementTimeout: time.Minute, lockTimeout: 10 * time.Second}
	require.Contains(t, pricingMatrixDeriveDSN("host=x", a), "default_transaction_read_only=on")
	a.apply = true
	dsn := pricingMatrixDeriveDSN("host=x", a)
	require.NotContains(t, dsn, "read_only")
	require.Contains(t, dsn, "statement_timeout=60000")
	require.Contains(t, dsn, "lock_timeout=10000")
}

// 输出的最后一行是一行 JSON 汇总。
func TestPrintPricingDeriveReport_LastLineIsJSON(t *testing.T) {
	r := &service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeDryRun,
		Groups: []service.PricingDeriveGroupSummary{{
			GroupID: 10, ChannelID: 1, Platform: "openai", Stage: "legacy",
			Status: service.PricingDeriveStatusChanged, Config: service.PricingDeriveConfigNew, CellsNew: 2, Warnings: 1,
			Notes: []service.DerivationNote{{Level: service.DerivationNoteWarn, Code: "x", Model: "m", Message: "msg"}},
		}},
		Failures: []service.PricingDeriveFailure{},
		Stale:    []service.PricingDeriveStaleGroup{{ChannelID: 2, GroupID: 20, Reason: service.PricingDeriveStaleDisabled}},
		Totals:   service.PricingDeriveTotals{Channels: 1, Groups: 1, GroupsChanged: 1, CellsNew: 2, GroupsStaleChannel: 1},
	}
	var out bytes.Buffer
	printPricingDeriveReport(&out, r, "db1", service.PricingDataInfo{Source: service.PricingSourceSnapshot, SnapshotID: 7, SHA256: "abc"})
	text := out.String()
	require.Contains(t, text, "group=10")
	require.Contains(t, text, "warn: x")
	require.Contains(t, text, "stale: channel=2 group=20 reason=channel_disabled")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var parsed struct {
		Command  string `json:"command"`
		Database string `json:"database"`
		Mode     string `json:"mode"`
		Totals   struct {
			Groups     int `json:"groups"`
			CellsNew   int `json:"cells_new"`
			StaleCount int `json:"groups_stale_channel"`
		} `json:"totals"`
		PricingSource string `json:"pricing_source"`
		SnapshotID    int64  `json:"pricing_snapshot_id"`
		Stale         []struct {
			GroupID int64 `json:"group_id"`
		} `json:"stale_groups"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &parsed))
	require.Equal(t, "pricing-matrix derive", parsed.Command)
	require.Equal(t, "db1", parsed.Database)
	require.Equal(t, "dry-run", parsed.Mode)
	require.Equal(t, 1, parsed.Totals.Groups)
	require.Equal(t, 2, parsed.Totals.CellsNew)
	require.Equal(t, 1, parsed.Totals.StaleCount)
	require.Equal(t, "snapshot", parsed.PricingSource)
	require.Equal(t, int64(7), parsed.SnapshotID)
	require.Len(t, parsed.Stale, 1)
}

func TestParsePricingMatrixDeriveArgs_ApplyRejectsPricingFile(t *testing.T) {
	var errOut bytes.Buffer
	_, err := parsePricingMatrixDeriveArgs([]string{"--apply", "--pricing-file", "p.json"}, &errOut)
	require.ErrorContains(t, err, "--apply")
	_, err = parsePricingMatrixDeriveArgs([]string{"--pricing-file", "p.json"}, &errOut)
	require.NoError(t, err, "dry-run 可以用文件覆盖")
}

type fakeCacheNotifier struct {
	err   error
	calls int
}

func (f *fakeCacheNotifier) NotifyUpdate(context.Context) error       { f.calls++; return f.err }
func (f *fakeCacheNotifier) SubscribeUpdates(context.Context, func()) {}

func TestNotifyChannelCacheUpdated(t *testing.T) {
	var out bytes.Buffer
	ok := &fakeCacheNotifier{}
	notifyChannelCacheUpdated(context.Background(), &out, ok)
	require.Equal(t, 1, ok.calls)
	require.Contains(t, out.String(), "published channel_cache_updated")
	require.NotContains(t, out.String(), "warning")

	out.Reset()
	notifyChannelCacheUpdated(context.Background(), &out, &fakeCacheNotifier{err: errors.New("redis down")})
	require.Contains(t, out.String(), "warning")
	require.Contains(t, out.String(), "redis down")

	out.Reset()
	notifyChannelCacheUpdated(context.Background(), &out, nil)
	require.Contains(t, out.String(), "warning")
}

func TestShouldNotifyChannelCache(t *testing.T) {
	applied := func(status string, applied bool) service.PricingDeriveGroupSummary {
		return service.PricingDeriveGroupSummary{Status: status, Applied: applied}
	}
	require.False(t, shouldNotifyChannelCache(nil))
	require.False(t, shouldNotifyChannelCache(&service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeDryRun, Groups: []service.PricingDeriveGroupSummary{applied(service.PricingDeriveStatusChanged, false)}}), "dry-run 不通知")
	require.False(t, shouldNotifyChannelCache(&service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeApply, Groups: []service.PricingDeriveGroupSummary{applied(service.PricingDeriveStatusUnchanged, true)}}), "没写任何东西不通知")
	require.True(t, shouldNotifyChannelCache(&service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeApply, Groups: []service.PricingDeriveGroupSummary{applied(service.PricingDeriveStatusChanged, true)}}))
	require.True(t, shouldNotifyChannelCache(&service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeApply, Failures: []service.PricingDeriveFailure{{ChannelID: 1, Error: "x"}}}), "有失败时写入状态不确定，也通知")
}

func TestRunPricingMatrixCommand_BadDeriveArgsShowUsage(t *testing.T) {
	var out bytes.Buffer
	err := runPricingMatrixCommand([]string{"derive", "--channel", "1", "--group", "2"}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pricing-matrix", "参数错误时附带用法")
}

// 报告里的孤儿、跳过原因、提示级备注、失败各有各的输出；apply 模式不打印 dry-run 提示。
func TestPrintPricingDeriveReport_AllLineKinds(t *testing.T) {
	r := &service.PricingDeriveBatchReport{
		Mode: service.PricingDeriveModeApply,
		Groups: []service.PricingDeriveGroupSummary{{
			GroupID: 11, ChannelID: 1, Platform: "openai", Stage: "v2",
			Status: service.PricingDeriveStatusSkipped, Config: service.PricingDeriveConfigSkipped,
			SkipReason: "stage_v2", Orphan: true,
			Notes: []service.DerivationNote{
				{Level: service.DerivationNoteInfo, Code: "only_info", Model: "m", Message: "quiet"},
				{Level: service.DerivationNoteWarn, Code: "loud", Model: "m", Message: "careful"},
			},
		}},
		Failures: []service.PricingDeriveFailure{{ChannelID: 5, GroupID: 0, Error: "boom"}},
		Totals:   service.PricingDeriveTotals{Groups: 1, GroupsSkipped: 1, Failures: 1},
	}
	var out bytes.Buffer
	printPricingDeriveReport(&out, r, "db1", service.PricingDataInfo{Source: service.PricingSourceFile, SHA256: "abc"})
	text := out.String()
	require.Contains(t, text, "orphan=true")
	require.Contains(t, text, "skipped: stage_v2")
	require.Contains(t, text, "warn: loud")
	require.NotContains(t, text, "only_info", "提示级备注不打印")
	require.Contains(t, text, "FAILED channel=5 group=0: boom")
	require.NotContains(t, text, "dry-run: nothing was written")
}

func TestLoadServicePricing_MissingPriceFileFails(t *testing.T) {
	_, _, err := loadServicePricing(context.Background(), nil, &config.Config{}, "/nonexistent/prices.json", true)
	require.ErrorContains(t, err, "load price file")
}
