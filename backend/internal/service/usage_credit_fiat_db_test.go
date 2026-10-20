//go:build unit

package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// BuildCreditFiatRate 的查库分支：这段决定了订阅记录到底按哪张卡的单价折算。
// 用内存 sqlite 起真实 ent client，比 mock 更接近线上——查询条件（卡 ID + 用户 ID）
// 写错时 mock 往往照样返回数据，而真实查询会直接查不到。

func newCreditFiatEntClient(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client, db
}

// user_id 是必填外键，卡必须挂在真实用户上；group_id 是可空边，测试里不需要。
func createCreditFiatUser(t *testing.T, client *dbent.Client, email string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("hash").
		SetUsername(email).
		Save(context.Background())
	require.NoError(t, err)
	return user
}

func createCreditFiatCard(t *testing.T, client *dbent.Client, userID int64, daily float64) *dbent.UserSubscription {
	t.Helper()
	now := time.Now()
	card, err := client.UserSubscription.Create().
		SetUserID(userID).
		SetStartsAt(now.Add(-time.Hour)).
		SetExpiresAt(now.Add(24 * time.Hour)).
		SetStatus(SubscriptionStatusActive).
		SetDailyAmountUsd(daily).
		Save(context.Background())
	require.NoError(t, err)
	return card
}

// 订阅记录必须按该卡自己的 u(D) 折算，而不是按充值倍率——这正是整个功能的意义：
// 订阅额度比充值额度便宜得多，一律按充值价折算会把订阅用户的花费高估近一倍。
func TestBuildCreditFiatRate_UsesCardUnitPrice(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "owner@example.com")
	card := createCreditFiatCard(t, client, owner.ID, 20)
	svc := NewUsageService(nil, nil, client, nil)
	cfg := prodPricingConfig()

	logs := []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(card.ID), ActualCost: 5},
	}
	rate := svc.BuildCreditFiatRate(context.Background(), owner.ID, 13, cfg, logs)

	want := cfg.UnitPrice(20)
	require.Greater(t, want, 0.0, "前置条件：生产定价下 u(20) 应为正")
	require.Less(t, want, 1.0/13.0, "前置条件：订阅单价必须低于钱包单价，否则这个折算没有意义")

	got := rate.FiatPerCredit(BillingTypeSubscription, card.ID)
	require.InDelta(t, want, got, 1e-9)
	require.InDelta(t, 5*want, rate.Convert(5, BillingTypeSubscription, card.ID), 1e-6)
}

// 查询按 user_id 二次过滤：即便记录里的 subscription_id 指向别人的卡，
// 也不能把别人卡的定价读出来。查不到就回落到钱包单价。
func TestBuildCreditFiatRate_IgnoresOtherUsersCard(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "owner@example.com")
	other := createCreditFiatUser(t, client, "other@example.com")
	card := createCreditFiatCard(t, client, owner.ID, 20)
	svc := NewUsageService(nil, nil, client, nil)

	logs := []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(card.ID), ActualCost: 5},
	}
	// 以另一个用户的身份去查这张卡
	rate := svc.BuildCreditFiatRate(context.Background(), other.ID, 13, prodPricingConfig(), logs)

	require.InDelta(t, 1.0/13.0, rate.FiatPerCredit(BillingTypeSubscription, card.ID), 1e-9)
}

// 已过期/已软删的卡同样要能查到：历史用量记录常常指向这类卡，
// 若把它们过滤掉，老账单会因为回落到充值价而突然「变贵」。
func TestBuildCreditFiatRate_IncludesExpiredCard(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "owner@example.com")
	now := time.Now()
	card, err := client.UserSubscription.Create().
		SetUserID(owner.ID).
		SetStartsAt(now.Add(-72 * time.Hour)).
		SetExpiresAt(now.Add(-48 * time.Hour)).
		SetStatus(SubscriptionStatusExpired).
		SetDailyAmountUsd(20).
		SetDeletedAt(now.Add(-24 * time.Hour)).
		Save(context.Background())
	require.NoError(t, err)

	svc := NewUsageService(nil, nil, client, nil)
	cfg := prodPricingConfig()
	logs := []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(card.ID), ActualCost: 5},
	}

	rate := svc.BuildCreditFiatRate(context.Background(), owner.ID, 13, cfg, logs)

	require.InDelta(t, cfg.UnitPrice(20), rate.FiatPerCredit(BillingTypeSubscription, card.ID), 1e-9)
}

// 整页都是钱包扣费时不该查库。这里连一张卡都没建，能拿到钱包单价即说明没走查询分支。
func TestBuildCreditFiatRate_WalletOnlyPageSkipsQuery(t *testing.T) {
	client, _ := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "owner@example.com")
	svc := NewUsageService(nil, nil, client, nil)

	logs := []UsageLog{
		{BillingType: BillingTypeBalance, ActualCost: 0.5},
		{BillingType: BillingTypeBalance, ActualCost: 1.5},
	}
	rate := svc.BuildCreditFiatRate(context.Background(), owner.ID, 13, prodPricingConfig(), logs)

	require.InDelta(t, 1.0/13.0, rate.FiatPerCredit(BillingTypeBalance, 0), 1e-9)
}

// 查库失败时整批回落到充值价，而不是让用量列表整个报错——
// 法币折算只是展示增强，不该成为列表接口的新故障点。
func TestBuildCreditFiatRate_QueryErrorFallsBackToWalletPrice(t *testing.T) {
	client, db := newCreditFiatEntClient(t)
	owner := createCreditFiatUser(t, client, "owner@example.com")
	card := createCreditFiatCard(t, client, owner.ID, 20)
	require.NoError(t, db.Close()) // 关掉底层连接，后续查询必然失败

	svc := NewUsageService(nil, nil, client, nil)
	logs := []UsageLog{
		{BillingType: BillingTypeSubscription, SubscriptionID: subIDPtr(card.ID), ActualCost: 5},
	}

	rate := svc.BuildCreditFiatRate(context.Background(), owner.ID, 13, prodPricingConfig(), logs)

	require.InDelta(t, 1.0/13.0, rate.FiatPerCredit(BillingTypeSubscription, card.ID), 1e-9)
	require.InDelta(t, 5.0/13.0, rate.Convert(5, BillingTypeSubscription, card.ID), 1e-6)
}
