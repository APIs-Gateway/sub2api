package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideAdminHandlersAttachesUpstreamBillingProbe(t *testing.T) {
	accountHandler := admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	probe := service.NewUpstreamBillingProbeService(nil, nil, nil)

	adminHandlers := ProvideAdminHandlers(
		nil, nil, nil, accountHandler, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, probe,
	)

	require.Same(t, accountHandler, adminHandlers.Account)
}
