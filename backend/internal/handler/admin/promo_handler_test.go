package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPromoHandlerUpdateClearsExpiryWhenZeroTimestampProvided(t *testing.T) {
	gin.SetMode(gin.TestMode)

	expiresAt := time.Now().Add(24 * time.Hour).UTC()
	repo := &promoCodeRepoForHandlerTest{
		code: service.PromoCode{
			ID:        1,
			Code:      "WELCOME",
			Status:    service.PromoCodeStatusActive,
			ExpiresAt: &expiresAt,
		},
	}
	handler := NewPromoHandler(service.NewPromoService(repo, nil, nil, nil, nil))

	router := gin.New()
	router.PUT("/promo-codes/:id", handler.Update)

	req := httptest.NewRequest(http.MethodPut, "/promo-codes/1", bytes.NewBufferString(`{"expires_at":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, repo.updated.ExpiresAt)
}

type promoCodeRepoForHandlerTest struct {
	code    service.PromoCode
	updated service.PromoCode
}

func (r *promoCodeRepoForHandlerTest) Create(context.Context, *service.PromoCode) error {
	panic("unexpected Create call")
}

func (r *promoCodeRepoForHandlerTest) GetByID(context.Context, int64) (*service.PromoCode, error) {
	code := r.code
	return &code, nil
}

func (r *promoCodeRepoForHandlerTest) GetByCode(context.Context, string) (*service.PromoCode, error) {
	panic("unexpected GetByCode call")
}

func (r *promoCodeRepoForHandlerTest) GetByCodeForUpdate(context.Context, string) (*service.PromoCode, error) {
	panic("unexpected GetByCodeForUpdate call")
}

func (r *promoCodeRepoForHandlerTest) Update(_ context.Context, code *service.PromoCode) error {
	r.updated = *code
	return nil
}

func (r *promoCodeRepoForHandlerTest) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (r *promoCodeRepoForHandlerTest) List(context.Context, pagination.PaginationParams) ([]service.PromoCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (r *promoCodeRepoForHandlerTest) ListWithFilters(context.Context, pagination.PaginationParams, string, string) ([]service.PromoCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (r *promoCodeRepoForHandlerTest) CreateUsage(context.Context, *service.PromoCodeUsage) error {
	panic("unexpected CreateUsage call")
}

func (r *promoCodeRepoForHandlerTest) GetUsageByPromoCodeAndUser(context.Context, int64, int64) (*service.PromoCodeUsage, error) {
	panic("unexpected GetUsageByPromoCodeAndUser call")
}

func (r *promoCodeRepoForHandlerTest) ListUsagesByPromoCode(context.Context, int64, pagination.PaginationParams) ([]service.PromoCodeUsage, *pagination.PaginationResult, error) {
	panic("unexpected ListUsagesByPromoCode call")
}

func (r *promoCodeRepoForHandlerTest) IncrementUsedCount(context.Context, int64) error {
	panic("unexpected IncrementUsedCount call")
}
