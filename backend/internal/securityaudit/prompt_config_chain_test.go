package securityaudit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActiveConfigIncludesAnyGroup(t *testing.T) {
	cfg := ActiveConfig{GroupIDs: []int64{42, 43}}
	require.True(t, cfg.IncludesAnyGroup([]int64{1, 42}))
	require.True(t, cfg.IncludesAnyGroup([]int64{43}))
	require.False(t, cfg.IncludesAnyGroup([]int64{1, 2}))
	require.False(t, cfg.IncludesAnyGroup(nil))

	all := ActiveConfig{AllGroups: true}
	require.True(t, all.IncludesAnyGroup([]int64{1}))
	require.False(t, all.IncludesAnyGroup(nil))
}

func TestActiveConfigScopeRequest(t *testing.T) {
	primary := int64(1)
	base := Request{GroupID: &primary, GroupName: "primary"}
	cfg := ActiveConfig{GroupIDs: []int64{42}}

	// 无链：原样返回，行为不变。
	got := cfg.ScopeRequest(base)
	require.Equal(t, &primary, got.GroupID)
	require.Equal(t, "primary", got.GroupName)
	require.False(t, cfg.IncludesGroup(got.GroupID))

	// 有链且兜底分组命中：GroupID / GroupName 保持主分组，命中的那一跳记在 ScopeGroup*（只给管理端）。
	chained := base
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 42, Name: "fallback"}}
	got = cfg.ScopeRequest(chained)
	require.NotNil(t, got.GroupID)
	require.EqualValues(t, 1, *got.GroupID)
	require.Equal(t, "primary", got.GroupName, "用户可见的分组名不得被换成链上的分组")
	require.NotNil(t, got.ScopeGroupID)
	require.EqualValues(t, 42, *got.ScopeGroupID)
	require.Equal(t, "fallback", got.ScopeGroupName)
	require.True(t, cfg.InScope(got))
	require.False(t, cfg.IncludesGroup(got.GroupID), "主分组本身不在范围内")
	require.Nil(t, base.ScopeGroupID, "不修改传入的原请求")

	// 有链但没有任何一跳命中：原样返回。
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 2, Name: "x"}}
	got = cfg.ScopeRequest(chained)
	require.EqualValues(t, 1, *got.GroupID)
	require.Nil(t, got.ScopeGroupID)
	require.False(t, cfg.InScope(got))

	// 主分组命中：保持主分组，不需要额外记录。
	both := ActiveConfig{GroupIDs: []int64{1, 42}}
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 42, Name: "fallback"}}
	got = both.ScopeRequest(chained)
	require.EqualValues(t, 1, *got.GroupID)
	require.Nil(t, got.ScopeGroupID)
	require.True(t, both.InScope(got))
}

// BK-1：all_groups=true 且链里有 admin head（隐藏分组排在主分组之前）时，用户可见的分组仍是主分组。
func TestActiveConfigScopeRequest_AllGroupsWithAdminHeadKeepsPrimary(t *testing.T) {
	primary := int64(1)
	req := Request{
		GroupID: &primary, GroupName: "primary",
		ChainGroups: []ChainGroup{{ID: 99, Name: "hidden-head"}, {ID: 1, Name: "primary"}, {ID: 42, Name: "tail"}},
	}
	got := ActiveConfig{AllGroups: true}.ScopeRequest(req)
	require.EqualValues(t, 1, *got.GroupID)
	require.Equal(t, "primary", got.GroupName)
	require.Nil(t, got.ScopeGroupID)
	require.Empty(t, got.ScopeGroupName)

	// 只有隐藏的 admin tail 在范围内：用户可见字段仍是主分组，隐藏分组只在 ScopeGroup*。
	req.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 77, Name: "hidden-tail"}}
	got = ActiveConfig{GroupIDs: []int64{77}}.ScopeRequest(req)
	require.EqualValues(t, 1, *got.GroupID)
	require.Equal(t, "primary", got.GroupName)
	require.EqualValues(t, 77, *got.ScopeGroupID)
	require.Equal(t, "hidden-tail", got.ScopeGroupName)
}

func TestRequestCloneCopiesChainGroups(t *testing.T) {
	scope := int64(2)
	req := Request{ChainGroups: []ChainGroup{{ID: 1}, {ID: 2}}, ScopeGroupID: &scope}
	cloned := req.Clone()
	cloned.ChainGroups[0].ID = 99
	*cloned.ScopeGroupID = 55
	require.EqualValues(t, 1, req.ChainGroups[0].ID)
	require.EqualValues(t, 2, *req.ScopeGroupID)

	empty := Request{}.Clone()
	require.Empty(t, empty.ChainGroups)
}

type fakeServedRecorder struct {
	calls     int
	requestID string
	groupID   int64
	err       error
}

func (f *fakeServedRecorder) RecordServedGroup(_ context.Context, requestID string, servedGroupID int64) error {
	f.calls++
	f.requestID, f.groupID = requestID, servedGroupID
	return f.err
}

func TestCoordinatorRecordServedGroup(t *testing.T) {
	// 未设置 recorder：空操作。
	c := NewCoordinator(nil, nil)
	require.NoError(t, c.RecordServedGroup(context.Background(), "r1", 5))

	rec := &fakeServedRecorder{}
	c.SetServedGroupRecorder(rec)
	require.NoError(t, c.RecordServedGroup(context.Background(), "r1", 5))
	require.Equal(t, 1, rec.calls)
	require.Equal(t, "r1", rec.requestID)
	require.EqualValues(t, 5, rec.groupID)

	// 参数无效不调用。
	require.NoError(t, c.RecordServedGroup(context.Background(), "", 5))
	require.NoError(t, c.RecordServedGroup(context.Background(), "r1", 0))
	require.Equal(t, 1, rec.calls)

	// 错误原样返回。
	rec.err = errors.New("boom")
	require.Error(t, c.RecordServedGroup(context.Background(), "r2", 6))

	// nil 安全。
	var nilC *Coordinator
	require.NoError(t, nilC.RecordServedGroup(context.Background(), "r1", 5))
	nilC.SetServedGroupRecorder(rec)
}
