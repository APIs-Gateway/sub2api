package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/pkg/emailcanon"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newUserEntRepo(t *testing.T) (*userRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return newUserRepositoryWithSQL(client, db), client
}

func TestUserRepositoryGetByEmailNormalizesLegacySpacingAndCase(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &service.User{
		Email:        " Legacy@Example.com ",
		Username:     "legacy-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})
	require.NoError(t, err)

	got, err := repo.GetByEmail(ctx, "legacy@example.com")
	require.NoError(t, err)
	require.Equal(t, " Legacy@Example.com ", got.Email)
}

func TestUserRepositoryExistsByEmailNormalizesLegacySpacingAndCase(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &service.User{
		Email:        " Legacy@Example.com ",
		Username:     "legacy-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})
	require.NoError(t, err)

	exists, err := repo.ExistsByEmail(ctx, "  LEGACY@example.com  ")
	require.NoError(t, err)
	require.True(t, exists)
}

func TestEmailAliasOwnerHandlesDefensiveInputs(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	previousAliasFilterEnabled := emailcanon.Enabled()
	emailcanon.SetEnabled(true)
	t.Cleanup(func() { emailcanon.SetEnabled(previousAliasFilterEnabled) })

	_, exists, err := emailAliasOwnerIDWithClient(ctx, nil, "owner@gmail.com", 0)
	require.NoError(t, err)
	require.False(t, exists)

	_, exists, err = emailAliasOwnerIDWithClient(ctx, client, "@gmail.com", 0)
	require.NoError(t, err)
	require.False(t, exists)

	require.ErrorIs(t, repo.UpdateEmailWithAliasGuard(ctx, 0, "owner@gmail.com", "hash"), service.ErrUserNotFound)
	require.Error(t, repo.UpdateEmailWithAliasGuard(ctx, 1, "", "hash"))
	require.Error(t, repo.UpdateEmailWithAliasGuard(ctx, 1, "owner@gmail.com", ""))
	require.Error(t, repo.UpdateEmailWithAliasGuard(ctx, 1, "owner@gmail.com", "hash"))
}

func TestEmailAliasOwnerRejectsMoreThanFiftyHistoricalAliases(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	previousAliasFilterEnabled := emailcanon.Enabled()
	emailcanon.SetEnabled(true)
	t.Cleanup(func() { emailcanon.SetEnabled(previousAliasFilterEnabled) })

	// These rows model accounts created before alias filtering was enabled. Each
	// spelling has the same canonical Gmail inbox but a distinct historical owner.
	for i := 0; i < 51; i++ {
		_, err := client.User.Create().
			SetEmail(fmt.Sprintf("his.tory.owner+legacy-%d@googlemail.com", i)).
			SetPasswordHash("hash").
			SetUsername(fmt.Sprintf("historical-alias-owner-%d", i)).
			Save(ctx)
		require.NoError(t, err)
	}

	currentUser, err := client.User.Create().
		SetEmail("current-owner@example.com").
		SetPasswordHash("hash").
		SetUsername("current-owner").
		Save(ctx)
	require.NoError(t, err)

	ownerID, exists, err := emailAliasOwnerIDWithClient(ctx, client, "history.owner+new@gmail.com", currentUser.ID)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotEqual(t, currentUser.ID, ownerID)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	txCtx := dbent.NewTxContext(ctx, tx)
	require.ErrorIs(
		t,
		repo.UpdateEmailWithAliasGuard(txCtx, currentUser.ID, "history.owner+new@gmail.com", "new-hash"),
		service.ErrEmailExists,
	)
}

func TestEmailAliasOwnerRejectsGmailFamilyFQDNTrailingDotAliases(t *testing.T) {
	previousAliasFilterEnabled := emailcanon.Enabled()
	emailcanon.SetEnabled(true)
	t.Cleanup(func() { emailcanon.SetEnabled(previousAliasFilterEnabled) })

	cases := []struct {
		name      string
		historical string
		requested  string
	}{
		{
			name:       "gmail trailing dot request matches googlemail dot tag history",
			historical: "f.oo+legacy@googlemail.com",
			requested:  "foo+new@gmail.com.",
		},
		{
			name:       "googlemail trailing dot request matches gmail dot tag history",
			historical: "f.oo+legacy@gmail.com.",
			requested:  "foo+new@googlemail.com.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, client := newUserEntRepo(t)
			ctx := context.Background()

			owner, err := client.User.Create().
				SetEmail(tc.historical).
				SetPasswordHash("hash").
				SetUsername("historical-owner").
				Save(ctx)
			require.NoError(t, err)
			currentUser, err := client.User.Create().
				SetEmail("current-owner@example.com").
				SetPasswordHash("hash").
				SetUsername("current-owner").
				Save(ctx)
			require.NoError(t, err)

			ownerID, exists, err := emailAliasOwnerIDWithClient(ctx, client, tc.requested, currentUser.ID)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, owner.ID, ownerID)

			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			txCtx := dbent.NewTxContext(ctx, tx)
			require.ErrorIs(
				t,
				repo.UpdateEmailWithAliasGuard(txCtx, currentUser.ID, tc.requested, "new-hash"),
				service.ErrEmailExists,
			)
		})
	}
}

func TestEmailAliasOwnerReportsQueryAndPersistenceErrors(t *testing.T) {
	_, client := newUserEntRepo(t)
	ctx := context.Background()

	previousAliasFilterEnabled := emailcanon.Enabled()
	emailcanon.SetEnabled(true)
	t.Cleanup(func() { emailcanon.SetEnabled(previousAliasFilterEnabled) })

	require.NoError(t, client.Close())
	_, _, err := emailAliasOwnerIDWithClient(ctx, client, "owner@gmail.com", 0)
	require.Error(t, err)

	// Use a separate fixture because the client above is intentionally closed.
	repo, client := newUserEntRepo(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	txCtx := dbent.NewTxContext(ctx, tx)
	require.ErrorIs(
		t,
		repo.UpdateEmailWithAliasGuard(txCtx, 999999, "missing-user@gmail.com", "hash"),
		service.ErrUserNotFound,
	)
}

func TestUserRepositoryCreateRejectsNormalizedEmailDuplicate(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &service.User{
		Email:        " Existing@Example.com ",
		Username:     "existing-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})
	require.NoError(t, err)

	err = repo.Create(ctx, &service.User{
		Email:        "existing@example.com",
		Username:     "duplicate-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})
	require.ErrorIs(t, err, service.ErrEmailExists)
}

func TestUserRepositoryUpdateRejectsNormalizedEmailDuplicate(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	first := &service.User{
		Email:        " Existing@Example.com ",
		Username:     "existing-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, first))

	second := &service.User{
		Email:        "second@example.com",
		Username:     "second-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, second))

	second.Email = " existing@example.com "
	err := repo.Update(ctx, second)
	require.ErrorIs(t, err, service.ErrEmailExists)
}

func TestUserRepositoryGetByEmailReportsNormalizedEmailConflict(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	_, err := client.User.Create().
		SetEmail("Conflict@Example.com").
		SetUsername("conflict-user-1").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.User.Create().
		SetEmail(" conflict@example.com ").
		SetUsername("conflict-user-2").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = repo.GetByEmail(ctx, "conflict@example.com")
	require.Error(t, err)
	require.ErrorContains(t, err, "normalized email lookup matched multiple users")
}

func TestUserRepositoryCreateSerializesNormalizedEmailConflictsUnderConcurrency(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	firstCreateStarted := make(chan struct{})
	releaseFirstCreate := make(chan struct{})
	var firstCreate sync.Once
	client.User.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			blocked := false
			if m.Op().Is(dbent.OpCreate) {
				firstCreate.Do(func() {
					blocked = true
					close(firstCreateStarted)
				})
			}
			if blocked {
				<-releaseFirstCreate
			}
			return next.Mutate(ctx, m)
		})
	})

	type createResult struct {
		err error
	}

	results := make(chan createResult, 2)
	go func() {
		results <- createResult{err: repo.Create(ctx, &service.User{
			Email:        " Race@Example.com ",
			Username:     "race-user-1",
			PasswordHash: "hash",
			Role:         service.RoleUser,
			Status:       service.StatusActive,
		})}
	}()

	<-firstCreateStarted

	go func() {
		results <- createResult{err: repo.Create(ctx, &service.User{
			Email:        "race@example.com",
			Username:     "race-user-2",
			PasswordHash: "hash",
			Role:         service.RoleUser,
			Status:       service.StatusActive,
		})}
	}()

	time.Sleep(100 * time.Millisecond)
	close(releaseFirstCreate)

	first := <-results
	second := <-results

	errors := []error{first.err, second.err}
	successes := 0
	conflicts := 0
	for _, err := range errors {
		switch err {
		case nil:
			successes++
		case service.ErrEmailExists:
			conflicts++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	count, err := client.User.Query().Where(userEmailLookupPredicate("race@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
