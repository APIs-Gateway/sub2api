package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// W6 PR9：LiteLLM 价格固定快照（设计文档 2.6、6.1）。
//
// 官方价有三层：固定快照（本文件相关）、内置 fallback、卡策略。快照层只管「LiteLLM 价格 JSON」这一份
// 数据：默认模式 auto 与今天完全相同（读 data_dir 下的价格文件并与远程同步）；切到 pinned 之后，
// 计费只读 status = 'active' 的那一份快照。

const (
	// SettingKeyPricingSnapshotMode 是价格数据源模式的存储键。W5 注册表里为 C 档、AI 不可写；
	// 通用设置接口按字段白名单写入，不会写到它。
	SettingKeyPricingSnapshotMode = "pricing_snapshot_mode"

	PricingSnapshotModeAuto   = "auto"
	PricingSnapshotModePinned = "pinned"
)

// 快照状态与来源，和迁移 216 的 CHECK 约束一一对应。
const (
	PricingSnapshotStatusCandidate  = "candidate"
	PricingSnapshotStatusActive     = "active"
	PricingSnapshotStatusSuperseded = "superseded"
	PricingSnapshotStatusRejected   = "rejected"

	PricingSnapshotSourceBootstrap = "bootstrap"
	PricingSnapshotSourceRemote    = "remote"
	PricingSnapshotSourceMerged    = "merged"
)

var (
	// ErrPricingSnapshotNotFound 表示没有符合条件的快照（例如没有生效快照）。
	ErrPricingSnapshotNotFound = errors.New("pricing snapshot not found")
	// ErrPricingSnapshotConflict 表示并发切换生效快照时输给了另一个事务（唯一索引冲突）。
	ErrPricingSnapshotConflict = errors.New("pricing snapshot: active snapshot changed concurrently")
	// ErrPricingSnapshotStartup 表示 pinned 模式（或无法判定模式）下启动加载生效快照失败。
	// 调用方必须把它当作致命错误：不能静默退回远端数据或内置文件（fail-closed）。
	ErrPricingSnapshotStartup = errors.New("pricing snapshot: startup load failed")
	// ErrPricingSnapshotsUnavailable 表示价格服务没有接上快照存储（未调用 ConfigureSnapshots）。
	ErrPricingSnapshotsUnavailable = errors.New("pricing snapshots are not configured")
	// ErrPricingPinned 表示价格服务处于 pinned 模式，不接受把远程数据直接写进生效数据。
	ErrPricingPinned = errors.New("pricing is pinned: remote data can only be stored as a candidate snapshot")
	// ErrPricingAlreadyPinned 表示已经是 pinned 模式，不需要再次固定。
	ErrPricingAlreadyPinned = errors.New("pricing is already pinned")
	// ErrPricingNotPinned 表示操作要求已有生效快照（先执行「固定当前价格」）。
	ErrPricingNotPinned = errors.New("pricing is not pinned: pin the current pricing first")
	// ErrPricingSnapshotBaselineChanged 表示批准时生效快照已不是预览时的那一份（基线已变），需要重新预览。
	ErrPricingSnapshotBaselineChanged = errors.New("pricing snapshot: the active snapshot changed since the preview")
	// ErrPricingSnapshotNotCandidate 表示指定的快照不是待批准的候选。
	ErrPricingSnapshotNotCandidate = errors.New("pricing snapshot is not a pending candidate")
	// ErrPricingSnapshotPlanMismatch 表示二次确认的 plan_hash 与按当前状态重新算出的不一致。
	ErrPricingSnapshotPlanMismatch = errors.New("pricing snapshot: plan hash does not match the current plan")
	// ErrPricingSnapshotNothingToApprove 表示没有任何被批准的差异（全部搁置或候选与基线相同）。
	ErrPricingSnapshotNothingToApprove = errors.New("pricing snapshot: nothing to approve")
	// ErrPricingSnapshotUnknownHold 表示搁置名单里有候选差异里不存在的模型。
	ErrPricingSnapshotUnknownHold = errors.New("pricing snapshot: hold list contains a model that is not in the diff")
	// ErrPricingBootstrapMismatch 表示 data_dir 里的价格文件与此刻内存里正在计费的数据不一致，
	// 固定会改变价格，所以拒绝（可能是远程同步正好在进行，稍后重试）。
	ErrPricingBootstrapMismatch = errors.New("pricing file differs from the data currently in use; refusing to pin")
)

// PricingSnapshotMeta 是快照的元数据（不含价格 JSON 本体）。
type PricingSnapshotMeta struct {
	ID                  int64      `json:"id"`
	Label               string     `json:"label"`
	Source              string     `json:"source"`
	SourceURL           string     `json:"source_url"`
	ContentSHA256       string     `json:"content_sha256"`
	ModelCount          int        `json:"model_count"`
	ParentSnapshotID    *int64     `json:"parent_snapshot_id"`
	CandidateSnapshotID *int64     `json:"candidate_snapshot_id"`
	Status              string     `json:"status"`
	FetchedBy           *int64     `json:"fetched_by"`
	FetchedAt           time.Time  `json:"fetched_at"`
	ApprovedBy          *int64     `json:"approved_by"`
	ApprovedAt          *time.Time `json:"approved_at"`
	ChangeSetID         *int64     `json:"change_set_id"`
	Note                string     `json:"note"`
}

// NewPricingSnapshot 是写入一份新快照所需的输入。Payload 是未压缩的 LiteLLM JSON 原文。
type NewPricingSnapshot struct {
	Label               string
	Source              string
	SourceURL           string
	ContentSHA256       string
	ModelCount          int
	Payload             []byte
	ParentSnapshotID    *int64
	CandidateSnapshotID *int64
	FetchedBy           *int64
	ApprovedBy          *int64
	Note                string
}

// PricingSnapshotRepository 是快照表的存取接口。
type PricingSnapshotRepository interface {
	// GetActiveMeta 返回生效快照（status = 'active'）的元数据；没有则返回 ErrPricingSnapshotNotFound。
	GetActiveMeta(ctx context.Context) (*PricingSnapshotMeta, error)
	// GetPayload 返回指定快照解压后的价格 JSON 原文；没有则返回 ErrPricingSnapshotNotFound。
	GetPayload(ctx context.Context, id int64) ([]byte, error)
	// GetMeta 返回指定快照的元数据；没有则返回 ErrPricingSnapshotNotFound。
	GetMeta(ctx context.Context, id int64) (*PricingSnapshotMeta, error)
	// List 按 fetched_at 倒序列出快照元数据（不含 payload）；statuses 为空表示全部状态。
	List(ctx context.Context, statuses []string, limit int) ([]PricingSnapshotMeta, error)
	// InsertCandidate 保存一份候选快照；同一 content_sha256 已有候选时不重复保存，返回已有的那份（created=false）。
	InsertCandidate(ctx context.Context, in NewPricingSnapshot) (meta *PricingSnapshotMeta, created bool, err error)
	// RejectCandidate 把候选置为 rejected；不是候选时返回 ErrPricingSnapshotNotCandidate。
	RejectCandidate(ctx context.Context, id int64) error
	// DeleteExpiredCandidates 删除 fetched_at 早于 before 的未批准候选与已拒绝快照（差异行级联删除），返回删除行数。
	DeleteExpiredCandidates(ctx context.Context, before time.Time) (int64, error)
	// ApplyMerged 批准：在一个事务里校验基线、写入 merged 快照并切换 active、落差异行与候选状态，
	// 并在提交前运行 req.Check；任何一步失败整体回滚。
	ApplyMerged(ctx context.Context, req ApplyMergedSnapshot) (*PricingSnapshotMeta, error)
	// RecentBillingModels 返回 usage_logs 近 days 天出现过的模型名（去重、封顶），差异页用它找出实际变价的名字。
	RecentBillingModels(ctx context.Context, days int) ([]string, error)
	// ActivateNew 在同一个事务里先把旧的生效行置 superseded，再插入新行并置 active。
	// 并发切换输掉时返回 ErrPricingSnapshotConflict。
	ActivateNew(ctx context.Context, in NewPricingSnapshot) (*PricingSnapshotMeta, error)
}

// PricingSnapshotDiffRecord 是 pricing_snapshot_diffs 的一行：候选对基线的单个模型差异与批准决定。
type PricingSnapshotDiffRecord struct {
	ModelKey      string
	ChangeType    string // added | removed | changed
	OldPrice      json.RawMessage
	NewPrice      json.RawMessage
	ChangedFields []string
	Decision      string // approve | hold
}

const (
	PricingDiffAdded   = "added"
	PricingDiffRemoved = "removed"
	PricingDiffChanged = "changed"

	PricingDiffDecisionApprove = "approve"
	PricingDiffDecisionHold    = "hold"
)

// ApplyMergedSnapshot 是批准写入的输入。
type ApplyMergedSnapshot struct {
	// New 是 merged 快照（Source = merged，ParentSnapshotID 与 CandidateSnapshotID 已填）。
	New NewPricingSnapshot
	// ExpectedActiveID / ExpectedActiveSHA 是预览时的生效快照；事务内锁住 active 行后必须仍然相同。
	ExpectedActiveID  int64
	ExpectedActiveSHA string
	// ConsumeCandidate 为真时把来源候选置为 superseded（没有被搁置的差异时）；否则它留在候选里等下次。
	ConsumeCandidate bool
	Diffs            []PricingSnapshotDiffRecord
	// Check 在新快照已置 active、提交之前运行，用同一个事务连接做保存时校验；返回错误则整体回滚。可为 nil。
	Check func(ctx context.Context, exec MatrixExecutor) error
}
