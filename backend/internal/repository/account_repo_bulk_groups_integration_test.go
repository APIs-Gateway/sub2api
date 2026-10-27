//go:build integration

package repository

import (
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// groupPriorities 返回账号当前的 group_id -> priority。
func (s *AccountRepoSuite) groupPriorities(accountID int64) map[int64]int {
	rows, err := s.client.AccountGroup.Query().
		Where(accountgroup.AccountIDEQ(accountID)).
		All(s.ctx)
	s.Require().NoError(err)
	out := make(map[int64]int, len(rows))
	for _, row := range rows {
		out[row.GroupID] = row.Priority
	}
	return out
}

func (s *AccountRepoSuite) TestBulkBindGroups_AppendKeepsExistingGroupsAndIsIdempotent() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-append-g1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-append-g2"})
	g3 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-append-g3"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-append-a1"})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-append-a2"})
	a3 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-append-a3"})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g1.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a3.ID, g1.ID, 5)
	mustBindAccountToGroup(s.T(), s.client, a3.ID, g3.ID, 7)

	ids := []int64{a1.ID, a2.ID, a3.ID}
	s.Require().NoError(s.repo.BulkBindGroups(s.ctx, ids, []int64{g1.ID, g2.ID}, service.AccountGroupBindModeAppend))

	// 已有绑定的 priority 不动；新绑定接在现有最大值之后。
	s.Require().Equal(map[int64]int{g1.ID: 1, g2.ID: 2}, s.groupPriorities(a1.ID))
	// 原本没有分组的账号得到 1..n，与单账号编辑一致。
	s.Require().Equal(map[int64]int{g1.ID: 1, g2.ID: 2}, s.groupPriorities(a2.ID))
	// 其他分组（g3）被保留。
	s.Require().Equal(map[int64]int{g1.ID: 5, g3.ID: 7, g2.ID: 8}, s.groupPriorities(a3.ID))

	// 重复追加同样的分组是幂等的：不报错、不新增行、不改 priority。
	s.Require().NoError(s.repo.BulkBindGroups(s.ctx, ids, []int64{g1.ID, g2.ID}, service.AccountGroupBindModeAppend))
	s.Require().Equal(map[int64]int{g1.ID: 1, g2.ID: 2}, s.groupPriorities(a1.ID))
	s.Require().Equal(map[int64]int{g1.ID: 1, g2.ID: 2}, s.groupPriorities(a2.ID))
	s.Require().Equal(map[int64]int{g1.ID: 5, g3.ID: 7, g2.ID: 8}, s.groupPriorities(a3.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_AppendDeduplicatesInputs() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-dedupe-g1"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-dedupe-a1"})

	s.Require().NoError(s.repo.BulkBindGroups(
		s.ctx,
		[]int64{a1.ID, a1.ID},
		[]int64{g1.ID, g1.ID},
		service.AccountGroupBindModeAppend,
	))

	s.Require().Equal(map[int64]int{g1.ID: 1}, s.groupPriorities(a1.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_RemoveOnlyDeletesListedGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-remove-g1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-remove-g2"})
	g3 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-remove-g3"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-remove-a1"})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-remove-a2"})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g1.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g2.ID, 2)
	mustBindAccountToGroup(s.T(), s.client, a2.ID, g2.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a2.ID, g3.ID, 2)

	// g3 不在 a1 上、999999 根本不存在：只会被忽略。
	s.Require().NoError(s.repo.BulkBindGroups(
		s.ctx,
		[]int64{a1.ID, a2.ID},
		[]int64{g2.ID, g3.ID, 999999},
		service.AccountGroupBindModeRemove,
	))

	s.Require().Equal(map[int64]int{g1.ID: 1}, s.groupPriorities(a1.ID))
	s.Require().Empty(s.groupPriorities(a2.ID))

	// 再移除一次已经不存在的绑定：幂等。
	s.Require().NoError(s.repo.BulkBindGroups(
		s.ctx,
		[]int64{a1.ID, a2.ID},
		[]int64{g2.ID, g3.ID},
		service.AccountGroupBindModeRemove,
	))
	s.Require().Equal(map[int64]int{g1.ID: 1}, s.groupPriorities(a1.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_ReplaceMatchesBindGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-replace-g1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-replace-g2"})
	g3 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-replace-g3"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-replace-a1"})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-replace-a2"})
	reference := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-replace-reference"})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g1.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g2.ID, 2)
	mustBindAccountToGroup(s.T(), s.client, a2.ID, g3.ID, 9)

	target := []int64{g2.ID, g3.ID}
	s.Require().NoError(s.repo.BulkBindGroups(s.ctx, []int64{a1.ID, a2.ID}, target, service.AccountGroupBindModeReplace))
	// 旧行为：单账号 BindGroups（先删后建，priority 为 1..n）。
	s.Require().NoError(s.repo.BindGroups(s.ctx, reference.ID, target))

	want := s.groupPriorities(reference.ID)
	s.Require().Equal(map[int64]int{g2.ID: 1, g3.ID: 2}, want)
	s.Require().Equal(want, s.groupPriorities(a1.ID), "g1 应被移除，其余按列表顺序重建")
	s.Require().Equal(want, s.groupPriorities(a2.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_ReplaceWithEmptyListClearsGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-clear-g1"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-clear-a1"})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g1.ID, 1)

	s.Require().NoError(s.repo.BulkBindGroups(s.ctx, []int64{a1.ID}, nil, service.AccountGroupBindModeReplace))

	s.Require().Empty(s.groupPriorities(a1.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_SkipsMissingAndDeletedAccounts() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-skip-g1"})
	alive := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-skip-alive"})
	deleted := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-skip-deleted"})
	s.Require().NoError(s.repo.Delete(s.ctx, deleted.ID))

	for _, mode := range []service.AccountGroupBindMode{
		service.AccountGroupBindModeAppend,
		service.AccountGroupBindModeReplace,
	} {
		s.Require().NoError(s.repo.BulkBindGroups(
			s.ctx,
			[]int64{alive.ID, deleted.ID, 999999},
			[]int64{g1.ID},
			mode,
		), "mode=%s", mode)
	}

	s.Require().Equal(map[int64]int{g1.ID: 1}, s.groupPriorities(alive.ID))
	s.Require().Empty(s.groupPriorities(deleted.ID))
}

func (s *AccountRepoSuite) TestBulkBindGroups_RejectsUnknownMode() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-mode-a1"})

	err := s.repo.BulkBindGroups(s.ctx, []int64{a1.ID}, []int64{1}, service.AccountGroupBindMode("merge"))

	s.Require().ErrorIs(err, service.ErrInvalidAccountGroupMode)
}

func (s *AccountRepoSuite) TestBulkBindGroups_PublishesSchedulerEventWithAffectedGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-event-g1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "bb-event-g2"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bb-event-a1"})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, g1.ID, 1)

	// replace 会让 g1 失去账号，调度器必须同时重建 g1 和 g2 的快照。
	s.Require().NoError(s.repo.BulkBindGroups(s.ctx, []int64{a1.ID}, []int64{g2.ID}, service.AccountGroupBindModeReplace))

	rows, err := s.repo.sql.QueryContext(s.ctx,
		"SELECT payload FROM scheduler_outbox WHERE event_type = $1 ORDER BY id DESC LIMIT 1",
		service.SchedulerOutboxEventAccountBulkChanged)
	s.Require().NoError(err)
	defer func() { _ = rows.Close() }()
	s.Require().True(rows.Next())
	var raw []byte
	s.Require().NoError(rows.Scan(&raw))
	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
		GroupIDs   []int64 `json:"group_ids"`
	}
	s.Require().NoError(json.Unmarshal(raw, &payload))
	s.Require().Equal([]int64{a1.ID}, payload.AccountIDs)
	s.Require().ElementsMatch([]int64{g1.ID, g2.ID}, payload.GroupIDs)
}

func (s *AccountRepoSuite) TestBulkUpdate_ExpiryFields() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk-expiry-1"})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk-expiry-2"})

	expiresAt := time.Now().Add(72 * time.Hour).Unix()
	autoPause := true
	_, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID, a2.ID}, service.AccountBulkUpdate{
		ExpiresAt:          &expiresAt,
		AutoPauseOnExpired: &autoPause,
	})
	s.Require().NoError(err)
	for _, id := range []int64{a1.ID, a2.ID} {
		got, err := s.repo.GetByID(s.ctx, id)
		s.Require().NoError(err)
		s.Require().NotNil(got.ExpiresAt)
		s.Require().Equal(expiresAt, got.ExpiresAt.Unix())
		s.Require().True(got.AutoPauseOnExpired)
	}

	// 只改 auto_pause_on_expired，不碰过期时间。
	autoPause = false
	_, err = s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, service.AccountBulkUpdate{AutoPauseOnExpired: &autoPause})
	s.Require().NoError(err)
	got1, err := s.repo.GetByID(s.ctx, a1.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got1.ExpiresAt)
	s.Require().False(got1.AutoPauseOnExpired)

	// expires_at <= 0 清除过期时间，与单账号编辑一致。
	clearExpiry := int64(0)
	_, err = s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, service.AccountBulkUpdate{ExpiresAt: &clearExpiry})
	s.Require().NoError(err)
	got1, err = s.repo.GetByID(s.ctx, a1.ID)
	s.Require().NoError(err)
	s.Require().Nil(got1.ExpiresAt)
	// 没选中的账号不受影响。
	got2, err := s.repo.GetByID(s.ctx, a2.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got2.ExpiresAt)
}
