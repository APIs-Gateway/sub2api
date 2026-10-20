package admin

import (
	"context"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 价格写入路径的最小依赖，便于测试时替换。
type (
	pricingCellGate interface {
		Propose(ctx context.Context, in service.PriceWriteProposal) (*service.PriceWriteTicket, error)
		Commit(ctx context.Context, in service.PriceWriteCommit) (*service.CellWriteResult, error)
	}
	pricingGroupConfigWriter interface {
		Propose(ctx context.Context, req service.GroupConfigWriteRequest) (*service.GroupConfigTicket, error)
		Commit(ctx context.Context, in service.GroupConfigCommit) (*service.GroupConfigWriteResult, error)
	}
	pricingKnownFreeWriter interface {
		Current(ctx context.Context) ([]service.BillingKnownFreeEntry, error)
		Preview(ctx context.Context, entries []service.BillingKnownFreeEntry) (*service.KnownFreeListChange, error)
		Update(ctx context.Context, actor service.PriceWriteActor, entries []service.BillingKnownFreeEntry, confirm bool) (*service.KnownFreeListChange, error)
	}
	pricingCatalogTransition interface {
		Preview(ctx context.Context, id int64, to service.ModelCatalogStatus) (*service.CatalogTransitionPreview, error)
		Transition(ctx context.Context, id int64, to service.ModelCatalogStatus, confirmUsage bool) (*service.ModelCatalogEntry, error)
		CreateChecked(ctx context.Context, in service.CreateModelCatalogInput, confirmUsage bool) (*service.ModelCatalogEntry, error)
	}
	pricingCostRuleWriter interface {
		Create(ctx context.Context, actor, groupID, baseline int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error)
		Update(ctx context.Context, actor, groupID, baseline, ruleID int64, spec service.CostRuleSpec) (*service.CostRuleWriteResult, error)
		Delete(ctx context.Context, actor, groupID, baseline, ruleID int64) (*service.CostRuleWriteResult, error)
	}
	pricingPublishChecker interface {
		PublishCheck(ctx context.Context, groupID int64) (*service.OpenPrecheckReport, error)
	}
)

// PricingWriteHandler 价格写入的管理接口（W6 PR4b-2b-2）：单元格与分组配置的预览和提交、模型目录状态转换、
// 成本核算规则、已知免费名单、发布预检。
//
// 提交类接口在路由上挂 middleware.RequireAdminJWT()：机器令牌与全局管理员密钥一律 403，涉价的写入只能由登录的
// 管理员会话完成。预览类接口只登记一条预览记录，不改价格，机器令牌可以调用。
// 操作人 id 与鉴权方式只取自鉴权中间件写进上下文的值，不取自请求体。
type PricingWriteHandler struct {
	cells     pricingCellGate
	groupCfg  pricingGroupConfigWriter
	knownFree pricingKnownFreeWriter
	catalog   pricingCatalogTransition
	costRules pricingCostRuleWriter
	publish   pricingPublishChecker
}

// NewPricingWriteHandler 创建价格写入 handler。
func NewPricingWriteHandler(s *service.PricingWriteServices) *PricingWriteHandler {
	return &PricingWriteHandler{
		cells: s.Cells, groupCfg: s.GroupCfg, knownFree: s.KnownFree,
		catalog: s.Catalog, costRules: s.CostRules, publish: s.Precheck,
	}
}

func pricingWriteActor(c *gin.Context) (actor service.PriceWriteActor, ok bool) {
	subject, found := middleware2.GetAuthSubjectFromContext(c)
	if !found {
		response.Unauthorized(c, "User not found in context")
		return actor, false
	}
	return service.PriceWriteActorFromAuthMethod(subject.UserID, c.GetString("auth_method")), true
}

func bindPricingWriteJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return false
	}
	return true
}

func pricingWriteParamID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", name+" must be a positive integer").
			WithMetadata(map[string]string{"param": name}))
		return 0, false
	}
	return id, true
}

// ---- 单元格 ----

type cellCommitRequest struct {
	ApprovalID int64                    `json:"approval_id"`
	Confirm    bool                     `json:"confirm"`
	Request    service.CellWriteRequest `json:"request"`
}

// PreviewCells 登记一次单元格写入的预览。
// POST /api/v1/admin/pricing-matrix/cells/preview  {ops, group_revisions}
func (h *PricingWriteHandler) PreviewCells(c *gin.Context) {
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req service.CellWriteRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	req.OperatorID = actor.ID
	ticket, err := h.cells.Propose(c.Request.Context(), service.PriceWriteProposal{Request: req})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, ticket)
}

// CommitCells 提交单元格写入（要交互式管理员会话，路由上有 RequireAdminJWT；服务层再校验一次）。
// POST /api/v1/admin/pricing-matrix/cells/commit  {approval_id, confirm, request:{ops, group_revisions}}
func (h *PricingWriteHandler) CommitCells(c *gin.Context) {
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var body cellCommitRequest
	if !bindPricingWriteJSON(c, &body) {
		return
	}
	body.Request.OperatorID = actor.ID
	res, err := h.cells.Commit(c.Request.Context(), service.PriceWriteCommit{
		ApprovalID: body.ApprovalID, Request: body.Request, Confirm: body.Confirm, Actor: actor,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// ---- 分组配置 ----

type groupConfigCommitRequest struct {
	ApprovalID int64                           `json:"approval_id"`
	Confirm    bool                            `json:"confirm"`
	Request    service.GroupConfigWriteRequest `json:"request"`
}

// PreviewGroupConfig 登记一次分组配置写入的预览；分组 id 取自路径。
// POST /api/v1/admin/pricing-matrix/groups/:id/config/preview  {baseline_revision, access_mode, ...}
func (h *PricingWriteHandler) PreviewGroupConfig(c *gin.Context) {
	id, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req service.GroupConfigWriteRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	req.GroupID, req.OperatorID = id, actor.ID
	ticket, err := h.groupCfg.Propose(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, ticket)
}

// CommitGroupConfig 提交分组配置写入；分组 id 取自路径。
// PUT /api/v1/admin/pricing-matrix/groups/:id/config  {approval_id, confirm, request:{...}}
func (h *PricingWriteHandler) CommitGroupConfig(c *gin.Context) {
	id, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var body groupConfigCommitRequest
	if !bindPricingWriteJSON(c, &body) {
		return
	}
	body.Request.GroupID, body.Request.OperatorID = id, actor.ID
	res, err := h.groupCfg.Commit(c.Request.Context(), service.GroupConfigCommit{
		ApprovalID: body.ApprovalID, Request: body.Request, Confirm: body.Confirm, Actor: actor,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// PublishCheck 发布预检（W5 group.publish 的前置检查）：v2 白名单分组有无价或 0 元的开放项就返回错误。
// GET /api/v1/admin/pricing-matrix/groups/:id/publish-check
func (h *PricingWriteHandler) PublishCheck(c *gin.Context) {
	id, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	rep, err := h.publish.PublishCheck(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, rep)
}

// ---- 模型目录 ----

type catalogTransitionRequest struct {
	To           string `json:"to"`
	ConfirmUsage bool   `json:"confirm_usage"`
}

type catalogCreateRequest struct {
	ModelKey       string   `json:"model_key"`
	Platform       string   `json:"platform"`
	DisplayName    string   `json:"display_name"`
	Aliases        []string `json:"aliases"`
	ReferenceModel *string  `json:"reference_model"`
	Status         string   `json:"status"`
	Note           string   `json:"note"`
	ConfirmUsage   bool     `json:"confirm_usage"`
}

// PreviewCatalogTransition 预览目录条目的状态转换，含近 7 天用量。
// GET /api/v1/admin/model-catalog/:id/transition-preview?to=retired
func (h *PricingWriteHandler) PreviewCatalogTransition(c *gin.Context) {
	id, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	p, err := h.catalog.Preview(c.Request.Context(), id, service.ModelCatalogStatus(strings.TrimSpace(c.Query("to"))))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, p)
}

// TransitionCatalog 转换目录条目的状态；会挡掉近 7 天有流量的模型时要带 confirm_usage。
// PUT /api/v1/admin/model-catalog/:id/status  {to, confirm_usage}
func (h *PricingWriteHandler) TransitionCatalog(c *gin.Context) {
	id, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	var req catalogTransitionRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	entry, err := h.catalog.Transition(c.Request.Context(), id, service.ModelCatalogStatus(strings.TrimSpace(req.To)), req.ConfirmUsage)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, entry)
}

// CreateCatalogEntry 新建目录条目。
// POST /api/v1/admin/model-catalog  {model_key, platform, display_name, aliases, reference_model, status, note, confirm_usage}
func (h *PricingWriteHandler) CreateCatalogEntry(c *gin.Context) {
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req catalogCreateRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	createdBy := actor.ID
	entry, err := h.catalog.CreateChecked(c.Request.Context(), service.CreateModelCatalogInput{
		ModelKey: req.ModelKey, Platform: req.Platform, DisplayName: req.DisplayName, Aliases: req.Aliases,
		ReferenceModel: req.ReferenceModel, Status: service.ModelCatalogStatus(strings.TrimSpace(req.Status)),
		Note: req.Note, CreatedBy: &createdBy,
	}, req.ConfirmUsage)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, entry)
}

// ---- 成本核算规则 ----

type costRuleRequest struct {
	BaselineRevision int64                `json:"baseline_revision"`
	Rule             service.CostRuleSpec `json:"rule"`
}

// CreateCostRule 在 v2 分组上新建一条成本核算规则。
// POST /api/v1/admin/pricing-matrix/groups/:id/cost-rules  {baseline_revision, rule}
func (h *PricingWriteHandler) CreateCostRule(c *gin.Context) {
	gid, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req costRuleRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	res, err := h.costRules.Create(c.Request.Context(), actor.ID, gid, req.BaselineRevision, req.Rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// UpdateCostRule 整个替换一条成本核算规则。
// PUT /api/v1/admin/pricing-matrix/groups/:id/cost-rules/:rule_id  {baseline_revision, rule}
func (h *PricingWriteHandler) UpdateCostRule(c *gin.Context) {
	gid, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	rid, ok := pricingWriteParamID(c, "rule_id")
	if !ok {
		return
	}
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req costRuleRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	res, err := h.costRules.Update(c.Request.Context(), actor.ID, gid, req.BaselineRevision, rid, req.Rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// DeleteCostRule 删除一条成本核算规则；基线 revision 走查询参数（DELETE 不带请求体）。
// DELETE /api/v1/admin/pricing-matrix/groups/:id/cost-rules/:rule_id?baseline_revision=3
func (h *PricingWriteHandler) DeleteCostRule(c *gin.Context) {
	gid, ok := pricingWriteParamID(c, "id")
	if !ok {
		return
	}
	rid, ok := pricingWriteParamID(c, "rule_id")
	if !ok {
		return
	}
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	baseline, err := strconv.ParseInt(strings.TrimSpace(c.Query("baseline_revision")), 10, 64)
	if err != nil || baseline <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PARAMETER", "baseline_revision must be a positive integer").
			WithMetadata(map[string]string{"param": "baseline_revision"}))
		return
	}
	res, err := h.costRules.Delete(c.Request.Context(), actor.ID, gid, baseline, rid)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// ---- 已知免费名单 ----

type knownFreeRequest struct {
	Entries []service.BillingKnownFreeEntry `json:"entries"`
	Confirm bool                            `json:"confirm"`
}

// GetKnownFreeList 读当前名单。
// GET /api/v1/admin/pricing-matrix/known-free-list
func (h *PricingWriteHandler) GetKnownFreeList(c *gin.Context) {
	list, err := h.knownFree.Current(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"entries": list})
}

// PreviewKnownFreeList 预览改名单的影响：被删的条目、会新增的违规（白名单分组里因此无价的开放项）。
// POST /api/v1/admin/pricing-matrix/known-free-list/preview  {entries}
func (h *PricingWriteHandler) PreviewKnownFreeList(c *gin.Context) {
	var req knownFreeRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	change, err := h.knownFree.Preview(c.Request.Context(), req.Entries)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, change)
}

// UpdateKnownFreeList 写入名单。这是 C 档动作：只允许交互式管理员会话，不开放给 AI 与机器令牌。
// PUT /api/v1/admin/pricing-matrix/known-free-list  {entries, confirm}
func (h *PricingWriteHandler) UpdateKnownFreeList(c *gin.Context) {
	actor, ok := pricingWriteActor(c)
	if !ok {
		return
	}
	var req knownFreeRequest
	if !bindPricingWriteJSON(c, &req) {
		return
	}
	change, err := h.knownFree.Update(c.Request.Context(), actor, req.Entries, req.Confirm)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, change)
}
