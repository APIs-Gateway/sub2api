//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pseReader struct {
	ids     []int64
	idsErr  error
	modes   map[int64]MatrixAccessMode
	cells   []ExposureCell
	idsSeen int
}

func (r *pseReader) AllowlistGroupIDsTx(_ context.Context, _ MatrixExecutor) ([]int64, error) {
	r.idsSeen++
	return r.ids, r.idsErr
}

func (r *pseReader) AccessModesTx(_ context.Context, _ MatrixExecutor, ids []int64) (map[int64]MatrixAccessMode, error) {
	out := map[int64]MatrixAccessMode{}
	for _, id := range ids {
		if m, ok := r.modes[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

func (r *pseReader) OpenCellsTx(_ context.Context, _ MatrixExecutor, _ []int64) ([]ExposureCell, error) {
	return r.cells, nil
}

func pseCell(group int64, model string) ExposureCell {
	return ExposureCell{GroupID: group, Cell: MatrixCell{ModelKey: model, Open: true, PriceMode: MatrixPriceInherit}}
}

func TestSnapshotExposureChecker_NilReaderFailsClosed(t *testing.T) {
	require.Nil(t, NewSnapshotExposureChecker(&config.Config{}, nil, nil))
}

func TestSnapshotExposureChecker_UsesMergedPrices(t *testing.T) {
	reader := &pseReader{
		ids:   []int64{3},
		modes: map[int64]MatrixAccessMode{3: MatrixAccessAllowlist},
		cells: []ExposureCell{pseCell(3, "snap-model-x")},
	}
	checker := NewSnapshotExposureChecker(&config.Config{}, reader, nil)
	require.NotNil(t, checker)

	priced := map[string]*LiteLLMModelPricing{"snap-model-x": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6}}
	require.NoError(t, checker.CheckSnapshotApproval(context.Background(), nil, priced))

	zero := map[string]*LiteLLMModelPricing{"snap-model-x": {}}
	err := checker.CheckSnapshotApproval(context.Background(), nil, zero)
	require.Error(t, err)
	require.Contains(t, err.Error(), "3:snap-model-x:")

	err = checker.CheckSnapshotApproval(context.Background(), nil, map[string]*LiteLLMModelPricing{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "3:snap-model-x:unpriced")
}

func TestSnapshotExposureChecker_OpenGroupsAreNotBlocked(t *testing.T) {
	reader := &pseReader{
		ids:   []int64{4},
		modes: map[int64]MatrixAccessMode{4: MatrixAccessOpen},
		cells: []ExposureCell{pseCell(4, "snap-model-x")},
	}
	checker := NewSnapshotExposureChecker(&config.Config{}, reader, nil)
	require.NoError(t, checker.CheckSnapshotApproval(context.Background(), nil, map[string]*LiteLLMModelPricing{}))
}

func TestSnapshotExposureChecker_NoAllowlistGroupsPasses(t *testing.T) {
	reader := &pseReader{}
	checker := NewSnapshotExposureChecker(&config.Config{}, reader, nil)
	require.NoError(t, checker.CheckSnapshotApproval(context.Background(), nil, map[string]*LiteLLMModelPricing{}))
	require.Equal(t, 1, reader.idsSeen)
}

func TestSnapshotExposureChecker_ListErrorPropagates(t *testing.T) {
	reader := &pseReader{idsErr: errors.New("boom")}
	checker := NewSnapshotExposureChecker(&config.Config{}, reader, nil)
	err := checker.CheckSnapshotApproval(context.Background(), nil, map[string]*LiteLLMModelPricing{})
	require.ErrorContains(t, err, "boom")
}

func TestExposureErrorText(t *testing.T) {
	require.Equal(t, "plain", exposureErrorText(errors.New("plain")))
	err := exposureError([]ExposureViolation{{GroupID: 3, ModelKey: "m", Reason: ExposureUnpriced}})
	text := exposureErrorText(err)
	require.Contains(t, text, "3:m:unpriced")
	require.Contains(t, text, "1 of them")
}
