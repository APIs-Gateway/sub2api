package service

import (
	"errors"
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
		require.Equal(t, tt.want, openAIWSFrameRequiresImagePermission(account, []byte(tt.body), tt.session, tt.tools), tt.body)
	}
}
