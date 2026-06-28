//go:build unit

package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// /v1/usage 三窗口展示用的纯格式化助手:limit<=0 视为「未配置」→ null;reset 时间按窗口推算。

func TestUsageLimitValue(t *testing.T) {
	require.Nil(t, usageLimitValue(0))
	require.Nil(t, usageLimitValue(-1))
	require.Equal(t, 10.0, usageLimitValue(10))
}

func TestUsageRemainingValue(t *testing.T) {
	require.Nil(t, usageRemainingValue(0, 5))   // 未配置限额 → null
	require.Equal(t, 7.0, usageRemainingValue(10, 3))
	require.Equal(t, 0.0, usageRemainingValue(10, 15)) // 超用 → 夹到 0,不为负
}

func TestUsageResetAt(t *testing.T) {
	require.Nil(t, usageResetAt(nil, "daily"))

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, start.Add(24*time.Hour), usageResetAt(&start, "daily"))
	require.Equal(t, start.Add(7*24*time.Hour), usageResetAt(&start, "weekly"))
	require.Equal(t, start.AddDate(0, 1, 0), usageResetAt(&start, "monthly"))
	require.Nil(t, usageResetAt(&start, "bogus"))
}
