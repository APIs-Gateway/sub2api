//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAntigravityClaudeFailoverExhaustedClientIdentityRedacted(t *testing.T) {
	code := http.StatusTeapot
	custom := "Contact support"
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			custom *string
			rules  bool
			want   string
			code   int
		}{
			{"default", nil, false, "Upstream access forbidden, please contact administrator", 502},
			{"passthrough", nil, true, "Upstream access forbidden, please contact administrator", 418},
			{"custom", &custom, true, custom, 418},
		} {
			name := tc.name
			if stream {
				name += "_stream"
			}
			t.Run(name, func(t *testing.T) {
				c, rec := newPassthroughTestCtx()
				if tc.rules {
					service.BindErrorPassthroughService(c, service.NewErrorPassthroughService(&antigravitySensitiveRuleRepo{rules: []*model.ErrorPassthroughRule{{
						ID: 1584, Name: "match private project", Enabled: true, ErrorCodes: []int{403}, Keywords: []string{"projects/private-project-123"},
						MatchMode: model.MatchModeAll, Platforms: []string{service.PlatformAntigravity}, ResponseCode: &code,
						PassthroughBody: tc.custom == nil, CustomMessage: tc.custom, SkipMonitoring: true,
					}}}, nil))
				}
				failover := &service.UpstreamFailoverError{StatusCode: 403, RedactClientMessage: true,
					ResponseBody: []byte(`{"error":{"message":"projects/private-project-123 caller pool-sa@internal.example.com","details":[{"secret":"do-not-echo"}]}}`)}
				(&GatewayHandler{}).handleFailoverExhausted(c, failover, service.PlatformAntigravity, stream)
				if !stream {
					require.Equal(t, tc.code, rec.Code)
				} else {
					require.Contains(t, rec.Body.String(), `data: {"type":"error"`)
				}
				require.Contains(t, rec.Body.String(), tc.want)
				for _, private := range []string{"private-project-123", "pool-sa@", "do-not-echo"} {
					require.NotContains(t, rec.Body.String(), private)
				}
				raw, _ := c.Get(service.OpsUpstreamErrorMessageKey)
				require.Contains(t, raw, "private-project-123")
				if tc.rules {
					skip, _ := c.Get(service.OpsSkipPassthroughKey)
					require.Equal(t, true, skip)
				}
				require.False(t, failover.BillingNoCharge)
				require.False(t, failover.SafeToFailoverAfterWrite)
			})
		}
	}
}
