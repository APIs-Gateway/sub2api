//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildBalanceLowEmailBodyForLocale_Chinese(t *testing.T) {
	s := &BalanceNotifyService{}
	body := s.buildBalanceLowEmailBodyForLocale("Alice", 3.14, 10.0, "MySite", "https://example.com/pay", "zh-CN")

	require.Contains(t, balanceLowEmailSubject("MySite", "zh"), "余额不足提醒")
	require.Contains(t, body, "MySite")
	require.Contains(t, body, "Alice")
	require.Contains(t, body, "$3.14")
	require.Contains(t, body, "$10.00")
	require.Contains(t, body, `href="https://example.com/pay"`)
	require.Contains(t, body, "立即充值")
	require.NotContains(t, body, "Balance Low Alert")
	require.NotContains(t, body, "Top Up Now")
	require.NotContains(t, body, "%!")
}

func TestBuildBalanceLowEmailBodyForLocale_English(t *testing.T) {
	s := &BalanceNotifyService{}
	body := s.buildBalanceLowEmailBodyForLocale("Alice", 3.14, 10.0, "MySite", "", "en-US")

	require.Contains(t, balanceLowEmailSubject("MySite", "en"), "Balance Low Alert")
	require.Contains(t, body, "MySite")
	require.Contains(t, body, "Alice")
	require.Contains(t, body, "$3.14")
	require.Contains(t, body, "$10.00")
	require.Contains(t, body, "your balance is running low")
	require.NotContains(t, body, `<a href`)
	require.NotContains(t, body, "您的余额不足")
	require.NotContains(t, body, "立即充值")
	require.NotContains(t, body, "%!")
}

func TestBuildBalanceLowEmailBodyForLocale_RechargeURLEscaped(t *testing.T) {
	s := &BalanceNotifyService{}
	body := s.buildBalanceLowEmailBodyForLocale("u", 1.0, 5.0, "Site", `https://example.com/?a=1&b=<script>`, "en")

	require.Contains(t, body, "&amp;")
	require.Contains(t, body, "&lt;script&gt;")
	require.NotContains(t, body, "<script>")
}

func TestBuildQuotaAlertEmailBodyForLocale_AllFieldsPresent(t *testing.T) {
	s := &BalanceNotifyService{}
	body := s.buildQuotaAlertEmailBodyForLocale(
		42,
		"acc-foo",
		"anthropic",
		"Daily quota",
		750.50,
		1000.0,
		249.50,
		"$249.50",
		"MySite",
		"en",
	)

	require.Contains(t, quotaAlertEmailSubject("MySite", "acc-foo", "en"), "Account Quota Alert")
	require.Contains(t, body, "MySite")
	require.Contains(t, body, "#42")
	require.Contains(t, body, "acc-foo")
	require.Contains(t, body, "anthropic")
	require.Contains(t, body, "Daily quota")
	require.Contains(t, body, "$750.50")
	require.Contains(t, body, "$1000.00")
	require.Contains(t, body, "$249.50")
	require.NotContains(t, body, "账号限额告警")
	require.NotContains(t, body, "%!")
}

func TestBuildQuotaAlertEmailBodyForLocale_UnlimitedDisplay(t *testing.T) {
	s := &BalanceNotifyService{}

	zhBody := s.buildQuotaAlertEmailBodyForLocale(1, "n", "p", "每日额度", 100.0, 0.0, 0.0, "30%", "Site", "zh")
	require.Contains(t, zhBody, "无限制")
	require.NotContains(t, zhBody, "Unlimited")

	enBody := s.buildQuotaAlertEmailBodyForLocale(1, "n", "p", "Daily quota", 100.0, 0.0, 0.0, "30%", "Site", "en")
	require.Contains(t, enBody, "Unlimited")
	require.NotContains(t, enBody, "无限制")
}

func TestQuotaDimLabelForLocale(t *testing.T) {
	require.Equal(t, "每日额度", quotaDimLabelForLocale(quotaDimDaily, "zh-CN"))
	require.Equal(t, "Weekly quota", quotaDimLabelForLocale(quotaDimWeekly, "en-US"))
	require.Equal(t, "custom", quotaDimLabelForLocale("custom", "en"))
}

func TestBalanceNotifyFallbackSendPathsUseLocalizedBuiltInEmails(t *testing.T) {
	server := startNotificationEmailTestSMTPServer(t)
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.SetMultiple(t.Context(), server.settings()))
	require.NoError(t, repo.Set(t.Context(), SettingKeyDefaultLocale, "en-US"))

	emailSvc := NewEmailService(repo, nil)
	svc := NewBalanceNotifyService(emailSvc, repo, nil)

	require.Equal(t, "en", svc.resolveNotificationFallbackLocale(t.Context(), 12, "User@Example.com"))
	svc.sendBalanceLowEmails(
		[]string{"User@Example.com"},
		12,
		"Alice",
		"user@example.com",
		3.5,
		10.0,
		"Sub2API",
		"https://example.com/pay",
	)
	require.Eventually(t, func() bool {
		return server.messageCount() == 1
	}, time.Second, 10*time.Millisecond)

	svc.sendQuotaAlertEmails(
		[]string{"admin@example.com"},
		42,
		"acc-foo",
		"anthropic",
		quotaDim{name: quotaDimTotal, limit: 1000, threshold: 30, thresholdType: thresholdTypePercentage},
		990,
		"Sub2API",
	)
	require.Eventually(t, func() bool {
		return server.messageCount() == 2
	}, time.Second, 10*time.Millisecond)

	svc.SetNotificationEmailService(NewNotificationEmailService(repo, nil))
	svc.sendBalanceLowEmails(
		[]string{"fallback@example.com"},
		12,
		"Alice",
		"user@example.com",
		2.5,
		10.0,
		"Sub2API",
		"https://example.com/pay",
	)
	require.Eventually(t, func() bool {
		return server.messageCount() == 3
	}, time.Second, 10*time.Millisecond)

	svc.sendQuotaAlertEmails(
		[]string{"fallback-admin@example.com"},
		43,
		"acc-bar",
		"openai",
		quotaDim{name: quotaDimWeekly, limit: 1000, threshold: 20, thresholdType: thresholdTypeFixed},
		995,
		"Sub2API",
	)
	require.Eventually(t, func() bool {
		return server.messageCount() == 4
	}, time.Second, 10*time.Millisecond)
}
