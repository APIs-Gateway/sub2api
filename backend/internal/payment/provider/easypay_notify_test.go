package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayVerifyNotificationAcceptsKeyingPaidStatus(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "pid-1",
		"type":         "alipay",
		"out_trade_no": "sub2_20260705NNXWSKUW",
		"trade_no":     "2026070509022580689",
		"name":         "HIYO CODEX余额 1.00",
		"money":        "1.00",
		"status":       "1",
	}
	params["sign"] = easyPaySign(params, "pkey-1")
	params["sign_type"] = signTypeMD5

	raw := url.Values{}
	for key, value := range params {
		raw.Set(key, value)
	}
	provider := newTestEasyPay(t, "https://api.example.com")
	notification, err := provider.VerifyNotification(context.Background(), raw.Encode(), nil)
	if err != nil {
		t.Fatalf("VerifyNotification returned error: %v", err)
	}
	if notification.Status != payment.ProviderStatusSuccess {
		t.Fatalf("status = %q, want %q", notification.Status, payment.ProviderStatusSuccess)
	}
	if notification.OrderID != params["out_trade_no"] {
		t.Fatalf("order id = %q, want %q", notification.OrderID, params["out_trade_no"])
	}
	if notification.TradeNo != params["trade_no"] {
		t.Fatalf("trade no = %q, want %q", notification.TradeNo, params["trade_no"])
	}
	if notification.Amount != 1 {
		t.Fatalf("amount = %v, want 1", notification.Amount)
	}
}
