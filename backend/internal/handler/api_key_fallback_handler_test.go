//go:build unit

package handler

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

type stubKeyFallbackUserService struct {
	getUser, getKey        int64
	getModel               string
	replaceUser            int64
	replaceKey             int64
	replaceIDs             []int64
	replaceModel           string
	listUser               int64
	view                   *service.KeyFallbackUserView
	overview               *service.KeyFallbackOverview
	err                    error
	getCalls, replaceCalls int
}

func (s *stubKeyFallbackUserService) GetUserChain(_ context.Context, userID, keyID int64, model string) (*service.KeyFallbackUserView, error) {
	s.getCalls++
	s.getUser, s.getKey, s.getModel = userID, keyID, model
	return s.view, s.err
}

func (s *stubKeyFallbackUserService) ReplaceUserChain(_ context.Context, userID, keyID int64, ids []int64, model string) (*service.KeyFallbackUserView, error) {
	s.replaceCalls++
	s.replaceUser, s.replaceKey, s.replaceIDs, s.replaceModel = userID, keyID, ids, model
	return s.view, s.err
}

func (s *stubKeyFallbackUserService) ListUserChains(_ context.Context, userID int64) (*service.KeyFallbackOverview, error) {
	s.listUser = userID
	return s.overview, s.err
}

type fallbackEnvelope struct {
	Code     int               `json:"code"`
	Message  string            `json:"message"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
	Data     json.RawMessage   `json:"data"`
}

func doFallbackRequest(t *testing.T, stub *stubKeyFallbackUserService, withSubject bool, method, target, body string) (*httptest.ResponseRecorder, fallbackEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if withSubject {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		}
		c.Next()
	})
	h := &APIKeyFallbackHandler{svc: stub}
	router.GET("/api/v1/keys/fallback-chains", h.ListChains)
	router.GET("/api/v1/keys/:id", func(c *gin.Context) { c.String(http.StatusOK, "plain key route") })
	router.GET("/api/v1/keys/:id/fallback-chain", h.GetChain)
	router.PUT("/api/v1/keys/:id/fallback-chain", h.ReplaceChain)

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var env fallbackEnvelope
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	}
	return rec, env
}

func TestAPIKeyFallbackHandler_GetChain(t *testing.T) {
	stub := &stubKeyFallbackUserService{view: &service.KeyFallbackUserView{KeyID: 7, Platform: "openai", MaxFallbacks: 5, Items: []service.KeyFallbackItemView{}, Available: []service.KeyFallbackAvailableView{}}}
	rec, env := doFallbackRequest(t, stub, true, http.MethodGet, "/api/v1/keys/7/fallback-chain?model=gpt-5.4", "")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, env.Code)
	require.Equal(t, int64(42), stub.getUser, "当前用户取自认证上下文，不取自请求")
	require.Equal(t, int64(7), stub.getKey)
	require.Equal(t, "gpt-5.4", stub.getModel)
	require.Contains(t, string(env.Data), `"key_id":7`)
}

// 校验失败时 metadata.group_id 标明出错的分组（前端据此把错误标在那一项上）。
func TestAPIKeyFallbackHandler_ValidationErrorCarriesGroupIDMetadata(t *testing.T) {
	stub := &stubKeyFallbackUserService{err: service.ErrFallbackGroupUnavailable.WithMetadata(map[string]string{"group_id": "21"})}
	rec, env := doFallbackRequest(t, stub, true, http.MethodPut, "/api/v1/keys/7/fallback-chain", `{"group_ids":[2,21]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "FALLBACK_GROUP_UNAVAILABLE", env.Reason)
	require.Equal(t, "21", env.Metadata["group_id"])
}

func TestAPIKeyFallbackHandler_ErrorsUseServiceCodes(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"idor / missing key", service.ErrAPIKeyNotFound, http.StatusNotFound, "API_KEY_NOT_FOUND"},
		{"not grouped", service.ErrFallbackKeyNotGrouped, http.StatusBadRequest, "FALLBACK_KEY_NOT_GROUPED"},
		{"duplicate", service.ErrFallbackGroupDup, http.StatusBadRequest, "FALLBACK_GROUP_DUPLICATE"},
		{"is primary", service.ErrFallbackGroupIsPrimary, http.StatusBadRequest, "FALLBACK_GROUP_IS_PRIMARY"},
		{"too long", service.ErrFallbackChainTooLong, http.StatusBadRequest, "FALLBACK_CHAIN_TOO_LONG"},
		{"platform mismatch", service.ErrFallbackPlatformMismatch, http.StatusBadRequest, "FALLBACK_GROUP_PLATFORM_MISMATCH"},
		{"not allowed", service.ErrFallbackGroupNotAllowed, http.StatusForbidden, "FALLBACK_GROUP_NOT_ALLOWED"},
		{"group not found", service.ErrFallbackGroupNotFound, http.StatusNotFound, "FALLBACK_GROUP_NOT_FOUND"},
		{"group unavailable", service.ErrFallbackGroupUnavailable, http.StatusNotFound, "FALLBACK_GROUP_UNAVAILABLE"},
		{"key changed", service.ErrFallbackKeyChanged, http.StatusConflict, "FALLBACK_KEY_CHANGED"},
		{"invalid model", service.ErrKeyEditorInvalidModel, http.StatusBadRequest, "FALLBACK_INVALID_MODEL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubKeyFallbackUserService{err: tc.err}

			rec, env := doFallbackRequest(t, stub, true, http.MethodPut, "/api/v1/keys/7/fallback-chain", `{"group_ids":[2]}`)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantReason, env.Reason)
			require.Equal(t, tc.wantStatus, env.Code)

			rec, env = doFallbackRequest(t, stub, true, http.MethodGet, "/api/v1/keys/7/fallback-chain", "")
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantReason, env.Reason)
		})
	}
}

func TestAPIKeyFallbackHandler_ReplaceChainBody(t *testing.T) {
	stub := &stubKeyFallbackUserService{view: &service.KeyFallbackUserView{KeyID: 7}}

	rec, _ := doFallbackRequest(t, stub, true, http.MethodPut, "/api/v1/keys/7/fallback-chain?model=gpt-5.4", `{"group_ids":[21,30]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), stub.replaceUser)
	require.Equal(t, int64(7), stub.replaceKey)
	require.Equal(t, []int64{21, 30}, stub.replaceIDs, "顺序即位置")
	require.Equal(t, "gpt-5.4", stub.replaceModel)

	// 空数组表示清空：必须以非 nil 的空切片传给服务层。
	rec, _ = doFallbackRequest(t, stub, true, http.MethodPut, "/api/v1/keys/7/fallback-chain", `{"group_ids":[]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, stub.replaceIDs)
	require.Empty(t, stub.replaceIDs)
}

func TestAPIKeyFallbackHandler_ReplaceChainRejectsMalformedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"missing group_ids": `{}`,
		"null group_ids":    `{"group_ids":null}`,
		"wrong type":        `{"group_ids":"21"}`,
		"non-integer item":  `{"group_ids":[1.5]}`,
		"string item":       `{"group_ids":["a"]}`,
		"not json":          `group_ids=21`,
		"empty body":        ``,
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubKeyFallbackUserService{view: &service.KeyFallbackUserView{}}
			rec, env := doFallbackRequest(t, stub, true, http.MethodPut, "/api/v1/keys/7/fallback-chain", body)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "FALLBACK_INVALID_REQUEST", env.Reason)
			require.Zero(t, stub.replaceCalls, "malformed requests must never reach the service (and never clear a chain)")
		})
	}
}

func TestAPIKeyFallbackHandler_RequiresAuthAndValidKeyID(t *testing.T) {
	stub := &stubKeyFallbackUserService{view: &service.KeyFallbackUserView{}, overview: &service.KeyFallbackOverview{}}

	for _, target := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/keys/7/fallback-chain", ""},
		{http.MethodPut, "/api/v1/keys/7/fallback-chain", `{"group_ids":[]}`},
		{http.MethodGet, "/api/v1/keys/fallback-chains", ""},
	} {
		rec, _ := doFallbackRequest(t, stub, false, target.method, target.path, target.body)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target.method+" "+target.path)
	}
	require.Zero(t, stub.getCalls+stub.replaceCalls)

	// 非法或非正的 Key ID：与「不存在」同一个 404，不调用服务。
	for _, id := range []string{"abc", "0", "-3", "1.5"} {
		rec, env := doFallbackRequest(t, stub, true, http.MethodGet, "/api/v1/keys/"+id+"/fallback-chain", "")
		require.Equal(t, http.StatusNotFound, rec.Code, id)
		require.Equal(t, "API_KEY_NOT_FOUND", env.Reason, id)
	}
	require.Zero(t, stub.getCalls)
}

func TestAPIKeyFallbackHandler_ListChainsRouteIsNotShadowedByKeyID(t *testing.T) {
	stub := &stubKeyFallbackUserService{overview: &service.KeyFallbackOverview{Enabled: true, Platforms: []service.KeyFallbackPlatformSection{}}}
	rec, env := doFallbackRequest(t, stub, true, http.MethodGet, "/api/v1/keys/fallback-chains", "")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), stub.listUser)
	require.JSONEq(t, `{"enabled":true,"platforms":[]}`, string(env.Data))

	// 普通的 /keys/:id 仍然走原路由。
	rec, _ = doFallbackRequest(t, stub, true, http.MethodGet, "/api/v1/keys/7", "")
	require.Equal(t, "plain key route", rec.Body.String())
}
