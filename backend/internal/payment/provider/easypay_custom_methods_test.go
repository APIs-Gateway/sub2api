package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// TestEasyPayCustomMethodsNilReceiver covers the defensive nil-receiver guard
// at the top of customMethods() — nil *EasyPay must not panic and must
// report no custom methods.
func TestEasyPayCustomMethodsNilReceiver(t *testing.T) {
	t.Parallel()

	var e *EasyPay
	if got := e.customMethods(); got != nil {
		t.Fatalf("customMethods() on nil receiver = %v, want nil", got)
	}
}

// TestEasyPayCustomMethodsEmptyConfigReturnsNil covers the branch where the
// customMethods config key is absent/blank.
func TestEasyPayCustomMethodsEmptyConfigReturnsNil(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   "https://pay.example.com",
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	if got := provider.customMethods(); got != nil {
		t.Fatalf("customMethods() with unset config = %v, want nil", got)
	}
}

// TestEasyPayCustomMethodsMalformedJSONReturnsNil covers the JSON parse
// failure fallback for customMethods.
func TestEasyPayCustomMethodsMalformedJSONReturnsNil(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"customMethods": "not-a-json-array",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	if got := provider.customMethods(); got != nil {
		t.Fatalf("customMethods() with malformed json = %v, want nil", got)
	}
}

// TestEasyPayCustomMethodsSkipsIncompleteEntries covers the continue branch
// that drops entries missing type/upstreamType, while keeping fully
// populated entries.
func TestEasyPayCustomMethodsSkipsIncompleteEntries(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   "https://pay.example.com",
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
		"customMethods": `[
			{"type":"","upstreamType":"epay"},
			{"type":"ldc","upstreamType":""},
			{"type":"valid","upstreamType":"epay","displayName":"Valid"}
		]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	got := provider.customMethods()
	if len(got) != 1 || got[0].Type != "valid" || got[0].UpstreamType != "epay" {
		t.Fatalf("customMethods() = %+v, want only the fully populated entry", got)
	}
}

// TestEasyPayUpstreamPaymentTypeFallsBackWhenNoCustomMethodMatches covers the
// passthrough branch of upstreamPaymentType when the requested type doesn't
// match any configured custom method (e.g. a standard alipay/wxpay request
// on an instance that also has custom methods configured).
func TestEasyPayUpstreamPaymentTypeFallsBackWhenNoCustomMethodMatches(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"customMethods": `[{"type":"ldc","upstreamType":"epay","displayName":"LDC"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	if got := provider.upstreamPaymentType("alipay"); got != "alipay" {
		t.Fatalf("upstreamPaymentType(alipay) = %q, want unchanged fallback %q", got, "alipay")
	}
}

// TestEasyPayCreateAPIPaymentUsesConfiguredUpstreamType covers the mapi.php
// (default, non-popup) payment creation path with a custom method configured
// — the API-call variant of the redirect-mode test already covered by
// TestEasyPayCustomMethodsUseConfiguredUpstreamType.
func TestEasyPayCreateAPIPaymentUsesConfiguredUpstreamType(t *testing.T) {
	t.Parallel()

	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mapi.php" {
			t.Errorf("path = %q, want /mapi.php", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"trade_no":"api-trade-1","payurl":"https://pay.example.com/x"}`))
	}))
	defer server.Close()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       server.URL,
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"cidAlipay":     "cid-alipay",
		"customMethods": `[{"type":"usdt_trc20","upstreamType":"alipay","displayName":"USDT-TRC20"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-api-custom-1",
		Amount:      "1.00",
		PaymentType: "usdt_trc20",
		Subject:     "Custom EasyPay API mode",
		ClientIP:    "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if resp.TradeNo != "api-trade-1" {
		t.Fatalf("TradeNo = %q, want api-trade-1", resp.TradeNo)
	}
	if got := gotForm.Get("type"); got != "alipay" {
		t.Fatalf("form[type] = %q, want mapped upstream type alipay (form=%v)", got, gotForm)
	}
	// cid is resolved from the mapped upstream type ("alipay"), not the
	// custom type name, so it must be attached too.
	if got := gotForm.Get("cid"); got != "cid-alipay" {
		t.Fatalf("form[cid] = %q, want cid-alipay (form=%v)", got, gotForm)
	}
}
