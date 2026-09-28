//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type antigravitySensitiveRuleRepo struct {
	rules []*model.ErrorPassthroughRule
}

func (r *antigravitySensitiveRuleRepo) List(context.Context) ([]*model.ErrorPassthroughRule, error) {
	return r.rules, nil
}
func (*antigravitySensitiveRuleRepo) GetByID(context.Context, int64) (*model.ErrorPassthroughRule, error) {
	return nil, nil
}
func (*antigravitySensitiveRuleRepo) Create(context.Context, *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	return nil, nil
}
func (*antigravitySensitiveRuleRepo) Update(context.Context, *model.ErrorPassthroughRule) (*model.ErrorPassthroughRule, error) {
	return nil, nil
}
func (*antigravitySensitiveRuleRepo) Delete(context.Context, int64) error {
	return nil
}

func TestAntigravityGeminiFailoverExhaustedNeverPassesPoolIdentityThroughRule(t *testing.T) {
	mappedCode := http.StatusTeapot
	customMessage := "Please contact support"
	for _, tc := range []struct {
		name         string
		passthrough  bool
		custom       *string
		wantMessage  string
	}{
		{name: "passthrough_body", passthrough: true, wantMessage: "Upstream access forbidden, please contact administrator"},
		{name: "custom_message", custom: &customMessage, wantMessage: customMessage},
	} {
		for _, entry := range []struct {
			name  string
			write func(*GatewayHandler, *gin.Context, *service.UpstreamFailoverError)
		}{
			{name: "native", write: func(h *GatewayHandler, c *gin.Context, err *service.UpstreamFailoverError) {
				h.handleGeminiFailoverExhausted(c, err)
			}},
			{name: "gateway", write: func(h *GatewayHandler, c *gin.Context, err *service.UpstreamFailoverError) {
				h.handleFailoverExhausted(c, err, service.PlatformGemini, false)
			}},
		} {
			t.Run(tc.name+"/"+entry.name, func(t *testing.T) {
				c, writer := newPassthroughTestCtx()
				rules := service.NewErrorPassthroughService(&antigravitySensitiveRuleRepo{rules: []*model.ErrorPassthroughRule{{
					ID: 1379, Name: "match raw pool project", Enabled: true, Priority: 1,
					ErrorCodes: []int{http.StatusForbidden}, Keywords: []string{"projects/123456789"},
					MatchMode: model.MatchModeAll, Platforms: []string{model.PlatformGemini},
					PassthroughCode: false, ResponseCode: &mappedCode,
					PassthroughBody: tc.passthrough, CustomMessage: tc.custom, SkipMonitoring: true,
				}}}, nil)
				service.BindErrorPassthroughService(c, rules)
				failover := &service.UpstreamFailoverError{
					StatusCode: http.StatusForbidden,
					ResponseBody: []byte(`{"error":{"message":"projects/123456789 caller pool-sa@internal.example.com","details":[{"secret":"do-not-echo"}]}}`),
					RedactClientMessage: true,
				}

				entry.write(&GatewayHandler{}, c, failover)
				require.Equal(t, mappedCode, writer.Code, "rule must still match the raw upstream body")
				require.Contains(t, writer.Body.String(), tc.wantMessage)
				for _, sensitive := range []string{"123456789", "pool-sa@", "do-not-echo"} {
					require.NotContains(t, writer.Body.String(), sensitive)
				}
				upstreamMessage, exists := c.Get(service.OpsUpstreamErrorMessageKey)
				require.True(t, exists)
				require.Contains(t, upstreamMessage, "projects/123456789")
				skipMonitoring, exists := c.Get(service.OpsSkipPassthroughKey)
				require.True(t, exists)
				require.Equal(t, true, skipMonitoring)
			})
		}
	}
}
