//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type configuredThresholdOpsRepo struct {
	*opsRepoMock
	overview *OpsDashboardOverview
	dbDown   bool
}

func (r *configuredThresholdOpsRepo) GetDashboardOverview(context.Context, *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	copy := *r.overview
	return &copy, nil
}

func (r *configuredThresholdOpsRepo) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	return &OpsSystemMetricsSnapshot{DBOK: boolPtr(!r.dbDown), RedisOK: boolPtr(true)}, nil
}

func TestOpsConfiguredThresholdsActualDashboard(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		missing   bool
		readError bool
		noRepo    bool
		errorRate float64
		upstream  float64
		ttft      *int
		idle      bool
		dbDown    bool
		want      int
	}{
		{name: "custom_request_threshold", raw: `{"request_error_rate_percent_max":50}`, errorRate: .10, want: 100},
		{name: "custom_ttft_threshold", raw: `{"ttft_p99_ms_max":2000}`, ttft: intPtr(5000), want: 96},
		{name: "independent_upstream_threshold", raw: `{"upstream_error_rate_percent_max":2}`, upstream: .02, want: 84},
		{name: "worst_configured_error", raw: `{"request_error_rate_percent_max":10,"upstream_error_rate_percent_max":2}`, errorRate: .05, upstream: .02, want: 84},
		{name: "absent_fields_disable_metrics", raw: `{}`, errorRate: .20, upstream: .30, ttft: intPtr(5000), want: 100},
		{name: "explicit_null_disables_metrics", raw: `{"request_error_rate_percent_max":null,"upstream_error_rate_percent_max":null,"ttft_p99_ms_max":null}`, errorRate: .20, upstream: .30, ttft: intPtr(5000), want: 100},
		{name: "zero_request_is_explicit", raw: `{"request_error_rate_percent_max":0}`, want: 65},
		{name: "zero_upstream_is_explicit", raw: `{"upstream_error_rate_percent_max":0}`, want: 65},
		{name: "zero_ttft_real_zero_sample", raw: `{"ttft_p99_ms_max":0}`, ttft: intPtr(0), want: 65},
		{name: "zero_ttft_missing_sample_neutral", raw: `{"ttft_p99_ms_max":0}`, want: 100},
		{name: "idle_even_with_zero_threshold", raw: `{"request_error_rate_percent_max":0,"ttft_p99_ms_max":0}`, idle: true, want: 100},
		{name: "missing_row_default", missing: true, errorRate: .02, want: 96},
		{name: "malformed_row_default", raw: `{invalid`, errorRate: .02, want: 96},
		{name: "read_failure_default", readError: true, errorRate: .02, want: 96},
		{name: "unavailable_repository_default", noRepo: true, errorRate: .02, want: 96},
		{name: "default_error_upper_boundary", missing: true, errorRate: .10, want: 65},
		{name: "default_error_lower_boundary", missing: true, errorRate: .01, want: 100},
		{name: "default_ttft_lower_boundary", missing: true, ttft: intPtr(1000), want: 100},
		{name: "default_ttft_midpoint", missing: true, ttft: intPtr(2000), want: 83},
		{name: "default_ttft_upper_boundary", missing: true, ttft: intPtr(3000), want: 65},
		{name: "sla_not_new_score_component", raw: `{"sla_percent_min":100}`, want: 100},
		{name: "infra_weight_retained", raw: `{}`, dbDown: true, want: 88},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := newRuntimeSettingRepoStub()
			if !tc.missing {
				settings.values[SettingKeyOpsMetricThresholds] = tc.raw
			}
			if tc.readError {
				settings.getValueFn = func(key string) (string, error) {
					if key == SettingKeyOpsMetricThresholds {
						return "", errors.New("threshold storage unavailable")
					}
					return "", ErrSettingNotFound
				}
			}
			ov := &OpsDashboardOverview{RequestCountTotal: 100, RequestCountSLA: 100,
				SLA: .50, ErrorRate: tc.errorRate, UpstreamErrorRate: tc.upstream, TTFT: OpsPercentiles{P99: tc.ttft}}
			if tc.idle {
				ov.RequestCountTotal, ov.RequestCountSLA = 0, 0
			}
			svc := &OpsService{opsRepo: &configuredThresholdOpsRepo{opsRepoMock: &opsRepoMock{}, overview: ov, dbDown: tc.dbDown}, settingRepo: settings}
			if tc.noRepo {
				svc.settingRepo = nil
			}
			now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
			got, err := svc.GetDashboardOverview(context.Background(), &OpsDashboardFilter{StartTime: now.Add(-time.Hour), EndTime: now, QueryMode: OpsQueryModeRaw})
			require.NoError(t, err)
			require.Equal(t, tc.want, got.HealthScore)
			require.Equal(t, .50, got.SLA)
			require.Equal(t, tc.errorRate, got.ErrorRate)
			require.Equal(t, tc.upstream, got.UpstreamErrorRate)
			require.Equal(t, tc.ttft, got.TTFT.P99)
			require.Zero(t, ov.HealthScore, "do not mutate repository snapshot")
			require.Zero(t, settings.setCalls, "dashboard threshold reads never initialize or rewrite settings")
		})
	}
}

func TestOpsConfiguredThresholdsDashboardReadCannotOverwriteConcurrentSave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings := newRuntimeSettingRepoStub()
	readStarted, releaseRead := make(chan struct{}), make(chan struct{})
	settings.getValueFn = func(key string) (string, error) {
		if key != SettingKeyOpsMetricThresholds {
			return "", ErrSettingNotFound
		}
		// Capture the missing-row result, then let an administrator save before
		// the dashboard observes that result. Defaults must stay in memory.
		close(readStarted)
		select {
		case <-releaseRead:
			return "", ErrSettingNotFound
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	svc := &OpsService{settingRepo: settings, opsRepo: &configuredThresholdOpsRepo{
		opsRepoMock: &opsRepoMock{}, overview: &OpsDashboardOverview{RequestCountTotal: 100, ErrorRate: .02},
	}}
	type result struct {
		overview *OpsDashboardOverview
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
		ov, err := svc.GetDashboardOverview(ctx, &OpsDashboardFilter{StartTime: now.Add(-time.Hour), EndTime: now, QueryMode: OpsQueryModeRaw})
		completed <- result{overview: ov, err: err}
	}()
	select {
	case <-readStarted:
	case <-ctx.Done():
		t.Fatal("dashboard did not reach captured missing threshold read")
	}
	custom := &OpsMetricThresholds{SLAPercentMin: float64Ptr(90), TTFTp99MsMax: float64Ptr(2000),
		RequestErrorRatePercentMax: float64Ptr(10), UpstreamErrorRatePercentMax: float64Ptr(20)}
	updated, err := svc.UpdateMetricThresholds(ctx, custom)
	close(releaseRead)
	require.NoError(t, err)
	require.Equal(t, custom, updated)
	select {
	case got := <-completed:
		require.NoError(t, got.err)
		require.Equal(t, 96, got.overview.HealthScore, "captured missing-row read uses default score")
	case <-ctx.Done():
		t.Fatal("dashboard did not complete after threshold read release")
	}
	settings.getValueFn = nil
	stored, err := svc.GetMetricThresholds(ctx)
	require.NoError(t, err)
	require.Equal(t, custom, stored)
	require.Equal(t, 1, settings.setCalls, "only the administrator's save writes settings")
}
