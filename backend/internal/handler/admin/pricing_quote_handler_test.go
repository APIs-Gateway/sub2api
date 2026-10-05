//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubPriceQuoter struct {
	calls   int
	lastReq service.QuoteRequest
	quote   *service.Quote
	err     error
	// reference 是 OfficialReference 的固定返回值（批量报价测试用）。
	reference service.QuoteOfficialReference
}

func (s *stubPriceQuoter) OfficialReference(model string) service.QuoteOfficialReference {
	ref := s.reference
	ref.Model = model
	return ref
}

func (s *stubPriceQuoter) Quote(_ context.Context, req service.QuoteRequest) (*service.Quote, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.quote, nil
}

type pricingQuoteEnvelope struct {
	Code     int               `json:"code"`
	Message  string            `json:"message"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
	Data     map[string]any    `json:"data"`
}

func doPricingQuoteRequest(t *testing.T, stub *stubPriceQuoter, rawQuery string) (*httptest.ResponseRecorder, pricingQuoteEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/pricing/quote", (&PricingQuoteHandler{quoter: stub}).Quote)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/pricing/quote?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var envelope pricingQuoteEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return rec, envelope
}

func TestPricingQuoteHandler_ParameterValidation(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantParam string
		wantRea   string
	}{
		{"missing model", "group_id=3", "model", "MISSING_PARAMETER"},
		{"blank model", "model=%20%20&group_id=3", "model", "MISSING_PARAMETER"},
		{"missing group_id", "model=gpt-5.5", "group_id", "MISSING_PARAMETER"},
		{"group_id not a number", "model=gpt-5.5&group_id=abc", "group_id", "INVALID_PARAMETER"},
		{"group_id zero", "model=gpt-5.5&group_id=0", "group_id", "INVALID_PARAMETER"},
		{"group_id negative", "model=gpt-5.5&group_id=-1", "group_id", "INVALID_PARAMETER"},
		{"user_id not a number", "model=gpt-5.5&group_id=3&user_id=x", "user_id", "INVALID_PARAMETER"},
		{"user_id negative", "model=gpt-5.5&group_id=3&user_id=-2", "user_id", "INVALID_PARAMETER"},
		{"served_group_id invalid", "model=gpt-5.5&group_id=3&served_group_id=0", "served_group_id", "INVALID_PARAMETER"},
		{"at not RFC3339", "model=gpt-5.5&group_id=3&at=yesterday", "at", "INVALID_PARAMETER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubPriceQuoter{}
			rec, envelope := doPricingQuoteRequest(t, stub, tt.query)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, tt.wantRea, envelope.Reason)
			require.Equal(t, tt.wantParam, envelope.Metadata["param"])
			require.Zero(t, stub.calls, "参数校验失败时不应调用 Quoter")
		})
	}
}

func TestPricingQuoteHandler_PassesParametersToQuoter(t *testing.T) {
	stub := &stubPriceQuoter{quote: &service.Quote{Model: "gpt-5.5"}}
	rec, _ := doPricingQuoteRequest(t, stub,
		"model=%20gpt-5.5%20&group_id=3&user_id=12&served_group_id=4&service_tier=%20priority%20&at=2026-10-05T02:00:00Z")
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, stub.calls)
	require.Equal(t, "gpt-5.5", stub.lastReq.Model)
	require.Equal(t, int64(3), stub.lastReq.GroupID)
	require.Equal(t, int64(12), stub.lastReq.UserID)
	require.Equal(t, int64(4), stub.lastReq.ServedGroupID)
	require.Equal(t, "priority", stub.lastReq.ServiceTier)
	require.True(t, stub.lastReq.At.Equal(time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)))
}

func TestPricingQuoteHandler_OptionalParametersDefaultToZero(t *testing.T) {
	stub := &stubPriceQuoter{quote: &service.Quote{Model: "gpt-5.5"}}
	rec, _ := doPricingQuoteRequest(t, stub, "model=gpt-5.5&group_id=3")
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, int64(0), stub.lastReq.UserID)
	require.Equal(t, int64(0), stub.lastReq.ServedGroupID)
	require.Equal(t, "", stub.lastReq.ServiceTier)
	require.True(t, stub.lastReq.At.IsZero())
}

func TestPricingQuoteHandler_ReturnsQuoteStructure(t *testing.T) {
	userMultiplier := 0.8
	stub := &stubPriceQuoter{quote: &service.Quote{
		Model:         "gpt-5.5",
		GroupID:       3,
		ServedGroupID: 3,
		UserID:        12,
		Access:        service.QuoteAccess{OK: true},
		Priced:        true,
		Source:        service.QuoteSourceFallback,
		BillingMode:   "token",
		Prices: &service.QuotePriceSet{
			PerToken: service.QuoteUnitPrices{Input: 2.5e-6, Output: 15e-6},
			PerMTok:  service.QuoteUnitPrices{Input: 2.5, Output: 15},
		},
		GroupMultiplier:     1.5,
		UserMultiplier:      &userMultiplier,
		EffectiveMultiplier: 0.8,
		ImageMultiplier:     0.8,
		Policy:              service.QuotePolicyFlags{LongContextPolicy: "gpt-5.4-5.5"},
	}}
	rec, envelope := doPricingQuoteRequest(t, stub, "model=gpt-5.5&group_id=3&user_id=12")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, envelope.Code)

	data := envelope.Data
	require.Equal(t, "gpt-5.5", data["model"])
	require.Equal(t, float64(3), data["group_id"])
	require.Equal(t, "fallback", data["source"])
	require.Equal(t, true, data["priced"])
	require.Equal(t, 0.8, data["effective_multiplier"])
	require.Equal(t, 0.8, data["user_multiplier"])
	require.Equal(t, 1.5, data["group_multiplier"])
	require.Equal(t, map[string]any{"ok": true}, data["access"])

	prices, ok := data["prices"].(map[string]any)
	require.True(t, ok, "prices 应为对象")
	perMTok, ok := prices["per_mtok"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 2.5, perMTok["input"])
	require.Equal(t, 15.0, perMTok["output"])

	policy, ok := data["policy"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-5.4-5.5", policy["long_context_policy"])

	// 内部状态不进 JSON。
	require.NotContains(t, data, "quoter")
	require.NotContains(t, data, "resolved")
}

func TestPricingQuoteHandler_QuoterErrorsMapToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"group not found", service.ErrGroupNotFound, http.StatusNotFound, "GROUP_NOT_FOUND"},
		{"invalid service tier", service.ErrPriceQuoteServiceTierInvalid, http.StatusBadRequest, "PRICE_QUOTE_SERVICE_TIER_INVALID"},
		{"quoter unavailable", service.ErrPriceQuoterUnavailable, http.StatusServiceUnavailable, "PRICE_QUOTER_UNAVAILABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, envelope := doPricingQuoteRequest(t, &stubPriceQuoter{err: tt.err}, "model=gpt-5.5&group_id=3")
			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantReason, envelope.Reason)
		})
	}

	t.Run("unknown error is a server error", func(t *testing.T) {
		rec, _ := doPricingQuoteRequest(t, &stubPriceQuoter{err: errors.New("boom")}, "model=gpt-5.5&group_id=3")
		require.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
	})
}
