package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}

func TestMarkOpsUpstreamFailoverRecovered(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "failover", Message: "first account failed"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "retry_exhausted_failover", Message: "retry failed before failover"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "final upstream error"})

	MarkOpsUpstreamFailoverRecovered(c)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 3)
	require.True(t, events[0].FailoverRecovered)
	require.True(t, events[1].FailoverRecovered)
	require.False(t, events[2].FailoverRecovered)

	payload, err := json.Marshal(events)
	require.NoError(t, err)
	jsonText := string(payload)
	require.Contains(t, jsonText, `"failover_recovered":true`)
	require.True(t, strings.Count(jsonText, `"failover_recovered":false`) >= 1)
}

func TestMarkOpsUpstreamFailoverRecoveredGuards(t *testing.T) {
	MarkOpsUpstreamFailoverRecovered(nil)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	MarkOpsUpstreamFailoverRecovered(c)
	_, ok := c.Get(OpsUpstreamErrorsKey)
	require.False(t, ok)

	c.Set(OpsUpstreamErrorsKey, "bad-type")
	MarkOpsUpstreamFailoverRecovered(c)
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	require.Equal(t, "bad-type", raw)

	events := []*OpsUpstreamErrorEvent{
		nil,
		{Kind: "  FAILOVER  ", Message: "spaced and upper case"},
	}
	c.Set(OpsUpstreamErrorsKey, events)
	MarkOpsUpstreamFailoverRecovered(c)
	require.True(t, events[1].FailoverRecovered)
}
