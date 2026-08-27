//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestInitLegacyInviteDB_Disabled 验证「没开这个功能就不连旧站库」。
//
// 单站部署根本不会填 legacy_invite 这一节，此时返回 (nil, nil) 是正常路径而非错误——
// 如果这里报错，整个进程会起不来。
func TestInitLegacyInviteDB_Disabled(t *testing.T) {
	t.Parallel()

	db, err := InitLegacyInviteDB(nil)
	require.NoError(t, err)
	require.Nil(t, db)

	cfg := &config.Config{}
	cfg.LegacyInvite.Enabled = false
	// 即便填了连接参数，只要开关是关的就不该去连
	cfg.LegacyInvite.Host = "127.0.0.1"
	cfg.LegacyInvite.Port = 1
	db, err = InitLegacyInviteDB(cfg)
	require.NoError(t, err)
	require.Nil(t, db)
}

// TestLegacyInviteQueryTimeout 验证超时取值：未配置或非正数都回落到 5 秒。
func TestLegacyInviteQueryTimeout(t *testing.T) {
	t.Parallel()

	require.Equal(t, 5*time.Second, legacyInviteQueryTimeout(nil))

	cfg := &config.Config{}
	require.Equal(t, 5*time.Second, legacyInviteQueryTimeout(cfg))

	cfg.LegacyInvite.QueryTimeoutSeconds = 0
	require.Equal(t, 5*time.Second, legacyInviteQueryTimeout(cfg))

	cfg.LegacyInvite.QueryTimeoutSeconds = 12
	require.Equal(t, 12*time.Second, legacyInviteQueryTimeout(cfg))
}

// TestNewLegacyPaidLookup_NilIsTrulyNil 是这组测试里最关键的一条。
//
// 上层用 `lookup != nil` 判断功能是否可用。如果这里返回的是一个包着 nil 指针的接口值，
// 那个判断会恒为 true，功能会被误认为「已配置」，最终在第一次查询时空指针崩溃。
// 所以必须断言拿到的是真正的 nil 接口。
func TestNewLegacyPaidLookup_NilIsTrulyNil(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewLegacyPaidLookup(nil, nil))
	require.Nil(t, NewLegacyPaidLookup(&LegacyInviteDB{}, nil))

	var lookup service.LegacyPaidLookup = NewLegacyPaidLookup(nil, nil)
	require.True(t, lookup == nil, "must be an untyped nil interface, not a nil pointer wrapped in one")
}

// TestLegacyPaidLookup_FindPaidUser 覆盖跨库查询的三种结果：查到、查不到、查询出错。
func TestLegacyPaidLookup_FindPaidUser(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT u.id").
			WithArgs("user@example.com", sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "paid"}).
				AddRow(int64(42), "user@example.com", 512.5))

		lookup := &legacyPaidLookup{db: db, timeout: time.Second}
		got, err := lookup.FindPaidUser(context.Background(), "user@example.com")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, int64(42), got.UserID)
		require.Equal(t, "user@example.com", got.Email)
		require.InDelta(t, 512.5, got.PaidAmount, 0.001)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found returns nil without error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT u.id").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "paid"}))

		lookup := &legacyPaidLookup{db: db, timeout: time.Second}
		got, err := lookup.FindPaidUser(context.Background(), "nobody@example.com")
		require.NoError(t, err, "旧站没这个邮箱不是错误，上层会当作不达标处理")
		require.Nil(t, got)
	})

	t.Run("query error propagates", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("SELECT u.id").WillReturnError(errors.New("connection refused"))

		lookup := &legacyPaidLookup{db: db, timeout: time.Second}
		got, err := lookup.FindPaidUser(context.Background(), "user@example.com")
		// 查询失败必须报错，绝不能用「查不到」冒充「不达标」
		require.Error(t, err)
		require.Nil(t, got)
	})
}

// TestLegacyPaidLookup_Ping 覆盖健康检查。
func TestLegacyPaidLookup_Ping(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectPing()
	lookup := &legacyPaidLookup{db: db, timeout: time.Second}
	require.NoError(t, lookup.Ping(context.Background()))
}

// TestLegacyInviteClaimRepository_GetByEmail 覆盖领取记录的查询。
func TestLegacyInviteClaimRepository_GetByEmail(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		mock.ExpectQuery("FROM legacy_invite_claims").
			WithArgs("user@example.com").
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "email", "legacy_user_id", "paid_amount", "redeem_code", "claimed_ip", "created_at",
			}).AddRow(int64(1), "user@example.com", int64(42), 512.5, "CODE-1", "1.2.3.4", now))

		repo := NewLegacyInviteClaimRepository(db)
		got, err := repo.GetByEmail(context.Background(), "user@example.com")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "CODE-1", got.RedeemCode)
		require.Equal(t, int64(42), got.LegacyUserID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("absent returns nil", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("FROM legacy_invite_claims").
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "email", "legacy_user_id", "paid_amount", "redeem_code", "claimed_ip", "created_at",
			}))

		repo := NewLegacyInviteClaimRepository(db)
		got, err := repo.GetByEmail(context.Background(), "nobody@example.com")
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

// TestLegacyInviteClaimRepository_Create 覆盖落库，重点是唯一冲突的翻译。
func TestLegacyInviteClaimRepository_Create(t *testing.T) {
	t.Parallel()

	t.Run("success fills id and created_at", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		now := time.Now()
		mock.ExpectQuery("INSERT INTO legacy_invite_claims").
			WithArgs("user@example.com", int64(42), 512.5, "CODE-1", "1.2.3.4").
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(7), now))

		repo := NewLegacyInviteClaimRepository(db)
		claim := &service.LegacyInviteClaim{
			Email:        "user@example.com",
			LegacyUserID: 42,
			PaidAmount:   512.5,
			RedeemCode:   "CODE-1",
			ClaimedIP:    "1.2.3.4",
		}
		require.NoError(t, repo.Create(context.Background(), claim))
		require.Equal(t, int64(7), claim.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("unique violation becomes already-claimed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("INSERT INTO legacy_invite_claims").
			WillReturnError(&pq.Error{Code: "23505"})

		repo := NewLegacyInviteClaimRepository(db)
		err = repo.Create(context.Background(), &service.LegacyInviteClaim{Email: "user@example.com"})
		// 这条翻译是「每人一个码」在并发下的最后一道闸门，必须精确
		require.ErrorIs(t, err, service.ErrLegacyInviteAlreadyClaimed)
	})

	t.Run("other errors are not swallowed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		mock.ExpectQuery("INSERT INTO legacy_invite_claims").
			WillReturnError(&pq.Error{Code: "23502"})

		repo := NewLegacyInviteClaimRepository(db)
		err = repo.Create(context.Background(), &service.LegacyInviteClaim{Email: "user@example.com"})
		require.Error(t, err)
		require.NotErrorIs(t, err, service.ErrLegacyInviteAlreadyClaimed)
	})

	t.Run("nil claim is rejected", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()

		repo := NewLegacyInviteClaimRepository(db)
		require.Error(t, repo.Create(context.Background(), nil))
	})
}
