//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）仓储层集成测试（补缺）：
// EnsureAccount 惰性建账幂等、admin ListWithdrawals / ListLedger 过滤+分页、
// 用户端 ListUserWithdrawals / ListUserLedger。这些是 handler 用 fake、单测覆盖不到的真 SQL 路径。

func TestPointsRepo_EnsureAccount_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")

	acct, err := repo.EnsureAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.ID, acct.UserID)
	require.Equal(t, int64(0), acct.Available)
	require.Equal(t, int64(0), acct.Frozen)

	// 再次确保：不报错、不重复建账。
	acct2, err := repo.EnsureAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.ID, acct2.UserID)

	cnt := querySingleInt(t, ctx, integrationEntClient,
		`SELECT COUNT(*) FROM user_points_accounts WHERE user_id=$1`, user.ID)
	require.Equal(t, 1, cnt, "exactly one account row")
}

// seedWithdrawal 给用户铺 earn 额度后建一张 pending 提现，返回提现 ID。
func seedWithdrawal(t *testing.T, repo service.PointsRepository, user *service.User, points int64) *service.PointsWithdrawal {
	t.Helper()
	ctx := context.Background()
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, float64(points))
	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: points, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	gross, fee, net := service.ComputeWithdrawalAmounts(points, pointsTestPeg, 0)
	w, err := repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: points,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: pointsTestPeg, PayoutMethod: service.PointsPayoutMethodAlipay,
		PayoutAlipayAccount: "13800000000", PayoutAlipayName: "李四",
	})
	require.NoError(t, err)
	return w
}

func TestPointsRepo_ListWithdrawals_FiltersAndPaging(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")
	admin := mustCreatePointsUser(t, "admin")

	w := seedWithdrawal(t, repo, user, 100)

	// 按 email 精确圈定该用户，避开共享库其他数据。
	ws, total, err := repo.ListWithdrawals(ctx, service.PointsWithdrawalFilter{Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, ws, 1)
	require.Equal(t, w.ID, ws[0].ID)
	require.Equal(t, service.PointsWithdrawalStatusPending, ws[0].Status)
	require.Equal(t, user.Email, ws[0].UserEmail)

	// Status=pending + Search 命中。
	ws, total, err = repo.ListWithdrawals(ctx, service.PointsWithdrawalFilter{Status: service.PointsWithdrawalStatusPending, Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, ws, 1)

	// Status=paid → 该用户没有 → 0。
	ws, total, err = repo.ListWithdrawals(ctx, service.PointsWithdrawalFilter{Status: service.PointsWithdrawalStatusPaid, Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 0, total)
	require.Empty(t, ws)

	// 审核通过后，paid 过滤命中、pending 过滤落空。
	_, err = repo.ReviewWithdrawal(ctx, w.ID, admin.ID, true, "", "proof-hash")
	require.NoError(t, err)
	ws, total, err = repo.ListWithdrawals(ctx, service.PointsWithdrawalFilter{Status: service.PointsWithdrawalStatusPaid, Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, ws, 1)
	require.Equal(t, service.PointsWithdrawalStatusPaid, ws[0].Status)

	// 分页越界 → 空但 total 仍计数。
	ws, total, err = repo.ListWithdrawals(ctx, service.PointsWithdrawalFilter{Search: user.Email, Page: 2, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Empty(t, ws)
}

func TestPointsRepo_ListLedger_FiltersAndPaging(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 300)

	// earn → 'earn' 行；随后 redeem-balance → 'redeem_balance' 行（两种 kind）。
	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 300, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	_, err = repo.RedeemToBalance(ctx, user.ID, 100, service.PointsToBalance(100, pointsTestPeg, 1), pointsTestPeg)
	require.NoError(t, err)

	// 全部（按 email 圈定）：earn + redeem_balance = 2。
	entries, total, err := repo.ListLedger(ctx, service.PointsLedgerFilter{Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, entries, 2)

	// Kind=earn → 1。
	entries, total, err = repo.ListLedger(ctx, service.PointsLedgerFilter{Kind: service.PointsKindEarn, Search: user.Email, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, entries, 1)
	require.Equal(t, service.PointsKindEarn, entries[0].Kind)

	// 分页 PageSize=1 → 1 行 / total=2。
	entries, total, err = repo.ListLedger(ctx, service.PointsLedgerFilter{Search: user.Email, Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, entries, 1)
}

func TestPointsRepo_ListUserWithdrawalsAndLedger(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")

	w := seedWithdrawal(t, repo, user, 150)

	// 用户端提现列表。
	ws, err := repo.ListUserWithdrawals(ctx, user.ID, 50)
	require.NoError(t, err)
	require.Len(t, ws, 1)
	require.Equal(t, w.ID, ws[0].ID)

	// 用户端流水：earn + withdraw_hold = 2。
	entries, total, err := repo.ListUserLedger(ctx, user.ID, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, entries, 2)
}

// TestPointsRepo_GuardBranches 集中命中各仓储方法 DB 前的入参 guard（service 层通常已挡，
// 但仓储自身也须健壮）：EarnPoints 无效入参/无来源、Clawback 无单/零退款、Redeem/Deduct 非正数与额度不足。
func TestPointsRepo_GuardBranches(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")

	// EarnPoints：Points<=0 / InviterID<=0 / 无来源 → 不入账、无错。
	applied, err := repo.EarnPoints(ctx, service.EarnPointsInput{InviterID: user.ID, Points: 0, SourceOrderID: 1})
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.EarnPoints(ctx, service.EarnPointsInput{InviterID: 0, Points: 10, SourceOrderID: 1})
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.EarnPoints(ctx, service.EarnPointsInput{InviterID: user.ID, Points: 10}) // 无 source
	require.NoError(t, err)
	require.False(t, applied)

	// ClawbackByOrder：sourceOrderID<=0 → 0。
	n, err := repo.ClawbackByOrder(ctx, 0, 50, 100)
	require.NoError(t, err)
	require.EqualValues(t, 0, n)

	// ClawbackByOrder：有 earn 行但 refundAmount=0 → claw<=0 → 0、不写流水。
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)
	_, err = repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)
	n, err = repo.ClawbackByOrder(ctx, orderID, 0, 100)
	require.NoError(t, err)
	require.EqualValues(t, 0, n)

	// RedeemToBalance：points<=0 → ErrPointsAmountInvalid。
	_, err = repo.RedeemToBalance(ctx, user.ID, 0, 0, pointsTestPeg)
	require.ErrorIs(t, err, service.ErrPointsAmountInvalid)

	// DeductForPlan：points<=0 → ErrPointsAmountInvalid；额度不足 → ErrPointsInsufficient。
	err = repo.DeductForPlan(ctx, user.ID, 0, pointsTestPeg, "n", "")
	require.ErrorIs(t, err, service.ErrPointsAmountInvalid)
	noFunds := mustCreatePointsUser(t, "user")
	_, err = repo.EnsureAccount(ctx, noFunds.ID)
	require.NoError(t, err)
	err = repo.DeductForPlan(ctx, noFunds.ID, 1_000_000, pointsTestPeg, "n", "")
	require.ErrorIs(t, err, service.ErrPointsInsufficient)
}
