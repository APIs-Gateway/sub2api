package service

import (
	"context"
	"testing"
	"time"

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
	require.EqualValues(t, 1, *logs[0].GroupID, "用户可见的分组保持主分组")
	require.Equal(t, "primary", logs[0].GroupName)
	require.NotNil(t, logs[0].ScopeGroupID)
	require.EqualValues(t, 42, *logs[0].ScopeGroupID, "触发审核的那一跳记在只给管理端的字段")
	require.Equal(t, "hop", logs[0].ScopeGroupName)
}

// BK-1：all_groups=true 且链里有 admin head（隐藏分组排在主分组之前）时，
// 日志里用户可见的分组、发给用户的邮件变量 group_name 都必须是主分组名。
func TestContentModerationCheck_ChainAllGroupsWithAdminHeadKeepsPrimaryGroupName(t *testing.T) {
	svc, repo := newContentModerationModelFilterTestService(t, chainModerationTestConfig(true))
	input := chainModerationTestInput(t, 1)
	input.ChainGroups = []ContentModerationChainGroup{
		{ID: 99, Name: "hidden-head"}, {ID: 1, Name: "primary"}, {ID: 42, Name: "user-tail"},
	}

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	logs := requireContentModerationLogCount(t, repo, 1)
	log := &logs[0]
	require.NotNil(t, log.GroupID)
	require.EqualValues(t, 1, *log.GroupID)
	require.Equal(t, "primary", log.GroupName)
	vars := contentModerationEmailVariables(log, nil)
	require.Equal(t, "primary", vars["group_name"], "违规邮件变量不得出现隐藏链分组的名字")
	require.NotContains(t, vars["group_name"], "hidden")
}

// BK-1：主分组与用户兜底都不在范围内，只有管理员隐藏 tail 在范围内时，邮件变量仍是主分组名。
func TestContentModerationCheck_ChainHiddenTailOnlyInScopeDoesNotLeakName(t *testing.T) {
	svc, repo := newContentModerationModelFilterTestService(t, chainModerationTestConfig(false, 77))
	input := chainModerationTestInput(t, 1)
	input.ChainGroups = []ContentModerationChainGroup{
		{ID: 1, Name: "primary"}, {ID: 77, Name: "hidden-tail"},
	}

	decision, err := svc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	logs := requireContentModerationLogCount(t, repo, 1)
	log := &logs[0]
	require.Equal(t, "primary", log.GroupName)
	require.EqualValues(t, 1, *log.GroupID)
	require.NotNil(t, log.ScopeGroupID)
	require.EqualValues(t, 77, *log.ScopeGroupID)
	require.Equal(t, "hidden-tail", log.ScopeGroupName)
	require.Equal(t, "primary", contentModerationEmailVariables(log, nil)["group_name"])
}

// BK-1 端到端：并集命中隐藏链分组后，真正发给用户的违规邮件和封号邮件正文里只有主分组名，
// 任何一封都不得出现隐藏分组的名字。
func TestContentModerationChain_SentUserEmailsNeverContainHiddenGroupName(t *testing.T) {
	checkSvc, repo := newContentModerationModelFilterTestService(t, chainModerationTestConfig(true))
	input := chainModerationTestInput(t, 1)
	input.ChainGroups = []ContentModerationChainGroup{{ID: 99, Name: "hidden-head"}, {ID: 1, Name: "primary"}}
	decision, err := checkSvc.Check(context.Background(), input)
	require.NoError(t, err)
	require.True(t, decision.Blocked)

	logs := requireContentModerationLogCount(t, repo, 1)
	log := logs[0]
	log.UserEmail = "user@example.com"
	log.ViolationCount = 2

	server := startNotificationEmailTestSMTPServer(t)
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.SetMultiple(t.Context(), server.settings()))
	require.NoError(t, settings.Set(t.Context(), SettingKeyDefaultLocale, "en-US"))
	require.NoError(t, settings.Set(t.Context(), SettingKeySiteName, "Sub2API"))
	mailSvc := NewContentModerationService(settings, nil, nil, nil, nil, nil, NewEmailService(settings, nil), nil)
	cfg := &ContentModerationConfig{BanThreshold: 3}

	require.NoError(t, mailSvc.sendViolationEmail(t.Context(), cfg, &log))
	require.Eventually(t, func() bool { return server.messageCount() == 1 }, time.Second, 10*time.Millisecond)
	violation := server.lastMessageBody(t)
	require.Contains(t, violation, "primary", "邮件里的所属分组是用户 Key 的主分组")
	require.NotContains(t, violation, "hidden-head", "违规邮件不得出现隐藏链分组的名字")

	require.NoError(t, mailSvc.sendAccountDisabledEmail(t.Context(), cfg, &log))
	require.Eventually(t, func() bool { return server.messageCount() == 2 }, time.Second, 10*time.Millisecond)
	disabled := server.lastMessageBody(t)
	require.Contains(t, disabled, "primary")
	require.NotContains(t, disabled, "hidden-head", "封号邮件不得出现隐藏链分组的名字")
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

// all_groups 时主分组本身在范围内，保持主分组。
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
