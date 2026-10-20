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

func signedEasyPayNotifyBody(params map[string]string) string {
	params["sign"] = easyPaySign(params, "pkey-1")
	params["sign_type"] = signTypeMD5
	raw := url.Values{}
	for key, value := range params {
		raw.Set(key, value)
	}
	return raw.Encode()
}

// 真实回调的参数形状：pid/trade_no/out_trade_no/type/name/money/trade_status/sign/sign_type。
func TestEasyPayVerifyNotificationAcceptsRealShape(t *testing.T) {
	t.Parallel()

	body := signedEasyPayNotifyBody(map[string]string{
		"pid":          "1001",
		"trade_no":     "2026070509022580689",
		"out_trade_no": "sub2_20260705NNXWSKUW",
		"type":         "alipay",
		"name":         "HIYO CODEX余额 A&B=1.00",
		"money":        "1.00",
		"trade_status": "TRADE_SUCCESS",
	})
	provider := newTestEasyPay(t, "https://api.example.com")
	n, err := provider.VerifyNotification(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("VerifyNotification returned error: %v", err)
	}
	if n.Status != payment.ProviderStatusSuccess || n.TradeNo != "2026070509022580689" {
		t.Fatalf("unexpected notification: %+v", n)
	}
}

// 下单时的 return_url 夹带了额外参数；把它截断后，被夹带的参数成为顶层参数，
// 拼接后的签名串与原签名逐字节相同，签名本身仍然成立，必须在验签之前被拒绝。
func TestEasyPayVerifyNotificationRejectsReturnURLSmuggling(t *testing.T) {
	t.Parallel()

	orderParams := map[string]string{
		"pid":          "pid-1",
		"type":         "alipay",
		"out_trade_no": "sub2_forged",
		"notify_url":   "https://example.com/api/v1/payment/webhook/easypay",
		"return_url":   "https://example.com/payment/result?order_id=1&status=success&trade_status=TRADE_SUCCESS",
		"name":         "HIYO CODEX余额 1.00",
		"money":        "1.00",
	}
	sign := easyPaySign(orderParams, "pkey-1")

	forged := map[string]string{}
	for k, v := range orderParams {
		forged[k] = v
	}
	forged["return_url"] = "https://example.com/payment/result?order_id=1&status=success"
	forged["trade_status"] = "TRADE_SUCCESS"
	if !easyPayVerifySign(forged, "pkey-1", sign) {
		t.Fatal("precondition: forged parameter set should collide with the original signature")
	}

	raw := url.Values{}
	for k, v := range forged {
		raw.Set(k, v)
	}
	raw.Set("sign", sign)
	raw.Set("sign_type", signTypeMD5)
	provider := newTestEasyPay(t, "https://api.example.com")
	if _, err := provider.VerifyNotification(context.Background(), raw.Encode(), nil); err == nil {
		t.Fatal("VerifyNotification must reject notifications carrying return_url")
	}
}

func TestEasyPayVerifyNotificationRejectsNotifyURLAndAmbiguousValues(t *testing.T) {
	t.Parallel()

	base := func() map[string]string {
		return map[string]string{
			"pid":          "pid-1",
			"trade_no":     "T1",
			"out_trade_no": "sub2_x",
			"type":         "alipay",
			"money":        "1.00",
			"trade_status": "TRADE_SUCCESS",
		}
	}
	cases := map[string]func(map[string]string){
		"notify_url":          func(p map[string]string) { p["notify_url"] = "https://example.com/n" },
		"return_url":          func(p map[string]string) { p["return_url"] = "https://example.com/r" },
		"ampersand in type":   func(p map[string]string) { p["type"] = "alipay&trade_status=TRADE_SUCCESS" },
		"equals in out_trade": func(p map[string]string) { p["out_trade_no"] = "a=b" },
		"ampersand in status": func(p map[string]string) { p["trade_status"] = "x&y" },
		"missing trade_no":    func(p map[string]string) { delete(p, "trade_no") },
		"blank trade_no":      func(p map[string]string) { p["trade_no"] = "  " },
		"ampersand in key":    func(p map[string]string) { p["a&b"] = "1" },
	}
	provider := newTestEasyPay(t, "https://api.example.com")
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := base()
			mutate(p)
			if _, err := provider.VerifyNotification(context.Background(), signedEasyPayNotifyBody(p), nil); err == nil {
				t.Fatalf("VerifyNotification should reject %s", name)
			}
		})
	}
}
