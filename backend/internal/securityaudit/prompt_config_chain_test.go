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

	// 有链且兜底分组命中：请求分组换成命中的那一跳。
	chained := base
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 42, Name: "fallback"}}
	got = cfg.ScopeRequest(chained)
	require.NotNil(t, got.GroupID)
	require.EqualValues(t, 42, *got.GroupID)
	require.Equal(t, "fallback", got.GroupName)
	require.True(t, cfg.IncludesGroup(got.GroupID))
	require.EqualValues(t, 1, *base.GroupID, "不修改传入的原请求")

	// 有链但没有任何一跳命中：原样返回。
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 2, Name: "x"}}
	got = cfg.ScopeRequest(chained)
	require.EqualValues(t, 1, *got.GroupID)
	require.False(t, cfg.IncludesGroup(got.GroupID))

	// 主分组命中：保持主分组。
	both := ActiveConfig{GroupIDs: []int64{1, 42}}
	chained.ChainGroups = []ChainGroup{{ID: 1, Name: "primary"}, {ID: 42, Name: "fallback"}}
	got = both.ScopeRequest(chained)
	require.EqualValues(t, 1, *got.GroupID)
}

func TestRequestCloneCopiesChainGroups(t *testing.T) {
	req := Request{ChainGroups: []ChainGroup{{ID: 1}, {ID: 2}}}
	cloned := req.Clone()
	cloned.ChainGroups[0].ID = 99
	require.EqualValues(t, 1, req.ChainGroups[0].ID)

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
