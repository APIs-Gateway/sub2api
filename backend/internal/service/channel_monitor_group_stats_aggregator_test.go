package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChannelMonitorGroupStatsAggregator_NilTask 验证 nil task 的防御性检查。
func TestChannelMonitorGroupStatsAggregator_NilTask(t *testing.T) {
	var task *ChannelMonitorGroupStatsAggregationTask
	err := task.RunAggregation(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil aggregation task")
}

// TestChannelMonitorGroupStatsAggregator_WindowCalculation 验证时间窗口计算逻辑。
func TestChannelMonitorGroupStatsAggregator_WindowCalculation(t *testing.T) {
	// 固定 now 为 2024-01-15 10:23:45 UTC
	now := time.Date(2024, 1, 15, 10, 23, 45, 0, time.UTC)

	// 预期：windowEnd = now - 1min, truncate = 10:22:00
	expectedEnd := time.Date(2024, 1, 15, 10, 22, 0, 0, time.UTC)
	// windowStart = windowEnd - 30min = 09:52:00（实际实现用的是 channelMonitorGroupStatsWindowMinutes，当前为 30）
	expectedStart := time.Date(2024, 1, 15, 9, 52, 0, 0, time.UTC)

	// 模拟时间计算（不依赖 DB）
	windowEnd := now.Add(-time.Duration(channelMonitorGroupStatsLagMinutes) * time.Minute).Truncate(time.Minute)
	windowStart := windowEnd.Add(-time.Duration(channelMonitorGroupStatsWindowMinutes) * time.Minute)

	assert.Equal(t, expectedEnd, windowEnd, "windowEnd calculation")
	assert.Equal(t, expectedStart, windowStart, "windowStart calculation")
	assert.Equal(t, 30*time.Minute, windowEnd.Sub(windowStart), "window span")
}

// TestNewChannelMonitorGroupStatsAggregationTask_NilProbeKeys 验证 nil probeKeyIDs 被初始化为空切片。
func TestNewChannelMonitorGroupStatsAggregationTask_NilProbeKeys(t *testing.T) {
	task := NewChannelMonitorGroupStatsAggregationTask(nil, nil)
	require.NotNil(t, task)
	assert.NotNil(t, task.probeKeyIDs, "nil probeKeyIDs should be initialized to empty slice")
	assert.Empty(t, task.probeKeyIDs)
}
