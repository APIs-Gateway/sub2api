package service

import (
	"context"
	"log/slog"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR5：阶段切换（设计 4.3）。
//
// PR5 只允许 legacy 与 shadow 两个值（S-13）：没有任何路径会把真实请求路由到矩阵，
// 所以切到 shadow 对用户的账单与准入零影响，price_delta 恒为 none。
// PR7b 起接上 PricingStageSwitcher（pricing_stage_switch.go）：目标是 v2 时走闸门、预览与事务，v2 回拨走归档与重新派生，
// 所有阶段变更（含 legacy 与 shadow 之间）都在事务里记审计。没有接 switcher 时（单元测试）仍然只允许 legacy 与 shadow。
// 切换登记为 W5 的 change-set 动作 pricing.stage_switch（Category 不是 price，touches_price=true，
// 档位由 W5 的 TierEngine 按 price_delta 得出，W6 不硬编码）。W5 落地之前，登记的含义是：
// 响应与日志里带上动作名与涉价字段，group_model_config 记下 stage_changed_at / stage_changed_by。
// PR10 之前没有界面，用管理员令牌调用 API。

// PricingActionStageSwitch 是 W5 动作注册表里的动作名（W6 PR5 登记）。
const PricingActionStageSwitch = "pricing.stage_switch"

// PricingActionCategoryStage 动作的类别：不是 price，但 touches_price=true。
const PricingActionCategoryStage = "pricing_stage"

// 阶段切换的错误原因。
const (
	ReasonPricingStageNotAllowed = "PRICING_STAGE_NOT_ALLOWED"
	ReasonPricingStageConfirm    = "PRICING_STAGE_CONFIRM_REQUIRED"
	ReasonPricingStageActor      = "PRICING_STAGE_ACTOR_REQUIRED"
	ReasonPricingStageNotDerived = "PRICING_STAGE_GROUP_NOT_DERIVED"
)

// PricingStageChange 是存储层一次切换的结果。
type PricingStageChange struct {
	GroupID   int64        `json:"group_id"`
	From      PricingStage `json:"from"`
	To        PricingStage `json:"to"`
	Changed   bool         `json:"changed"`
	Revision  int64        `json:"revision"`
	ChangedAt time.Time    `json:"changed_at"`
}

// PricingStageStore 阶段的读写。
type PricingStageStore interface {
	// SwitchStage 在一个事务里：对 group_model_config 的该分组行 SELECT ... FOR UPDATE
	// （与派生钩子互斥，设计 4.2 混合阶段规则第 3 条），把阶段改成 to，记录 stage_changed_at / stage_changed_by，
	// 内容变了才把 revision 加一。分组不存在或已软删除返回 ErrGroupNotFound；
	// 分组没有配置行（还没有派生过）返回 ErrPricingStageNotDerived。已经是目标阶段时 Changed 为 false，不写库。
	SwitchStage(ctx context.Context, groupID int64, to PricingStage, operatorID int64, now time.Time) (*PricingStageChange, error)
}

// ErrPricingStageNotDerived 分组还没有矩阵配置行，不能切换阶段。
var ErrPricingStageNotDerived = infraerrors.Conflict(ReasonPricingStageNotDerived,
	"the group has no derived pricing configuration yet; run the derive seed first")

// PricingStageSwitchRequest 切换请求。
type PricingStageSwitchRequest struct {
	GroupID    int64
	To         PricingStage
	OperatorID int64
	// Confirm 管理员的二次确认。
	Confirm bool
	// ApprovalID 预览凭证；切到 v2 必须带，其余变更不需要。
	ApprovalID int64
	// AuthMethod 是鉴权中间件记下的 auth_method（c.GetString("auth_method")），不取自请求体；
	// 只有 JWT 会话算交互式管理员（PriceWriteActorFromAuthMethod）。
	AuthMethod string
}

// PricingStageSwitchResult 切换结果，带上登记的动作信息。
type PricingStageSwitchResult struct {
	Action       string     `json:"action"`
	Category     string     `json:"category"`
	TouchesPrice bool       `json:"touches_price"`
	PriceDelta   PriceDelta `json:"price_delta"`
	// Kind 是 advance（向后推进）、rollback（回拨）或 noop（已经是目标阶段，没有写任何东西）。
	Kind       string `json:"kind,omitempty"`
	ApprovalID int64  `json:"approval_id,omitempty"`
	AuditID    int64  `json:"audit_id,omitempty"`
	// SnapshotReady 为 false 表示变更已经提交，但本实例没能把分组快照同步加载好（其他实例经通知失效，TTL 60 秒兜底）。
	SnapshotReady bool                 `json:"snapshot_ready"`
	Gate          *StageGateReport     `json:"gate,omitempty"`
	Archived      *StageArchiveSummary `json:"archived,omitempty"`
	PricingStageChange
}

// PricingStageService 阶段切换，以及影子比对结果的只读查看。
type PricingStageService struct {
	store    PricingStageStore
	policy   *stagedPolicy
	recorder *PricingShadowRecorder
	// switcher 为 nil 时只允许 legacy 与 shadow，走 store.SwitchStage（PR5 的路径）。
	switcher *PricingStageSwitcher
	now      func() time.Time
}

// NewPricingStageService 创建阶段切换服务。policy 用来在写库之后失效分组快照、读取影子计数；recorder 用来读取差异样本。
func NewPricingStageService(store PricingStageStore, policy *StagedGroupPolicy, recorder *PricingShadowRecorder) *PricingStageService {
	return &PricingStageService{store: store, policy: policy, recorder: recorder, now: time.Now}
}

// SetSwitcher 接上阶段切换器（PR7b）。只在装配阶段调用；之后 v2 才是允许的目标阶段。
func (s *PricingStageService) SetSwitcher(sw *PricingStageSwitcher) {
	s.switcher = sw
}

// pricingStageAllowed 判断 to 是不是允许切到的阶段：legacy 与 shadow 一直允许，v2 要接上切换器（闸门、预览、事务）才允许。
func pricingStageAllowed(to PricingStage, v2Open bool) bool {
	return to == PricingStageLegacy || to == PricingStageShadow || (to == PricingStageV2 && v2Open)
}

// Switch 切换分组的价格体系阶段。
func (s *PricingStageService) Switch(ctx context.Context, req PricingStageSwitchRequest) (*PricingStageSwitchResult, error) {
	if req.OperatorID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPricingStageActor, "an administrator is required")
	}
	if req.GroupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_PARAMETER", "group id must be a positive integer")
	}
	if !pricingStageAllowed(req.To, s.switcher != nil) {
		return nil, infraerrors.BadRequest(ReasonPricingStageNotAllowed,
			"only legacy and shadow are allowed; the v2 stage is not available yet")
	}
	if !req.Confirm {
		return nil, infraerrors.BadRequest(ReasonPricingStageConfirm, "confirm the stage switch explicitly")
	}
	if s.switcher != nil {
		return s.switcher.Commit(ctx, req)
	}

	change, err := s.store.SwitchStage(ctx, req.GroupID, req.To, req.OperatorID, s.now())
	if err != nil {
		return nil, err
	}
	// 写库之后失效快照：本实例立即重新加载，其他实例经通知失效。已经是目标阶段时也失效一次，代价只是一次重新加载。
	if s.policy != nil {
		s.policy.InvalidateGroups(req.GroupID)
	}
	slog.Info(PricingActionStageSwitch,
		"group_id", change.GroupID, "from", string(change.From), "to", string(change.To), "changed", change.Changed,
		"operator_id", req.OperatorID, "revision", change.Revision, "touches_price", true, "price_delta", string(PriceDeltaNone))

	return &PricingStageSwitchResult{
		Action:       PricingActionStageSwitch,
		Category:     PricingActionCategoryStage,
		TouchesPrice: true,
		// legacy 与 shadow 之间的切换不改变任何账单与准入：price_delta 恒为 none。
		// 影子比对不为 0 的分组也不会因为切到 shadow 而变价；需要比对结果来定档位的是 shadow 到 v2（PR7）。
		PriceDelta:         PriceDeltaNone,
		PricingStageChange: *change,
	}, nil
}

// Preview 预览一次阶段变更（PR7b）：目标是 v2 时评估闸门并登记预览凭证。没有接切换器时返回「不允许」。
func (s *PricingStageService) Preview(ctx context.Context, req PricingStagePreviewRequest) (*PricingStagePreview, error) {
	if s.switcher == nil {
		return nil, infraerrors.BadRequest(ReasonPricingStageNotAllowed, "stage preview is not available")
	}
	return s.switcher.Preview(ctx, req)
}

// Audit 返回分组最近的阶段变更审计。
func (s *PricingStageService) Audit(ctx context.Context, groupID int64, limit int) ([]StageAuditEntry, error) {
	if s.switcher == nil {
		return []StageAuditEntry{}, nil
	}
	return s.switcher.Audit(ctx, groupID, limit)
}

// ShadowStats 返回影子比对的进程内计数。
func (s *PricingStageService) ShadowStats() PricingShadowStats {
	if s.policy == nil {
		return PricingShadowStats{ComparedTotal: []PricingShadowComparedCount{}, DiffTotal: []PricingShadowDiffCount{}, SkippedTotal: map[string]int64{}}
	}
	return s.policy.Stats()
}

// MatrixSnapshotStats 返回矩阵快照缓存的进程内计数。
func (s *PricingStageService) MatrixSnapshotStats() MatrixSnapshotStats {
	if s.policy == nil {
		return MatrixSnapshotStats{}
	}
	return s.policy.MatrixSnapshotStats()
}

// ShadowSamples 读取最近的差异样本。groupID 为 0 表示不限分组。
func (s *PricingStageService) ShadowSamples(ctx context.Context, groupID int64, limit int) ([]PricingShadowSample, error) {
	if s.recorder == nil {
		return []PricingShadowSample{}, nil
	}
	return s.recorder.List(ctx, groupID, limit)
}
