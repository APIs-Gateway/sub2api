//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type fakePricingStageStore struct {
	calls  int
	last   PricingStageSwitchRequest
	change *PricingStageChange
	err    error
}

func (f *fakePricingStageStore) SwitchStage(_ context.Context, groupID int64, to PricingStage, operatorID int64, _ time.Time) (*PricingStageChange, error) {
	f.calls++
	f.last = PricingStageSwitchRequest{GroupID: groupID, To: to, OperatorID: operatorID}
	if f.err != nil {
		return nil, f.err
	}
	if f.change != nil {
		return f.change, nil
	}
	return &PricingStageChange{GroupID: groupID, From: PricingStageLegacy, To: to, Changed: true, Revision: 2}, nil
}

func TestPricingStageService_Validation(t *testing.T) {
	store := &fakePricingStageStore{}
	svc := NewPricingStageService(store, nil, nil)
	ctx := context.Background()

	cases := []struct {
		name   string
		req    PricingStageSwitchRequest
		reason string
	}{
		{"no operator", PricingStageSwitchRequest{GroupID: 1, To: PricingStageShadow, Confirm: true}, ReasonPricingStageActor},
		{"v2 refused", PricingStageSwitchRequest{GroupID: 1, To: PricingStageV2, OperatorID: 9, Confirm: true}, ReasonPricingStageNotAllowed},
		{"no confirm", PricingStageSwitchRequest{GroupID: 1, To: PricingStageShadow, OperatorID: 9}, ReasonPricingStageConfirm},
	}
	for _, c := range cases {
		_, err := svc.Switch(ctx, c.req)
		require.Error(t, err, c.name)
		require.Equal(t, c.reason, infraerrors.Reason(err), c.name)
	}
	require.Zero(t, store.calls)
}

func TestPricingStageService_SwitchOK(t *testing.T) {
	store := &fakePricingStageStore{}
	svc := NewPricingStageService(store, nil, nil)
	res, err := svc.Switch(context.Background(), PricingStageSwitchRequest{GroupID: 3, To: PricingStageShadow, OperatorID: 9, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, PricingActionStageSwitch, res.Action)
	require.True(t, res.TouchesPrice)
	require.Equal(t, PriceDeltaNone, res.PriceDelta)
	require.Equal(t, PricingStageShadow, res.To)
	require.Equal(t, 1, store.calls)
	require.Equal(t, int64(9), store.last.OperatorID)
}

func TestPricingStageService_StoreErrorPropagates(t *testing.T) {
	store := &fakePricingStageStore{err: ErrPricingStageNotDerived}
	svc := NewPricingStageService(store, nil, nil)
	_, err := svc.Switch(context.Background(), PricingStageSwitchRequest{GroupID: 3, To: PricingStageLegacy, OperatorID: 9, Confirm: true})
	require.ErrorIs(t, err, ErrPricingStageNotDerived)
}

func TestPricingStageService_NilPolicyAndRecorder(t *testing.T) {
	svc := NewPricingStageService(&fakePricingStageStore{}, nil, nil)
	require.NotNil(t, svc.ShadowStats().ComparedTotal)
	samples, err := svc.ShadowSamples(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, samples)
	require.Equal(t, MatrixSnapshotStats{}, svc.MatrixSnapshotStats())
}
