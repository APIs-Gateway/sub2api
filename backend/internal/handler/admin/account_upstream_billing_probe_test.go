package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type billingProbeAdminSettingRepo struct {
	service.SettingRepository
	values map[string]string
	getErr error
	setErr error
}

func (r *billingProbeAdminSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *billingProbeAdminSettingRepo) Set(_ context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

func setupUpstreamBillingProbeRouter(probe *service.UpstreamBillingProbeService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamBillingProbeService(probe)

	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-probe/settings", handler.GetUpstreamBillingProbeSettings)
	router.PUT("/admin/accounts/upstream-billing-probe/settings", handler.UpdateUpstreamBillingProbeSettings)
	router.POST("/admin/accounts/upstream-billing-probe/batch", handler.ProbeUpstreamBillingBatch)
	router.PUT("/admin/accounts/:id/upstream-billing-probe", handler.SetUpstreamBillingProbeEnabled)
	router.POST("/admin/accounts/:id/upstream-billing-probe", handler.ProbeUpstreamBilling)
	return router
}

func TestAccountHandlerGetUpstreamBillingProbeSettingsReturnsDefaults(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, nil))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data service.UpstreamBillingProbeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Data.Enabled)
	require.Equal(t, 30, payload.Data.IntervalMinutes)
}

func TestAccountHandlerUpstreamBillingProbeRequiresService(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestAccountHandlerUpstreamBillingProbeRequiresServiceForAllRoutes(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(nil)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "update settings", method: http.MethodPut, path: "/admin/accounts/upstream-billing-probe/settings", body: `{"enabled":true,"interval_minutes":30}`},
		{name: "batch probe", method: http.MethodPost, path: "/admin/accounts/upstream-billing-probe/batch", body: `{"account_ids":[1]}`},
		{name: "set account", method: http.MethodPut, path: "/admin/accounts/1/upstream-billing-probe", body: `{"enabled":true}`},
		{name: "probe account", method: http.MethodPost, path: "/admin/accounts/1/upstream-billing-probe"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		})
	}
}

func TestAccountHandlerUpstreamBillingProbeValidatesRequests(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, nil))

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "empty batch", method: http.MethodPost, path: "/admin/accounts/upstream-billing-probe/batch", body: `{"account_ids":[]}`},
		{name: "negative batch id", method: http.MethodPost, path: "/admin/accounts/upstream-billing-probe/batch", body: `{"account_ids":[0]}`},
		{name: "duplicate batch ids are accepted", method: http.MethodPost, path: "/admin/accounts/upstream-billing-probe/batch", body: `{"account_ids":[1,1]}`},
		{name: "malformed batch", method: http.MethodPost, path: "/admin/accounts/upstream-billing-probe/batch", body: `{`},
		{name: "malformed settings", method: http.MethodPut, path: "/admin/accounts/upstream-billing-probe/settings", body: `{`},
		{name: "invalid enabled payload", method: http.MethodPut, path: "/admin/accounts/1/upstream-billing-probe", body: `{}`},
		{name: "invalid enabled id", method: http.MethodPut, path: "/admin/accounts/not-an-id/upstream-billing-probe", body: `{"enabled":true}`},
		{name: "invalid probe id", method: http.MethodPost, path: "/admin/accounts/not-an-id/upstream-billing-probe", body: ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if tc.name == "duplicate batch ids are accepted" {
				require.Equal(t, http.StatusOK, recorder.Code)
				return
			}
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}

	tooMany := make([]int, service.UpstreamBillingProbeMaxBatchSize+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	body, err := json.Marshal(map[string]any{"account_ids": tooMany})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/batch", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAccountHandlerUpdateUpstreamBillingProbeSettingsReportsUnavailable(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/upstream-billing-probe/settings", bytes.NewBufferString(`{"enabled":true,"interval_minutes":30}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestAccountHandlerUpdateUpstreamBillingProbeSettingsRoundTrip(t *testing.T) {
	settingRepo := &billingProbeAdminSettingRepo{values: map[string]string{}}
	settingService := service.NewSettingService(settingRepo, nil)
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, settingService))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/upstream-billing-probe/settings", bytes.NewBufferString(`{"enabled":false,"interval_minutes":15}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data service.UpstreamBillingProbeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.False(t, payload.Data.Enabled)
	require.Equal(t, 15, payload.Data.IntervalMinutes)
}

func TestAccountHandlerUpstreamBillingProbeSettingsReportsReadError(t *testing.T) {
	settingRepo := &billingProbeAdminSettingRepo{
		values: map[string]string{},
		getErr: errors.New("settings unavailable"),
	}
	settingService := service.NewSettingService(settingRepo, nil)
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, settingService))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestAccountHandlerUpstreamBillingProbeOperationErrors(t *testing.T) {
	router := setupUpstreamBillingProbeRouter(service.NewUpstreamBillingProbeService(nil, nil, nil))
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPut, path: "/admin/accounts/1/upstream-billing-probe", body: `{"enabled":true}`},
		{method: http.MethodPost, path: "/admin/accounts/1/upstream-billing-probe"},
	}

	for _, tc := range requests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	}
}
