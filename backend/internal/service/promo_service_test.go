package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestPromoServiceUpdateClearsExpiryWhenZeroTimeProvided(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UTC()
	repo := &promoCodeRepoForUpdateTest{
		code: PromoCode{
			ID:        1,
			Code:      "WELCOME",
			Status:    PromoCodeStatusActive,
			ExpiresAt: &expiresAt,
		},
	}
	service := NewPromoService(repo, nil, nil, nil, nil)

	updated, err := service.Update(context.Background(), 1, &UpdatePromoCodeInput{
		ExpiresAt: &time.Time{},
	})

	require.NoError(t, err)
	require.Nil(t, updated.ExpiresAt)
	require.Nil(t, repo.updated.ExpiresAt)
}

func TestPromoServiceUpdatePreservesExpiryWhenOmitted(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UTC()
	repo := &promoCodeRepoForUpdateTest{
		code: PromoCode{
			ID:        1,
			Code:      "WELCOME",
			Status:    PromoCodeStatusActive,
			ExpiresAt: &expiresAt,
		},
	}
	service := NewPromoService(repo, nil, nil, nil, nil)

	updated, err := service.Update(context.Background(), 1, &UpdatePromoCodeInput{})

	require.NoError(t, err)
	require.NotNil(t, updated.ExpiresAt)
	require.True(t, expiresAt.Equal(*updated.ExpiresAt))
	require.NotNil(t, repo.updated.ExpiresAt)
	require.True(t, expiresAt.Equal(*repo.updated.ExpiresAt))
}

type promoCodeRepoForUpdateTest struct {
	code    PromoCode
	updated PromoCode
}

func (r *promoCodeRepoForUpdateTest) Create(context.Context, *PromoCode) error {
	panic("unexpected Create call")
}

func (r *promoCodeRepoForUpdateTest) GetByID(context.Context, int64) (*PromoCode, error) {
	code := r.code
	return &code, nil
}

func (r *promoCodeRepoForUpdateTest) GetByCode(context.Context, string) (*PromoCode, error) {
	panic("unexpected GetByCode call")
}

func (r *promoCodeRepoForUpdateTest) GetByCodeForUpdate(context.Context, string) (*PromoCode, error) {
	panic("unexpected GetByCodeForUpdate call")
}

func (r *promoCodeRepoForUpdateTest) Update(_ context.Context, code *PromoCode) error {
	r.updated = *code
	return nil
}

func (r *promoCodeRepoForUpdateTest) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (r *promoCodeRepoForUpdateTest) List(context.Context, pagination.PaginationParams) ([]PromoCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (r *promoCodeRepoForUpdateTest) ListWithFilters(context.Context, pagination.PaginationParams, string, string) ([]PromoCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (r *promoCodeRepoForUpdateTest) CreateUsage(context.Context, *PromoCodeUsage) error {
	panic("unexpected CreateUsage call")
}

func (r *promoCodeRepoForUpdateTest) GetUsageByPromoCodeAndUser(context.Context, int64, int64) (*PromoCodeUsage, error) {
	panic("unexpected GetUsageByPromoCodeAndUser call")
}

func (r *promoCodeRepoForUpdateTest) ListUsagesByPromoCode(context.Context, int64, pagination.PaginationParams) ([]PromoCodeUsage, *pagination.PaginationResult, error) {
	panic("unexpected ListUsagesByPromoCode call")
}

func (r *promoCodeRepoForUpdateTest) IncrementUsedCount(context.Context, int64) error {
	panic("unexpected IncrementUsedCount call")
}
