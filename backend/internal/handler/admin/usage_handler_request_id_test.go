package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdminUsageListFiltersByRequestID covers upstream #4873 / local item16:
// GET /api/v1/admin/usage accepts a request_id query param and forwards it to
// the repository filter.
func TestAdminUsageListFiltersByRequestID(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_id=req-abc-123", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "req-abc-123", repo.listFilters.RequestID)
}

// TestAdminUsageListRequestIDTrimsWhitespace ensures stray whitespace from
// copy/pasted request ids doesn't silently produce a never-matching filter.
func TestAdminUsageListRequestIDTrimsWhitespace(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_id=%20req-abc-123%20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "req-abc-123", repo.listFilters.RequestID)
}

// TestAdminUsageListWithoutRequestIDLeavesFilterEmpty confirms omitting the
// query param does not accidentally scope results to an empty request_id
// match (repository treats "" as "no filter").
func TestAdminUsageListWithoutRequestIDLeavesFilterEmpty(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, repo.listFilters.RequestID)
}
