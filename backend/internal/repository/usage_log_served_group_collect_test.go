package repository

import (
	"sort"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// served 分组要和主分组一起批量加载，用户端 / 管理端才能显示 served_group 的名字。
func TestCollectUsageLogIDs_IncludesServedGroupIDs(t *testing.T) {
	g1, g2, g3 := int64(1), int64(2), int64(3)
	logs := []service.UsageLog{
		{UserID: 1, APIKeyID: 1, AccountID: 1, GroupID: &g1},
		{UserID: 1, APIKeyID: 1, AccountID: 1, GroupID: &g1, ServedGroupID: &g2},
		{UserID: 1, APIKeyID: 1, AccountID: 1, GroupID: &g1, ServedGroupID: &g3},
		{UserID: 1, APIKeyID: 1, AccountID: 1, GroupID: &g1, ServedGroupID: &g2},
		{UserID: 1, APIKeyID: 1, AccountID: 1}, // 无分组
	}

	ids := collectUsageLogIDs(logs)

	got := append([]int64(nil), ids.groupIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	require.Equal(t, []int64{1, 2, 3}, got, "主分组与 served 分组去重后一起加载")
}
