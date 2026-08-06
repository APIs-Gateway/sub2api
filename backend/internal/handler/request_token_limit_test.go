package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayMaxInputTokens(t *testing.T) {
	require.Zero(t, gatewayMaxInputTokens(nil), "nil config must disable the check")

	cfg := &config.Config{}
	require.Zero(t, gatewayMaxInputTokens(cfg), "zero value must disable the check")

	cfg.Gateway.MaxInputTokens = 1000
	require.Equal(t, 1000, gatewayMaxInputTokens(cfg))
}

// A default deployment (max_input_tokens unset) must behave exactly as before:
// no request is ever rejected, however large.
func TestInputTokensWithinLimitDisabledByDefault(t *testing.T) {
	cfg := &config.Config{}
	body := []byte(`{"input":"` + strings.Repeat("a", 100_000) + `"}`)

	est, limit, ok := inputTokensWithinLimit(body, cfg)
	require.True(t, ok)
	require.Zero(t, limit)
	require.Zero(t, est)
}

func TestInputTokensWithinLimitRejectsOversized(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.MaxInputTokens = 100

	// 8000 ASCII chars -> ~2000 tokens, well over the 100 token ceiling.
	body := []byte(`{"input":"` + strings.Repeat("a", 8000) + `"}`)

	est, limit, ok := inputTokensWithinLimit(body, cfg)
	require.False(t, ok)
	require.Equal(t, 100, limit)
	require.Greater(t, est, 100)
}

func TestInputTokensWithinLimitAcceptsNormalRequest(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.MaxInputTokens = 1_000_000

	body := []byte(`{"model":"gpt-5.5","input":"hello world"}`)
	est, limit, ok := inputTokensWithinLimit(body, cfg)
	require.True(t, ok)
	require.Equal(t, 1_000_000, limit)
	require.Zero(t, est, "small bodies must skip estimation")
}

func TestBuildInputTokensTooLargeMessage(t *testing.T) {
	msg := buildInputTokensTooLargeMessage(6_956_114, 1_000_000)
	require.Contains(t, msg, "6956114", "message must report the estimate")
	require.Contains(t, msg, "1000000", "message must report the limit")
	require.Contains(t, msg, "too large")
}

// Exercises the exact shape the gateway handlers use: read body, check the
// limit, reject with 400 before any upstream work happens.
func TestInputTokenLimitRejectionFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Gateway.MaxInputTokens = 100

	forwarded := false
	router := gin.New()
	router.POST("/test", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		if est, limit, ok := inputTokensWithinLimit(body, cfg); !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": buildInputTokensTooLargeMessage(est, limit),
			})
			return
		}
		forwarded = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	t.Run("oversized is rejected before forwarding", func(t *testing.T) {
		forwarded = false
		huge := []byte(`{"input":"` + strings.Repeat("a", 8000) + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(huge))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "too large")
		require.False(t, forwarded, "over-limit request must never reach the upstream path")
	})

	t.Run("normal request passes through", func(t *testing.T) {
		forwarded = false
		small := []byte(`{"input":"hi there"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(small))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, forwarded)
	})
}
