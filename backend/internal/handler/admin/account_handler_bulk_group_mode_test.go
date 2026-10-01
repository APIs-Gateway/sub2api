//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func postBulkUpdate(t *testing.T, adminSvc *stubAdminService, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	router := setupAccountMixedChannelRouter(adminSvc)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/bulk-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func decodeErrorReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Reason
}

func TestBulkUpdateGroupModeForwarded(t *testing.T) {
	for _, mode := range []service.AccountGroupBindMode{
		service.AccountGroupBindModeAppend,
		service.AccountGroupBindModeRemove,
		service.AccountGroupBindModeReplace,
	} {
		t.Run(string(mode), func(t *testing.T) {
			adminSvc := newStubAdminService()
			rec := postBulkUpdate(t, adminSvc, map[string]any{
				"account_ids": []int64{1, 2},
				"group_ids":   []int64{27},
				"group_mode":  string(mode),
			})

			require.Equal(t, http.StatusOK, rec.Code)
			require.NotNil(t, adminSvc.lastBulkUpdateInput)
			require.Equal(t, mode, adminSvc.lastBulkUpdateInput.GroupMode)
			require.NotNil(t, adminSvc.lastBulkUpdateInput.GroupIDs)
			require.Equal(t, []int64{27}, *adminSvc.lastBulkUpdateInput.GroupIDs)
		})
	}
}

func TestBulkUpdateMissingGroupModeMeansReplace(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids": []int64{1, 2},
		"group_ids":   []int64{27},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.lastBulkUpdateInput)
	require.Equal(t, service.AccountGroupBindModeReplace, adminSvc.lastBulkUpdateInput.GroupMode)
}

func TestBulkUpdateInvalidGroupModeReturns400(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids": []int64{1, 2},
		"group_ids":   []int64{27},
		"group_mode":  "merge",
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "INVALID_GROUP_MODE", decodeErrorReason(t, rec))
	require.Nil(t, adminSvc.lastBulkUpdateInput, "非法 group_mode 不应进入 service")
}

func TestBulkUpdateAppendOrRemoveWithoutGroupsReturns400(t *testing.T) {
	for _, mode := range []string{"append", "remove"} {
		t.Run(mode, func(t *testing.T) {
			adminSvc := newStubAdminService()
			rec := postBulkUpdate(t, adminSvc, map[string]any{
				"account_ids": []int64{1},
				"group_ids":   []int64{},
				"group_mode":  mode,
			})

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "GROUP_IDS_REQUIRED", decodeErrorReason(t, rec))
			require.Nil(t, adminSvc.lastBulkUpdateInput)
		})
	}
}

func TestBulkUpdateGroupModeWithoutGroupIDsReturns400(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids": []int64{1},
		"schedulable": true,
		"group_mode":  "append",
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "GROUP_IDS_REQUIRED", decodeErrorReason(t, rec))
	require.Nil(t, adminSvc.lastBulkUpdateInput)
}

func TestBulkUpdateReplaceWithEmptyGroupsStillAllowed(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids": []int64{1},
		"group_ids":   []int64{},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.lastBulkUpdateInput)
	require.NotNil(t, adminSvc.lastBulkUpdateInput.GroupIDs)
	require.Empty(t, *adminSvc.lastBulkUpdateInput.GroupIDs)
}

func TestBulkUpdateExpiryFieldsAreUpdatesAndForwarded(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids":           []int64{1, 2},
		"expires_at":            1893456000,
		"auto_pause_on_expired": true,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.lastBulkUpdateInput)
	require.NotNil(t, adminSvc.lastBulkUpdateInput.ExpiresAt)
	require.Equal(t, int64(1893456000), *adminSvc.lastBulkUpdateInput.ExpiresAt)
	require.NotNil(t, adminSvc.lastBulkUpdateInput.AutoPauseOnExpired)
	require.True(t, *adminSvc.lastBulkUpdateInput.AutoPauseOnExpired)
}

func TestBulkUpdateExpiresAtZeroMeansClear(t *testing.T) {
	adminSvc := newStubAdminService()
	rec := postBulkUpdate(t, adminSvc, map[string]any{
		"account_ids": []int64{1},
		"expires_at":  0,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.lastBulkUpdateInput)
	require.NotNil(t, adminSvc.lastBulkUpdateInput.ExpiresAt)
	require.Equal(t, int64(0), *adminSvc.lastBulkUpdateInput.ExpiresAt)
}
