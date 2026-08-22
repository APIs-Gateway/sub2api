package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestApplyProfilePoolSettings_Grok(t *testing.T) {
	t.Run("nil cfg falls back to 120s default", func(t *testing.T) {
		s := &httpUpstreamService{}
		got := s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileGrok)
		require.Equal(t, 120*time.Second, got.responseHeaderTimeout)
	})

	t.Run("zero config value uses provider-safe default, not no-timeout", func(t *testing.T) {
		s := &httpUpstreamService{cfg: &config.Config{}}
		got := s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileGrok)
		require.Equal(t, 120*time.Second, got.responseHeaderTimeout,
			"a zero GrokResponseHeaderTimeout must mean 'use the provider-safe default', matching the field's own doc comment — not silently disable the timeout")
	})

	t.Run("positive config value overrides the default", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.GrokResponseHeaderTimeout = 45
		s := &httpUpstreamService{cfg: cfg}
		got := s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileGrok)
		require.Equal(t, 45*time.Second, got.responseHeaderTimeout)
	})
}

func TestResolveProtocolMode_Grok(t *testing.T) {
	s := &httpUpstreamService{}
	got := s.resolveProtocolMode(service.HTTPUpstreamProfileGrok, "", nil)
	require.Equal(t, upstreamProtocolModeGrok, got)
}
