//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionBindingHash_IsStableAndChangesWithFingerprint(t *testing.T) {
	binding := &SessionBinding{IP: " 203.0.113.7 ", UserAgent: "  test-agent/1.0 "}

	first := binding.Hash()
	require.Len(t, first, 32)
	require.Equal(t, first, (&SessionBinding{IP: "203.0.113.7", UserAgent: "test-agent/1.0"}).Hash())
	require.NotEqual(t, first, (&SessionBinding{IP: "203.0.113.8", UserAgent: "test-agent/1.0"}).Hash())
	require.NotEqual(t, first, (&SessionBinding{IP: "203.0.113.7", UserAgent: "test-agent/2.0"}).Hash())
}

func TestSessionBindingHash_EmptyDataDoesNotBind(t *testing.T) {
	require.Empty(t, (*SessionBinding)(nil).Hash())
	require.Empty(t, (&SessionBinding{}).Hash())
	require.NotEmpty(t, (&SessionBinding{UserAgent: "agent"}).Hash())
}

func TestSessionBindingContext_RoundTripsAndPreservesEmptyContext(t *testing.T) {
	ctx := context.Background()
	binding := &SessionBinding{IP: "203.0.113.7", UserAgent: "agent"}

	require.Nil(t, SessionBindingFromContext(nil))
	require.Nil(t, SessionBindingFromContext(ctx))
	require.Same(t, binding, SessionBindingFromContext(WithSessionBinding(ctx, binding)))
	require.Equal(t, ctx, WithSessionBinding(ctx, nil))
}
