//go:build unit

package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStubSessionLimitCacheUnregisterSessionIsNoOp(t *testing.T) {
	cache := StubSessionLimitCache{}

	require.NoError(t, cache.UnregisterSession(context.Background(), 1, "session-a"))
	require.NoError(t, cache.UnregisterSession(context.Background(), 1, ""))
}
