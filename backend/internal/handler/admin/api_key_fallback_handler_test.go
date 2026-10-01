//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubKeyFallbackAdminService struct {
	view     *service.KeyFallbackAdminView
	routes   *service.KeyFallbackRoutesByGroupView
	models   map[string]string
	err      error
	lastCall string

	keyID, adminID int64
	head, tail     []int64
	note           string
	groupID        int64
	setModels      map[string]string
}

func (s *stubKeyFallbackAdminService) AdminGetChain(_ context.Context, keyID int64) (*service.KeyFallbackAdminView, error) {
	s.lastCall, s.keyID = "get", keyID
	return s.view, s.err
}

func (s *stubKeyFallbackAdminService) AdminReplaceHiddenChain(_ context.Context, adminID, keyID int64, head, tail []int64, note string) (*service.KeyFallbackAdminView, error) {
	s.lastCall, s.adminID, s.keyID, s.head, s.tail, s.note = "replace", adminID, keyID, head, tail, note
	return s.view, s.err
}

func (s *stubKeyFallbackAdminService) AdminClearHiddenChain(_ context.Context, adminID, keyID int64) (*service.KeyFallbackAdminView, error) {
	s.lastCall, s.adminID, s.keyID = "clear", adminID, keyID
	return s.view, s.err
}

func (s *stubKeyFallbackAdminService) AdminRoutesByGroup(_ context.Context, groupID int64) (*service.KeyFallbackRoutesByGroupView, error) {
	s.lastCall, s.groupID = "routes", groupID
	return s.routes, s.err
}

func (s *stubKeyFallbackAdminService) AdminReferenceModels(context.Context) map[string]string {
	return s.models
}

func (s *stubKeyFallbackAdminService) AdminSetReferenceModels(_ context.Context, m map[string]string) (map[string]string, error) {
	s.lastCall, s.setModels = "set-models", m
	return m, s.err
}

type adminFallbackEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Reason  string          `json:"reason"`
	Data    json.RawMessage `json:"data"`
}

func doAdminFallbackRequest(t *testing.T, stub *stubKeyFallbackAdminService, method, target, body string) (*httptest.ResponseRecorder, adminFallbackEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9})
		c.Next()
	})
	h := &APIKeyFallbackHandler{svc: stub}
	router.GET("/api/v1/admin/api-keys/:id/fallback-chain", h.GetChain)
	router.PUT("/api/v1/admin/api-keys/:id/hidden-fallback-chain", h.ReplaceHiddenChain)
	router.DELETE("/api/v1/admin/api-keys/:id/hidden-fallback-chain", h.ClearHiddenChain)
	router.GET("/api/v1/admin/groups/:id/fallback-routes", h.ListRoutesByGroup)
	router.GET("/api/v1/admin/key-editor/reference-models", h.GetReferenceModels)
	router.PUT("/api/v1/admin/key-editor/reference-models", h.SetReferenceModels)

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var env adminFallbackEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec, env
}

func TestAdminAPIKeyFallbackHandler_GetAndClear(t *testing.T) {
	stub := &stubKeyFallbackAdminService{view: &service.KeyFallbackAdminView{KeyID: 1908, UserID: 513, Platform: "openai"}}

	rec, env := doAdminFallbackRequest(t, stub, http.MethodGet, "/api/v1/admin/api-keys/1908/fallback-chain", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "get", stub.lastCall)
	require.Equal(t, int64(1908), stub.keyID)
	require.Contains(t, string(env.Data), `"user_id":513`)

	rec, _ = doAdminFallbackRequest(t, stub, http.MethodDelete, "/api/v1/admin/api-keys/1908/hidden-fallback-chain", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "clear", stub.lastCall)
	require.Equal(t, int64(9), stub.adminID, "操作人取自认证上下文")
}

func TestAdminAPIKeyFallbackHandler_ReplaceHiddenChain(t *testing.T) {
	stub := &stubKeyFallbackAdminService{view: &service.KeyFallbackAdminView{KeyID: 1908}}

	rec, _ := doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/api-keys/1908/hidden-fallback-chain",
		`{"head":[88],"tail":[],"note":"513 专属号替代 openai_forced_account_routes"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "replace", stub.lastCall)
	require.Equal(t, int64(9), stub.adminID)
	require.Equal(t, int64(1908), stub.keyID)
	require.Equal(t, []int64{88}, stub.head)
	require.Empty(t, stub.tail)
	require.Equal(t, "513 专属号替代 openai_forced_account_routes", stub.note)

	// 只给 tail 也可以；缺省的 head 视为清空 head。
	rec, _ = doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/api-keys/1908/hidden-fallback-chain", `{"tail":[5,6]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, stub.head)
	require.Equal(t, []int64{5, 6}, stub.tail)
}

func TestAdminAPIKeyFallbackHandler_ReplaceHiddenChainRejectsBodiesThatWouldSilentlyClear(t *testing.T) {
	for name, body := range map[string]string{
		"empty object":  `{}`,
		"only note":     `{"note":"x"}`,
		"null both":     `{"head":null,"tail":null}`,
		"wrong type":    `{"head":"88"}`,
		"not json":      `head=88`,
		"string member": `{"tail":["a"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubKeyFallbackAdminService{view: &service.KeyFallbackAdminView{}}
			rec, env := doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/api-keys/1/hidden-fallback-chain", body)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "FALLBACK_INVALID_REQUEST", env.Reason)
			require.Empty(t, stub.lastCall)
		})
	}
}

func TestAdminAPIKeyFallbackHandler_ErrorsUseServiceCodes(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantReason string
	}{
		{service.ErrAPIKeyNotFound, http.StatusNotFound, "API_KEY_NOT_FOUND"},
		{service.ErrFallbackNoteRequired, http.StatusBadRequest, "FALLBACK_NOTE_REQUIRED"},
		{service.ErrFallbackNoteTooLong, http.StatusBadRequest, "FALLBACK_NOTE_TOO_LONG"},
		{service.ErrFallbackKeyNotGrouped, http.StatusBadRequest, "FALLBACK_KEY_NOT_GROUPED"},
		{service.ErrFallbackGroupDup, http.StatusBadRequest, "FALLBACK_GROUP_DUPLICATE"},
		{service.ErrFallbackGroupIsPrimary, http.StatusBadRequest, "FALLBACK_GROUP_IS_PRIMARY"},
		{service.ErrFallbackChainTooLong, http.StatusBadRequest, "FALLBACK_CHAIN_TOO_LONG"},
		{service.ErrFallbackPlatformMismatch, http.StatusBadRequest, "FALLBACK_GROUP_PLATFORM_MISMATCH"},
		{service.ErrFallbackGroupNotFound, http.StatusNotFound, "FALLBACK_GROUP_NOT_FOUND"},
		{service.ErrFallbackGroupUnavailable, http.StatusNotFound, "FALLBACK_GROUP_UNAVAILABLE"},
		{service.ErrFallbackKeyChanged, http.StatusConflict, "FALLBACK_KEY_CHANGED"},
	}
	for _, tc := range cases {
		t.Run(tc.wantReason, func(t *testing.T) {
			stub := &stubKeyFallbackAdminService{err: tc.err}
			rec, env := doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/api-keys/1/hidden-fallback-chain", `{"head":[1],"note":"abcd"}`)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantReason, env.Reason)
		})
	}
}

func TestAdminAPIKeyFallbackHandler_InvalidIDs(t *testing.T) {
	stub := &stubKeyFallbackAdminService{view: &service.KeyFallbackAdminView{}}
	for _, target := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/api-keys/abc/fallback-chain", ""},
		{http.MethodGet, "/api/v1/admin/api-keys/0/fallback-chain", ""},
		{http.MethodPut, "/api/v1/admin/api-keys/-1/hidden-fallback-chain", `{"tail":[1]}`},
		{http.MethodDelete, "/api/v1/admin/api-keys/x/hidden-fallback-chain", ""},
		{http.MethodGet, "/api/v1/admin/groups/0/fallback-routes", ""},
	} {
		rec, _ := doAdminFallbackRequest(t, stub, target.method, target.path, target.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, target.path)
	}
	require.Empty(t, stub.lastCall)
}

func TestAdminAPIKeyFallbackHandler_RoutesByGroupAndReferenceModels(t *testing.T) {
	stub := &stubKeyFallbackAdminService{
		routes: &service.KeyFallbackRoutesByGroupView{GroupID: 21, Items: []service.KeyFallbackRouteRefView{{KeyID: 1, UserID: 2, Source: "admin", Placement: "head"}}},
		models: map[string]string{"openai": "gpt-5.5"},
	}

	rec, env := doAdminFallbackRequest(t, stub, http.MethodGet, "/api/v1/admin/groups/21/fallback-routes", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(21), stub.groupID)
	require.JSONEq(t, `{"group_id":21,"items":[{"key_id":1,"user_id":2,"source":"admin","placement":"head"}],"truncated":false}`, string(env.Data))

	rec, env = doAdminFallbackRequest(t, stub, http.MethodGet, "/api/v1/admin/key-editor/reference-models", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"models":{"openai":"gpt-5.5"}}`, string(env.Data))

	rec, env = doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/key-editor/reference-models", `{"models":{"openai":"gpt-5.6-sol"}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, map[string]string{"openai": "gpt-5.6-sol"}, stub.setModels)
	require.JSONEq(t, `{"models":{"openai":"gpt-5.6-sol"}}`, string(env.Data))

	for _, body := range []string{`{}`, `{"models":null}`, `x`} {
		rec, env = doAdminFallbackRequest(t, stub, http.MethodPut, "/api/v1/admin/key-editor/reference-models", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, "FALLBACK_INVALID_REQUEST", env.Reason)
	}
}
