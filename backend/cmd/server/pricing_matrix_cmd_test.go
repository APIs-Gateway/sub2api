package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	a, err := parsePricingMatrixDeriveArgs([]string{"--apply", "--channel", "7", "--pricing-file", "p.json",
		"--channel-timeout", "30s", "--statement-timeout", "5s", "--lock-timeout", "1s"}, &errOut)
	require.NoError(t, err)
	require.True(t, a.apply)
	require.Equal(t, int64(7), a.channelID)
	require.Equal(t, "p.json", a.pricingFile)
	require.Equal(t, 30*time.Second, a.channelTimeout)

	g, err := parsePricingMatrixDeriveArgs([]string{"--group", "9"}, &errOut)
	require.NoError(t, err)
	require.Equal(t, int64(9), g.groupID)
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
		Totals:   service.PricingDeriveTotals{Channels: 1, Groups: 1, GroupsChanged: 1, CellsNew: 2},
	}
	var out bytes.Buffer
	printPricingDeriveReport(&out, r, "db1")
	text := out.String()
	require.Contains(t, text, "group=10")
	require.Contains(t, text, "warn: x")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var parsed struct {
		Command  string `json:"command"`
		Database string `json:"database"`
		Mode     string `json:"mode"`
		Totals   struct {
			Groups   int `json:"groups"`
			CellsNew int `json:"cells_new"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &parsed))
	require.Equal(t, "pricing-matrix derive", parsed.Command)
	require.Equal(t, "db1", parsed.Database)
	require.Equal(t, "dry-run", parsed.Mode)
	require.Equal(t, 1, parsed.Totals.Groups)
	require.Equal(t, 2, parsed.Totals.CellsNew)
}
