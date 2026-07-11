//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayServiceDiagnoseModelAvailabilityForPlatform(t *testing.T) {
	groupID := int64(7)

	newService := func(accounts []Account) *OpenAIGatewayService {
		return &OpenAIGatewayService{
			accountRepo: newModelAvailabilityAccountRepo(accounts),
			cfg:         testConfig(),
		}
	}

	t.Run("nil receiver stays on the conservative fallback", func(t *testing.T) {
		var svc *OpenAIGatewayService

		diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.True(t, diag.HasModelSupport)
	})

	t.Run("empty requested model stays on the conservative fallback", func(t *testing.T) {
		diag := newService(nil).DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "  ", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.True(t, diag.HasModelSupport)
	})

	t.Run("lookup errors stay on the conservative fallback", func(t *testing.T) {
		repo := newModelAvailabilityAccountRepo(nil)
		repo.listByGroupErr = errors.New("database unavailable")
		svc := &OpenAIGatewayService{
			accountRepo: repo,
			cfg:         testConfig(),
		}

		diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.True(t, diag.HasModelSupport)
	})

	t.Run("empty pool remains a 503 signal", func(t *testing.T) {
		diag := newService(nil).DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5", PlatformOpenAI)

		require.False(t, diag.HasAccountsInPool)
		require.False(t, diag.HasModelSupport)
	})

	accounts := []Account{{
		ID:          1,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5": "gpt-5"},
		},
	}}

	t.Run("matching mapping keeps the service-unavailable classification", func(t *testing.T) {
		diag := newService(accounts).DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.True(t, diag.HasModelSupport)
	})

	t.Run("unsupported mapping emits the model-not-found signal", func(t *testing.T) {
		diag := newService(accounts).DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5-mini", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.False(t, diag.HasModelSupport)
	})

	t.Run("simple mode uses the OpenAI platform pool", func(t *testing.T) {
		cfg := testConfig()
		cfg.RunMode = config.RunModeSimple
		svc := &OpenAIGatewayService{
			accountRepo: newModelAvailabilityAccountRepo(accounts),
			cfg:         cfg,
		}

		diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5-mini", PlatformOpenAI)

		require.True(t, diag.HasAccountsInPool)
		require.False(t, diag.HasModelSupport)
	})
}
