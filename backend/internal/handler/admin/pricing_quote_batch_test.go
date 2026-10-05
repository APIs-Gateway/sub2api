//go:build unit

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func doPricingQuoteBatchRequest(t *testing.T, stub *stubPriceQuoter, rawQuery string) (*httptest.ResponseRecorder, pricingQuoteEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/pricing/quote-batch", (&PricingQuoteHandler{quoter: stub}).QuoteBatch)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/pricing/quote-batch?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var envelope pricingQuoteEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return rec, envelope
}

func TestPricingQuoteBatch_ParameterValidation(t *testing.T) {
	manyGroups := make([]string, quoteBatchMaxGroups+1)
	for i := range manyGroups {
		manyGroups[i] = fmt.Sprint(i + 1)
	}
	manyModels := make([]string, quoteBatchMaxModels+1)
	for i := range manyModels {
		manyModels[i] = fmt.Sprintf("m%d", i)
	}
	tests := []struct {
		name      string
		query     string
		wantParam string
		wantRea   string
	}{
		{"group_ids not a number", "group_ids=1,x&models=a", "group_ids", "INVALID_PARAMETER"},
		{"group_ids zero", "group_ids=0&models=a", "group_ids", "INVALID_PARAMETER"},
		{"too many group_ids", "group_ids=" + strings.Join(manyGroups, ",") + "&models=a", "group_ids", "INVALID_PARAMETER"},
		{"missing models", "group_ids=1", "models", "MISSING_PARAMETER"},
		{"too many models", "group_ids=1&models=" + strings.Join(manyModels, ","), "models", "INVALID_PARAMETER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubPriceQuoter{}
			rec, envelope := doPricingQuoteBatchRequest(t, stub, tt.query)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, tt.wantRea, envelope.Reason)
			require.Equal(t, tt.wantParam, envelope.Metadata["param"])
			require.Zero(t, stub.calls, "参数校验失败时不应调用 Quoter")
		})
	}
}

func TestPricingQuoteBatch_WithoutGroupsReturnsOnlyReferences(t *testing.T) {
	stub := &stubPriceQuoter{reference: service.QuoteOfficialReference{Priced: true, Source: service.QuoteSourceFallback}}
	for _, query := range []string{"models=a,b", "group_ids=%20,%20&models=a,b"} {
		rec, envelope := doPricingQuoteBatchRequest(t, stub, query)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Zero(t, stub.calls, "没有分组时不应报价")
		require.Len(t, envelope.Data["models"], 2)
		require.Empty(t, envelope.Data["cells"])
	}
}

func TestPricingQuoteBatch_ReturnsCellsAndReferences(t *testing.T) {
	extra := 1.2
	stub := &stubPriceQuoter{
		quote: &service.Quote{
			Access:              service.QuoteAccess{OK: true},
			Priced:              true,
			Source:              service.QuoteSourceLiteLLM,
			BillingMode:         "token",
			GroupMultiplier:     1.4,
			ExtraMultiplier:     &extra,
			EffectiveMultiplier: 1.68,
			FinalPrices: &service.QuotePriceSet{
				PerMTok: service.QuoteUnitPrices{Input: 3.36, Output: 16.8},
			},
		},
		reference: service.QuoteOfficialReference{
			Priced:  true,
			Source:  service.QuoteSourceLiteLLM,
			PerMTok: &service.QuoteUnitPrices{Input: 2, Output: 10},
		},
	}
	// 重复项与空白会被去掉：2 个分组 × 2 个模型 = 4 个格子。
	rec, envelope := doPricingQuoteBatchRequest(t, stub, "group_ids=3,4,3&models=%20gpt-5.5,gpt-5.4-mini,gpt-5.5")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 4, stub.calls)

	models, ok := envelope.Data["models"].([]any)
	require.True(t, ok)
	require.Len(t, models, 2)
	cells, ok := envelope.Data["cells"].([]any)
	require.True(t, ok)
	require.Len(t, cells, 4)

	first := cells[0].(map[string]any)
	require.Equal(t, float64(3), first["group_id"])
	require.Equal(t, "gpt-5.5", first["model"])
	require.Equal(t, 1.2, first["extra_multiplier"])
	require.Equal(t, 1.68, first["effective_multiplier"])
	require.Equal(t, "litellm", first["source"])
	require.Equal(t, map[string]any{"ok": true}, first["access"])
	final, ok := first["final_per_mtok"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 3.36, final["input"])
	require.NotContains(t, first, "per_request_price")
	require.NotContains(t, first, "error")
	require.Equal(t, float64(4), cells[3].(map[string]any)["group_id"])
}

func TestPricingQuoteBatch_PerRequestAndErrors(t *testing.T) {
	t.Run("per request price is multiplied", func(t *testing.T) {
		stub := &stubPriceQuoter{quote: &service.Quote{
			Priced:              true,
			EffectiveMultiplier: 2,
			PerRequest:          &service.QuotePerRequest{DefaultPrice: 0.05},
		}}
		rec, envelope := doPricingQuoteBatchRequest(t, stub, "group_ids=3&models=img")
		require.Equal(t, http.StatusOK, rec.Code)
		cell := envelope.Data["cells"].([]any)[0].(map[string]any)
		require.Equal(t, 0.1, cell["per_request_price"])
		require.NotContains(t, cell, "final_per_mtok")
	})

	t.Run("quote error becomes a cell error", func(t *testing.T) {
		stub := &stubPriceQuoter{err: service.ErrGroupNotFound}
		rec, envelope := doPricingQuoteBatchRequest(t, stub, "group_ids=3&models=a")
		require.Equal(t, http.StatusOK, rec.Code)
		cell := envelope.Data["cells"].([]any)[0].(map[string]any)
		require.Equal(t, "GROUP_NOT_FOUND", cell["error"])
	})

	t.Run("unknown error and nil quote use a generic reason", func(t *testing.T) {
		stub := &stubPriceQuoter{err: fmt.Errorf("boom")}
		_, envelope := doPricingQuoteBatchRequest(t, stub, "group_ids=3&models=a")
		require.Equal(t, "QUOTE_FAILED", envelope.Data["cells"].([]any)[0].(map[string]any)["error"])

		stub = &stubPriceQuoter{}
		_, envelope = doPricingQuoteBatchRequest(t, stub, "group_ids=3&models=a")
		require.Equal(t, "QUOTE_FAILED", envelope.Data["cells"].([]any)[0].(map[string]any)["error"])
	})
}
