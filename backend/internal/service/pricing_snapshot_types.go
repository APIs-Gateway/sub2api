package service

import (
	"context"
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
	// ErrPricingBootstrapMismatch 表示 data_dir 里的价格文件与此刻内存里正在计费的数据不一致，
	// 固定会改变价格，所以拒绝（可能是远程同步正好在进行，稍后重试）。
	ErrPricingBootstrapMismatch = errors.New("pricing file differs from the data currently in use; refusing to pin")
)

// PricingSnapshotMeta 是快照的元数据（不含价格 JSON 本体）。
type PricingSnapshotMeta struct {
	ID                  int64
	Label               string
	Source              string
	SourceURL           string
	ContentSHA256       string
	ModelCount          int
	ParentSnapshotID    *int64
	CandidateSnapshotID *int64
	Status              string
	FetchedBy           *int64
	FetchedAt           time.Time
	ApprovedBy          *int64
	ApprovedAt          *time.Time
	ChangeSetID         *int64
	Note                string
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
	// ActivateNew 在同一个事务里先把旧的生效行置 superseded，再插入新行并置 active。
	// 并发切换输掉时返回 ErrPricingSnapshotConflict。
	ActivateNew(ctx context.Context, in NewPricingSnapshot) (*PricingSnapshotMeta, error)
}
