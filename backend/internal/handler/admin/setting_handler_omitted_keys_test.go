//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestUpdateSettings_PartialUpdateDoesNotClobberOmittedFields covers the core
// scenario from upstream #4868 / local item17: PUT-ing only a subset of
// UpdateSettingsRequest's fields must not reset the fields the request body
// never mentioned back to their Go zero value in the database.
func TestUpdateSettings_PartialUpdateDoesNotClobberOmittedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			service.SettingKeyRegistrationEnabled: "true",
			service.SettingKeyPromoCodeEnabled:    "true",
			service.SettingKeySiteName:            "Old Site",
		},
	}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	// Only site_name is provided; registration_enabled/promo_code_enabled are
	// entirely absent from the body.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewBufferString(`{"site_name":"New Site"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, "New Site", repo.values[service.SettingKeySiteName])
	require.Equal(t, "true", repo.values[service.SettingKeyRegistrationEnabled], "omitted field must keep its stored value")
	require.Equal(t, "true", repo.values[service.SettingKeyPromoCodeEnabled], "omitted field must keep its stored value")

	_, wasWritten := repo.lastUpdates[service.SettingKeyRegistrationEnabled]
	require.False(t, wasWritten, "omitted key must not be part of the persisted update set")

	// A GET round trip must also observe the preserved value (exercises the
	// GetAllSettings reload path used to refresh in-process caches).
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	handler.GetSettings(c2)
	require.Equal(t, http.StatusOK, rec2.Code)

	var resp response.Response
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["registration_enabled"])
	require.Equal(t, "New Site", data["site_name"])
}

// TestUpdateSettings_ExplicitFalseOverridesStoredTrue ensures the omission
// mechanism does not over-protect: explicitly sending a zero value must still
// write it.
func TestUpdateSettings_ExplicitFalseOverridesStoredTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			service.SettingKeyRegistrationEnabled: "true",
		},
	}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewBufferString(`{"registration_enabled":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateSettings(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[service.SettingKeyRegistrationEnabled])
	_, wasWritten := repo.lastUpdates[service.SettingKeyRegistrationEnabled]
	require.True(t, wasWritten, "explicitly provided key must be part of the persisted update set")
}

func TestDetectOmittedSettingKeys(t *testing.T) {
	raw := map[string]json.RawMessage{
		"site_name": json.RawMessage(`"New Site"`),
	}
	omitted := detectOmittedSettingKeys(raw)
	require.True(t, omitted.Has(service.SettingKeyRegistrationEnabled))
	require.False(t, omitted.Has(service.SettingKeySiteName))
	require.Len(t, omitted, len(settingsOmittableKeys)-1)
}

func TestDetectOmittedSettingKeys_EmptyBodyOmitsEverything(t *testing.T) {
	omitted := detectOmittedSettingKeys(map[string]json.RawMessage{})
	require.Len(t, omitted, len(settingsOmittableKeys))
}
