//go:build unit

package service_test

import (
	"context"
	"database/sql"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// stealingRedeemRepo 在真正占码前一刻让“另一个注册”抢先把码标成已用，
// 用真实的条件更新（WHERE status='unused'）复现并发败者的路径。
//
// 抢码必须走 ctx 里的注册事务：内存 SQLite（cache=shared）同一时刻只允许一个写事务，
// 注册事务已插入用户并持有写锁，若用事务外的连接去更新会互相等锁而卡死。
// 在同一事务里先把码标成已用，等价于“另一注册已提交占用”，随后内层 Use 的条件更新
// 落空（affected==0 → ErrRedeemCodeUsed）；事务回滚时抢码与建号一起撤销。
type stealingRedeemRepo struct {
	service.RedeemCodeRepository
	client   *dbent.Client
	innerErr error
}

func (r *stealingRedeemRepo) Use(ctx context.Context, id, userID int64) error {
	c := r.client
	if tx := dbent.TxFromContext(ctx); tx != nil {
		c = tx.Client()
	}
	if err := c.RedeemCode.UpdateOneID(id).SetStatus(service.StatusUsed).Exec(ctx); err != nil {
		r.innerErr = err
		return err
	}
	r.innerErr = r.RedeemCodeRepository.Use(ctx, id, userID)
	return r.innerErr
}

func newInvitationTxAuthService(
	t *testing.T,
	dsnName string,
	wrapRedeem func(service.RedeemCodeRepository, *dbent.Client) service.RedeemCodeRepository,
) (*service.AuthService, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dsnName+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		JWT:     config.JWTConfig{Secret: "test-invitation-tx-secret", ExpireHour: 1},
		Default: config.DefaultConfig{UserBalance: 0, UserConcurrency: 1},
	}
	settingSvc := service.NewSettingService(&authIdentitySettingRepoStub{values: map[string]string{
		service.SettingKeyRegistrationEnabled:                            "true",
		service.SignupSourceEnabledSettingKey(service.SignupSourceEmail): "true",
		service.SettingKeyInvitationCodeEnabled:                          "true",
	}}, cfg)

	var redeemRepo service.RedeemCodeRepository = repository.NewRedeemCodeRepository(client)
	if wrapRedeem != nil {
		redeemRepo = wrapRedeem(redeemRepo, client)
	}
	svc := service.NewAuthService(client, repository.NewUserRepository(client, db), redeemRepo, nil, cfg, settingSvc, nil, nil, nil, nil, nil, nil, nil)
	return svc, client
}

func seedInvitationCode(t *testing.T, client *dbent.Client, code string) int64 {
	t.Helper()
	c, err := client.RedeemCode.Create().
		SetCode(code).
		SetType(service.RedeemTypeInvitation).
		SetStatus(service.StatusUnused).
		SetValue(0).
		Save(context.Background())
	require.NoError(t, err)
	return c.ID
}

// 事务路径：建号与占码在同一个 ent 事务里一起提交。
func TestRegisterWithInvitationCommitsUserAndClaimTogether(t *testing.T) {
	svc, client := newInvitationTxAuthService(t, "auth_invitation_tx_commit", nil)
	ctx := context.Background()
	codeID := seedInvitationCode(t, client, "TXINV-COMMIT")

	token, registered, err := svc.RegisterWithVerification(ctx, "tx-commit@example.com", "password", "", "", "TXINV-COMMIT", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, registered)

	stored, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, stored.Status)
	require.NotNil(t, stored.UsedBy)
	require.Equal(t, registered.ID, *stored.UsedBy)

	// 同一个码再注册一次必须被拒绝，且不产生第二个账号。
	_, _, err = svc.RegisterWithVerification(ctx, "tx-commit-2@example.com", "password", "", "", "TXINV-COMMIT", "")
	require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
	n, err := client.User.Query().Where(user.EmailEQ("tx-commit-2@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

// 事务路径：检查时码还是 unused，占码时已被抢走——整笔事务回滚，账号不落库。
func TestRegisterWithInvitationRollsBackUserWhenClaimLost(t *testing.T) {
	var stealer *stealingRedeemRepo
	svc, client := newInvitationTxAuthService(t, "auth_invitation_tx_lost",
		func(inner service.RedeemCodeRepository, c *dbent.Client) service.RedeemCodeRepository {
			stealer = &stealingRedeemRepo{RedeemCodeRepository: inner, client: c}
			return stealer
		})
	ctx := context.Background()
	codeID := seedInvitationCode(t, client, "TXINV-LOST")

	_, _, err := svc.RegisterWithVerification(ctx, "tx-lost@example.com", "password", "", "", "TXINV-LOST", "")
	require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
	// 确认走的是真实的条件更新落空分支，而不是数据库错误被映射成 INVALID。
	require.ErrorIs(t, stealer.innerErr, service.ErrRedeemCodeUsed)

	n, err := client.User.Query().Where(user.EmailEQ("tx-lost@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "占码失败时建号必须随事务一起回滚")

	stored, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
	require.NoError(t, err)
	require.Nil(t, stored.UsedBy, "败者不能被记为 used_by")
}
