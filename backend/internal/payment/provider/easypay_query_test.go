package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayQueryOrderStatusMapping(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantTradeNo string
		wantAmount  float64
	}{
		{
			name:        "top level trade success is paid",
			body:        `{"code":1,"trade_status":"TRADE_SUCCESS","status":0,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"WAITING","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "empty trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"","status":1,"money":"12.34"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  12.34,
		},
		{
			name:        "nested data trade success is paid",
			body:        `{"code":1,"data":{"trade_status":"TRADE_SUCCESS","status":0,"money":"9.99","trade_no":"data-456"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "data-456",
			wantAmount:  9.99,
		},
		{
			name:        "legacy numeric paid status remains compatible",
			body:        `{"code":1,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "keying string paid status remains compatible",
			body:        `{"code":1,"status":"1","money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "keying order response with matching out trade no is paid",
			body:        `{"code":1,"msg":"查询订单号成功！","trade_no":"K202605260001","out_trade_no":"order-123","type":"alipay","pid":"10001","addtime":"2026-05-26 10:30:00","endtime":"2026-05-26 10:31:12","name":"AI credits","money":"9.99","status":1,"param":"account_123","buyer":""}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "K202605260001",
			wantAmount:  9.99,
		},
		{
			name:        "mismatched out trade no cannot become paid",
			body:        `{"code":1,"trade_status":"TRADE_SUCCESS","status":1,"money":"12.34","trade_no":"gateway-123","out_trade_no":"other-order"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "nested mismatched out trade no cannot become paid",
			body:        `{"code":1,"data":{"trade_status":"TRADE_SUCCESS","status":1,"money":"12.34","trade_no":"gateway-123","out_trade_no":"other-order"}}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "legacy numeric non paid status is pending",
			body:        `{"code":1,"status":0,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "query failure with missing status is pending",
			body:        `{"code":0,"msg":"订单不存在"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "explicit failed code ignores trade success fields",
			body:        `{"code":0,"trade_status":"TRADE_SUCCESS","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "explicit failed code ignores legacy paid status",
			body:        `{"code":0,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "missing fields are pending",
			body:        `{}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %q, want %q", r.Method, http.MethodGet)
				}
				if r.URL.Path != "/api.php" {
					t.Errorf("path = %q, want /api.php", r.URL.Path)
				}
				query := r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
				for key, want := range map[string]string{
					"act":          "order",
					"pid":          "pid-1",
					"key":          "pkey-1",
					"out_trade_no": orderID,
				} {
					if got := query.Get(key); got != want {
						t.Fatalf("query[%s] = %q, want %q (query=%v)", key, got, want, query)
					}
				}
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, tt.wantStatus, resp)
			}
			if resp.TradeNo != tt.wantTradeNo {
				t.Fatalf("trade_no = %q, want %q", resp.TradeNo, tt.wantTradeNo)
			}
			if resp.Amount != tt.wantAmount {
				t.Fatalf("amount = %v, want %v", resp.Amount, tt.wantAmount)
			}
		})
	}
}

func TestEasyPayQueryOrderUsesGETForKeyingPay(t *testing.T) {
	t.Parallel()

	const orderID = "sub2_20260705NNXWSKUW"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			query := r.URL.Query()
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("query[%s] = %q, want %q (query=%v)", key, got, want, query)
				}
			}
			_, _ = w.Write([]byte(`{"code":1,"msg":"succ","trade_no":"2026070509022580689","out_trade_no":"sub2_20260705NNXWSKUW","money":"1.00","status":"1"}`))
		default:
			t.Fatalf("method = %q, want GET", r.Method)
		}
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, payment.ProviderStatusPaid, resp)
	}
	if resp.TradeNo != "2026070509022580689" {
		t.Fatalf("trade_no = %q, want gateway trade no", resp.TradeNo)
	}
	if resp.Amount != 1 {
		t.Fatalf("amount = %v, want 1", resp.Amount)
	}
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("methods = %v, want [GET]", methods)
	}
}

func TestEasyPayQueryOrderFallsBackToPOSTForLegacyGateway(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"code":-5,"msg":"No Act!"}`))
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := r.PostForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, r.PostForm)
				}
			}
			_, _ = w.Write([]byte(`{"code":1,"trade_no":"legacy-123","money":"2.00","status":1}`))
		default:
			t.Fatalf("method = %q, want GET then POST", r.Method)
		}
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, payment.ProviderStatusPaid, resp)
	}
	if resp.TradeNo != "legacy-123" {
		t.Fatalf("trade_no = %q, want legacy-123", resp.TradeNo)
	}
	if resp.Amount != 2 {
		t.Fatalf("amount = %v, want 2", resp.Amount)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("methods = %v, want [GET POST]", methods)
	}
}
