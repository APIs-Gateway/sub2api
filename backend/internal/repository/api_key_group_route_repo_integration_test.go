//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// 回退链仓储的集成测试。ReplaceChain 自己开事务（SELECT ... FOR UPDATE），
// 所以夹具必须真实提交，结束时手工清理，不能套外层回滚事务。
type APIKeyGroupRouteRepoSuite struct {
	suite.Suite
	ctx    context.Context
	db     *sql.DB
	repo   service.APIKeyGroupRouteRepository
	userID int64
	keyID  int64
	groups []int64 // groups[0] 是 Key 的主分组
}

func TestAPIKeyGroupRouteRepoSuite(t *testing.T) {
	suite.Run(t, new(APIKeyGroupRouteRepoSuite))
}

func (s *APIKeyGroupRouteRepoSuite) SetupTest() {
	s.ctx = context.Background()
	s.db = integrationDB
	s.repo = NewAPIKeyGroupRouteRepository(integrationDB)
	client := testEntClient(s.T())

	tag := uniqueTestValue(s.T(), "agr")
	user := mustCreateUser(s.T(), client, &service.User{Email: tag + "@example.com"})
	s.userID = user.ID
	s.groups = nil
	for i := 0; i < 8; i++ {
		g := mustCreateGroup(s.T(), client, &service.Group{Name: fmt.Sprintf("%s-g%d", tag, i), Platform: service.PlatformOpenAI})
		s.groups = append(s.groups, g.ID)
	}
	primary := s.groups[0]
	key := mustCreateApiKey(s.T(), client, &service.APIKey{UserID: user.ID, Key: "sk-" + tag, Name: tag, GroupID: &primary})
	s.keyID = key.ID

	s.T().Cleanup(func() {
		_, _ = s.db.Exec(`DELETE FROM api_key_group_routes WHERE api_key_id = $1`, s.keyID)
		_, _ = s.db.Exec(`DELETE FROM api_keys WHERE id = $1`, s.keyID)
		for _, gid := range s.groups {
			_, _ = s.db.Exec(`DELETE FROM groups WHERE id = $1`, gid)
		}
		_, _ = s.db.Exec(`DELETE FROM users WHERE id = $1`, s.userID)
	})
}

func (s *APIKeyGroupRouteRepoSuite) rawInsert(groupID int64, source, placement string, position int) error {
	_, err := s.db.ExecContext(s.ctx, `
		INSERT INTO api_key_group_routes (api_key_id, group_id, platform, source, placement, position)
		VALUES ($1, $2, 'openai', $3, $4, $5)`, s.keyID, groupID, source, placement, position)
	return err
}

func (s *APIKeyGroupRouteRepoSuite) requirePGCode(err error, code string) {
	var pqErr *pq.Error
	s.Require().True(errors.As(err, &pqErr), "expected pq error, got %v", err)
	s.Require().Equal(code, string(pqErr.Code))
}

func (s *APIKeyGroupRouteRepoSuite) items(source, placement string, groupIDs ...int64) []service.RouteItem {
	out := make([]service.RouteItem, 0, len(groupIDs))
	for i, gid := range groupIDs {
		out = append(out, service.RouteItem{GroupID: gid, Platform: "openai", Source: source, Placement: placement, Position: i})
	}
	return out
}

func (s *APIKeyGroupRouteRepoSuite) ids(items []service.RouteItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.GroupID)
	}
	return out
}

// --- 表约束 ---

func (s *APIKeyGroupRouteRepoSuite) TestConstraints() {
	s.Require().NoError(s.rawInsert(s.groups[1], "user", "tail", 0))

	// 同 source 内同一分组只能出现一次
	s.requirePGCode(s.rawInsert(s.groups[1], "user", "tail", 1), "23505")
	// 跨 source 允许同一分组（用户提交的分组撞了隐藏链不能报错）
	s.Require().NoError(s.rawInsert(s.groups[1], "admin", "tail", 0))
	// 同 (key, platform, source, placement) 内位置唯一
	s.requirePGCode(s.rawInsert(s.groups[2], "user", "tail", 0), "23505")
	// head 仅 admin 允许
	s.requirePGCode(s.rawInsert(s.groups[3], "user", "head", 0), "23514")
	s.Require().NoError(s.rawInsert(s.groups[3], "admin", "head", 0))
	// source / placement / position 取值范围
	s.requirePGCode(s.rawInsert(s.groups[4], "other", "tail", 0), "23514")
	s.requirePGCode(s.rawInsert(s.groups[4], "user", "middle", 0), "23514")
	s.requirePGCode(s.rawInsert(s.groups[4], "user", "tail", 16), "23514")
	s.requirePGCode(s.rawInsert(s.groups[4], "user", "tail", -1), "23514")
	// 外键：分组不存在
	s.requirePGCode(s.rawInsert(987654321, "user", "tail", 5), "23503")
}

func (s *APIKeyGroupRouteRepoSuite) TestCascadeOnPhysicalDelete() {
	s.Require().NoError(s.rawInsert(s.groups[1], "user", "tail", 0))
	_, err := s.db.ExecContext(s.ctx, `DELETE FROM groups WHERE id = $1`, s.groups[1])
	s.Require().NoError(err)
	var n int
	s.Require().NoError(s.db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM api_key_group_routes WHERE api_key_id = $1`, s.keyID).Scan(&n))
	s.Require().Zero(n)

	s.Require().NoError(s.rawInsert(s.groups[2], "user", "tail", 0))
	_, err = s.db.ExecContext(s.ctx, `DELETE FROM api_keys WHERE id = $1`, s.keyID)
	s.Require().NoError(err)
	s.Require().NoError(s.db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM api_key_group_routes WHERE api_key_id = $1`, s.keyID).Scan(&n))
	s.Require().Zero(n)
}

// --- ReplaceChain / ListByKey ---

func (s *APIKeyGroupRouteRepoSuite) TestReplaceChain_ReplacesOnlyItsSource() {
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "admin", ExpectedPrimaryGroupID: s.groups[0],
		Items: append(s.items("admin", "head", s.groups[1]), s.items("admin", "tail", s.groups[2])...),
	}))
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "user", ExpectedPrimaryGroupID: s.groups[0],
		Items: s.items("user", "tail", s.groups[2], s.groups[3]), // 与 admin tail 同一分组：跨 source 允许
	}))

	user, err := s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Equal([]int64{s.groups[2], s.groups[3]}, s.ids(user))
	for _, it := range user {
		s.Require().Equal("user", it.Source)
	}
	admin, err := s.repo.ListByKey(s.ctx, s.keyID, "admin")
	s.Require().NoError(err)
	s.Require().Len(admin, 2)
	all, err := s.repo.ListByKey(s.ctx, s.keyID, "")
	s.Require().NoError(err)
	s.Require().Len(all, 4)

	// 整条替换：旧项消失，admin 不受影响
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "user", ExpectedPrimaryGroupID: s.groups[0],
		Items: s.items("user", "tail", s.groups[4]),
	}))
	user, err = s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Equal([]int64{s.groups[4]}, s.ids(user))
	admin, err = s.repo.ListByKey(s.ctx, s.keyID, "admin")
	s.Require().NoError(err)
	s.Require().Len(admin, 2)

	// 清空
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{APIKeyID: s.keyID, Source: "user", ExpectedPrimaryGroupID: s.groups[0]}))
	user, err = s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Empty(user)
}

func (s *APIKeyGroupRouteRepoSuite) TestReplaceChain_NoteAndCreatedByRoundTrip() {
	by := int64(42)
	items := s.items("admin", "head", s.groups[1])
	items[0].Note = "513 pin"
	items[0].CreatedBy = &by
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{APIKeyID: s.keyID, Source: "admin", Items: items}))
	got, err := s.repo.ListByKey(s.ctx, s.keyID, "admin")
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Require().Equal("513 pin", got[0].Note)
	s.Require().NotNil(got[0].CreatedBy)
	s.Require().Equal(by, *got[0].CreatedBy)
	s.Require().False(got[0].CreatedAt.IsZero())
}

func (s *APIKeyGroupRouteRepoSuite) TestReplaceChain_KeyMissingSoftDeletedOrPrimaryChanged() {
	err := s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{APIKeyID: 987654321, Source: "user"})
	s.Require().ErrorIs(err, service.ErrAPIKeyNotFound)

	err = s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "user", ExpectedPrimaryGroupID: s.groups[5],
		Items: s.items("user", "tail", s.groups[1]),
	})
	s.Require().ErrorIs(err, service.ErrFallbackKeyChanged)
	got, err := s.repo.ListByKey(s.ctx, s.keyID, "")
	s.Require().NoError(err)
	s.Require().Empty(got, "a rejected replace must not write")

	_, err = s.db.ExecContext(s.ctx, `UPDATE api_keys SET deleted_at = NOW() WHERE id = $1`, s.keyID)
	s.Require().NoError(err)
	err = s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{APIKeyID: s.keyID, Source: "user"})
	s.Require().ErrorIs(err, service.ErrAPIKeyNotFound)
}

func (s *APIKeyGroupRouteRepoSuite) TestListByKey_FiltersSoftDeletedKeyAndGroup() {
	s.Require().NoError(s.rawInsert(s.groups[1], "user", "tail", 0))
	s.Require().NoError(s.rawInsert(s.groups[2], "user", "tail", 1))

	_, err := s.db.ExecContext(s.ctx, `UPDATE groups SET deleted_at = NOW() WHERE id = $1`, s.groups[1])
	s.Require().NoError(err)
	got, err := s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Equal([]int64{s.groups[2]}, s.ids(got))

	_, err = s.db.ExecContext(s.ctx, `UPDATE api_keys SET deleted_at = NOW() WHERE id = $1`, s.keyID)
	s.Require().NoError(err)
	got, err = s.repo.ListByKey(s.ctx, s.keyID, "")
	s.Require().NoError(err)
	s.Require().Empty(got)
}

// 两个并发整条替换：先锁 api_keys 行再先删后插，后者排队等待，不会撞位置唯一约束。
func (s *APIKeyGroupRouteRepoSuite) TestReplaceChain_ConcurrentReplacesDoNotCollide() {
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 每个 worker 写位置 0..2、分组有重叠的链
			ids := []int64{s.groups[1+i%3], s.groups[4+i%3], s.groups[7]}
			errs[i] = s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
				APIKeyID: s.keyID, Source: "user", ExpectedPrimaryGroupID: s.groups[0],
				Items: s.items("user", "tail", ids...),
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		s.Require().NoError(err, "worker %d", i)
	}
	got, err := s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Len(got, 3, "final state must be exactly one worker's chain")
}

// --- ApplyPrimaryGroupChange ---

func (s *APIKeyGroupRouteRepoSuite) TestApplyPrimaryGroupChange_RemovesNewPrimaryAndCompacts() {
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "user", Items: s.items("user", "tail", s.groups[1], s.groups[2], s.groups[3]),
	}))
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "admin", Items: append(s.items("admin", "head", s.groups[2]), s.items("admin", "tail", s.groups[5])...),
	}))

	s.Require().NoError(s.repo.ApplyPrimaryGroupChange(s.ctx, s.keyID, s.groups[2], "openai"))

	user, err := s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Equal([]int64{s.groups[1], s.groups[3]}, s.ids(user))
	s.Require().Equal([]int{0, 1}, []int{user[0].Position, user[1].Position})
	admin, err := s.repo.ListByKey(s.ctx, s.keyID, "admin")
	s.Require().NoError(err)
	s.Require().Equal([]int64{s.groups[5]}, s.ids(admin), "the new primary is removed from the hidden chain too")

	// 没有需要改的内容：幂等
	s.Require().NoError(s.repo.ApplyPrimaryGroupChange(s.ctx, s.keyID, s.groups[2], "openai"))
}

func (s *APIKeyGroupRouteRepoSuite) TestApplyPrimaryGroupChange_PlatformChangeClearsUserChainKeepsAdmin() {
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "user", Items: s.items("user", "tail", s.groups[1]),
	}))
	s.Require().NoError(s.repo.ReplaceChain(s.ctx, service.ReplaceRoutesParams{
		APIKeyID: s.keyID, Source: "admin", Items: s.items("admin", "tail", s.groups[2]),
	}))

	s.Require().NoError(s.repo.ApplyPrimaryGroupChange(s.ctx, s.keyID, 987654, "anthropic"))

	user, err := s.repo.ListByKey(s.ctx, s.keyID, "user")
	s.Require().NoError(err)
	s.Require().Empty(user)
	admin, err := s.repo.ListByKey(s.ctx, s.keyID, "admin")
	s.Require().NoError(err)
	s.Require().Len(admin, 1)
	s.Require().Equal("openai", admin[0].Platform, "admin row keeps its platform and is judged invalid_platform at runtime")

	s.Require().ErrorIs(s.repo.ApplyPrimaryGroupChange(s.ctx, 987654321, 1, "openai"), service.ErrAPIKeyNotFound)
}
