package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/gin-gonic/gin"
)

func TestSanitizeAdminPaymentOrderForResponseAddsCurrency(t *testing.T) {
	result := sanitizeAdminPaymentOrderForResponse(&dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			"currency": "usd",
		},
	})
	if result == nil {
		t.Fatal("expected sanitized order")
	}
	if result.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", result.Currency)
	}

	result = sanitizeAdminPaymentOrderForResponse(&dbent.PaymentOrder{})
	if result == nil {
		t.Fatal("expected sanitized default order")
	}
	if result.Currency != payment.DefaultPaymentCurrency {
		t.Fatalf("default Currency = %q, want %s", result.Currency, payment.DefaultPaymentCurrency)
	}
}

func TestAdminQueryRefundRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewPaymentHandler(nil, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "123"}}
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/payment/orders/123/refund/query",
		bytes.NewBufferString("{"),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.QueryRefund(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAdminQueryRefundRejectsInvalidOrderID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewPaymentHandler(nil, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "not-a-number"}}
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/payment/orders/not-a-number/refund/query",
		bytes.NewBufferString(`{"refund_id":"refund-123"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.QueryRefund(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
