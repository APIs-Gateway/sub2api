package routes

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServerTimingOnlyWiredIntoAdminRoutes is a static wiring guard: issue
// #748 (narrowed scope) activates Server-Timing observability for the admin
// route group only. User-facing web API and gateway routes must not gain
// this middleware as a side effect of an unrelated change, since extending
// it to user-facing endpoints is an explicit, separately-evaluated decision
// (see the issue) and not something this test should silently start
// allowing.
func TestServerTimingOnlyWiredIntoAdminRoutes(t *testing.T) {
	adminSource, err := os.ReadFile("admin.go")
	require.NoError(t, err)
	require.Contains(t, string(adminSource), "middleware.ServerTiming()",
		"admin.go must mount the Server-Timing middleware on the admin route group")

	otherRouteFiles := []string{
		"auth.go",
		"common.go",
		"gateway.go",
		"legacy_invite.go",
		"payment.go",
		"user.go",
	}
	for _, filename := range otherRouteFiles {
		source, readErr := os.ReadFile(filename)
		require.NoError(t, readErr)
		require.NotContainsf(t, string(source), "ServerTiming",
			"%s must not reference the admin-only Server-Timing middleware", filename)
	}
}
