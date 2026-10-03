package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ctxAwareNotificationSettingRepo 像真实的数据库仓储一样：ctx 已取消或超时，读写直接返回 ctx.Err()。
// 内存仓储不看 ctx，测不出「邮件发出后 ctx 才超时」这种情况。
type ctxAwareNotificationSettingRepo struct {
	*notificationEmailMemorySettingRepo
	// afterSMTPConfigRead 在读完 SMTP 配置之后触发一次：模拟发信期间调用方的 ctx 到期。
	afterSMTPConfigRead func()
}

func (r *ctxAwareNotificationSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.notificationEmailMemorySettingRepo.Get(ctx, key)
}

func (r *ctxAwareNotificationSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.notificationEmailMemorySettingRepo.GetValue(ctx, key)
}

func (r *ctxAwareNotificationSettingRepo) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.notificationEmailMemorySettingRepo.Set(ctx, key, value)
}

func (r *ctxAwareNotificationSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := r.notificationEmailMemorySettingRepo.GetMultiple(ctx, keys)
	if r.afterSMTPConfigRead != nil {
		for _, key := range keys {
			if key == SettingKeySMTPHost {
				trigger := r.afterSMTPConfigRead
				r.afterSMTPConfigRead = nil
				trigger()
				break
			}
		}
	}
	return values, err
}

// S1：邮件已经发出去，调用方的 ctx 才超时（提醒扫描共用 runOnce 的 10 秒 ctx）。
// 标记仍然必须写成功，否则下一轮扫描会把同一封再发一遍。
func TestNotificationEmailSend_MarkerWrittenEvenWhenContextExpiresAfterSend(t *testing.T) {
	inner := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, inner.SetMultiple(context.Background(), smtpServer.settings()))

	repo := &ctxAwareNotificationSettingRepo{notificationEmailMemorySettingRepo: inner}
	notifier := NewNotificationEmailService(repo, NewEmailService(repo, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo.afterSMTPConfigRead = cancel

	input := NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: expiryReminderTestEmail,
		RecipientName:  "Alice",
		UserID:         expiryReminderTestUserID,
		SourceType:     "user_subscription",
		SourceID:       "2001",
		ReminderKey:    "7d",
		Variables:      map[string]string{"expiry_time": "2026-10-10 12:00", "days_remaining": "7"},
	}
	require.NoError(t, notifier.Send(ctx, input))
	require.Error(t, ctx.Err(), "测试前提：发信过程中 ctx 已经取消")
	require.Equal(t, int64(1), smtpServer.messageCount())

	key := notificationEmailDeliveryKey(input.Event, input.SourceType, input.SourceID, input.RecipientEmail, input.ReminderKey)
	_, err := inner.GetValue(context.Background(), key)
	require.NoError(t, err, "邮件已发出，标记必须落库")

	// 下一轮扫描（新的 ctx）不会再发。
	require.NoError(t, notifier.Send(context.Background(), input))
	require.Equal(t, int64(1), smtpServer.messageCount())
}

func TestNotificationEmailMarkerCoversCycle(t *testing.T) {
	cycleEnd := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	jsonMarker := func(expiresAt time.Time) string {
		return notificationEmailDeliveryMarkerValue(expiresAt.Add(-3*24*time.Hour), expiresAt)
	}
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "新格式：到期时间相同", value: jsonMarker(cycleEnd), want: true},
		{name: "新格式：只差亚秒也算相同", value: jsonMarker(cycleEnd.Add(300 * time.Millisecond)), want: true},
		{name: "新格式：续费后到期时间变了", value: jsonMarker(cycleEnd.Add(-30 * 24 * time.Hour)), want: false},
		{name: "旧格式：本周期内写入", value: cycleEnd.Add(-3 * 24 * time.Hour).Format(time.RFC3339Nano), want: true},
		{name: "旧格式：恰好 7 天前（最早一档）", value: cycleEnd.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano), want: true},
		{name: "旧格式：8 天窗口内", value: cycleEnd.Add(-8*24*time.Hour + time.Minute).Format(time.RFC3339Nano), want: true},
		{name: "旧格式：8 天窗口之外", value: cycleEnd.Add(-8*24*time.Hour - time.Minute).Format(time.RFC3339Nano), want: false},
		{name: "旧格式：上个周期", value: cycleEnd.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano), want: false},
		{name: "读不懂的值按已发处理", value: "sent", want: true},
		{name: "坏掉的 JSON 按已发处理", value: "{not-json", want: true},
		{name: "JSON 没有到期时间按已发处理", value: `{"sent_at":"2026-10-01T00:00:00Z"}`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, notificationEmailMarkerCoversCycle(tc.value, cycleEnd))
		})
	}
}

func TestNotificationEmailDeliveryMarkerValue(t *testing.T) {
	sentAt := time.Date(2026, 10, 17, 9, 30, 0, 123, time.UTC)

	// 没有周期信息：保持旧格式，其他邮件的标记完全不变。
	require.Equal(t, sentAt.Format(time.RFC3339Nano), notificationEmailDeliveryMarkerValue(sentAt, time.Time{}))

	cycleEnd := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var marker notificationEmailDeliveryMarker
	require.NoError(t, json.Unmarshal([]byte(notificationEmailDeliveryMarkerValue(sentAt, cycleEnd)), &marker))
	require.True(t, marker.SentAt.Equal(sentAt))
	require.True(t, marker.ExpiresAt.Equal(cycleEnd))
}

// 旧格式的标记（含更早的旧版标记键）：写入时间在本周期内的不重发，上个周期的可以再发。
func TestNotificationEmailDeliveryExistsLegacyMarkers(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	cycleEnd := time.Now().Add(60 * time.Hour)
	key := notificationEmailDeliveryKey(NotificationEmailEventSubscriptionExpiryReminder, "user_subscription", "2002", expiryReminderTestEmail, "3d")
	legacyKey := legacyNotificationEmailDeliveryKey(NotificationEmailEventSubscriptionExpiryReminder, "user_subscription", "2002", expiryReminderTestEmail, "3d")
	require.NotEqual(t, key, legacyKey)

	sent, err := svc.deliveryExists(ctx, cycleEnd, key, legacyKey)
	require.NoError(t, err)
	require.False(t, sent, "没有任何标记")

	require.NoError(t, repo.Set(ctx, legacyKey, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano)))
	sent, err = svc.deliveryExists(ctx, cycleEnd, key, legacyKey)
	require.NoError(t, err)
	require.True(t, sent, "旧键的标记写在本周期内")

	require.NoError(t, repo.Set(ctx, legacyKey, time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339Nano)))
	sent, err = svc.deliveryExists(ctx, cycleEnd, key, legacyKey)
	require.NoError(t, err)
	require.False(t, sent, "旧键的标记是上个周期的")

	// 不带周期信息的调用方：标记存在即已发，和以前一样。
	sent, err = svc.deliveryExists(ctx, time.Time{}, key, legacyKey)
	require.NoError(t, err)
	require.True(t, sent)
}

// S2：同一张卡续费延期后，同一档提醒在新周期里可以再发一次；同周期内仍然只发一次。
func TestSubscriptionExpiryService_RenewedCardGetsReminderForNewCycle(t *testing.T) {
	ctx := context.Background()
	sub := newExpiryReminderTestSubscription(2003, 0, nil, 60*time.Hour) // 3 天档
	h := newSubscriptionExpiryReminderHarness(t, "en", sub)

	h.svc.sendExpiryReminders(ctx)
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(1), h.smtp.messageCount(), "同周期只发一次")

	// 标记里记着发送时卡的到期时间。
	value, err := h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "3d"))
	require.NoError(t, err)
	var marker notificationEmailDeliveryMarker
	require.NoError(t, json.Unmarshal([]byte(value), &marker))
	require.WithinDuration(t, h.subs.subs[0].ExpiresAt, marker.ExpiresAt, time.Second)

	// 续费后到期时间是另一个时间点，时间推进到新周期的 3 天档：同一档可以再发一次。
	h.subs.subs[0].ExpiresAt = time.Now().Add(66 * time.Hour)
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(2), h.smtp.messageCount(), "续费后的新周期要再发")
	require.Contains(t, h.smtp.lastMessageBody(t), "<strong>3</strong> day(s)")

	// 新周期内同样只发一次，标记已更新成新到期时间。
	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(2), h.smtp.messageCount())
	value, err = h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "3d"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(value), &marker))
	require.WithinDuration(t, h.subs.subs[0].ExpiresAt, marker.ExpiresAt, time.Second)
}

// 上线前写下的旧标记只有写入时间：本周期内写的不重发；上个周期写的（卡之后续过费）可以发。
func TestSubscriptionExpiryService_LegacyMarkerCompatibility(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		markerAge   time.Duration
		wantMessage int64
	}{
		{name: "旧标记在本周期内：不重发", markerAge: 2 * time.Hour, wantMessage: 0},
		{name: "旧标记写在到期前约 6.5 天（本周期最早一档）：不重发", markerAge: 4 * 24 * time.Hour, wantMessage: 0},
		{name: "旧标记在上个周期：会发", markerAge: 25 * 24 * time.Hour, wantMessage: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := newExpiryReminderTestSubscription(2004, 7, &Group{ID: 7, Name: "Codex Pro"}, 60*time.Hour) // 3 天档
			h := newSubscriptionExpiryReminderHarness(t, "en", sub)
			legacy := time.Now().Add(-tc.markerAge).UTC().Format(time.RFC3339Nano)
			require.NoError(t, h.settings.Set(ctx, expiryReminderDeliveryKey(sub.ID, "3d"), legacy))

			h.svc.sendExpiryReminders(ctx)
			h.svc.sendExpiryReminders(ctx)

			require.Equal(t, tc.wantMessage, h.smtp.messageCount())
			value, err := h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "3d"))
			require.NoError(t, err)
			if tc.wantMessage == 0 {
				require.Equal(t, legacy, value, "不重发时旧标记保持原样")
			} else {
				var marker notificationEmailDeliveryMarker
				require.NoError(t, json.Unmarshal([]byte(value), &marker), "重发后标记升级为带到期时间的新格式")
				require.WithinDuration(t, h.subs.subs[0].ExpiresAt, marker.ExpiresAt, time.Second)
			}
		})
	}
}

// 档位跨越：续费后的新周期直接落在 1 天档，只发 1d，不补发上个周期已经发过的 7d、没有发过的 3d。
func TestSubscriptionExpiryService_RenewedCardDoesNotBackfillMissedTiers(t *testing.T) {
	ctx := context.Background()
	sub := newExpiryReminderTestSubscription(2005, 0, nil, 6*24*time.Hour+12*time.Hour) // 7 天档
	require.Equal(t, 7, sub.DaysRemaining())
	h := newSubscriptionExpiryReminderHarness(t, "en", sub)

	h.svc.sendExpiryReminders(ctx)
	require.Equal(t, int64(1), h.smtp.messageCount())
	require.Contains(t, h.smtp.lastMessageBody(t), "<strong>7</strong> day(s)")

	// 续费后新周期的到期时间不同，且已经进入 1 天档。
	h.subs.subs[0].ExpiresAt = time.Now().Add(10 * time.Hour)
	h.svc.sendExpiryReminders(ctx)
	h.svc.sendExpiryReminders(ctx)

	require.Equal(t, int64(2), h.smtp.messageCount(), "只多发 1d 一封")
	require.Contains(t, h.smtp.lastMessageBody(t), "<strong>1</strong> day(s)")
	_, err := h.settings.GetValue(ctx, expiryReminderDeliveryKey(sub.ID, "3d"))
	require.ErrorIs(t, err, ErrSettingNotFound, "3d 没有发过，也不补发")
}
