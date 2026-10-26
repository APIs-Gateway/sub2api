//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-2b-2：PricingWriteHandler。路由层的 RequireAdminJWT 另有路由测试；这里验证 handler 自己：
// 操作人与鉴权方式只取自上下文、路径参数覆盖请求体、错误原样映射。

type pwStubs struct {
	err error

	cellProposal service.PriceWriteProposal
	cellCommit   service.PriceWriteCommit
	gcReq        service.GroupConfigWriteRequest
	gcCommit     service.GroupConfigCommit
	free         []service.BillingKnownFreeEntry
	freeActor    service.PriceWriteActor
	freeConfirm  bool
	catID        int64
	catTo        service.ModelCatalogStatus
	catConfirm   bool
	catInput     service.CreateModelCatalogInput
	costActor    int64
	costGroup    int64
	costBase     int64
	costRule     int64
	costSpec     service.CostRuleSpec
	publishID    int64
}

func (s *pwStubs) Propose(_ context.Context, in service.PriceWriteProposal) (*service.PriceWriteTicket, error) {
	s.cellProposal = in
	return &service.PriceWriteTicket{ApprovalID: 5}, s.err
}

func (s *pwStubs) Commit(_ context.Context, in service.PriceWriteCommit) (*service.CellWriteResult, error) {
	s.cellCommit = in
	return &service.CellWriteResult{ChangedGroupIDs: []int64{1}}, s.err
}

type pwGroupStub struct{ *pwStubs }

func (s pwGroupStub) Propose(_ context.Context, req service.GroupConfigWriteRequest) (*service.GroupConfigTicket, error) {
	s.gcReq = req
	return &service.GroupConfigTicket{ApprovalID: 6}, s.err
}

func (s pwGroupStub) Commit(_ context.Context, in service.GroupConfigCommit) (*service.GroupConfigWriteResult, error) {
	s.gcCommit = in
	return &service.GroupConfigWriteResult{Changed: true}, s.err
}

type pwFreeStub struct{ *pwStubs }

func (s pwFreeStub) Current(context.Context) ([]service.BillingKnownFreeEntry, error) {
	return []service.BillingKnownFreeEntry{{Model: "m"}}, s.err
}

func (s pwFreeStub) Preview(_ context.Context, e []service.BillingKnownFreeEntry) (*service.KnownFreeListChange, error) {
	s.free = e
	return &service.KnownFreeListChange{Changed: true}, s.err
}

func (s pwFreeStub) Update(_ context.Context, a service.PriceWriteActor, e []service.BillingKnownFreeEntry, confirm bool) (*service.KnownFreeListChange, error) {
	s.free, s.freeActor, s.freeConfirm = e, a, confirm
	return &service.KnownFreeListChange{Changed: true}, s.err
}

type pwCatStub struct{ *pwStubs }

func (s pwCatStub) Preview(_ context.Context, id int64, to service.ModelCatalogStatus) (*service.CatalogTransitionPreview, error) {
	s.catID, s.catTo = id, to
	return &service.CatalogTransitionPreview{To: to}, s.err
}

func (s pwCatStub) Transition(_ context.Context, id int64, to service.ModelCatalogStatus, confirm bool) (*service.ModelCatalogEntry, error) {
	s.catID, s.catTo, s.catConfirm = id, to, confirm
	return &service.ModelCatalogEntry{ID: id, Status: to}, s.err
}

func (s pwCatStub) CreateChecked(_ context.Context, in service.CreateModelCatalogInput, confirm bool) (*service.ModelCatalogEntry, error) {
	s.catInput, s.catConfirm = in, confirm
	return &service.ModelCatalogEntry{ID: 1}, s.err
}

type pwCostStub struct{ *pwStubs }

func (s pwCostStub) Create(_ context.Context, actor, group, base int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error) {
	s.costActor, s.costGroup, s.costBase, s.costSpec = actor, group, base, spec
	return &service.CostRuleWriteResult{RuleID: 1}, s.err
}

func (s pwCostStub) Update(_ context.Context, actor, group, base, rule int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error) {
	s.costActor, s.costGroup, s.costBase, s.costRule, s.costSpec = actor, group, base, rule, spec
	return &service.CostRuleWriteResult{RuleID: rule}, s.err
}

func (s pwCostStub) Delete(_ context.Context, actor, group, base, rule int64) (*service.CostRuleWriteResult, error) {
	s.costActor, s.costGroup, s.costBase, s.costRule = actor, group, base, rule
	return &service.CostRuleWriteResult{RuleID: rule}, s.err
}

type pwPublishStub struct{ *pwStubs }

func (s pwPublishStub) PublishCheck(_ context.Context, id int64) (*service.OpenPrecheckReport, error) {
	s.publishID = id
	return &service.OpenPrecheckReport{GroupID: id}, s.err
}

func pwWriteRouter(s *pwStubs, user bool, authMethod string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &PricingWriteHandler{
		cells: s, groupCfg: pwGroupStub{s}, knownFree: pwFreeStub{s}, catalog: pwCatStub{s}, costRules: pwCostStub{s}, publish: pwPublishStub{s},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		}
		if authMethod != "" {
			c.Set("auth_method", authMethod)
		}
		c.Next()
	})
	r.POST("/cells/preview", h.PreviewCells)
	r.POST("/cells/commit", h.CommitCells)
	r.POST("/groups/:id/config/preview", h.PreviewGroupConfig)
	r.PUT("/groups/:id/config", h.CommitGroupConfig)
	r.GET("/groups/:id/publish-check", h.PublishCheck)
	r.GET("/model-catalog/:id/transition-preview", h.PreviewCatalogTransition)
	r.PUT("/model-catalog/:id/status", h.TransitionCatalog)
	r.POST("/model-catalog", h.CreateCatalogEntry)
	r.POST("/groups/:id/cost-rules", h.CreateCostRule)
	r.PUT("/groups/:id/cost-rules/:rule_id", h.UpdateCostRule)
	r.DELETE("/groups/:id/cost-rules/:rule_id", h.DeleteCostRule)
	r.GET("/known-free-list", h.GetKnownFreeList)
	r.POST("/known-free-list/preview", h.PreviewKnownFreeList)
	r.PUT("/known-free-list", h.UpdateKnownFreeList)
	return r
}

func pwDo(r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, pricingMatrixEnvelope) {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	var env pricingMatrixEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

func TestPricingWriteHandler_CellsAndGroupConfig(t *testing.T) {
	s := &pwStubs{}
	r := pwWriteRouter(s, true, service.AuditAuthMethodJWT)

	rec, _ := pwDo(r, http.MethodPost, "/cells/preview", `{"ops":[{"group_id":1,"model_key":"m","kind":"upsert"}],"group_revisions":{"1":3}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), s.cellProposal.Request.OperatorID, "操作人取自上下文")
	require.Len(t, s.cellProposal.Request.Ops, 1)

	// 请求体里伪造的操作人字段不起作用（json:"-"）。
	pwDo(r, http.MethodPost, "/cells/preview", `{"ops":[],"OperatorID":1,"operator_id":1}`)
	require.Equal(t, int64(42), s.cellProposal.Request.OperatorID)

	rec, _ = pwDo(r, http.MethodPost, "/cells/commit", `{"approval_id":5,"confirm":true,"request":{"ops":[],"group_revisions":{"1":3}}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(5), s.cellCommit.ApprovalID)
	require.True(t, s.cellCommit.Confirm)
	require.True(t, s.cellCommit.Actor.Interactive)
	require.Equal(t, int64(42), s.cellCommit.Actor.ID)
	require.Equal(t, int64(42), s.cellCommit.Request.OperatorID)

	// 机器令牌不是交互式会话；请求体里的 interactive 字段没有对应入口。
	r = pwWriteRouter(s, true, service.AuditAuthMethodAdminToken)
	pwDo(r, http.MethodPost, "/cells/commit", `{"approval_id":5,"confirm":true,"interactive":true,"request":{"ops":[]}}`)
	require.False(t, s.cellCommit.Actor.Interactive)
	r = pwWriteRouter(s, true, "")
	pwDo(r, http.MethodPost, "/cells/commit", `{"request":{"ops":[]}}`)
	require.False(t, s.cellCommit.Actor.Interactive)

	// 分组 id 取自路径，覆盖请求体。
	r = pwWriteRouter(s, true, service.AuditAuthMethodJWT)
	rec, _ = pwDo(r, http.MethodPost, "/groups/9/config/preview", `{"group_id":1,"baseline_revision":3,"access_mode":"allowlist"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(9), s.gcReq.GroupID)
	require.Equal(t, int64(42), s.gcReq.OperatorID)
	rec, _ = pwDo(r, http.MethodPut, "/groups/9/config", `{"approval_id":6,"confirm":true,"request":{"group_id":1,"baseline_revision":3}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(9), s.gcCommit.Request.GroupID)
	require.Equal(t, int64(6), s.gcCommit.ApprovalID)
	require.True(t, s.gcCommit.Actor.Interactive)
}

func TestPricingWriteHandler_RejectsBadInput(t *testing.T) {
	s := &pwStubs{}
	r := pwWriteRouter(s, true, service.AuditAuthMethodJWT)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/cells/preview", `not json`},
		{http.MethodPost, "/cells/commit", `not json`},
		{http.MethodPost, "/groups/x/config/preview", `{}`},
		{http.MethodPost, "/groups/1/config/preview", `not json`},
		{http.MethodPut, "/groups/0/config", `{}`},
		{http.MethodPut, "/groups/1/config", `not json`},
		{http.MethodGet, "/groups/x/publish-check", ``},
		{http.MethodGet, "/model-catalog/x/transition-preview", ``},
		{http.MethodPut, "/model-catalog/x/status", `{}`},
		{http.MethodPut, "/model-catalog/1/status", `not json`},
		{http.MethodPost, "/model-catalog", `not json`},
		{http.MethodPost, "/groups/x/cost-rules", `{}`},
		{http.MethodPost, "/groups/1/cost-rules", `not json`},
		{http.MethodPut, "/groups/x/cost-rules/1", `{}`},
		{http.MethodPut, "/groups/1/cost-rules/x", `{}`},
		{http.MethodPut, "/groups/1/cost-rules/1", `not json`},
		{http.MethodDelete, "/groups/x/cost-rules/1?baseline_revision=1", ``},
		{http.MethodDelete, "/groups/1/cost-rules/x?baseline_revision=1", ``},
		{http.MethodDelete, "/groups/1/cost-rules/1", ``},
		{http.MethodDelete, "/groups/1/cost-rules/1?baseline_revision=-2", ``},
		{http.MethodPost, "/known-free-list/preview", `not json`},
		{http.MethodPut, "/known-free-list", `not json`},
	} {
		rec, env := pwDo(r, c.method, c.path, c.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, c.method+" "+c.path)
		require.NotEmpty(t, env.Reason, c.method+" "+c.path)
	}

	// 没有登录主体：401，不进入服务层。
	r = pwWriteRouter(s, false, "")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/cells/preview", `{}`},
		{http.MethodPost, "/cells/commit", `{}`},
		{http.MethodPost, "/groups/1/config/preview", `{}`},
		{http.MethodPut, "/groups/1/config", `{}`},
		{http.MethodPost, "/model-catalog", `{}`},
		{http.MethodPost, "/groups/1/cost-rules", `{}`},
		{http.MethodPut, "/groups/1/cost-rules/1", `{}`},
		{http.MethodDelete, "/groups/1/cost-rules/1?baseline_revision=1", ``},
		{http.MethodPut, "/known-free-list", `{}`},
	} {
		rec, _ := pwDo(r, c.method, c.path, c.body)
		require.Equal(t, http.StatusUnauthorized, rec.Code, c.method+" "+c.path)
	}
}

func TestPricingWriteHandler_ServiceErrorsAreMapped(t *testing.T) {
	s := &pwStubs{err: infraerrors.Conflict("PRICE_BASELINE_CHANGED", "changed")}
	r := pwWriteRouter(s, true, service.AuditAuthMethodJWT)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/cells/preview", `{}`},
		{http.MethodPost, "/cells/commit", `{}`},
		{http.MethodPost, "/groups/1/config/preview", `{}`},
		{http.MethodPut, "/groups/1/config", `{}`},
		{http.MethodGet, "/groups/1/publish-check", ``},
		{http.MethodGet, "/model-catalog/1/transition-preview?to=retired", ``},
		{http.MethodPut, "/model-catalog/1/status", `{"to":"retired"}`},
		{http.MethodPost, "/model-catalog", `{}`},
		{http.MethodPost, "/groups/1/cost-rules", `{}`},
		{http.MethodPut, "/groups/1/cost-rules/1", `{}`},
		{http.MethodDelete, "/groups/1/cost-rules/1?baseline_revision=1", ``},
		{http.MethodGet, "/known-free-list", ``},
		{http.MethodPost, "/known-free-list/preview", `{}`},
		{http.MethodPut, "/known-free-list", `{}`},
	} {
		rec, env := pwDo(r, c.method, c.path, c.body)
		require.Equal(t, http.StatusConflict, rec.Code, c.method+" "+c.path)
		require.Equal(t, "PRICE_BASELINE_CHANGED", env.Reason)
	}
}

func TestPricingWriteHandler_CatalogCostRulesAndKnownFree(t *testing.T) {
	s := &pwStubs{}
	r := pwWriteRouter(s, true, service.AuditAuthMethodJWT)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/groups/3/publish-check", ``},
		{http.MethodGet, "/model-catalog/4/transition-preview?to=%20retired%20", ``},
		{http.MethodPut, "/model-catalog/4/status", `{"to":" retired ","confirm_usage":true}`},
		{http.MethodPost, "/model-catalog", `{"model_key":"m","platform":"openai","status":" active ","confirm_usage":true,"aliases":["a"]}`},
		{http.MethodPost, "/groups/5/cost-rules", `{"baseline_revision":3,"rule":{"name":"r","prices":[]}}`},
		{http.MethodPut, "/groups/5/cost-rules/6", `{"baseline_revision":3,"rule":{"name":"r"}}`},
		{http.MethodDelete, "/groups/5/cost-rules/6?baseline_revision=3", ``},
		{http.MethodGet, "/known-free-list", ``},
		{http.MethodPost, "/known-free-list/preview", `{"entries":[{"group_id":1,"model":"m"}]}`},
		{http.MethodPut, "/known-free-list", `{"entries":[{"group_id":1,"model":"m"}],"confirm":true}`},
	} {
		rec, env := pwDo(r, c.method, c.path, c.body)
		require.Equal(t, http.StatusOK, rec.Code, c.method+" "+c.path+" "+rec.Body.String())
		require.Equal(t, 0, env.Code)
	}
	require.Equal(t, int64(3), s.publishID)
	require.Equal(t, int64(4), s.catID)
	require.Equal(t, service.ModelCatalogRetired, s.catTo)
	require.True(t, s.catConfirm)
	require.Equal(t, service.ModelCatalogActive, s.catInput.Status)
	require.Equal(t, []string{"a"}, s.catInput.Aliases)
	require.NotNil(t, s.catInput.CreatedBy)
	require.Equal(t, int64(42), *s.catInput.CreatedBy)
	require.Equal(t, int64(42), s.costActor)
	require.Equal(t, int64(5), s.costGroup)
	require.Equal(t, int64(3), s.costBase)
	require.Equal(t, int64(6), s.costRule)
	require.Len(t, s.free, 1)
	require.True(t, s.freeActor.Interactive)
	require.True(t, s.freeConfirm)
}
