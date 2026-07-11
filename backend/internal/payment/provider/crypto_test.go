package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestCryptoCreatePaymentConvertsCNYWithIndependentRateAdjustment(t *testing.T) {
	const apiToken = "test-api-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secure":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", Path: "/"})
			http.Redirect(w, r, "/#/login", http.StatusFound)
		case "/api/auth/login":
			if _, err := r.Cookie("session"); err != nil {
				t.Fatalf("login missing session cookie: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"admin-session"}}`))
		case "/api/rate/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"raw_rate":10}]}`))
		case "/api/v1/order/create-transaction":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode order request: %v", err)
			}
			if got := payload["trade_type"]; got != "usdt.polygon" {
				t.Fatalf("trade_type = %v", got)
			}
			if got := payload["rate"]; got != "~1.002" {
				t.Fatalf("rate = %v", got)
			}
			if got := payload["amount"]; got != float64(100) {
				t.Fatalf("amount = %v", got)
			}
			if got := payload["notify_url"]; got != "https://codex.example.com/api/v1/payment/webhook/crypto" {
				t.Fatalf("notify_url = %v", got)
			}
			redirectURL, _ := payload["redirect_url"].(string)
			if len(redirectURL) > cryptoMaxRedirectURL || strings.Contains(redirectURL, "resume_token") || !strings.Contains(redirectURL, "order_id=1") {
				t.Fatalf("redirect_url = %q", redirectURL)
			}
			if got := payload["signature"]; got != signCryptoMap(payloadWithoutSignature(payload), apiToken) {
				t.Fatalf("signature = %v, want %s", got, signCryptoMap(payloadWithoutSignature(payload), apiToken))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"status_code":200,"data":{"trade_id":"trade-123","payment_url":"https://upstream.example.com/pay/checkout/trade-123"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := NewCrypto("1", map[string]string{
		"beBase":          server.URL,
		"publicBase":      "https://pay.example.com",
		"callbackBase":    "https://codex.example.com",
		"adminUsername":   "admin",
		"adminPassword":   "password",
		"adminSecurePath": "/secure",
		"apiToken":        apiToken,
		"rateMarkup":      "1.002",
		"minUsdt":         "5",
	})
	if err != nil {
		t.Fatalf("NewCrypto: %v", err)
	}
	result, err := provider.CreatePayment(t.Context(), payment.CreatePaymentRequest{
		OrderID:       "order-1",
		Amount:        "100.00",
		PaymentType:   payment.TypeCrypto,
		Subject:       "套餐",
		ReturnURL:     "https://codex.example.com/payment/result?order_id=1&resume_token=" + strings.Repeat("x", 256),
		CryptoNetwork: "polygon",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.TradeNo != "trade-123" || result.Currency != "CNY" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.PayURL != "https://pay.example.com/pay/checkout/trade-123" {
		t.Fatalf("PayURL = %s", result.PayURL)
	}
}

func TestCryptoVerifyNotification(t *testing.T) {
	provider, err := NewCrypto("1", map[string]string{
		"beBase":          "http://127.0.0.1:18090",
		"publicBase":      "https://pay.example.com",
		"callbackBase":    "https://codex.example.com",
		"adminUsername":   "admin",
		"adminPassword":   "password",
		"adminSecurePath": "/secure",
		"apiToken":        "test-api-token",
	})
	if err != nil {
		t.Fatalf("NewCrypto: %v", err)
	}
	values := map[string]any{
		"trade_id":             "trade-123",
		"order_id":             "order-1",
		"amount":               100.0,
		"actual_amount":        "9.98",
		"trade_type":           "usdt.trc20",
		"block_transaction_id": "tx-123",
		"status":               2,
	}
	values["signature"] = signCryptoMap(values, "test-api-token")
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	notification, err := provider.VerifyNotification(t.Context(), string(raw), nil)
	if err != nil {
		t.Fatalf("VerifyNotification: %v", err)
	}
	if notification == nil || notification.OrderID != "order-1" || notification.TradeNo != "trade-123" || notification.Amount != 100 {
		t.Fatalf("unexpected notification: %+v", notification)
	}

	values["signature"] = "bad"
	raw, _ = json.Marshal(values)
	if _, err := provider.VerifyNotification(t.Context(), string(raw), nil); err == nil {
		t.Fatal("invalid signature accepted")
	}
}

func payloadWithoutSignature(payload map[string]any) map[string]any {
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		if key != "signature" {
			copyPayload[key] = value
		}
	}
	return copyPayload
}

func cryptoTestConfig(overrides map[string]string) map[string]string {
	config := map[string]string{
		"beBase":          "http://127.0.0.1:18090",
		"publicBase":      "https://pay.example.com",
		"callbackBase":    "https://codex.example.com",
		"adminUsername":   "admin",
		"adminPassword":   "password",
		"adminSecurePath": "/secure",
		"apiToken":        "test-api-token",
	}
	for key, value := range overrides {
		config[key] = value
	}
	return config
}

func TestNewCryptoValidationAndDefaults(t *testing.T) {
	t.Parallel()

	base := cryptoTestConfig(nil)
	if provider, err := NewCrypto("1", base); err != nil {
		t.Fatalf("NewCrypto with defaults: %v", err)
	} else {
		if provider.Name() != "Crypto Pay" || provider.ProviderKey() != payment.TypeCrypto {
			t.Fatalf("unexpected provider identity: %s/%s", provider.Name(), provider.ProviderKey())
		}
		if got := provider.SupportedTypes(); len(got) != 1 || got[0] != payment.TypeCrypto {
			t.Fatalf("SupportedTypes = %v", got)
		}
		if provider.markup != cryptoDefaultMarkup || provider.minUSDT != cryptoDefaultMinUSDT || provider.timeoutSec != cryptoDefaultTimeout {
			t.Fatalf("unexpected defaults: markup=%v min=%v timeout=%v", provider.markup, provider.minUSDT, provider.timeoutSec)
		}
	}

	tests := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{name: "missing required", overrides: map[string]string{"apiToken": ""}, want: "apiToken"},
		{name: "invalid base", overrides: map[string]string{"beBase": "localhost:18090"}, want: "beBase"},
		{name: "invalid public base", overrides: map[string]string{"publicBase": "pay.example.com"}, want: "publicBase"},
		{name: "invalid callback base", overrides: map[string]string{"callbackBase": "codex.example.com"}, want: "callbackBase"},
		{name: "invalid markup", overrides: map[string]string{"rateMarkup": "not-a-number"}, want: "rateMarkup"},
		{name: "invalid minimum", overrides: map[string]string{"minUsdt": "0"}, want: "minUsdt"},
		{name: "invalid timeout", overrides: map[string]string{"timeoutSec": "60"}, want: "timeoutSec"},
		{name: "invalid networks", overrides: map[string]string{"networks": "bitcoin"}, want: "networks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCrypto("1", cryptoTestConfig(tt.overrides))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewCrypto error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCryptoQueryAndRefund(t *testing.T) {
	provider, err := NewCrypto("1", cryptoTestConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	query, err := provider.QueryOrder(t.Context(), "trade-1")
	if err != nil || query.TradeNo != "trade-1" || query.Status != payment.ProviderStatusPending {
		t.Fatalf("QueryOrder = %#v, %v", query, err)
	}
	if _, err := provider.Refund(t.Context(), payment.RefundRequest{}); err == nil {
		t.Fatal("Refund unexpectedly succeeded")
	}
}

func TestCryptoVerifyNotificationRejectsInvalidPayloads(t *testing.T) {
	provider, err := NewCrypto("1", cryptoTestConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.VerifyNotification(t.Context(), "not-json", nil); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	statusValues := map[string]any{"trade_id": "trade-1", "status": 3}
	statusValues["signature"] = signCryptoMap(statusValues, "test-api-token")
	raw, _ := json.Marshal(statusValues)
	notification, err := provider.VerifyNotification(t.Context(), string(raw), nil)
	if err != nil || notification != nil {
		t.Fatalf("non-success callback = %#v, %v", notification, err)
	}

	invalidValues := map[string]any{
		"trade_id":             "trade-1",
		"order_id":             "order-1",
		"amount":               100,
		"actual_amount":        "0",
		"block_transaction_id": "tx-1",
		"status":               2,
	}
	invalidValues["signature"] = signCryptoMap(invalidValues, "test-api-token")
	raw, _ = json.Marshal(invalidValues)
	if _, err := provider.VerifyNotification(t.Context(), string(raw), nil); err == nil {
		t.Fatal("zero-amount callback accepted")
	}
}

func TestCryptoHelpers(t *testing.T) {
	t.Parallel()

	if got := normalizeCryptoNetwork("tron"); got != "usdt.trc20" || normalizeCryptoNetwork("unknown") != "" {
		t.Fatalf("unexpected network normalization")
	}
	if got := parseCryptoNetworks("trc20, matic\nsol"); len(got) != 3 {
		t.Fatalf("parsed networks = %v", got)
	}
	if got := parseCryptoNetworks(""); len(got) != len(supportedCryptoNetworks) {
		t.Fatalf("default networks = %v", got)
	}
	if _, err := normalizeCryptoBaseURL("/relative"); err == nil {
		t.Fatal("relative base URL accepted")
	}
	if got := normalizeCryptoPaymentURL("", "https://pay.example.com", "trade/1"); got != "https://pay.example.com/pay/checkout/trade%2F1" {
		t.Fatalf("fallback payment URL = %s", got)
	}
	if got := normalizeCryptoPaymentURL("/pay/checkout/trade-1", "https://pay.example.com", "trade-1"); got != "https://pay.example.com/pay/checkout/trade-1" {
		t.Fatalf("relative payment URL = %s", got)
	}
	if got, err := cryptoFloatValue(json.Number("1.25")); err != nil || got != 1.25 {
		t.Fatalf("json number conversion = %v, %v", got, err)
	}
	if _, err := cryptoFloatValue(struct{}{}); err == nil {
		t.Fatal("unsupported number type accepted")
	}
	if got, err := cryptoFloatValue(float64(2.5)); err != nil || got != 2.5 {
		t.Fatalf("float64 conversion = %v, %v", got, err)
	}
	if got := cryptoIntValue("2"); got != 2 || cryptoIntValue("bad") != 0 {
		t.Fatalf("integer conversion failed")
	}
	_ = signCryptoMap(map[string]any{"nil": nil, "invalid": json.Number("bad"), "signature": "ignored"}, "token")
}

func TestCryptoCurrentRateRejectsMalformedAdminResponses(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"login-malformed", "login-failed", "rate-malformed"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/secure":
					w.WriteHeader(http.StatusFound)
				case "/api/auth/login":
					switch scenario {
					case "login-malformed":
						_, _ = w.Write([]byte(`{`))
					case "login-failed":
						_, _ = w.Write([]byte(`{"code":500}`))
					default:
						_, _ = w.Write([]byte(`{"code":200,"data":{"token":"admin-session"}}`))
					}
				case "/api/rate/list":
					if scenario == "rate-malformed" {
						_, _ = w.Write([]byte(`{`))
					} else {
						_, _ = w.Write([]byte(`{"code":200,"data":[{"raw_rate":10}]}`))
					}
				}
			}))
			defer server.Close()
			provider, err := NewCrypto("1", cryptoTestConfig(map[string]string{"beBase": server.URL}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.currentRate(t.Context()); err == nil {
				t.Fatalf("currentRate unexpectedly succeeded for %s", scenario)
			}
		})
	}
}

func TestCryptoCreatePaymentRejectsInputAndUpstreamErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		network  string
		amount   string
		wantErr  string
	}{
		{name: "disabled network", scenario: "success", network: "polygon", amount: "100", wantErr: "network is not enabled"},
		{name: "invalid amount", scenario: "success", network: "trc20", amount: "0", wantErr: "invalid crypto payment amount"},
		{name: "malformed response", scenario: "malformed-response", network: "trc20", amount: "100", wantErr: "decode crypto payment response"},
		{name: "status error", scenario: "status-error", network: "trc20", amount: "100", wantErr: "upstream error"},
		{name: "message error", scenario: "msg-error", network: "trc20", amount: "100", wantErr: "message error"},
		{name: "generic error", scenario: "generic-error", network: "trc20", amount: "100", wantErr: "BEpusdt rejected"},
		{name: "missing trade", scenario: "missing-trade", network: "trc20", amount: "100", wantErr: "BEpusdt rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/secure":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session"})
					w.WriteHeader(http.StatusFound)
				case "/api/auth/login":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"code":200,"data":{"token":"admin-session"}}`))
				case "/api/rate/list":
					w.Header().Set("Content-Type", "application/json")
					switch tt.scenario {
					case "rate-error":
						_, _ = w.Write([]byte(`{"code":500,"data":[]}`))
					case "rate-fallback":
						_, _ = w.Write([]byte(`{"code":200,"data":[{"raw_rate":0,"rate":"10"}]}`))
					case "invalid-rate":
						_, _ = w.Write([]byte(`{"code":200,"data":[{"raw_rate":0,"rate":"0"}]}`))
					default:
						_, _ = w.Write([]byte(`{"code":200,"data":[{"raw_rate":10}]}`))
					}
				case "/api/v1/order/create-transaction":
					w.Header().Set("Content-Type", "application/json")
					switch tt.scenario {
					case "malformed-response", "rate-fallback":
						_, _ = w.Write([]byte(`{`))
					case "status-error":
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"message":"upstream error"}`))
					case "msg-error":
						_, _ = w.Write([]byte(`{"code":500,"msg":"message error"}`))
					case "generic-error":
						_, _ = w.Write([]byte(`{"code":500}`))
					case "missing-trade":
						_, _ = w.Write([]byte(`{"code":200,"status_code":200,"data":{}}`))
					default:
						_, _ = w.Write([]byte(`{"code":200,"status_code":200,"data":{"trade_id":"trade-1"}}`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			provider, err := NewCrypto("1", cryptoTestConfig(map[string]string{
				"beBase":     server.URL,
				"publicBase": "https://pay.example.com",
				"networks":   "usdt.trc20",
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.CreatePayment(t.Context(), payment.CreatePaymentRequest{
				OrderID: "order-1", Amount: tt.amount, PaymentType: payment.TypeCrypto, CryptoNetwork: tt.network,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CreatePayment error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
