//go:build unit

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type redeemAffiliateSettingRepoStub struct {
	values map[string]string
}

func (s *redeemAffiliateSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *redeemAffiliateSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *redeemAffiliateSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *redeemAffiliateSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			out[key] = v
		}
	}
	return out, nil
}

func (s *redeemAffiliateSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *redeemAffiliateSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *redeemAffiliateSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

type redeemAffiliateRepoStub struct {
	inviteeID int64
	inviterID int64

	subscriptionBaseAmounts map[string]float64
	applyCalls              []service.AffiliateRedeemCashbackInput
}

var _ service.AffiliateRepository = (*redeemAffiliateRepoStub)(nil)
var _ service.AffiliateCashbackRepository = (*redeemAffiliateRepoStub)(nil)

func (s *redeemAffiliateRepoStub) EnsureUserAffiliate(_ context.Context, userID int64) (*service.AffiliateSummary, error) {
	summary := &service.AffiliateSummary{UserID: userID}
	if userID == s.inviteeID {
		inviterID := s.inviterID
		summary.InviterID = &inviterID
	}
	return summary, nil
}

func (s *redeemAffiliateRepoStub) GetAffiliateByCode(context.Context, string) (*service.AffiliateSummary, error) {
	panic("unexpected GetAffiliateByCode call")
}

func (s *redeemAffiliateRepoStub) BindInviter(context.Context, int64, int64) (bool, error) {
	panic("unexpected BindInviter call")
}

func (s *redeemAffiliateRepoStub) AccrueQuota(context.Context, int64, int64, float64, int, *int64) (bool, error) {
	panic("unexpected AccrueQuota call")
}

func (s *redeemAffiliateRepoStub) GetAccruedRebateFromInvitee(context.Context, int64, int64) (float64, error) {
	panic("unexpected GetAccruedRebateFromInvitee call")
}

func (s *redeemAffiliateRepoStub) ThawFrozenQuota(context.Context, int64) (float64, error) {
	panic("unexpected ThawFrozenQuota call")
}

func (s *redeemAffiliateRepoStub) TransferQuotaToBalance(context.Context, int64) (float64, float64, error) {
	panic("unexpected TransferQuotaToBalance call")
}

func (s *redeemAffiliateRepoStub) ListInvitees(context.Context, int64, int) ([]service.AffiliateInvitee, error) {
	panic("unexpected ListInvitees call")
}

func (s *redeemAffiliateRepoStub) UpdateUserAffCode(context.Context, int64, string) error {
	panic("unexpected UpdateUserAffCode call")
}

func (s *redeemAffiliateRepoStub) ResetUserAffCode(context.Context, int64) (string, error) {
	panic("unexpected ResetUserAffCode call")
}

func (s *redeemAffiliateRepoStub) SetUserRebateRate(context.Context, int64, *float64) error {
	panic("unexpected SetUserRebateRate call")
}

func (s *redeemAffiliateRepoStub) BatchSetUserRebateRate(context.Context, []int64, *float64) error {
	panic("unexpected BatchSetUserRebateRate call")
}

func (s *redeemAffiliateRepoStub) ListUsersWithCustomSettings(context.Context, service.AffiliateAdminFilter) ([]service.AffiliateAdminEntry, int64, error) {
	panic("unexpected ListUsersWithCustomSettings call")
}

func (s *redeemAffiliateRepoStub) ListAffiliateInviteRecords(context.Context, service.AffiliateRecordFilter) ([]service.AffiliateInviteRecord, int64, error) {
	panic("unexpected ListAffiliateInviteRecords call")
}

func (s *redeemAffiliateRepoStub) ListAffiliateRebateRecords(context.Context, service.AffiliateRecordFilter) ([]service.AffiliateRebateRecord, int64, error) {
	panic("unexpected ListAffiliateRebateRecords call")
}

func (s *redeemAffiliateRepoStub) ListAffiliateTransferRecords(context.Context, service.AffiliateRecordFilter) ([]service.AffiliateTransferRecord, int64, error) {
	panic("unexpected ListAffiliateTransferRecords call")
}

func (s *redeemAffiliateRepoStub) GetAffiliateUserOverview(context.Context, int64) (*service.AffiliateUserOverview, error) {
	panic("unexpected GetAffiliateUserOverview call")
}

func (s *redeemAffiliateRepoStub) ListCashbackSubscriptionMappings(context.Context) ([]service.AffiliateCashbackSubscriptionMapping, error) {
	panic("unexpected ListCashbackSubscriptionMappings call")
}

func (s *redeemAffiliateRepoStub) ReplaceCashbackSubscriptionMappings(context.Context, []service.AffiliateCashbackSubscriptionMapping) error {
	panic("unexpected ReplaceCashbackSubscriptionMappings call")
}

func (s *redeemAffiliateRepoStub) GetSubscriptionCashbackBaseAmount(_ context.Context, groupID int64, validityDays int) (float64, bool, error) {
	amount, ok := s.subscriptionBaseAmounts[fmt.Sprintf("%d:%d", groupID, validityDays)]
	return amount, ok, nil
}

func (s *redeemAffiliateRepoStub) ApplyRedeemCashback(_ context.Context, input service.AffiliateRedeemCashbackInput) (bool, error) {
	s.applyCalls = append(s.applyCalls, input)
	return true, nil
}

func (s *redeemAffiliateRepoStub) ListCashbackRecords(context.Context, service.AffiliateRecordFilter) ([]service.AffiliateCashbackRecord, int64, error) {
	panic("unexpected ListCashbackRecords call")
}

func (s *redeemAffiliateRepoStub) ListUserCashbackRecords(context.Context, int64, int) ([]service.AffiliateCashbackRecord, error) {
	panic("unexpected ListUserCashbackRecords call")
}

func (s *redeemAffiliateRepoStub) GetUserCashbackTotal(context.Context, int64) (float64, error) {
	panic("unexpected GetUserCashbackTotal call")
}

func newRedeemAffiliateTestEnt(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()

	dsn := fmt.Sprintf("file:redeem_affiliate_cashback_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return db, client
}

func TestRedeemService_RedeemSubscriptionAccruesAffiliateCashback(t *testing.T) {
	ctx := context.Background()
	db, client := newRedeemAffiliateTestEnt(t)

	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	userSubRepo := repository.NewUserSubscriptionRepository(client)
	redeemRepo := repository.NewRedeemCodeRepository(client)
	subscriptionService := service.NewSubscriptionService(groupRepo, userSubRepo, userRepo, nil, nil, client, nil, nil)

	inviter, err := client.User.Create().
		SetEmail("inviter@example.com").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(0).
		SetConcurrency(1).
		Save(ctx)
	require.NoError(t, err)

	invitee, err := client.User.Create().
		SetEmail("invitee@example.com").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(0).
		SetConcurrency(1).
		Save(ctx)
	require.NoError(t, err)

	group, err := client.Group.Create().
		SetName("Subscription Plan").
		SetPlatform(service.PlatformOpenAI).
		SetRateMultiplier(1).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeSubscription).
		SetDailyLimitUsd(10). // per-day：提供有效 D（D 走输入/group 回退，须 > 0）
		Save(ctx)
	require.NoError(t, err)

	code, err := client.RedeemCode.Create().
		SetCode("SUB-CASHBACK").
		SetType(service.RedeemTypeSubscription).
		SetValue(10).
		SetStatus(service.StatusUnused).
		SetGroupID(group.ID).
		SetValidityDays(30).
		Save(ctx)
	require.NoError(t, err)

	affiliateRepo := &redeemAffiliateRepoStub{
		inviteeID: invitee.ID,
		inviterID: inviter.ID,
		subscriptionBaseAmounts: map[string]float64{
			fmt.Sprintf("%d:%d", group.ID, 30): 90,
		},
	}
	settingService := service.NewSettingService(&redeemAffiliateSettingRepoStub{
		values: map[string]string{
			service.SettingKeyAffiliateCashbackEnabled:     "true",
			service.SettingKeyAffiliateCashbackRatePercent: "100",
		},
	}, nil)
	affiliateService := service.NewAffiliateService(affiliateRepo, settingService, nil, nil)

	redeemService := service.NewRedeemService(
		redeemRepo,
		userRepo,
		subscriptionService,
		nil,
		nil,
		client,
		nil,
		affiliateService,
	)

	redeemed, err := redeemService.Redeem(ctx, invitee.ID, code.Code)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, redeemed.Status)
	require.NotNil(t, redeemed.UsedBy)
	require.Equal(t, invitee.ID, *redeemed.UsedBy)

	require.Len(t, affiliateRepo.applyCalls, 1)
	call := affiliateRepo.applyCalls[0]
	require.Equal(t, inviter.ID, call.InviterID)
	require.Equal(t, invitee.ID, call.InviteeUserID)
	require.Equal(t, code.ID, call.RedeemCodeID)
	require.Equal(t, code.Code, call.RedeemCode)
	require.Equal(t, service.RedeemTypeSubscription, call.RedeemCodeType)
	require.InDelta(t, 10, call.RedeemValue, 1e-9)
	require.NotNil(t, call.SubscriptionGroupID)
	require.Equal(t, group.ID, *call.SubscriptionGroupID)
	require.NotNil(t, call.SubscriptionValidity)
	require.Equal(t, 30, *call.SubscriptionValidity)
	require.InDelta(t, 90, call.BaseAmount, 1e-9)
	require.InDelta(t, 100, call.RatePercent, 1e-9)
	require.InDelta(t, 90, call.CashbackAmount, 1e-9)

	sub, err := userSubRepo.GetByUserIDAndGroupID(ctx, invitee.ID, group.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, sub.Status)
}
