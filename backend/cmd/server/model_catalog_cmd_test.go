package main

import (
	"bytes"
	"flag"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestParseModelCatalogSeedArgs_Defaults(t *testing.T) {
	var errOut bytes.Buffer
	opts, err := parseModelCatalogSeedArgs(nil, &errOut)
	require.NoError(t, err)
	require.False(t, opts.Apply, "默认是 dry-run")
	require.Zero(t, opts.UsageDays, "默认不扫 usage_logs")
	require.Equal(t, repository.DefaultModelCatalogSeedStatementTimeout, opts.StatementTimeout)
	require.Empty(t, errOut.String())
}

func TestParseModelCatalogSeedArgs_AllFlags(t *testing.T) {
	var errOut bytes.Buffer
	opts, err := parseModelCatalogSeedArgs([]string{"--apply", "--include-usage-days", "30", "--statement-timeout", "90s"}, &errOut)
	require.NoError(t, err)
	require.True(t, opts.Apply)
	require.Equal(t, 30, opts.UsageDays)
	require.Equal(t, 90*time.Second, opts.StatementTimeout)

	opts, err = parseModelCatalogSeedArgs([]string{"-include-usage-days=365", "-statement-timeout=10m"}, &errOut)
	require.NoError(t, err)
	require.Equal(t, 365, opts.UsageDays, "上限含 365")
	require.Equal(t, 10*time.Minute, opts.StatementTimeout, "上限含 10m")
}

func TestParseModelCatalogSeedArgs_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"负的天数", []string{"--include-usage-days", "-1"}, "--include-usage-days"},
		{"天数超过上限", []string{"--include-usage-days", "366"}, "--include-usage-days"},
		{"语句超时为零", []string{"--statement-timeout", "0s"}, "--statement-timeout"},
		{"语句超时为负", []string{"--statement-timeout", "-5s"}, "--statement-timeout"},
		{"语句超时超过上限", []string{"--statement-timeout", "11m"}, "--statement-timeout"},
		{"多余的位置参数", []string{"--apply", "extra"}, `unexpected argument "extra"`},
		{"未知参数", []string{"--force"}, "flag provided but not defined"},
		{"天数不是整数", []string{"--include-usage-days", "many"}, "invalid value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			opts, err := parseModelCatalogSeedArgs(tc.args, &errOut)
			require.Error(t, err)
			require.Contains(t, err.Error()+errOut.String(), tc.message)
			require.False(t, opts.Apply, "解析失败时不会得到可执行的选项")
		})
	}
}

func TestParseModelCatalogSeedArgs_Help(t *testing.T) {
	var errOut bytes.Buffer
	_, err := parseModelCatalogSeedArgs([]string{"-h"}, &errOut)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, errOut.String(), "-apply")
	require.Contains(t, errOut.String(), "include-usage-days")
}

// 子命令名不对、缺子命令、参数不合法时，都在连接数据库之前就报错返回（不读配置、不开连接）。
func TestRunModelCatalogCommand_FailsBeforeTouchingDatabase(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{"unknown"},
		{"--apply"},
	} {
		var out bytes.Buffer
		err := runModelCatalogCommand(args, &out)
		require.EqualError(t, err, modelCatalogUsage)
		require.Empty(t, out.String())
	}

	var out bytes.Buffer
	err := runModelCatalogCommand([]string{"seed", "--include-usage-days", "400"}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--include-usage-days")

	err = runModelCatalogCommand([]string{"seed", "extra"}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected argument")
}

func TestModelCatalogUsage_MentionsEveryFlag(t *testing.T) {
	for _, want := range []string{"model-catalog seed", "--apply", "--include-usage-days", "--statement-timeout"} {
		require.Contains(t, modelCatalogUsage, want, "usage 缺少 %s", want)
	}
}
