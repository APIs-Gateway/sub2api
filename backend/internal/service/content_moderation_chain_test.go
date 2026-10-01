package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func chainModerationTestConfig(allGroups bool, groupIDs ...int64) *ContentModerationConfig {
	cfg := defaultContentModerationModelFilterTestConfig()
	cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
	cfg.AllGroups = allGroups
	cfg.GroupIDs = groupIDs
	return cfg
}

func chainModerationTestInput(t *testing.T, primary int64, chain ...int64) ContentModerationCheckInput {
	t.Helper()
	groupID := primary
	input := ContentModerationCheckInput{
		UserID:    7,
		GroupID:   &groupID,
		GroupName: "primary",
		Protocol:  ContentModerationProtocolAnthropicMessages,
		Model:     "claude-test",
		Body:      reminderTestBody(t, ContentModerationProtocolAnthropicMessages, []string{"secret-token"}),
	}
	for _, id := range chain {
		input.ChainGroups = append(input.ChainGroups, ContentModerationChainGroup{ID: id, Name: "hop"})
	}
	return input
}

func TestContentModerationConfig_IncludesAnyGroup(t *testing.T) {
	cfg := &ContentModerationConfig{GroupIDs: []int64{42, 43}}
	require.True(t, cfg.IncludesAnyGroup([]int64{1, 42}))
	require.True(t, cfg.IncludesAnyGroup([]int64{43}))
	require.False(t, cfg.IncludesAnyGroup([]int64{1, 2}))
	require.False(t, cfg.IncludesAnyGroup(nil))

	all := &ContentModerationConfig{AllGroups: true}
	require.True(t, all.IncludesAnyGroup([]int64{1}))
	require.False(t, all.IncludesAnyGroup(nil), "没有任何分组时没有命中")

	var nilCfg *ContentModerationConfig
	require.False(t, nilCfg.IncludesAnyGroup([]int64{1}))
}

// 审查 B1：主分组不在审核范围、兜底分组在范围内，请求必须被拦截。
func TestContentModerationCheck_ChainFallbackGroupInScopeBlocksRequest(t *testing.T) {
	svc, repo := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 42))
	input := chainModerationTestInput(t, 1, 1, 42)

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked, "兜底分组在审核范围内，必须拦截")

	logs := requireContentModerationLogCount(t, repo, 1)
	require.NotNil(t, logs[0].GroupID)
	require.EqualValues(t, 42, *logs[0].GroupID, "审核记录的分组是触发审核的那一跳")
}

// 无链：行为与现状一致，主分组不在范围内就不审核。
func TestContentModerationCheck_NoChainUnchanged(t *testing.T) {
	svc, _ := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 42))
	decision, err := svc.Check(context.Background(), chainModerationTestInput(t, 1))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.False(t, decision.Blocked)

	// 无链且主分组在范围内：照常拦截。
	svc2, _ := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 1))
	decision, err = svc2.Check(context.Background(), chainModerationTestInput(t, 1))
	require.NoError(t, err)
	require.True(t, decision.Blocked)
}

// 链上没有任何一跳在范围内：不审核（与无链主分组不在范围内一致）。
func TestContentModerationCheck_ChainNoGroupInScopeAllows(t *testing.T) {
	svc, _ := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 42))
	decision, err := svc.Check(context.Background(), chainModerationTestInput(t, 1, 1, 2, 3))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

// 主分组本身在范围内：命中的就是主分组，记录的分组保持主分组。
func TestContentModerationCheck_ChainPrimaryInScopeKeepsPrimaryGroup(t *testing.T) {
	svc, repo := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 1, 42))
	decision, err := svc.Check(context.Background(), chainModerationTestInput(t, 1, 1, 42))
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	logs := requireContentModerationLogCount(t, repo, 1)
	require.NotNil(t, logs[0].GroupID)
	require.EqualValues(t, 1, *logs[0].GroupID)
}

// all_groups 时按链顺序取第一跳（即主分组）。
func TestContentModerationCheck_ChainAllGroupsBlocks(t *testing.T) {
	svc, _ := newContentModerationModelFilterTestService(t, chainModerationTestConfig(true))
	decision, err := svc.Check(context.Background(), chainModerationTestInput(t, 1, 1, 42))
	require.NoError(t, err)
	require.True(t, decision.Blocked)
}

// 范围内但模型不在范围内：并集只放宽分组维度，模型过滤照常生效。
func TestContentModerationCheck_ChainStillHonorsModelScope(t *testing.T) {
	cfg := chainModerationTestConfig(false, 42)
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterInclude, Models: []string{"other-model"}}
	svc, _ := newContentModerationModelFilterTestService(t, cfg)
	decision, err := svc.Check(context.Background(), chainModerationTestInput(t, 1, 1, 42))
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}
