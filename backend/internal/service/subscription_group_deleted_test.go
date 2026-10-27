package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 删除分组不再连带删卡：卡上的 group_id 仍指向已软删的分组，读取时 Group 边为 nil。
// 以下用例固定「来源分组已删」的订阅卡在各 service 入口既不 panic，也不显示错误数据；
// 仓储里没有配置 groupRepo，若有路径回头按 sub.GroupID 去查分组，这里会直接 nil 解引用而失败。

const deletedGroupCardGroupID int64 = 777

func newDeletedGroupCard() *UserSubscription {
	now := time.Now()
	return &UserSubscription{
		ID:              301,
		UserID:          42,
		GroupID:         deletedGroupCardGroupID, // 来源分组已被软删
		Group:           nil,                     // WithGroup 在分组软删后读不到边
		Status:          SubscriptionStatusActive,
		StartsAt:        now.Add(-10 * 24 * time.Hour),
		ExpiresAt:       now.Add(20 * 24 * time.Hour),
		DailyAmountUSD:  30,
		DailyLimitUSD:   ptrFloat64(30),
		GrantedTotalUSD: 600,
	}
}

type deletedGroupCardRepoStub struct {
	userSubRepoNoop
	card *UserSubscription
}

func (s deletedGroupCardRepoStub) GetActiveByUserID(context.Context, int64) (*UserSubscription, error) {
	cp := *s.card
	return &cp, nil
}

func (s deletedGroupCardRepoStub) GetLatestActiveStatusByUserID(context.Context, int64) (*UserSubscription, error) {
	cp := *s.card
	return &cp, nil
}

func (s deletedGroupCardRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	return []UserSubscription{*s.card}, nil
}

func TestSubscriptionWithDeletedGroup_ProgressUsesNeutralName(t *testing.T) {
	svc := &SubscriptionService{userSubRepo: deletedGroupCardRepoStub{card: newDeletedGroupCard()}}

	progresses, err := svc.GetUserSubscriptionsWithProgress(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, progresses, 1)
	require.Equal(t, int64(301), progresses[0].ID)
	require.Equal(t, "All groups", progresses[0].GroupName, "分组已删时用中性名称，不能带出旧分组名或空串")
	require.NotNil(t, progresses[0].Burndown, "额度进度按卡级字段计算，不依赖分组")
	require.InDelta(t, 30, progresses[0].Burndown.DailyAmountUSD, 1e-9)
}

func TestSubscriptionWithDeletedGroup_ActiveCardStillResolved(t *testing.T) {
	svc := &SubscriptionService{userSubRepo: deletedGroupCardRepoStub{card: newDeletedGroupCard()}}

	// 认证 / 计费入口：按用户取唯一生效卡，不按分组匹配。
	sub, err := svc.GetActiveUserSubscription(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, int64(301), sub.ID)
	require.Equal(t, deletedGroupCardGroupID, sub.GroupID)
	require.Nil(t, sub.Group)

	// 用户端订阅列表。
	subs, err := svc.ListActiveUserSubscriptions(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Nil(t, subs[0].Group)
}

func TestSubscriptionWithDeletedGroup_ValidateAndCheckLimitsAcceptsNilGroup(t *testing.T) {
	svc := &SubscriptionService{}
	card := newDeletedGroupCard()

	// 卡级限额已冻结在卡上；key 没有分组（或分组已删）时传 nil 也必须正常校验。
	_, err := svc.ValidateAndCheckLimits(card, nil)
	require.NoError(t, err)
}

func TestSubscriptionWithDeletedGroup_RenewQuoteKeepsCardAndSkipsGroupLookup(t *testing.T) {
	// groupRepo 未配置：续费报价若回头按 GroupID 查分组会 nil 解引用。
	svc := &SubscriptionService{userSubRepo: deletedGroupCardRepoStub{card: newDeletedGroupCard()}}

	quote, err := svc.QuoteRenewOrder(context.Background(), 42, 30)
	require.NoError(t, err)
	require.Equal(t, int64(301), quote.SubscriptionID, "续费作用于用户唯一生效卡")
	require.InDelta(t, 30, quote.DailyAmountUSD, 1e-9)
	require.Equal(t, 30, quote.AddedDays)
	require.Greater(t, quote.Price, 0.0)
	require.Equal(t, deletedGroupCardGroupID, quote.GroupID, "GroupID 仅作订单记录，沿用卡上的历史值")
}

func TestSubscriptionGroupDisplayName_DeletedGroup(t *testing.T) {
	// 读不到分组（无分组卡、来源分组已删）返回空串，由通知服务按收件人 locale 回退，不在这里写死语言。
	require.Empty(t, subscriptionGroupDisplayName(nil))
	require.Empty(t, subscriptionGroupDisplayName(&Group{Name: "  "}))
	require.Equal(t, "Pro", subscriptionGroupDisplayName(&Group{Name: "Pro"}))
}
