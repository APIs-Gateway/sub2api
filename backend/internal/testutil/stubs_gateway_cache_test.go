//go:build unit

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestStubGatewayCacheReasoningContentIsNoOp(t *testing.T) {
	cache := StubGatewayCache{}
	ctx := context.Background()

	require.NoError(t, cache.SetReasoningContent(ctx, "item", "text", time.Minute))
	got, err := cache.GetReasoningContent(ctx, "item")
	require.ErrorIs(t, err, service.ErrReasoningContentNotFound)
	require.Empty(t, got)
}
