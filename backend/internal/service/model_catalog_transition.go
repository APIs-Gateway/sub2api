package service

import (
	"context"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-2：模型目录的状态转换与 7 天用量检查（REVIEW_OPUS_2 S-7 与给 4b-2b 的要求 ⑥）。
//
// draft 与 retired 都会挡掉这个模型的所有新请求（开放分组与白名单分组都挡），所以把一个近期还有流量的模型转成这两个状态
// （或者新建一个已经有流量的名字、状态为 draft）会立刻让真实流量失败。检查放在这个服务里，而不是放在界面：
// W5 的 AI 通道（model.onboard、model.expose）不经过界面。近 7 天有用量时必须带 confirm_usage 才能转换，
// 预览接口把用量展示给管理员。转成 active 不需要检查。

// CatalogUsageWindow 用量检查的窗口。
const CatalogUsageWindow = 7 * 24 * time.Hour

// 目录状态转换的错误原因。
const (
	// ReasonCatalogUsageConfirm 近 7 天有用量，要带 confirm_usage 才能把模型转成 draft 或 retired；metadata 里有用量。
	ReasonCatalogUsageConfirm = "MODEL_CATALOG_USAGE_CONFIRM_REQUIRED"
	// ReasonCatalogStatusChanged 转换时条目的状态已经被别人改了。
	ReasonCatalogStatusChanged = "MODEL_CATALOG_STATUS_CHANGED"
)

// CatalogUsage 一个模型近一段时间的用量（按 usage_logs 里的模型名与请求模型名统计）。
type CatalogUsage struct {
	Requests   int64      `json:"requests"`
	LastUsedAt *time.Time `json:"last_used_at"`
	WindowDays int        `json:"window_days"`
}

// ModelCatalogStatusStore 目录条目的读取、状态更新与用量统计。
type ModelCatalogStatusStore interface {
	GetByID(ctx context.Context, id int64) (*ModelCatalogEntry, error)
	// UpdateStatus 把状态从 from 改成 to；条目的状态已经不是 from 时返回 false。
	UpdateStatus(ctx context.Context, id int64, from, to ModelCatalogStatus) (bool, error)
	// CountModelUsageSince 统计自 since 以来 models 里任一名字出现在 model 或 requested_model 上的用量。
	CountModelUsageSince(ctx context.Context, models []string, since time.Time) (CatalogUsage, error)
}

// ModelCatalogTransitionService 目录状态转换（含 7 天用量检查）。
type ModelCatalogTransitionService struct {
	store   ModelCatalogStatusStore
	catalog *ModelCatalogService
	now     func() time.Time
}

// NewModelCatalogTransitionService 创建服务。
func NewModelCatalogTransitionService(store ModelCatalogStatusStore, catalog *ModelCatalogService) *ModelCatalogTransitionService {
	return &ModelCatalogTransitionService{store: store, catalog: catalog, now: time.Now}
}

// CatalogTransitionPreview 一次状态转换的预览。
type CatalogTransitionPreview struct {
	Entry ModelCatalogEntry  `json:"entry"`
	From  ModelCatalogStatus `json:"from"`
	To    ModelCatalogStatus `json:"to"`
	Usage CatalogUsage       `json:"usage"`
	// ConfirmRequired 为真表示转换会挡掉近 7 天有流量的模型，提交时要带 confirm_usage。
	ConfirmRequired bool `json:"confirm_required"`
}

// blocksTraffic 目标状态是否会挡掉这个模型的新请求。
func blocksTraffic(to ModelCatalogStatus) bool {
	return to == ModelCatalogDraft || to == ModelCatalogRetired
}

func (s *ModelCatalogTransitionService) usage(ctx context.Context, entry *ModelCatalogEntry) (CatalogUsage, error) {
	names := append([]string{entry.ModelKey}, entry.Aliases...)
	u, err := s.store.CountModelUsageSince(ctx, names, s.now().Add(-CatalogUsageWindow))
	if err != nil {
		return CatalogUsage{}, err
	}
	u.WindowDays = int(CatalogUsageWindow / (24 * time.Hour))
	return u, nil
}

func (s *ModelCatalogTransitionService) load(ctx context.Context, id int64, to ModelCatalogStatus) (*ModelCatalogEntry, error) {
	if !to.valid() {
		return nil, ErrModelCatalogInvalid.WithMetadata(map[string]string{"param": "status"})
	}
	entry, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, ErrModelCatalogNotFound
	}
	return entry, nil
}

// Preview 预览把条目转成 to：给出近 7 天的用量，以及提交时是否要带 confirm_usage。
func (s *ModelCatalogTransitionService) Preview(ctx context.Context, id int64, to ModelCatalogStatus) (*CatalogTransitionPreview, error) {
	entry, err := s.load(ctx, id, to)
	if err != nil {
		return nil, err
	}
	p := &CatalogTransitionPreview{Entry: *entry, From: entry.Status, To: to}
	if entry.Status == to || !blocksTraffic(to) {
		return p, nil
	}
	if p.Usage, err = s.usage(ctx, entry); err != nil {
		return nil, err
	}
	p.ConfirmRequired = p.Usage.Requests > 0
	return p, nil
}

// Transition 把条目转成 to。目标是 draft 或 retired 且近 7 天有用量时，必须 confirmUsage。状态没变是空操作。
func (s *ModelCatalogTransitionService) Transition(ctx context.Context, id int64, to ModelCatalogStatus, confirmUsage bool) (*ModelCatalogEntry, error) {
	p, err := s.Preview(ctx, id, to)
	if err != nil {
		return nil, err
	}
	if p.From == to {
		return &p.Entry, nil
	}
	if p.ConfirmRequired && !confirmUsage {
		return nil, usageConfirmError(p.Usage)
	}
	changed, err := s.store.UpdateStatus(ctx, id, p.From, to)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, infraerrors.Conflict(ReasonCatalogStatusChanged, "the catalog entry changed since it was read")
	}
	out := p.Entry
	out.Status = to
	return &out, nil
}

// CreateChecked 新建目录条目。状态不是 active（默认 draft）且这个名字（或别名）近 7 天已经有用量时，必须 confirmUsage：
// 新建一个已经在跑的名字为 draft，会立刻挡掉它的流量。
func (s *ModelCatalogTransitionService) CreateChecked(ctx context.Context, in CreateModelCatalogInput, confirmUsage bool) (*ModelCatalogEntry, error) {
	entry, err := buildCatalogEntry(in)
	if err != nil {
		return nil, err
	}
	if blocksTraffic(entry.Status) {
		u, err := s.usage(ctx, entry)
		if err != nil {
			return nil, err
		}
		if u.Requests > 0 && !confirmUsage {
			return nil, usageConfirmError(u)
		}
	}
	return s.catalog.Create(ctx, in)
}

func usageConfirmError(u CatalogUsage) error {
	md := map[string]string{
		"requests":    strconv.FormatInt(u.Requests, 10),
		"window_days": strconv.Itoa(u.WindowDays),
	}
	if u.LastUsedAt != nil {
		md["last_used_at"] = u.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return infraerrors.Conflict(ReasonCatalogUsageConfirm,
		"the model has recent traffic; confirm to block it").WithMetadata(md)
}
