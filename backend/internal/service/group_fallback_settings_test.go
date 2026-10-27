//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultGroupFallbackSettings_DisabledByDefault(t *testing.T) {
	d := DefaultGroupFallbackSettings()
	require.False(t, d.Enabled, "the switch must default to off so PR1 changes no live behaviour")
	require.Equal(t, 2000, d.BusyWaitMS)
	require.Equal(t, 8000, d.StickyWaitMS)
	require.Equal(t, 3, d.BreakerMinDistinctUser)
	require.Equal(t, 25000, d.TotalBudgetNonStreamMS)
	require.Equal(t, 60000, d.TotalBudgetStreamMS)
	require.Equal(t, 12, d.MaxTotalAttempts)
	require.Equal(t, 3, d.MaxPerHopSwitches)
}

func TestParseGroupFallbackSettings(t *testing.T) {
	require.Equal(t, DefaultGroupFallbackSettings(), parseGroupFallbackSettings(nil))

	got := parseGroupFallbackSettings(map[string]string{
		SettingKeyGroupFallbackEnabled:              " TRUE ",
		SettingKeyGroupFallbackBusyWaitMS:           "1500",
		SettingKeyGroupFallbackStickyQueueShare:     "0.25",
		SettingKeyGroupFallbackBreakerMinUsers:      "5",
		SettingKeyGroupFallbackTotalBudgetNonStream: "-1",  // 非法回落默认
		SettingKeyGroupFallbackMaxTotalAttempts:     "abc", // 非法回落默认
		SettingKeyGroupFallbackMaxPerHopSwitches:    "0",   // 非正数回落默认
		SettingKeyGroupFallbackStickyWaitMS:         "",    // 空串回落默认
		SettingKeyGroupFallbackBreakerThreshold:     "7",
		SettingKeyGroupFallbackBreakerOpenTTLMS:     "1000",
		SettingKeyGroupFallbackBreakerWindowMS:      "2000",
		SettingKeyGroupFallbackTotalBudgetStream:    "90000",
	})
	require.True(t, got.Enabled)
	require.Equal(t, 1500, got.BusyWaitMS)
	require.Equal(t, 0.25, got.StickyQueueShare)
	require.Equal(t, 5, got.BreakerMinDistinctUser)
	require.Equal(t, 7, got.BreakerThreshold)
	require.Equal(t, 1000, got.BreakerOpenTTLMS)
	require.Equal(t, 2000, got.BreakerWindowMS)
	require.Equal(t, 90000, got.TotalBudgetStreamMS)
	d := DefaultGroupFallbackSettings()
	require.Equal(t, d.TotalBudgetNonStreamMS, got.TotalBudgetNonStreamMS)
	require.Equal(t, d.MaxTotalAttempts, got.MaxTotalAttempts)
	require.Equal(t, d.MaxPerHopSwitches, got.MaxPerHopSwitches)
	require.Equal(t, d.StickyWaitMS, got.StickyWaitMS)

	// 份额必须在 (0,1]
	require.Equal(t, d.StickyQueueShare, parseGroupFallbackSettings(map[string]string{SettingKeyGroupFallbackStickyQueueShare: "1.5"}).StickyQueueShare)
	require.Equal(t, d.StickyQueueShare, parseGroupFallbackSettings(map[string]string{SettingKeyGroupFallbackStickyQueueShare: "0"}).StickyQueueShare)
	// 只有字面 true 才开启
	require.False(t, parseGroupFallbackSettings(map[string]string{SettingKeyGroupFallbackEnabled: "1"}).Enabled)
	require.False(t, parseGroupFallbackSettings(map[string]string{SettingKeyGroupFallbackEnabled: ""}).Enabled)
}

type groupFallbackSettingRepoStub struct {
	SettingRepository
	vals map[string]string
	err  error
}

func (r *groupFallbackSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.vals[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func TestSettingServiceGetGroupFallbackSettings(t *testing.T) {
	ctx := context.Background()

	var nilSvc *SettingService
	require.False(t, nilSvc.IsGroupFallbackEnabled(ctx))
	require.False(t, (&SettingService{}).IsGroupFallbackEnabled(ctx))

	svc := &SettingService{settingRepo: &groupFallbackSettingRepoStub{vals: map[string]string{}}}
	require.False(t, svc.IsGroupFallbackEnabled(ctx), "unset => disabled")

	svc = &SettingService{settingRepo: &groupFallbackSettingRepoStub{vals: map[string]string{SettingKeyGroupFallbackEnabled: "true", SettingKeyGroupFallbackBusyWaitMS: "999"}}}
	got := svc.GetGroupFallbackSettings(ctx)
	require.True(t, got.Enabled)
	require.Equal(t, 999, got.BusyWaitMS)

	svc = &SettingService{settingRepo: &groupFallbackSettingRepoStub{err: errors.New("boom")}}
	require.False(t, svc.IsGroupFallbackEnabled(ctx), "read failure must fail closed")
}
