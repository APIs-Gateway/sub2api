//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSimpleModeEligibleImagesReachUpstreamWhileManualGroupStaysDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, eligible := range []bool{true, false} {
		t.Run(map[bool]string{true: "system default", false: "manual group"}[eligible], func(t *testing.T) {
			gid := int64(8800)
			repo := &openAIImagesBalanceSwitchAccountRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{{ID: 8802, Name: "image-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "test", "base_url": "https://images.example.test/v1"}}}}}
			upstream := &openAIImagesBalanceSwitchUpstream{}
			cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{DisableOpenAIImagesStreaming: true}}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw","stream":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 8899, GroupID: &gid, Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SimpleModeAutoImageEligible: eligible}, User: &service.User{ID: 8898}})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 8898})
			h.Images(c)
			if eligible {
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, []int64{8802}, upstream.calls)
			} else {
				require.Equal(t, http.StatusForbidden, rec.Code)
				require.Empty(t, upstream.calls)
			}
		})
	}
}

type simpleModeResponsesImageUpstream struct {
	service.HTTPUpstream
	calls int
}

func (u *simpleModeResponsesImageUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_images","status":"completed","output":[{"id":"ig1","type":"image_generation_call","result":"aGVsbG8=","size":"1024x1024"}],"usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func TestSimpleModeEligibleResponsesHandlerReachUpstreamWhileManualGroupStaysDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, eligible := range []bool{true, false} {
		gid := int64(8800)
		repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{{ID: 8802, Name: "response-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "test", "base_url": "https://responses.example.test/v1"}, Extra: map[string]any{"openai_responses_supported": true}}}}
		upstream := &simpleModeResponsesImageUpstream{}
		cfg := &config.Config{RunMode: config.RunModeSimple}
		gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
		t.Cleanup(billing.Stop)
		h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.1","tools":[{"type":"image_generation"}],"input":"draw","stream":false}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 8899, GroupID: &gid, Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SimpleModeAutoImageEligible: eligible}, User: &service.User{ID: 8898}})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 8898})
		h.Responses(c)
		if eligible {
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 1, upstream.calls)
		} else {
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Zero(t, upstream.calls)
		}
	}
}
