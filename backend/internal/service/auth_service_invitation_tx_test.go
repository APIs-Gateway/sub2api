//go:build unit

package service_test

import (
	"context"
	"database/sql"
	"errors"
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

// txFaultDriver 包一层 ent driver，按需让开事务或提交事务失败，
// 用来覆盖注册事务自身出错（而非占码落空）的路径。
type txFaultDriver struct {
	dialect.Driver
	beginErr  error
	commitErr error
}

func (d *txFaultDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, commitErr: d.commitErr}, nil
}

type commitFaultTx struct {
	dialect.Tx
	commitErr error
}

func (t *commitFaultTx) Commit() error {
	if t.commitErr != nil {
		// 模拟提交失败：底层事务被撤销，已写入的用户和占码都不能生效。
		_ = t.Tx.Rollback()
		return t.commitErr
	}
	return t.Tx.Commit()
}

func newInvitationTxAuthService(
	t *testing.T,
	dsnName string,
	wrapRedeem func(service.RedeemCodeRepository, *dbent.Client) service.RedeemCodeRepository,
) (*service.AuthService, *dbent.Client) {
	t.Helper()
	return newInvitationTxAuthServiceWithTxDriver(t, dsnName, wrapRedeem, nil)
}

// newInvitationTxAuthServiceWithTxDriver 与 newInvitationTxAuthService 相同，但允许替换
// AuthService 用来开注册事务的 ent client 的 driver（仓储仍用未包装的 client）。
func newInvitationTxAuthServiceWithTxDriver(
	t *testing.T,
	dsnName string,
	wrapRedeem func(service.RedeemCodeRepository, *dbent.Client) service.RedeemCodeRepository,
	wrapTxDriver func(dialect.Driver) dialect.Driver,
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
	txClient := client
	if wrapTxDriver != nil {
		txClient = dbent.NewClient(dbent.Driver(wrapTxDriver(drv)))
	}
	svc := service.NewAuthService(txClient, repository.NewUserRepository(client, db), redeemRepo, nil, cfg, settingSvc, nil, nil, nil, nil, nil, nil, nil)
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

// 事务路径：开不了注册事务时直接返回服务不可用，不建号、不占码。
func TestRegisterWithInvitationFailsClosedWhenTxCannotStart(t *testing.T) {
	svc, client := newInvitationTxAuthServiceWithTxDriver(t, "auth_invitation_tx_begin_fail", nil,
		func(d dialect.Driver) dialect.Driver {
			return &txFaultDriver{Driver: d, beginErr: errors.New("begin exploded")}
		})
	ctx := context.Background()
	codeID := seedInvitationCode(t, client, "TXINV-BEGIN")

	_, _, err := svc.RegisterWithVerification(ctx, "tx-begin@example.com", "password", "", "", "TXINV-BEGIN", "")
	require.ErrorIs(t, err, service.ErrServiceUnavailable)

	n, err := client.User.Query().Where(user.EmailEQ("tx-begin@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	stored, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, service.StatusUnused, stored.Status)
	require.Nil(t, stored.UsedBy)
}

// 事务路径：建号和占码都成功但提交失败——返回服务不可用，账号和占码一起作废，码可再用。
func TestRegisterWithInvitationFailsClosedWhenCommitFails(t *testing.T) {
	svc, client := newInvitationTxAuthServiceWithTxDriver(t, "auth_invitation_tx_commit_fail", nil,
		func(d dialect.Driver) dialect.Driver {
			return &txFaultDriver{Driver: d, commitErr: errors.New("commit exploded")}
		})
	ctx := context.Background()
	codeID := seedInvitationCode(t, client, "TXINV-COMMITFAIL")

	_, _, err := svc.RegisterWithVerification(ctx, "tx-commitfail@example.com", "password", "", "", "TXINV-COMMITFAIL", "")
	require.ErrorIs(t, err, service.ErrServiceUnavailable)

	n, err := client.User.Query().Where(user.EmailEQ("tx-commitfail@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "提交失败时账号不能落库")

	stored, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(codeID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, service.StatusUnused, stored.Status, "提交失败时注册码不能被烧掉")
	require.Nil(t, stored.UsedBy)
}
