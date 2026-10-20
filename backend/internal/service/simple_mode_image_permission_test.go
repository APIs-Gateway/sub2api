package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGroupAllowsImageGenerationForMode(t *testing.T) {
	for _, mode := range []string{config.RunModeSimple, config.RunModeStandard} {
		for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic} {
			for _, eligible := range []bool{false, true} {
				for _, allowed := range []bool{false, true} {
					g := &Group{ID: 1, Hydrated: true, Status: StatusActive, Platform: platform, AllowImageGeneration: allowed, SimpleModeAutoImageEligible: eligible}
					want := allowed || (mode == config.RunModeSimple && platform == PlatformOpenAI && eligible)
					require.Equal(t, want, GroupAllowsImageGenerationForMode(g, &config.Config{RunMode: mode}))
				}
			}
		}
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	require.True(t, GroupAllowsImageGenerationForMode(nil, cfg))
	for _, g := range []*Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: true}, {Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: true}, {ID: 1, Hydrated: true, Status: StatusDisabled, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: true}} {
		require.False(t, GroupAllowsImageGenerationForMode(g, cfg))
	}
	require.False(t, GroupAllowsImageGenerationForMode(&Group{SimpleModeAutoImageEligible: true}, nil))
}

func TestWSImagePermissionFailsClosedAfterAdminDisable(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := &OpenAIGatewayService{cfg: cfg}
	initial := &APIKey{Group: &Group{ID: 1, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: true}}
	group := *initial.Group
	hooks := &OpenAIWSIngressHooks{BeforeImagePermission: func() (*Group, error) { return &group, nil }}
	require.True(t, svc.currentWSImagePermission(hooks, initial))
	group.SimpleModeAutoImageEligible = false
	require.False(t, svc.currentWSImagePermission(hooks, initial))
	group.AllowImageGeneration = true
	require.True(t, svc.currentWSImagePermission(hooks, initial))
	hooks.BeforeImagePermission = func() (*Group, error) { return initial.Group, errors.New("database unavailable") }
	require.False(t, svc.currentWSImagePermission(hooks, initial))
	hooks.BeforeImagePermission = func() (*Group, error) { return nil, nil }
	require.False(t, svc.currentWSImagePermission(hooks, initial))
}

func TestWSImageSessionPermissionCoversInheritedAndMappedModels(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "gpt-image-1"}}}
	for _, tt := range []struct {
		body, session string
		tools, want   bool
	}{
		{`{"type":"response.create","model":"gpt-5.1"}`, "", false, false},
		{`{"type":"response.create"}`, "gpt-image-1", false, true},
		{`{"type":"response.create","model":"gpt-5.1"}`, "gpt-image-1", false, false},
		{`{"type":"response.create","model":"gpt-5.1"}`, "", true, true},
		{`{"type":"response.create","model":"gpt-5.1","tools":[]}`, "", true, false},
		{`{"type":"response.create","model":"alias"}`, "", false, true},
		{`{"type":"session.update","session":{"tools":[{"type":"image_generation"}]}}`, "", false, true},
		{`{"type":"session.update","session":{"model":"alias"}}`, "", false, true},
		{`{"type":"session.update","session":{"tools":[]}}`, "", true, false},
	} {
		require.Equal(t, tt.want, openAIWSFrameRequiresImagePermission(account, []byte(tt.body), tt.session, tt.tools, false), tt.body)
	}
}

func TestWSInheritedImageChoiceUsesItsOwnOverrideField(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI}
	require.True(t, openAIWSFrameRequiresImagePermission(account, []byte(`{"type":"response.create","model":"gpt-5.1","tools":[{"type":"namespace","name":"image_gen"}]}`), "gpt-5.1", false, true))
	require.False(t, openAIWSFrameRequiresImagePermission(account, []byte(`{"type":"response.create","model":"gpt-5.1","tool_choice":"none"}`), "gpt-5.1", false, true))
}

func TestSimpleModeEligibleResponsesReachUpstreamAndKeepStandardPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{config.RunModeSimple, config.RunModeStandard} {
		for _, eligible := range []bool{true, false} {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":[{"id":"ig1","type":"image_generation_call","result":"aGVsbG8=","size":"1024x1024"}],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
			cfg := &config.Config{RunMode: mode}
			cfg.Security.URLAllowlist.Enabled = false
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://example.test/v1", "model_mapping": map[string]any{"draw-alias": "gpt-image-2"}}, Extra: openAIResponsesSupportedTestExtra()}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("api_key", &APIKey{Group: &Group{ID: 1, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: eligible}})
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"draw-alias","input":"draw","stream":false}`))
			if mode == config.RunModeSimple && eligible {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, upstream.lastReq)
			} else {
				require.Error(t, err)
				require.Nil(t, upstream.lastReq)
				require.Equal(t, http.StatusForbidden, rec.Code)
			}
		}
	}
}
