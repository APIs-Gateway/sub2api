//go:build integration

package repository

import (
	"context"
	"database/sql"
	"math"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	_ "modernc.org/sqlite"
)

// This proves the refund repository's new primitive, not full MySQL bootstrap
// (the existing historical migration 184 limitation remains independent).
func TestRefundAtomicPostgres_RefundBalanceDialectContract(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"postgres", "sqlite", "mysql84"} {
		t.Run(name, func(t *testing.T) {
			var db *sql.DB
			var client *dbent.Client
			var userID int64 = 1
			if name == "postgres" {
				var user *dbent.User
				client, _, _, user, _ = refundAtomicFixture(t, 10, service.OrderStatusCompleted)
				db, userID = inflightTestDB(t), user.ID
			} else {
				driver, dialectName, dsn := "sqlite", dialect.SQLite, ":memory:"
				if name == "mysql84" {
					container, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("refund_atomic"), tcmysql.WithUsername("root"), tcmysql.WithPassword("refund_atomic"))
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
					dsn, err = container.ConnectionString(ctx)
					require.NoError(t, err)
					driver, dialectName = "mysql", dialect.MySQL
				}
				var err error
				db, err = sql.Open(driver, dsn)
				require.NoError(t, err)
				db.SetMaxOpenConns(1)
				client = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialectName, db)))
				t.Cleanup(func() { require.NoError(t, client.Close()) })
				_, err = db.Exec(`CREATE TABLE users (id BIGINT PRIMARY KEY, balance DECIMAL(20,8) NOT NULL, deleted_at DATETIME NULL, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO users (id,balance) VALUES (1,10)`)
				require.NoError(t, err)
			}
			repo := newUserRepositoryWithSQL(client, db)
			for _, tc := range []struct {
				name                                       string
				balance, requested, wantDebit, wantBalance float64
				partial, insufficient                      bool
			}{
				{"full", 10, 3, 3, 7, false, false},
				{"requires force", 2, 10, 0, 2, false, true},
				{"forced actual", 2, 10, 2, 0, true, false},
				{"zero", 0, 10, 0, 0, true, false},
				{"negative untouched", -5, 10, 0, -5, true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, err := client.User.Update().Where(dbuser.IDEQ(userID)).SetBalance(tc.balance).Save(ctx)
					require.NoError(t, err)
					debit, err := repo.DeductRefundBalance(ctx, userID, tc.requested, tc.partial)
					if tc.insufficient {
						require.ErrorIs(t, err, service.ErrRefundBalanceInsufficient)
					} else {
						require.NoError(t, err)
					}
					require.Equal(t, tc.wantDebit, debit)
					user, err := client.User.Query().Where(dbuser.IDEQ(userID)).Select("id", "balance").Only(ctx)
					require.NoError(t, err)
					require.Equal(t, tc.wantBalance, user.Balance)
				})
			}
			_, err := repo.DeductRefundBalance(ctx, userID+999999, 1, true)
			require.ErrorIs(t, err, service.ErrUserNotFound)
			for _, amount := range []float64{0, -1, math.NaN(), math.Inf(1)} {
				_, err = repo.DeductRefundBalance(ctx, userID, amount, true)
				require.Error(t, err)
			}
			// An outer transaction owns the debit; rolling it back restores
			// the wallet rather than committing through a second connection.
			_, err = client.User.Update().Where(dbuser.IDEQ(userID)).SetBalance(10).Save(ctx)
			require.NoError(t, err)
			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			debit, err := repo.DeductRefundBalance(dbent.NewTxContext(ctx, tx), userID, 4, false)
			require.NoError(t, err)
			require.Equal(t, 4.0, debit)
			require.NoError(t, tx.Rollback())
			user, err := client.User.Query().Where(dbuser.IDEQ(userID)).Select("id", "balance").Only(ctx)
			require.NoError(t, err)
			require.Equal(t, 10.0, user.Balance)
		})
	}
}
