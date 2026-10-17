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

// TestComputeAggregationWindow 验证聚合窗口计算逻辑。
func TestComputeAggregationWindow(t *testing.T) {
	tests := []struct {
		name            string
		now             time.Time
		expectedStart   time.Time
		expectedEnd     time.Time
		expectedSpanMin int
		startOnBoundary bool
	}{
		{
			name:            "aligned case - now at 10:23:45",
			now:             time.Date(2024, 1, 15, 10, 23, 45, 0, time.UTC),
			expectedEnd:     time.Date(2024, 1, 15, 10, 22, 0, 0, time.UTC),
			expectedStart:   time.Date(2024, 1, 15, 9, 50, 0, 0, time.UTC),
			expectedSpanMin: 32,
			startOnBoundary: true,
		},
		{
			name:            "windowEnd falls mid-bucket - now at 10:27:00",
			now:             time.Date(2024, 1, 15, 10, 27, 0, 0, time.UTC),
			expectedEnd:     time.Date(2024, 1, 15, 10, 26, 0, 0, time.UTC),
			expectedStart:   time.Date(2024, 1, 15, 9, 50, 0, 0, time.UTC),
			expectedSpanMin: 36,
			startOnBoundary: true,
		},
		{
			name:            "windowEnd at bucket boundary - now at 10:31:00",
			now:             time.Date(2024, 1, 15, 10, 31, 0, 0, time.UTC),
			expectedEnd:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			expectedStart:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			expectedSpanMin: 30,
			startOnBoundary: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windowStart, windowEnd := computeAggregationWindow(tt.now)

			assert.Equal(t, tt.expectedEnd, windowEnd, "windowEnd should match")
			assert.Equal(t, tt.expectedStart, windowStart, "windowStart should match")

			span := int(windowEnd.Sub(windowStart).Minutes())
			assert.Equal(t, tt.expectedSpanMin, span, "window span in minutes")

			if tt.startOnBoundary {
				// windowStart 必须对齐到 10 分钟边界
				assert.Equal(t, 0, windowStart.Minute()%10, "windowStart must be on 10-minute boundary")
			}
		})
	}
}

// TestNewChannelMonitorGroupStatsAggregationTask_NilProbeKeys 验证 nil probeKeyIDs 被初始化为空切片。
func TestNewChannelMonitorGroupStatsAggregationTask_NilProbeKeys(t *testing.T) {
	task := NewChannelMonitorGroupStatsAggregationTask(nil, nil)
	require.NotNil(t, task)
	assert.NotNil(t, task.probeKeyIDs, "nil probeKeyIDs should be initialized to empty slice")
	assert.Empty(t, task.probeKeyIDs)
}
