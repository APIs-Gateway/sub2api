package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type batchLimitsAdminServiceStub struct {
	*stubAdminService
	userIDs     []int64
	concurrency *int
	rpmLimit    *int
}

func (s *batchLimitsAdminServiceStub) BatchUpdateLimits(_ context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	s.userIDs = append([]int64(nil), userIDs...)
	s.concurrency = cloneIntPointer(concurrency)
	s.rpmLimit = cloneIntPointer(rpmLimit)
	return len(userIDs), nil
}

func setupBatchLimitsRouter(adminService service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewUserHandler(adminService, nil, nil, nil)
	router.POST("/api/v1/admin/users/batch-limits", handler.BatchUpdateLimits)
	return router
}

func postBatchLimits(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/batch-limits", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func TestUserHandlerBatchUpdateLimitsAcceptsPartialAndZeroValues(t *testing.T) {
	tests := []struct {
		name                string
		body                string
		expectedConcurrency *int
		expectedRPMLimit    *int
	}{
		{name: "concurrency only", body: `{"user_ids":[1,2],"concurrency":10}`, expectedConcurrency: batchIntPtr(10)},
		{name: "both limits", body: `{"user_ids":[1,2],"concurrency":8,"rpm_limit":60}`, expectedConcurrency: batchIntPtr(8), expectedRPMLimit: batchIntPtr(60)},
		{name: "explicit zero", body: `{"user_ids":[1,2],"concurrency":0,"rpm_limit":0}`, expectedConcurrency: batchIntPtr(0), expectedRPMLimit: batchIntPtr(0)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adminService := &batchLimitsAdminServiceStub{stubAdminService: newStubAdminService()}
			recorder := postBatchLimits(t, setupBatchLimitsRouter(adminService), test.body)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, []int64{1, 2}, adminService.userIDs)
			require.Equal(t, test.expectedConcurrency, adminService.concurrency)
			require.Equal(t, test.expectedRPMLimit, adminService.rpmLimit)

			var response struct {
				Data struct {
					Affected int `json:"affected"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, 2, response.Data.Affected)
		})
	}
}

func TestUserHandlerBatchUpdateLimitsAllIgnoresProvidedIDs(t *testing.T) {
	adminService := &batchLimitsAdminServiceStub{stubAdminService: newStubAdminService()}
	recorder := postBatchLimits(t, setupBatchLimitsRouter(adminService), `{"all":true,"user_ids":[999],"rpm_limit":0}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{1}, adminService.userIDs)
	require.Equal(t, batchIntPtr(0), adminService.rpmLimit)
}

func TestUserHandlerBatchUpdateLimitsRejectsMoreThan500IDs(t *testing.T) {
	userIDs := make([]int64, 501)
	for index := range userIDs {
		userIDs[index] = int64(index + 1)
	}
	body, err := json.Marshal(map[string]any{"user_ids": userIDs, "rpm_limit": 10})
	require.NoError(t, err)

	adminService := &batchLimitsAdminServiceStub{stubAdminService: newStubAdminService()}
	recorder := postBatchLimits(t, setupBatchLimitsRouter(adminService), string(body))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, adminService.userIDs)
}

func TestUserHandlerBatchUpdateLimitsRejectsInvalidRequests(t *testing.T) {
	tests := []string{
		`{"user_ids":[1]}`,
		`{"rpm_limit":10}`,
		`{"user_ids":[1],"concurrency":-1}`,
		`{"user_ids":`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			adminService := &batchLimitsAdminServiceStub{stubAdminService: newStubAdminService()}
			recorder := postBatchLimits(t, setupBatchLimitsRouter(adminService), body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, adminService.userIDs)
		})
	}
}

func batchIntPtr(value int) *int {
	return &value
}
