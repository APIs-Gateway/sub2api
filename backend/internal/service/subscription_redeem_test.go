//go:build unit

package service_test

import (
	"context"
	"database/sql"
	"math"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newRedeemSubscriptionTestEnt(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:subscription_redeem?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return db, client
}

func TestApplyRedeemSubscription_MergesIntoExistingActiveCard(t *testing.T) {
	ctx := context.Background()
	db, client := newRedeemSubscriptionTestEnt(t)

	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	userSubRepo := repository.NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(groupRepo, userSubRepo, userRepo, nil, nil, client, nil, nil)

	group, err := client.Group.Create().
		SetName("Legacy Subscription Plan").
		SetPlatform(service.PlatformOpenAI).
		SetRateMultiplier(1).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeSubscription).
		SetDailyLimitUsd(30).
		Save(ctx)
	require.NoError(t, err)

	user, err := client.User.Create().
		SetEmail("redeem-merge@example.com").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(0).
		SetConcurrency(20).
		Save(ctx)
	require.NoError(t, err)

	active, err := svc.AssignSubscription(ctx, &service.AssignSubscriptionInput{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 30,
		ValidityDays:   10,
	})
	require.NoError(t, err)
	today := service.TodayEastDayNumber()
	_, err = client.UserSubscription.UpdateOneID(active.ID).
		SetTodayDay(today - 1).
		SetTodayRemaining(3).
		SetDailySpentUsd(7).
		SetDailySpentDay(today - 1).
		SetNotes("existing note").
		Save(ctx)
	require.NoError(t, err)

	merged, reused, err := svc.ApplyRedeemSubscription(ctx, &service.RedeemSubscriptionInput{
		UserID:         user.ID,
		DailyAmountUSD: 60,
		ValidityDays:   20,
		Notes:          "test subscription CDK",
	})
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, active.ID, merged.ID)
	require.Equal(t, int64(0), merged.GroupID)
	require.InDelta(t, 90, merged.DailyAmountUSD, 1e-9)
	require.InDelta(t, 90, merged.TodayRemaining, 1e-9)
	require.Equal(t, today, merged.TodayDay)
	require.InDelta(t, 0, merged.DailySpentUSD, 1e-9)
	require.Equal(t, today, merged.DailySpentDay)
	require.Contains(t, merged.Notes, "existing note")
	require.Contains(t, merged.Notes, "test subscription CDK")

	expectedDays := int(math.Ceil((30*10 + 60*20) / 90.0))
	require.Equal(t, today+expectedDays-1, merged.ExpireDay)
	require.InDelta(t, 90*float64(expectedDays), merged.GrantedTotalUSD, 1e-9)

	activeCount, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(user.ID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.DeletedAtIsNil(),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, activeCount)

	reloadedUser, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 20, reloadedUser.Concurrency, "redeem merge must not downgrade existing higher concurrency")
}

func TestApplyRedeemSubscription_CreatesNoGroupCardAndRaisesConcurrency(t *testing.T) {
	ctx := context.Background()
	db, client := newRedeemSubscriptionTestEnt(t)

	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	userSubRepo := repository.NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(groupRepo, userSubRepo, userRepo, nil, nil, client, nil, nil)

	user, err := client.User.Create().
		SetEmail("redeem-create@example.com").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(0).
		SetConcurrency(1).
		Save(ctx)
	require.NoError(t, err)

	created, reused, err := svc.ApplyRedeemSubscription(ctx, &service.RedeemSubscriptionInput{
		UserID:         user.ID,
		DailyAmountUSD: 25,
		ValidityDays:   7,
		Notes:          "fresh subscription CDK",
	})
	require.NoError(t, err)
	require.False(t, reused)
	require.NotZero(t, created.ID)
	require.Equal(t, int64(0), created.GroupID)
	require.InDelta(t, 25, created.DailyAmountUSD, 1e-9)
	require.Equal(t, service.TodayEastDayNumber()+6, created.ExpireDay)

	reloadedUser, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 3, reloadedUser.Concurrency, "ceil(D/10) should raise low concurrency")
}

func TestApplyRedeemSubscription_RejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	db, client := newRedeemSubscriptionTestEnt(t)

	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	userSubRepo := repository.NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(groupRepo, userSubRepo, userRepo, nil, nil, client, nil, nil)

	for _, tc := range []struct {
		name  string
		input *service.RedeemSubscriptionInput
	}{
		{name: "nil input"},
		{name: "missing user", input: &service.RedeemSubscriptionInput{DailyAmountUSD: 10, ValidityDays: 1}},
		{name: "zero daily", input: &service.RedeemSubscriptionInput{UserID: 1, ValidityDays: 1}},
		{name: "nan daily", input: &service.RedeemSubscriptionInput{UserID: 1, DailyAmountUSD: math.NaN(), ValidityDays: 1}},
		{name: "zero validity", input: &service.RedeemSubscriptionInput{UserID: 1, DailyAmountUSD: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reused, err := svc.ApplyRedeemSubscription(ctx, tc.input)
			require.Error(t, err)
			require.Nil(t, got)
			require.False(t, reused)
		})
	}
}

func TestApplyRedeemSubscription_ReturnsErrorWhenUserDoesNotExist(t *testing.T) {
	ctx := context.Background()
	db, client := newRedeemSubscriptionTestEnt(t)

	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	userSubRepo := repository.NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(groupRepo, userSubRepo, userRepo, nil, nil, client, nil, nil)

	got, reused, err := svc.ApplyRedeemSubscription(ctx, &service.RedeemSubscriptionInput{
		UserID:         404,
		DailyAmountUSD: 30,
		ValidityDays:   30,
	})
	require.Error(t, err)
	require.Nil(t, got)
	require.False(t, reused)
}

func TestApplyRedeemSubscriptionRequiresTransactionClient(t *testing.T) {
	svc := service.NewSubscriptionService(nil, nil, nil, nil, nil, nil, nil, nil)

	got, reused, err := svc.ApplyRedeemSubscription(context.Background(), &service.RedeemSubscriptionInput{
		UserID:         1,
		DailyAmountUSD: 30,
		ValidityDays:   30,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires ent transaction")
	require.Nil(t, got)
	require.False(t, reused)
}
