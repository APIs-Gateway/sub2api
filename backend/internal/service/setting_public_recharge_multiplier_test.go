//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 前端要靠 /settings/public 里的充值倍率把额度折算成法币展示，
// 这个字段一旦漏掉或算错，整站的「¥」金额就全错了。
func TestSettingService_GetPublicSettings_ExposesBalanceRechargeMultiplier(t *testing.T) {
	repo := &settingPublicRepoStub{
		values: map[string]string{SettingBalanceRechargeMult: "13"},
	}
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 13.0, settings.BalanceRechargeMultiplier, 1e-9)
}

// 缺失或损坏的倍率必须回落到 1（即「不折算」），绝不能变成 0 或负数——
// 前端拿 1/multiplier 做折算，0 会得到 Inf，负数会把花费显示成负的。
func TestSettingService_GetPublicSettings_BrokenMultiplierFallsBackToOne(t *testing.T) {
	for _, raw := range []string{"", "abc", "0", "-13"} {
		values := map[string]string{}
		if raw != "" {
			values[SettingBalanceRechargeMult] = raw
		}
		svc := NewSettingService(&settingPublicRepoStub{values: values}, &config.Config{})

		settings, err := svc.GetPublicSettings(context.Background())
		require.NoError(t, err)
		require.InDeltaf(t, 1.0, settings.BalanceRechargeMultiplier, 1e-9,
			"倍率原值 %q 应回落到 1", raw)
	}
}
