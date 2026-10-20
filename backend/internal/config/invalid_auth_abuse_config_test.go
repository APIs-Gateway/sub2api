package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaultInvalidAuthAbuseConfig(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.APIKeyAuth.InvalidAbuse.Enabled)
	require.Equal(t, 120, cfg.APIKeyAuth.InvalidAbuse.Threshold)
	require.Equal(t, 60, cfg.APIKeyAuth.InvalidAbuse.WindowSeconds)
	require.Equal(t, 60, cfg.APIKeyAuth.InvalidAbuse.BlockSeconds)
	require.Equal(t, 16384, cfg.APIKeyAuth.InvalidAbuse.Capacity)
}

func TestValidateInvalidAuthAbuseConfigBounds(t *testing.T) {
	tests := []struct {
		name string
		cfg  InvalidAuthAbuseConfig
		want string
	}{
		{
			name: "threshold",
			cfg:  InvalidAuthAbuseConfig{Enabled: true, Threshold: 9, WindowSeconds: 60, BlockSeconds: 60, Capacity: 16384},
			want: "threshold",
		},
		{
			name: "window",
			cfg:  InvalidAuthAbuseConfig{Enabled: true, Threshold: 120, WindowSeconds: 3601, BlockSeconds: 60, Capacity: 16384},
			want: "window_seconds",
		},
		{
			name: "block",
			cfg:  InvalidAuthAbuseConfig{Enabled: true, Threshold: 120, WindowSeconds: 60, BlockSeconds: 3601, Capacity: 16384},
			want: "block_seconds",
		},
		{
			name: "capacity",
			cfg:  InvalidAuthAbuseConfig{Enabled: true, Threshold: 120, WindowSeconds: 60, BlockSeconds: 60, Capacity: 255},
			want: "capacity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{APIKeyAuth: APIKeyAuthCacheConfig{InvalidAbuse: tt.cfg}}
			err := cfg.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}
