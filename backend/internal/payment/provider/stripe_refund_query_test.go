//go:build unit

package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	stripe "github.com/stripe/stripe-go/v85"
	"github.com/stretchr/testify/require"
)

func newStripeWithTestBackend(t *testing.T, handler http.HandlerFunc) *Stripe {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		URL:               stripe.String(server.URL),
		HTTPClient:        server.Client(),
		MaxNetworkRetries: stripe.Int64(0),
	})
	return &Stripe{
		config:      map[string]string{"secretKey": "sk_test_123", "currency": "CNY"},
		sc:          stripe.NewClient("sk_test_123", stripe.WithBackends(backends)),
		initialized: true,
	}
}

func TestStripeQueryRefundByRefundID(t *testing.T) {
	s := newStripeWithTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1/refunds/re_123", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_123","object":"refund","status":"succeeded"}`))
	})

	resp, err := s.QueryRefund(context.Background(), payment.RefundQueryRequest{RefundID: " re_123 ", TradeNo: "pi_1"})
	require.NoError(t, err)
	require.Equal(t, payment.RefundResponse{RefundID: "re_123", Status: payment.ProviderStatusSuccess}, *resp)
}

func TestStripeQueryRefundFallsBackToLatestPaymentIntentRefund(t *testing.T) {
	s := newStripeWithTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/refunds", r.URL.Path)
		require.Equal(t, "pi_1", r.URL.Query().Get("payment_intent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","url":"/v1/refunds","has_more":false,"data":[{"id":"re_latest","object":"refund","status":"pending"}]}`))
	})

	resp, err := s.QueryRefund(context.Background(), payment.RefundQueryRequest{TradeNo: "pi_1"})
	require.NoError(t, err)
	require.Equal(t, "re_latest", resp.RefundID)
	require.Equal(t, payment.ProviderStatusPending, resp.Status)
}

func TestStripeQueryRefundErrors(t *testing.T) {
	s := newStripeWithTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/refunds" {
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/refunds","has_more":false,"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"No such refund"}}`))
	})

	_, err := s.QueryRefund(context.Background(), payment.RefundQueryRequest{})
	require.ErrorContains(t, err, "missing payment intent id")

	_, err = s.QueryRefund(context.Background(), payment.RefundQueryRequest{TradeNo: "pi_empty"})
	require.ErrorContains(t, err, "no refund found")

	_, err = s.QueryRefund(context.Background(), payment.RefundQueryRequest{RefundID: "re_missing"})
	require.ErrorContains(t, err, "stripe query refund")
}

func TestStripeQueryRefundListError(t *testing.T) {
	s := newStripeWithTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"bad payment intent"}}`))
	})

	_, err := s.QueryRefund(context.Background(), payment.RefundQueryRequest{TradeNo: "pi_bad"})
	require.ErrorContains(t, err, "stripe query refund")
}

func TestStripeRefundProviderStatus(t *testing.T) {
	require.Equal(t, payment.ProviderStatusSuccess, stripeRefundProviderStatus(stripe.RefundStatusSucceeded))
	require.Equal(t, payment.ProviderStatusFailed, stripeRefundProviderStatus(stripe.RefundStatusFailed))
	require.Equal(t, payment.ProviderStatusFailed, stripeRefundProviderStatus(stripe.RefundStatusCanceled))
	require.Equal(t, payment.ProviderStatusPending, stripeRefundProviderStatus(stripe.RefundStatusPending))
	require.Equal(t, payment.ProviderStatusPending, stripeRefundProviderStatus(stripe.RefundStatusRequiresAction))
}
