//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestModerationConfigSaveRejectsGroupDeletedAfterValidation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	settings := NewSettingRepository(client).(*settingRepository)
	group := &service.Group{Name: uniqueTestValue(t, "moderation-race"), Platform: service.PlatformAnthropic, RateMultiplier: 1, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
	require.NoError(t, groups.Create(ctx, group))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", group.ID) })
	key := service.SettingKeyContentModerationConfig
	previous, previousErr := settings.GetValue(ctx, key)
	t.Cleanup(func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	})
	require.NoError(t, settings.Set(ctx, key, `{"all_groups":false,"group_ids":[]}`))

	// The writer has already validated a live ID, but must recheck it after
	// waiting for the deleter's row lock and before persisting the setting.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM groups WHERE id = $1 FOR UPDATE", group.ID).Scan(&lockedID))
	finished := make(chan error, 1)
	go func() {
		finished <- settings.SetContentModerationConfig(ctx,
			fmt.Sprintf(`{"all_groups":false,"group_ids":[%d]}`, group.ID), []int64{group.ID})
	}()
	select {
	case err := <-finished:
		t.Fatalf("config write passed a locked group before deletion: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_, err = tx.ExecContext(ctx, "UPDATE groups SET deleted_at = NOW() WHERE id = $1", group.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	select {
	case err := <-finished:
		require.ErrorIs(t, err, service.ErrGroupNotFound)
	case <-time.After(5 * time.Second):
		t.Fatal("config save remained blocked after group deletion")
	}
	stored, err := settings.GetValue(ctx, key)
	require.NoError(t, err)
	require.JSONEq(t, `{"all_groups":false,"group_ids":[]}`, stored)
}

type SettingRepoSuite struct {
	suite.Suite
	ctx  context.Context
	repo *settingRepository
}

func (s *SettingRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.repo = NewSettingRepository(tx.Client()).(*settingRepository)
}

func TestSettingRepoSuite(t *testing.T) {
	suite.Run(t, new(SettingRepoSuite))
}

func (s *SettingRepoSuite) TestSetAndGetValue() {
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v1"), "Set")
	got, err := s.repo.GetValue(s.ctx, "k1")
	s.Require().NoError(err, "GetValue")
	s.Require().Equal("v1", got, "GetValue mismatch")
}

func (s *SettingRepoSuite) TestSet_Upsert() {
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v1"), "Set")
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v2"), "Set upsert")
	got, err := s.repo.GetValue(s.ctx, "k1")
	s.Require().NoError(err, "GetValue after upsert")
	s.Require().Equal("v2", got, "upsert mismatch")
}

func (s *SettingRepoSuite) TestGetValue_Missing() {
	_, err := s.repo.GetValue(s.ctx, "nonexistent")
	s.Require().Error(err, "expected error for missing key")
	s.Require().ErrorIs(err, service.ErrSettingNotFound)
}

func (s *SettingRepoSuite) TestSetMultiple_AndGetMultiple() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"k2": "v2", "k3": "v3"}), "SetMultiple")
	m, err := s.repo.GetMultiple(s.ctx, []string{"k2", "k3"})
	s.Require().NoError(err, "GetMultiple")
	s.Require().Equal("v2", m["k2"])
	s.Require().Equal("v3", m["k3"])
}

func (s *SettingRepoSuite) TestGetMultiple_EmptyKeys() {
	m, err := s.repo.GetMultiple(s.ctx, []string{})
	s.Require().NoError(err, "GetMultiple with empty keys")
	s.Require().Empty(m, "expected empty map")
}

func (s *SettingRepoSuite) TestGetMultiple_Subset() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"a": "1", "b": "2", "c": "3"}))
	m, err := s.repo.GetMultiple(s.ctx, []string{"a", "c", "nonexistent"})
	s.Require().NoError(err, "GetMultiple subset")
	s.Require().Equal("1", m["a"])
	s.Require().Equal("3", m["c"])
	_, exists := m["nonexistent"]
	s.Require().False(exists, "nonexistent key should not be in map")
}

func (s *SettingRepoSuite) TestGetAll() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"x": "1", "y": "2"}))
	all, err := s.repo.GetAll(s.ctx)
	s.Require().NoError(err, "GetAll")
	s.Require().GreaterOrEqual(len(all), 2, "expected at least 2 settings")
	s.Require().Equal("1", all["x"])
	s.Require().Equal("2", all["y"])
}

func (s *SettingRepoSuite) TestDelete() {
	s.Require().NoError(s.repo.Set(s.ctx, "todelete", "val"))
	s.Require().NoError(s.repo.Delete(s.ctx, "todelete"), "Delete")
	_, err := s.repo.GetValue(s.ctx, "todelete")
	s.Require().Error(err, "expected missing key error after Delete")
	s.Require().ErrorIs(err, service.ErrSettingNotFound)
}

func (s *SettingRepoSuite) TestDelete_Idempotent() {
	// Delete a key that doesn't exist should not error
	s.Require().NoError(s.repo.Delete(s.ctx, "nonexistent_delete"), "Delete nonexistent should be idempotent")
}

func (s *SettingRepoSuite) TestSetMultiple_Upsert() {
	s.Require().NoError(s.repo.Set(s.ctx, "upsert_key", "old_value"))
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"upsert_key": "new_value", "new_key": "new_val"}))

	got, err := s.repo.GetValue(s.ctx, "upsert_key")
	s.Require().NoError(err)
	s.Require().Equal("new_value", got, "SetMultiple should upsert existing key")

	got2, err := s.repo.GetValue(s.ctx, "new_key")
	s.Require().NoError(err)
	s.Require().Equal("new_val", got2)
}

// TestSet_EmptyValue 测试保存空字符串值
// 这是一个回归测试，确保可选设置（如站点Logo、API端点地址等）可以保存为空字符串
func (s *SettingRepoSuite) TestSet_EmptyValue() {
	// 测试 Set 方法保存空值
	s.Require().NoError(s.repo.Set(s.ctx, "empty_key", ""), "Set with empty value should succeed")

	got, err := s.repo.GetValue(s.ctx, "empty_key")
	s.Require().NoError(err, "GetValue for empty value")
	s.Require().Equal("", got, "empty value should be preserved")
}

// TestSetMultiple_WithEmptyValues 测试批量保存包含空字符串的设置
// 模拟用户保存站点设置时部分字段为空的场景
func (s *SettingRepoSuite) TestSetMultiple_WithEmptyValues() {
	// 模拟保存站点设置，部分字段有值，部分字段为空
	settings := map[string]string{
		"site_name":     "Sub2api",
		"site_subtitle": "Subscription to API",
		"site_logo":     "", // 用户未上传Logo
		"api_base_url":  "", // 用户未设置API地址
		"contact_info":  "", // 用户未设置联系方式
		"doc_url":       "", // 用户未设置文档链接
	}

	s.Require().NoError(s.repo.SetMultiple(s.ctx, settings), "SetMultiple with empty values should succeed")

	// 验证所有值都正确保存
	result, err := s.repo.GetMultiple(s.ctx, []string{"site_name", "site_subtitle", "site_logo", "api_base_url", "contact_info", "doc_url"})
	s.Require().NoError(err, "GetMultiple after SetMultiple with empty values")

	s.Require().Equal("Sub2api", result["site_name"])
	s.Require().Equal("Subscription to API", result["site_subtitle"])
	s.Require().Equal("", result["site_logo"], "empty site_logo should be preserved")
	s.Require().Equal("", result["api_base_url"], "empty api_base_url should be preserved")
	s.Require().Equal("", result["contact_info"], "empty contact_info should be preserved")
	s.Require().Equal("", result["doc_url"], "empty doc_url should be preserved")
}

// TestSetMultiple_UpdateToEmpty 测试将已有值更新为空字符串
// 确保用户可以清空之前设置的值
func (s *SettingRepoSuite) TestSetMultiple_UpdateToEmpty() {
	// 先设置非空值
	s.Require().NoError(s.repo.Set(s.ctx, "clearable_key", "initial_value"))

	got, err := s.repo.GetValue(s.ctx, "clearable_key")
	s.Require().NoError(err)
	s.Require().Equal("initial_value", got)

	// 更新为空值
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"clearable_key": ""}), "Update to empty should succeed")

	got, err = s.repo.GetValue(s.ctx, "clearable_key")
	s.Require().NoError(err)
	s.Require().Equal("", got, "value should be updated to empty string")
}
