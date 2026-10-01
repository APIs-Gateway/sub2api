//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAccountListIDsRouter() (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts", handler.List)
	router.GET("/api/v1/admin/accounts/ids", handler.ListIDs)
	return router, adminSvc
}

func getAccountsEndpoint(router *gin.Engine, path string, query url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path+"?"+query.Encode(), nil)
	router.ServeHTTP(rec, req)
	return rec
}

func TestAccountHandlerListIDsReturnsIDsAndSummary(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()
	adminSvc.accountIDList = &service.AccountIDList{
		IDs:       []int64{4, 8, 15},
		Total:     3,
		Platforms: []string{"anthropic", "openai"},
		Types:     []string{"apikey", "oauth"},
	}

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{})

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Code int `json:"code"`
		Data struct {
			IDs       []int64  `json:"ids"`
			Total     int64    `json:"total"`
			Platforms []string `json:"platforms"`
			Types     []string `json:"types"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 0, payload.Code)
	require.Equal(t, []int64{4, 8, 15}, payload.Data.IDs)
	require.Equal(t, int64(3), payload.Data.Total)
	require.Equal(t, []string{"anthropic", "openai"}, payload.Data.Platforms)
	require.Equal(t, []string{"apikey", "oauth"}, payload.Data.Types)
}

func TestAccountHandlerListIDsEmptyResultSerializesAsArrays(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()
	adminSvc.accountIDList = &service.AccountIDList{}

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{})

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.JSONEq(t, "[]", string(payload.Data["ids"]))
	require.JSONEq(t, "[]", string(payload.Data["platforms"]))
	require.JSONEq(t, "[]", string(payload.Data["types"]))
}

// ListIDs 必须和 List 使用完全相同的筛选条件，否则「选中全部筛选结果」会和列表对不上。
func TestAccountHandlerListIDsUsesSameFiltersAsList(t *testing.T) {
	queries := []url.Values{
		{},
		{"platform": {"openai"}, "type": {"apikey"}, "status": {"active"}, "search": {"  relay.example.com  "}},
		{"group": {"12"}, "privacy_mode": {" training_set_cf_blocked "}},
		{"group": {"ungrouped"}, "search": {"42"}},
	}
	for _, query := range queries {
		t.Run(query.Encode(), func(t *testing.T) {
			router, adminSvc := setupAccountListIDsRouter()

			require.Equal(t, http.StatusOK, getAccountsEndpoint(router, "/api/v1/admin/accounts", query).Code)
			require.Equal(t, http.StatusOK, getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", query).Code)

			listed := adminSvc.lastListAccounts
			ids := adminSvc.lastListAccountIDs
			require.Equal(t, 1, ids.calls)
			require.Equal(t, listed.platform, ids.platform)
			require.Equal(t, listed.accountType, ids.accountType)
			require.Equal(t, listed.status, ids.status)
			require.Equal(t, listed.search, ids.search)
			require.Equal(t, listed.groupID, ids.groupID)
			require.Equal(t, listed.privacyMode, ids.privacyMode)
		})
	}
}

func TestAccountHandlerListIDsParsesFilters(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{
		"platform":     {"openai"},
		"type":         {"apikey"},
		"status":       {"rate_limited"},
		"search":       {"  relay  "},
		"group":        {"12"},
		"privacy_mode": {" __unset__ "},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	got := adminSvc.lastListAccountIDs
	require.Equal(t, "openai", got.platform)
	require.Equal(t, "apikey", got.accountType)
	require.Equal(t, "rate_limited", got.status)
	require.Equal(t, "relay", got.search)
	require.Equal(t, int64(12), got.groupID)
	require.Equal(t, "__unset__", got.privacyMode)
}

func TestAccountHandlerListIDsUngroupedFilter(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{"group": {"ungrouped"}})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.AccountListGroupUngrouped, adminSvc.lastListAccountIDs.groupID)
}

func TestAccountHandlerListIDsRejectsInvalidGroupFilter(t *testing.T) {
	for _, group := range []string{"abc", "-1"} {
		t.Run(group, func(t *testing.T) {
			router, adminSvc := setupAccountListIDsRouter()

			rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{"group": {group}})

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "INVALID_GROUP_FILTER", decodeErrorReason(t, rec))
			require.Zero(t, adminSvc.lastListAccountIDs.calls)
		})
	}
}

func TestAccountHandlerListIDsReportsLimitExceeded(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()
	adminSvc.accountIDListErr = service.NewAccountIDsLimitExceededError(7321)

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{"platform": {"openai"}})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var payload struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, service.AccountIDsLimitExceededReason, payload.Reason)
	require.Equal(t, "7321", payload.Metadata["total"])
	require.Equal(t, "5000", payload.Metadata["limit"])
}

func TestAccountHandlerListIDsPropagatesServiceError(t *testing.T) {
	router, adminSvc := setupAccountListIDsRouter()
	adminSvc.accountIDListErr = infraerrors.InternalServer("LIST_FAILED", "boom")

	rec := getAccountsEndpoint(router, "/api/v1/admin/accounts/ids", url.Values{})

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
