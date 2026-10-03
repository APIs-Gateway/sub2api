package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

const (
	expiryReminderTestUserID = int64(42)
	expiryReminderTestEmail  = "alice@example.com"
)

// subscriptionExpiryReminderRepoStub 在通用 stub 之上返回固定的生效订阅列表，用来驱动提醒扫描。
type subscriptionExpiryReminderRepoStub struct {
	subscriptionExpiryRepoStub
	subs []UserSubscription
}

func (r *subscriptionExpiryReminderRepoStub) List(context.Context, pagination.PaginationParams, *int64, *int64, string, string, string, string) ([]UserSubscription, *pagination.PaginationResult, error) {
	r.listCalls++
	return r.subs, &pagination.PaginationResult{Page: 1, Pages: 1}, nil
}

type subscriptionExpiryReminderHarness struct {
	settings *notificationEmailMemorySettingRepo
	smtp     *notificationEmailTestSMTPServer
	subs     *subscriptionExpiryReminderRepoStub
	svc      *SubscriptionExpiryService
}

// newSubscriptionExpiryReminderHarness 组装一套「真实通知服务 + 本地 SMTP 桩」的提醒扫描环境。
// 收件人 locale 通过已记住的 locale 设置，与 Send 在没有显式 locale 时的解析顺序一致。
func newSubscriptionExpiryReminderHarness(t *testing.T, locale string, subs ...UserSubscription) *subscriptionExpiryReminderHarness {
	t.Helper()
	ctx := context.Background()

	settings := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, settings.SetMultiple(ctx, smtpServer.settings()))

	notifier := NewNotificationEmailService(settings, NewEmailService(settings, nil))
	notifier.RememberRecipientLocale(ctx, expiryReminderTestUserID, expiryReminderTestEmail, locale)

	subRepo := &subscriptionExpiryReminderRepoStub{subs: subs}
	svc := NewSubscriptionExpiryService(subRepo, time.Minute)
	svc.SetSettingRepository(settings)
	svc.SetNotificationEmailService(notifier)

	return &subscriptionExpiryReminderHarness{settings: settings, smtp: smtpServer, subs: subRepo, svc: svc}
}

// newExpiryReminderTestSubscription 构造一张生效卡；remaining 取值要避开整天边界，使 DaysRemaining 稳定。
func newExpiryReminderTestSubscription(id int64, groupID int64, group *Group, remaining time.Duration) UserSubscription {
	return UserSubscription{
		ID:        id,
		UserID:    expiryReminderTestUserID,
		GroupID:   groupID,
		Group:     group,
		ExpiresAt: time.Now().Add(remaining),
		Status:    SubscriptionStatusActive,
		User:      &User{ID: expiryReminderTestUserID, Email: expiryReminderTestEmail, Username: "Alice"},
	}
}

func expiryReminderDeliveryKey(subID int64, reminderKey string) string {
	return notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		strconv.FormatInt(subID, 10),
		expiryReminderTestEmail,
		reminderKey,
	)
}

// 部分卡没有可用的分组（自定义卡、转套餐卡、来源分组已删除），以前会在 sub.Group == nil 时被静默跳过，
// 现在必须和有分组的卡一样收到提醒；分组名位置改用通用称呼。
func TestSubscriptionExpiryService_SendsReminderRegardlessOfGroup(t *testing.T) {
	const twoAndHalfDays = 60 * time.Hour // DaysRemaining() == 3

	cases := []struct {
		name    string
		groupID int64
		group   *Group
		want    string
	}{
		{name: "normal group", groupID: 7, group: &Group{ID: 7, Name: "Codex Pro"}, want: "<strong>Codex Pro</strong>"},
		{name: "custom card without group", groupID: 0, group: nil, want: "<strong>Subscription</strong>"},
		{name: "source group deleted", groupID: 7, group: nil, want: "<strong>Subscription</strong>"},
		{name: "blank group name", groupID: 7, group: &Group{ID: 7, Name: "  "}, want: "<strong>Subscription</strong>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := newExpiryReminderTestSubscription(1001, tc.groupID, tc.group, twoAndHalfDays)
			require.Equal(t, 3, sub.DaysRemaining())
			h := newSubscriptionExpiryReminderHarness(t, "en", sub)

			h.svc.sendExpiryReminders(context.Background())

			require.Equal(t, int64(1), h.smtp.messageCount())
			body := h.smtp.lastMessageBody(t)
			require.Contains(t, body, tc.want)
			require.Contains(t, body, "<strong>3</strong> day(s)")
			require.NotContains(t, body, "Claude Pro", "不能把预览样例值发给用户")
		})
	}
}

// 分组名位置的通用称呼必须跟收件人的邮件 locale 走：zh-CN「订阅」、zh-HK「訂閱」、en「Subscription」。
func TestSubscriptionExpiryService_NoGroupPlaceholderFollowsRecipientLocale(t *testing.T) {
	cases := []struct {
		locale string
		want   string
		not    []string
	}{
		{locale: "zh-CN", want: "<strong>订阅</strong>", not: []string{"<strong>訂閱</strong>", "<strong>Subscription</strong>"}},
		{locale: "zh-HK", want: "<strong>訂閱</strong>", not: []string{"<strong>订阅</strong>", "<strong>Subscription</strong>"}},
		{locale: "en", want: "<strong>Subscription</strong>", not: []string{"<strong>订阅</strong>", "<strong>訂閱</strong>"}},
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			sub := newExpiryReminderTestSubscription(1002, 0, nil, 60*time.Hour)
			h := newSubscriptionExpiryReminderHarness(t, tc.locale, sub)

			h.svc.sendExpiryReminders(context.Background())

			require.Equal(t, int64(1), h.smtp.messageCount())
			body := h.smtp.lastMessageBody(t)
			require.Contains(t, body, tc.want)
			for _, other := range tc.not {
				require.NotContains(t, body, other)
			}
		})
	}
}

// 去重：发送标记按「卡 ID + 收件人 + 档位」记录，与分组无关。无分组的卡第一次被扫到时，
// 只会发它当前所处的那一档，不会把 7 / 3 / 1 三封一起补发；同一档反复扫描也只发一次。
func TestSubscriptionExpiryService_NoGroupCardGetsOnlyCurrentTierOnce(t *testing.T) {
	ctx := context.Background()
	sub := newExpiryReminderTestSubscription(1003, 0, nil, 60*time.Hour) // 剩 2.5 天，处于 3 天档
	h := newSubscriptionExpiryReminderHarness(t, "en", sub)

	// 上线后第一次扫描：只发当前这一档（3d），7d / 1d 都没有发送记录。
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(1), h.smtp.messageCount())
	require.Contains(t, h.smtp.lastMessageBody(t), "<strong>3</strong> day(s)")
	_, err := h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "3d"))
	require.NoError(t, err)
	for _, other := range []string{"7d", "1d"} {
		_, err := h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, other))
		require.ErrorIs(t, err, ErrSettingNotFound, other)
	}

	// 同一档反复扫描：不再重发。
	h.svc.sendExpiryReminders(ctx)
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(1), h.smtp.messageCount())

	// 进入 1 天档后才发 1d，总共两封；7d 因为已经错过，不会补发。
	h.subs.subs[0].ExpiresAt = time.Now().Add(10 * time.Hour)
	h.svc.sendExpiryReminders(ctx)
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(2), h.smtp.messageCount())
	require.Contains(t, h.smtp.lastMessageBody(t), "<strong>1</strong> day(s)")
	_, err = h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "7d"))
	require.ErrorIs(t, err, ErrSettingNotFound)
}

// 不在 7 / 3 / 1 天档的卡不发；已经有发送标记的卡（例如这次修复之前就发过的）不会重发。
func TestSubscriptionExpiryService_NoGroupCardSkipsOffTierAndAlreadySent(t *testing.T) {
	ctx := context.Background()
	offTier := newExpiryReminderTestSubscription(1004, 0, nil, 4*24*time.Hour+12*time.Hour) // DaysRemaining() == 5
	sent := newExpiryReminderTestSubscription(1005, 7, nil, 60*time.Hour)
	require.Equal(t, 5, offTier.DaysRemaining())
	h := newSubscriptionExpiryReminderHarness(t, "en", offTier, sent)
	require.NoError(t, h.settings.Set(ctx, expiryReminderDeliveryKey(sent.ID, "3d"), time.Now().UTC().Format(time.RFC3339Nano)))

	h.svc.sendExpiryReminders(ctx)

	require.Zero(t, h.smtp.messageCount())
}

func TestNotificationEmailSubscriptionFallbackName(t *testing.T) {
	cases := map[string]string{
		"zh-CN":   "订阅",
		"zh":      "订阅",
		"zh-Hans": "订阅",
		"zh-HK":   "訂閱",
		"zh-TW":   "訂閱",
		"en":      "Subscription",
		"en-US":   "Subscription",
	}
	for locale, want := range cases {
		require.Equal(t, want, notificationEmailSubscriptionFallbackName(locale), locale)
	}
}

// 订阅类邮件的 subscription_group：调用方传空串、纯空白或干脆不传，都按 locale 回退；传了名字就原样使用。
func TestNotificationEmailRuntimeVariablesSubscriptionGroupFallback(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	events := []string{
		NotificationEmailEventSubscriptionExpiryReminder,
		NotificationEmailEventSubscriptionPurchaseSuccess,
	}
	fallbacks := map[string]string{"zh-CN": "订阅", "zh-HK": "訂閱", "en": "Subscription"}

	for _, event := range events {
		for locale, fallback := range fallbacks {
			t.Run(event+"/"+locale, func(t *testing.T) {
				for _, variables := range []map[string]string{
					nil,
					{},
					{"subscription_group": ""},
					{"subscription_group": "   "},
				} {
					got := svc.runtimeVariables(ctx, event, locale, NotificationEmailSendInput{Variables: variables})
					require.Equal(t, fallback, got["subscription_group"])
				}

				got := svc.runtimeVariables(ctx, event, locale, NotificationEmailSendInput{
					Variables: map[string]string{"subscription_group": "Codex Pro"},
				})
				require.Equal(t, "Codex Pro", got["subscription_group"])
			})
		}
	}
}

// S-6：购买成功邮件拿不到分组名时（自定义卡、分组已删除），以前固定写英文 Subscription，现在按收件人 locale 回退。
func TestSubscriptionPurchaseSuccessNotification_NoGroupNameUsesLocaleFallback(t *testing.T) {
	groupID := int64(7)
	days := 30
	cases := []struct {
		name    string
		groupID *int64
	}{
		{name: "order without group", groupID: nil},
		{name: "group lookup unavailable", groupID: &groupID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			settings := newNotificationEmailMemorySettingRepo()
			smtpServer := startNotificationEmailTestSMTPServer(t)
			require.NoError(t, settings.SetMultiple(ctx, smtpServer.settings()))
			notifier := NewNotificationEmailService(settings, NewEmailService(settings, nil))
			notifier.RememberRecipientLocale(ctx, expiryReminderTestUserID, expiryReminderTestEmail, "zh-HK")
			svc := &PaymentService{notificationEmailService: notifier}

			err := svc.sendSubscriptionPurchaseSuccessNotification(ctx, &dbent.PaymentOrder{
				ID:                  99,
				UserID:              expiryReminderTestUserID,
				UserEmail:           expiryReminderTestEmail,
				UserName:            "Alice",
				SubscriptionDays:    &days,
				SubscriptionGroupID: tc.groupID,
			})

			require.NoError(t, err)
			require.Equal(t, int64(1), smtpServer.messageCount())
			body := smtpServer.lastMessageBody(t)
			require.Contains(t, body, "<strong>訂閱</strong>")
			require.NotContains(t, body, "<strong>Subscription</strong>")
		})
	}
}
