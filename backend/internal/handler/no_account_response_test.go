//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestGatewayHandlerRespondNoAccountErrorUsesEachProtocolEnvelope(t *testing.T) {
	styles := []gatewayNoAccountResponseStyle{
		gatewayNoAccountResponseStreaming,
		gatewayNoAccountResponseClaude,
		gatewayNoAccountResponseChatCompletions,
		gatewayNoAccountResponseResponses,
		gatewayNoAccountResponseGemini,
		gatewayNoAccountResponseStyle(99),
	}
	for _, style := range styles {
		t.Run("style", func(t *testing.T) {
			c := newTestGinContextWithRequest()
			h := &GatewayHandler{}
			fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false}}
			apiKey := &service.APIKey{GroupID: ptrInt64(7)}

			h.respondNoAccountError(c, fd, apiKey, "missing-model", "missing-model", service.PlatformOpenAI, "Service temporarily unavailable", service.ErrNoAvailableAccounts, noAccountCapacityMarkIfNoAvailable, style, false)

			require.Equal(t, http.StatusNotFound, c.Writer.Status())
			require.Len(t, fd.calls, 1)
		})
	}
}

func TestOpenAIGatewayHandlerRespondNoAccountErrorUsesEachProtocolEnvelope(t *testing.T) {
	styles := []openAINoAccountResponseStyle{
		openAINoAccountResponseStreaming,
		openAINoAccountResponseJSON,
		openAINoAccountResponseAnthropic,
		openAINoAccountResponseStyle(99),
	}
	for _, style := range styles {
		t.Run("style", func(t *testing.T) {
			c := newTestGinContextWithRequest()
			h := &OpenAIGatewayHandler{}
			fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false}}
			apiKey := &service.APIKey{GroupID: ptrInt64(7)}

			h.respondNoAccountError(c, fd, apiKey, "missing-model", "missing-model", service.PlatformOpenAI, "Service temporarily unavailable", service.ErrNoAvailableAccounts, noAccountCapacityMarkIfNoAvailable, style, false)

			require.Equal(t, http.StatusNotFound, c.Writer.Status())
			require.Len(t, fd.calls, 1)
		})
	}
}
