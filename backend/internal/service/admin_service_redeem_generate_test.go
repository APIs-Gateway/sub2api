package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type captureRedeemCreateRepo struct {
	created []RedeemCode
}

func (r *captureRedeemCreateRepo) Create(_ context.Context, code *RedeemCode) error {
	code.ID = int64(len(r.created) + 1)
	r.created = append(r.created, *code)
	return nil
}

func (r *captureRedeemCreateRepo) CreateBatch(context.Context, []RedeemCode) error {
	panic("unexpected CreateBatch call")
}
func (r *captureRedeemCreateRepo) GetByID(context.Context, int64) (*RedeemCode, error) {
	panic("unexpected GetByID call")
}
func (r *captureRedeemCreateRepo) GetByCode(context.Context, string) (*RedeemCode, error) {
	panic("unexpected GetByCode call")
}
func (r *captureRedeemCreateRepo) Update(context.Context, *RedeemCode) error {
	panic("unexpected Update call")
}
func (r *captureRedeemCreateRepo) BatchUpdate(context.Context, []int64, RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected BatchUpdate call")
}
func (r *captureRedeemCreateRepo) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}
func (r *captureRedeemCreateRepo) Use(context.Context, int64, int64) error {
	panic("unexpected Use call")
}
func (r *captureRedeemCreateRepo) List(context.Context, pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}
func (r *captureRedeemCreateRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}
func (r *captureRedeemCreateRepo) ListByUser(context.Context, int64, int) ([]RedeemCode, error) {
	panic("unexpected ListByUser call")
}
func (r *captureRedeemCreateRepo) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}
func (r *captureRedeemCreateRepo) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

func TestAdminServiceGenerateRedeemCodes_SubscriptionUsesDailyAmountWithoutGroup(t *testing.T) {
	repo := &captureRedeemCreateRepo{}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	codes, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{
		Type:         RedeemTypeSubscription,
		Value:        30,
		ValidityDays: 30,
		Count:        2,
	})

	require.NoError(t, err)
	require.Len(t, codes, 2)
	require.Len(t, repo.created, 2)
	for _, code := range repo.created {
		require.Equal(t, RedeemTypeSubscription, code.Type)
		require.Equal(t, 30.0, code.Value)
		require.Equal(t, 30, code.ValidityDays)
		require.Nil(t, code.GroupID)
	}
}

func TestAdminServiceGenerateRedeemCodes_SubscriptionRejectsMissingDailyAmountOrValidity(t *testing.T) {
	svc := &adminServiceImpl{redeemCodeRepo: &captureRedeemCreateRepo{}}

	tests := []struct {
		name  string
		input *GenerateRedeemCodesInput
	}{
		{
			name:  "missing validity",
			input: &GenerateRedeemCodesInput{Type: RedeemTypeSubscription, Value: 30, Count: 1},
		},
		{
			name:  "missing daily amount",
			input: &GenerateRedeemCodesInput{Type: RedeemTypeSubscription, ValidityDays: 30, Count: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codes, err := svc.GenerateRedeemCodes(context.Background(), tt.input)
			require.Error(t, err)
			require.Nil(t, codes)
		})
	}
}
