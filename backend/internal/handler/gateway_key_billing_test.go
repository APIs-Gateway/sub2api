package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type keyBillingUserGroupRateRepo struct {
	service.UserGroupRateRepository
	rate       *float64
	err        error
	gotUserID  int64
	gotGroupID int64
	calls      int
}

func (r *keyBillingUserGroupRateRepo) GetByUserAndGroup(_ context.Context, userID, groupID int64) (*float64, error) {
	r.gotUserID = userID
	r.gotGroupID = groupID
	r.calls++
	return r.rate, r.err
}

func newKeyBillingGatewayService(repo service.UserGroupRateRepository) *service.GatewayService {
	return service.NewGatewayService(
		nil, nil, nil, nil, nil, nil, repo, nil, &config.Config{}, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil,
	)
}

func newKeyBillingOpenAIGatewayService(repo service.UserGroupRateRepository) *service.OpenAIGatewayService {
	return service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, repo, nil, &config.Config{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func newKeyBillingContext(apiKey *service.APIKey) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
	if apiKey != nil {
		context.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	}
	return context, recorder
}

func TestGatewayHandlerKeyBillingInfoUsesGroupRate(t *testing.T) {
	groupID := int64(7)
	apiKey := &service.APIKey{
		UserID:  11,
		Key:     "sk-sensitive-value",
		GroupID: &groupID,
		Group: &service.Group{
			ID:             groupID,
			Name:           "private-group-name",
			RateMultiplier: 0.75,
		},
	}
	context, recorder := newKeyBillingContext(apiKey)
	handler := &GatewayHandler{gatewayService: newKeyBillingGatewayService(nil)}

	handler.KeyBillingInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	var response keyBillingInfoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "sub2api.key_billing", response.Object)
	require.Equal(t, 1, response.SchemaVersion)
	require.Equal(t, "token", response.BillingScope)
	require.Equal(t, 0.75, response.GroupRateMultiplier)
	require.Nil(t, response.UserRateMultiplier)
	require.Equal(t, 0.75, response.ResolvedRateMultiplier)
	require.False(t, response.PeakRateEnabled)
	require.Nil(t, response.AppliedPeakMultiplier)
	require.Equal(t, 0.75, response.EffectiveRateMultiplier)
	require.False(t, response.ObservedAt.IsZero())
	require.NotContains(t, recorder.Body.String(), apiKey.Key)
	require.NotContains(t, recorder.Body.String(), apiKey.Group.Name)
}

func TestGatewayHandlerKeyBillingInfoUsesUserOverride(t *testing.T) {
	groupID := int64(7)
	userRate := 0.5
	repo := &keyBillingUserGroupRateRepo{rate: &userRate}
	apiKey := &service.APIKey{
		UserID:  11,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, RateMultiplier: 0.75},
	}
	context, recorder := newKeyBillingContext(apiKey)
	handler := &GatewayHandler{gatewayService: newKeyBillingGatewayService(repo)}

	handler.KeyBillingInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, apiKey.UserID, repo.gotUserID)
	require.Equal(t, groupID, repo.gotGroupID)
	var response keyBillingInfoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.UserRateMultiplier)
	require.Equal(t, userRate, *response.UserRateMultiplier)
	require.Equal(t, userRate, response.ResolvedRateMultiplier)
}

func TestGatewayHandlerKeyBillingInfoUsesOpenAIResolver(t *testing.T) {
	groupID := int64(7)
	userRate := 0.6
	repo := &keyBillingUserGroupRateRepo{rate: &userRate}
	apiKey := &service.APIKey{
		UserID:  11,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 0.9},
	}
	context, recorder := newKeyBillingContext(apiKey)
	handler := &GatewayHandler{openAIGatewayService: newKeyBillingOpenAIGatewayService(repo)}

	handler.KeyBillingInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, repo.calls)
	var response keyBillingInfoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, userRate, response.ResolvedRateMultiplier)
}

func TestGatewayHandlerKeyBillingInfoErrorsAreSafe(t *testing.T) {
	groupID := int64(7)
	tests := []struct {
		name    string
		apiKey  *service.APIKey
		handler *GatewayHandler
		cfg     *config.Config
		status  int
	}{
		{name: "missing API key", status: http.StatusUnauthorized},
		{name: "ungrouped API key", apiKey: &service.APIKey{}, handler: &GatewayHandler{}, status: http.StatusForbidden},
		{name: "missing group", apiKey: &service.APIKey{GroupID: &groupID}, handler: &GatewayHandler{}, status: http.StatusInternalServerError},
		{name: "missing service", apiKey: &service.APIKey{GroupID: &groupID, Group: &service.Group{RateMultiplier: 1}}, handler: &GatewayHandler{}, status: http.StatusInternalServerError},
		{name: "simple mode", apiKey: &service.APIKey{GroupID: &groupID, Group: &service.Group{RateMultiplier: 1}}, handler: &GatewayHandler{gatewayService: newKeyBillingGatewayService(nil)}, cfg: &config.Config{RunMode: config.RunModeSimple}, status: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context, recorder := newKeyBillingContext(test.apiKey)
			handler := test.handler
			if handler == nil {
				handler = &GatewayHandler{}
			}
			handler.cfg = test.cfg

			handler.KeyBillingInfo(context)

			require.Equal(t, test.status, recorder.Code)
		})
	}
}

func TestGatewayHandlerKeyBillingInfoResolverFallbackIsSafe(t *testing.T) {
	groupID := int64(7)
	repo := &keyBillingUserGroupRateRepo{err: errors.New("database password leaked")}
	apiKey := &service.APIKey{
		UserID:  11,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, RateMultiplier: 1},
	}
	context, recorder := newKeyBillingContext(apiKey)
	handler := &GatewayHandler{gatewayService: newKeyBillingGatewayService(repo)}

	handler.KeyBillingInfo(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response keyBillingInfoResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 1.0, response.ResolvedRateMultiplier)
	require.NotContains(t, recorder.Body.String(), "database password leaked")
}

func TestBuildKeyBillingInfoNormalizesObservedAt(t *testing.T) {
	groupID := int64(7)
	now := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	response := buildKeyBillingInfo(&service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, RateMultiplier: 1.2},
	}, 0.8, now)

	require.Equal(t, "sub2api.key_billing", response.Object)
	require.False(t, response.PeakRateEnabled)
	require.Equal(t, 0.8, response.EffectiveRateMultiplier)
	require.Equal(t, now.UTC(), response.ObservedAt)
}
