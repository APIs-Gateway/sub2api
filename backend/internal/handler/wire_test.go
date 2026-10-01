package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/stretchr/testify/require"
)

func TestProvideAdminHandlersIncludesPromptAuditHandler(t *testing.T) {
	promptAudit := securityaudit.NewPromptEventAdminHandler(nil)
	adminHandlers := ProvideAdminHandlers(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, // grokOAuthHandler
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, promptAudit,
		nil,
		nil, // pricingQuoteHandler
		nil, // adminTokenHandler
		nil, // auditLogHandler
	)

	require.Same(t, promptAudit, adminHandlers.PromptAudit)
}
