//go:build integration

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestUserCreateJoinsOuterTransactionWithInvitationClaim 验证用户创建会加入调用方开启的
// 外部 ent 事务（注册流程“建用户 + 占用邀请码”原子性的基础）：
//   - 外层事务回滚后，用户与邀请码占用必须一并撤销（不得残留孤儿账号）；
//   - 外层事务提交后，用户与邀请码占用同时生效。
func TestUserCreateJoinsOuterTransactionWithInvitationClaim(t *testing.T) {
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)

	ctx := context.Background()
	// redeem_codes.code 有长度上限，后缀用 36 进制纳秒时间戳保持简短且唯一。
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	// 清理：本测试会真实提交少量数据，确保不影响同包其它集成测试。
	var committedUserIDs []int64
	var seededCodeIDs []int64
	t.Cleanup(func() {
		for _, id := range seededCodeIDs {
			_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE id = $1`, id)
		}
		for _, id := range committedUserIDs {
			_, _ = integrationDB.Exec(`DELETE FROM auth_identities WHERE user_id = $1`, id)
			_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, id)
		}
	})

	seedCode := func(t *testing.T, code string) int64 {
		t.Helper()
		c, err := client.RedeemCode.Create().
			SetCode(code).
			SetType(service.RedeemTypeInvitation).
			SetStatus(service.StatusUnused).
			SetValue(0).
			Save(ctx)
		require.NoError(t, err, "seed redeem code")
		seededCodeIDs = append(seededCodeIDs, c.ID)
		return c.ID
	}

	newUser := func(email string) *service.User {
		return &service.User{
			Email:        email,
			PasswordHash: "test-password-hash",
			Role:         service.RoleUser,
			Status:       service.StatusActive,
			Balance:      0,
			Concurrency:  1,
		}
	}

	t.Run("rollback removes user and releases claim", func(t *testing.T) {
		codeID := seedCode(t, "ITXR-" + suffix)
		email := "itx-rollback-" + suffix + "@example.com"
		tx, err := client.Tx(ctx)
		require.NoError(t, err)
		txCtx := dbent.NewTxContext(ctx, tx)

		u := newUser(email)
		require.NoError(t, userRepo.Create(txCtx, u))
		require.Greater(t, u.ID, int64(0), "create 应回填用户 ID")
		require.NoError(t, redeemRepo.Use(txCtx, codeID, u.ID))
		require.NoError(t, tx.Rollback())

		exists, err := userRepo.ExistsByEmail(ctx, email)
		require.NoError(t, err)
		require.False(t, exists, "回滚后不得残留孤儿用户")

		after, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, service.StatusUnused, after.Status, "回滚后邀请码应保持 unused")
		require.Nil(t, after.UsedBy)
	})

	t.Run("commit persists user and claim together", func(t *testing.T) {
		codeID := seedCode(t, "ITXC-" + suffix)
		email := "itx-commit-" + suffix + "@example.com"
		tx, err := client.Tx(ctx)
		require.NoError(t, err)
		txCtx := dbent.NewTxContext(ctx, tx)

		u := newUser(email)
		require.NoError(t, userRepo.Create(txCtx, u))
		require.NoError(t, redeemRepo.Use(txCtx, codeID, u.ID))
		require.NoError(t, tx.Commit())
		committedUserIDs = append(committedUserIDs, u.ID)

		exists, err := userRepo.ExistsByEmail(ctx, email)
		require.NoError(t, err)
		require.True(t, exists, "提交后用户应存在")

		after, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, service.StatusUsed, after.Status, "提交后邀请码应为 used")
		require.NotNil(t, after.UsedBy)
		require.Equal(t, u.ID, *after.UsedBy)
	})

	t.Run("second claim in another transaction is rejected", func(t *testing.T) {
		codeID := seedCode(t, "ITXS-" + suffix)

		tx1, err := client.Tx(ctx)
		require.NoError(t, err)
		tx1Ctx := dbent.NewTxContext(ctx, tx1)
		winner := newUser("itx-winner-" + suffix + "@example.com")
		require.NoError(t, userRepo.Create(tx1Ctx, winner))
		require.NoError(t, redeemRepo.Use(tx1Ctx, codeID, winner.ID))
		require.NoError(t, tx1.Commit())
		committedUserIDs = append(committedUserIDs, winner.ID)

		tx2, err := client.Tx(ctx)
		require.NoError(t, err)
		tx2Ctx := dbent.NewTxContext(ctx, tx2)
		loserEmail := "itx-loser-" + suffix + "@example.com"
		loser := newUser(loserEmail)
		require.NoError(t, userRepo.Create(tx2Ctx, loser))
		require.ErrorIs(t, redeemRepo.Use(tx2Ctx, codeID, loser.ID), service.ErrRedeemCodeUsed)
		require.NoError(t, tx2.Rollback())

		exists, err := userRepo.ExistsByEmail(ctx, loserEmail)
		require.NoError(t, err)
		require.False(t, exists, "占码失败回滚后不得残留败者账号")
	})
}
