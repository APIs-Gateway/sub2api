//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）仓储层集成测试：真实 PG，覆盖单测覆盖不到的部分——
// partial-unique 幂等、frozen/available 拆分、clawback 转负、冻结到期解冻、
// 提现 hold/one-pending/审核状态机、以及 NULL 行原始 SQL 扫描（docs/notes/nullable-column-rawsql-scan.md）。
//
// 全部用持久化 client（integrationEntClient）+ 每测唯一用户：production-faithful，
// 且避免「withTx 复用 TxFromContext 时 unique 违例毒化外层 tx」的陷阱（见提现 one-pending 用例）。

const pointsTestPeg = 0.01

var pointsTestSeq int64

func pointsUniq() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&pointsTestSeq, 1))
}

func newPointsRepo() service.PointsRepository {
	return NewPointsRepository(integrationEntClient, integrationDB)
}

func mustCreatePointsUser(t *testing.T, role string) *service.User {
	t.Helper()
	roleConst := service.RoleUser
	if role == "admin" {
		roleConst = service.RoleAdmin
	}
	return mustCreateUser(t, integrationEntClient, &service.User{
		Email:       fmt.Sprintf("points-%s-%s@example.com", role, pointsUniq()),
		Username:    "pts_" + pointsUniq(),
		Role:        roleConst,
		Status:      service.StatusActive,
		Concurrency: 5,
	})
}

// mustCreatePointsOrder 建一张最小法币订单，仅用于满足 earn/clawback 的 source_order_id FK。
func mustCreatePointsOrder(t *testing.T, u *service.User, amount float64) int64 {
	t.Helper()
	now := time.Now()
	suffix := pointsUniq()
	order, err := integrationEntClient.PaymentOrder.Create().
		SetUserID(u.ID).
		SetUserEmail(u.Email).
		SetUserName(u.Username).
		SetAmount(amount).
		SetPayAmount(amount).
		SetRechargeCode("PTS-" + suffix).
		SetOutTradeNo("pts_out_" + suffix).
		SetPaymentType("alipay").
		SetPaymentTradeNo("pts_trade_" + suffix).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(now).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err, "create points source order")
	return order.ID
}

func pointsLedgerCount(t *testing.T, userID int64, kind string) int {
	t.Helper()
	return querySingleInt(t, context.Background(), integrationEntClient,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id = $1 AND kind = $2`, userID, kind)
}

// --- earning ---

func TestPointsRepo_EarnPoints_NoFreeze_AndIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	in := service.EarnPointsInput{
		InviterID:     inviter.ID,
		SourceUserID:  invitee.ID,
		SourceOrderID: orderID,
		Points:        50,
		FreezeHours:   0,
		PegAt:         pointsTestPeg,
	}

	applied, err := repo.EarnPoints(ctx, in)
	require.NoError(t, err)
	require.True(t, applied, "first earn should be applied")

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct.Available)
	require.Equal(t, int64(0), acct.Frozen)
	require.Equal(t, int64(50), acct.LifetimeEarned)

	// 重放同一来源单：partial-unique on (user_id, source_order_id) WHERE kind='earn' → 不重复入账。
	applied2, err := repo.EarnPoints(ctx, in)
	require.NoError(t, err)
	require.False(t, applied2, "replayed earn must be idempotent (not applied)")

	acct2, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct2.Available, "available must not double on replay")
	require.Equal(t, int64(50), acct2.LifetimeEarned)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindEarn), "exactly one earn ledger row")

	// 快照回填：available_after = 50。
	availAfter := querySingleInt(t, ctx, integrationEntClient,
		`SELECT available_after FROM user_points_ledger WHERE user_id=$1 AND kind='earn' AND source_order_id=$2`,
		inviter.ID, orderID)
	require.Equal(t, 50, availAfter)
}

func TestPointsRepo_EarnPoints_RedeemSource_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	code := mustCreateRedeemCode(t, integrationEntClient, &service.RedeemCode{
		Code:  "PTS-RC-" + pointsUniq(),
		Type:  service.RedeemTypeBalance,
		Value: 100,
	})
	// 该 redeem_code 经全局 client 提交、不随测试 tx 回滚；清理掉以免污染 RedeemCode 列表类断言
	// （FK source_redeem_code_id ON DELETE SET NULL 会自动置空台账引用）。
	t.Cleanup(func() {
		_ = integrationEntClient.RedeemCode.DeleteOneID(code.ID).Exec(context.Background())
	})

	// 方案 C：兑换码来源 earning（source_redeem_code_id 锚）。
	in := service.EarnPointsInput{
		InviterID:          inviter.ID,
		SourceUserID:       invitee.ID,
		SourceRedeemCodeID: code.ID,
		Points:             50,
		PegAt:              pointsTestPeg,
	}
	applied, err := repo.EarnPoints(ctx, in)
	require.NoError(t, err)
	require.True(t, applied, "first redeem-source earn should apply")

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct.Available)

	// 重放同一兑换码：partial-unique on (user_id, source_redeem_code_id) WHERE kind='earn' → 不重复。
	applied2, err := repo.EarnPoints(ctx, in)
	require.NoError(t, err)
	require.False(t, applied2, "replayed redeem-source earn must be idempotent")

	acct2, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(50), acct2.Available, "available must not double on replay")

	// earn 流水:source_redeem_code_id 命中、source_order_id 为 NULL。
	cnt := querySingleInt(t, ctx, integrationEntClient,
		`SELECT COUNT(*) FROM user_points_ledger WHERE user_id=$1 AND kind='earn' AND source_redeem_code_id=$2 AND source_order_id IS NULL`,
		inviter.ID, code.ID)
	require.Equal(t, 1, cnt, "exactly one redeem-sourced earn row with NULL source_order_id")
}

func TestPointsRepo_EarnPoints_Freeze_ThenThaw(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	in := service.EarnPointsInput{
		InviterID:     inviter.ID,
		SourceUserID:  invitee.ID,
		SourceOrderID: orderID,
		Points:        80,
		FreezeHours:   24,
		PegAt:         pointsTestPeg,
	}
	applied, err := repo.EarnPoints(ctx, in)
	require.NoError(t, err)
	require.True(t, applied)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available, "frozen earn must not touch available")
	require.Equal(t, int64(80), acct.Frozen)

	// 尚未到期 → 不解冻。
	thawed, err := repo.ThawDuePoints(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), thawed)

	// 把冻结到期时间改到过去，模拟成熟。
	_, err = integrationDB.ExecContext(ctx,
		`UPDATE user_points_ledger SET frozen_until = NOW() - interval '1 hour'
		 WHERE user_id=$1 AND kind='earn' AND source_order_id=$2`, inviter.ID, orderID)
	require.NoError(t, err)

	thawed, err = repo.ThawDuePoints(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(80), thawed)

	acct, err = repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(80), acct.Available, "thaw moves frozen → available")
	require.Equal(t, int64(0), acct.Frozen)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindThaw), "one thaw ledger row")

	// 再解冻无新到期 → 0。
	thawed, err = repo.ThawDuePoints(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), thawed)
}

// --- clawback ---

func TestPointsRepo_Clawback_FullRefund_AllEarned_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	// 全额退（refund >= original）→ 撤回全部 earned。
	clawed, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), clawed)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindClawback))

	// 一单一撤：重复落单不再撤。
	clawed2, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), clawed2)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindClawback), "clawback stays idempotent")
}

func TestPointsRepo_Clawback_Partial_Floor(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 2)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 3, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	// 部分退按比例 floor：floor(3 × 1/2) = 1（.5 边界向下，不多撤）。
	clawed, err := repo.ClawbackByOrder(ctx, orderID, 1, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), clawed)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), acct.Available, "earned 3 − clawed 1 = 2")
}

func TestPointsRepo_Clawback_GoesNegative_AfterSpent(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	// 邀请人把积分花光（换余额）。
	_, err = repo.RedeemToBalance(ctx, inviter.ID, 100, service.PointsToBalance(100, pointsTestPeg), pointsTestPeg)
	require.NoError(t, err)
	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available)

	// 退款 clawback：可扣成负（欠账）。
	clawed, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), clawed)

	acct, err = repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(-100), acct.Available, "clawback may drive available negative (debt)")

	availAfter := querySingleInt(t, ctx, integrationEntClient,
		`SELECT available_after FROM user_points_ledger WHERE user_id=$1 AND kind='clawback' AND source_order_id=$2`,
		inviter.ID, orderID)
	require.Equal(t, -100, availAfter)
}

func TestPointsRepo_Clawback_DrainsFrozenFirst(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, FreezeHours: 24, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	clawed, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), clawed)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Frozen, "clawback drains frozen first")
	require.Equal(t, int64(0), acct.Available, "available untouched while frozen covers the claw")
}

func TestPointsRepo_Clawback_NoEarn_NoOp(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	clawed, err := repo.ClawbackByOrder(ctx, orderID, 100, 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), clawed, "no earn for this order → nothing to claw")
}

// --- redeem to balance ---

func TestPointsRepo_RedeemToBalance_DeductsAndCredits_InsufficientGuard(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	inviter := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: inviter.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	delta := service.PointsToBalance(100, pointsTestPeg) // 100 × 0.01 = 1.0
	newBalance, err := repo.RedeemToBalance(ctx, inviter.ID, 100, delta, pointsTestPeg)
	require.NoError(t, err)
	require.InDelta(t, 1.0, newBalance, 1e-9)

	acct, err := repo.GetAccount(ctx, inviter.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available)
	require.Equal(t, 1, pointsLedgerCount(t, inviter.ID, service.PointsKindToBalance))

	dbBalance := querySingleFloat(t, ctx, integrationEntClient,
		`SELECT balance::double precision FROM users WHERE id=$1`, inviter.ID)
	require.InDelta(t, 1.0, dbBalance, 1e-9)

	// 余额不足：available 已为 0 → 守卫拦截。
	_, err = repo.RedeemToBalance(ctx, inviter.ID, 1, service.PointsToBalance(1, pointsTestPeg), pointsTestPeg)
	require.ErrorIs(t, err, service.ErrPointsInsufficient)
}

// --- withdrawals ---

func TestPointsRepo_CreateWithdrawal_HoldsAndOnePendingGuard(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 200)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 200, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	gross, fee, net := service.ComputeWithdrawalAmounts(100, pointsTestPeg, 0)
	w, err := repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: 100,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: pointsTestPeg, FeePercentAt: 0,
		PayoutMethod:        service.PointsPayoutMethodAlipay,
		PayoutAlipayAccount: "13800000000",
		PayoutAlipayName:    "张三",
	})
	require.NoError(t, err)
	require.Equal(t, service.PointsWithdrawalStatusPending, w.Status)
	require.Equal(t, "13800000000", w.PayoutAlipayAccount)
	require.Equal(t, "张三", w.PayoutAlipayName)

	acct, err := repo.GetAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), acct.Available, "withdrawal holds 100 (200 − 100)")
	require.Equal(t, 1, pointsLedgerCount(t, user.ID, service.PointsKindWithdrawHold))

	// 第二张提现：available(100) 足够扣，但已有 pending → one-pending 唯一索引拦截。
	// 注：CreateWithdrawal 自起 tx，unique 违例回滚内层 tx，hold 被撤回（available 复原 100）。
	_, err = repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: 100,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: pointsTestPeg, FeePercentAt: 0,
		PayoutMethod:        service.PointsPayoutMethodAlipay,
		PayoutAlipayAccount: "13800000000",
		PayoutAlipayName:    "张三",
	})
	require.ErrorIs(t, err, service.ErrPointsWithdrawPending)

	acct, err = repo.GetAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), acct.Available, "failed second hold must roll back (available restored)")

	total := querySingleInt(t, ctx, integrationEntClient,
		`SELECT COUNT(*) FROM user_points_withdrawals WHERE user_id=$1`, user.ID)
	require.Equal(t, 1, total, "only one withdrawal row persisted")
}

func TestPointsRepo_ReviewWithdrawal_Approve_NonPendingGuard(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	admin := mustCreatePointsUser(t, "admin")
	user := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	gross, fee, net := service.ComputeWithdrawalAmounts(100, pointsTestPeg, 0)
	w, err := repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: 100,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: pointsTestPeg, PayoutMethod: service.PointsPayoutMethodUSDT,
		PayoutUSDTAddress: "TXxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
	})
	require.NoError(t, err)

	approved, err := repo.ReviewWithdrawal(ctx, w.ID, admin.ID, true, "", "tx-proof-hash")
	require.NoError(t, err)
	require.Equal(t, service.PointsWithdrawalStatusPaid, approved.Status)
	require.NotNil(t, approved.ReviewedBy)
	require.Equal(t, admin.ID, *approved.ReviewedBy)
	require.Equal(t, "tx-proof-hash", approved.PayoutProof)

	// 通过后积分保持已扣（钱已打出），不回补。
	acct, err := repo.GetAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available)
	require.Equal(t, 1, pointsLedgerCount(t, user.ID, service.PointsKindWithdrawPaid))

	// 已非 pending → 再审拒绝。
	_, err = repo.ReviewWithdrawal(ctx, w.ID, admin.ID, true, "", "")
	require.ErrorIs(t, err, service.ErrPointsWithdrawNotPending)
}

func TestPointsRepo_ReviewWithdrawal_Reject_RefundsPoints(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	admin := mustCreatePointsUser(t, "admin")
	user := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	_, err := repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	gross, fee, net := service.ComputeWithdrawalAmounts(100, pointsTestPeg, 0)
	w, err := repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: 100,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: pointsTestPeg, PayoutMethod: service.PointsPayoutMethodAlipay,
		PayoutAlipayAccount: "alice@example.com", PayoutAlipayName: "李四",
	})
	require.NoError(t, err)

	acct, err := repo.GetAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), acct.Available, "hold drained available to 0")

	rejected, err := repo.ReviewWithdrawal(ctx, w.ID, admin.ID, false, "信息有误", "")
	require.NoError(t, err)
	require.Equal(t, service.PointsWithdrawalStatusRejected, rejected.Status)
	require.Equal(t, "信息有误", rejected.ReviewNote)

	acct, err = repo.GetAccount(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(100), acct.Available, "reject refunds held points")
	require.Equal(t, 1, pointsLedgerCount(t, user.ID, service.PointsKindWithdrawRefund))
}

// --- NULL 行原始 SQL 扫描（docs/notes/nullable-column-rawsql-scan.md / §9 NULL 行扫描）---

func TestPointsRepo_NullRowScan_LedgerAndWithdrawal(t *testing.T) {
	ctx := context.Background()
	repo := newPointsRepo()
	user := mustCreatePointsUser(t, "user")
	invitee := mustCreatePointsUser(t, "user")
	orderID := mustCreatePointsOrder(t, invitee, 100)

	// 直接插一行 'adjust'：peg_at/source_*/withdrawal_id/frozen_until/available_after/frozen_after/note 全 NULL。
	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO user_points_ledger (user_id, kind, points, created_at, updated_at)
		 VALUES ($1, 'adjust', 5, NOW(), NOW())`, user.ID)
	require.NoError(t, err)

	entries, total, err := repo.ListUserLedger(ctx, user.ID, 1, 20)
	require.NoError(t, err, "scanning ledger rows with NULL nullable columns must not error")
	require.Equal(t, int64(1), total)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, service.PointsKindAdjust, e.Kind)
	require.Equal(t, int64(5), e.Points)
	require.Nil(t, e.PegAt)
	require.Nil(t, e.SourceUserID)
	require.Nil(t, e.SourceOrderID)
	require.Nil(t, e.WithdrawalID)
	require.Nil(t, e.FrozenUntil)
	require.Nil(t, e.AvailableAfter)
	require.Nil(t, e.FrozenAfter)
	require.Equal(t, "", e.Note)

	// USDT 提现：peg_at=0→NULL、fee_percent_at=0→NULL、alipay 字段 NULL → 扫描须干净。
	_, err = repo.EarnPoints(ctx, service.EarnPointsInput{
		InviterID: user.ID, SourceUserID: invitee.ID, SourceOrderID: orderID,
		Points: 100, PegAt: pointsTestPeg,
	})
	require.NoError(t, err)

	gross, fee, net := service.ComputeWithdrawalAmounts(50, pointsTestPeg, 0)
	w, err := repo.CreateWithdrawal(ctx, service.CreateWithdrawalInput{
		UserID: user.ID, Points: 50,
		GrossAmount: gross, FeeAmount: fee, NetAmount: net,
		PegAt: 0, FeePercentAt: 0, // 故意置 0 → 落库为 NULL
		PayoutMethod:      service.PointsPayoutMethodUSDT,
		PayoutUSDTAddress: "TYyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
	})
	require.NoError(t, err)
	require.Nil(t, w.PegAt)
	require.Nil(t, w.FeePercentAt)
	require.Equal(t, "", w.PayoutAlipayAccount)
	require.Equal(t, "", w.PayoutAlipayName)
	require.Equal(t, "TYyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", w.PayoutUSDTAddress)

	list, err := repo.ListUserWithdrawals(ctx, user.ID, 50)
	require.NoError(t, err, "scanning withdrawal rows with NULL columns must not error")
	require.Len(t, list, 1)
	require.Nil(t, list[0].PegAt)
	require.Equal(t, service.PointsPayoutMethodUSDT, list[0].PayoutMethod)

	// GetWithdrawal 单行同样路径。
	got, err := repo.GetWithdrawal(ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, w.ID, got.ID)

	// 不存在的提现单 → ErrPointsWithdrawalNotFound。
	_, err = repo.GetWithdrawal(ctx, 999999999)
	require.True(t, errors.Is(err, service.ErrPointsWithdrawalNotFound))
}
