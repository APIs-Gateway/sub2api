package service

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-2：已知免费名单（settings 里的 billing_known_free_list）的写入口（REVIEW_OPUS_2 给 4b-2b 的要求 ⑥⑦）。
//
// 名单会影响保存时校验：名单里的（分组、模型）可以是 0 元的 open 单元格；删掉一项，白名单分组里原本合法的
// 0 元单元格就变成违规。所以名单的写入：
//   - 是 C 档、AI 不可写：只允许交互式管理员会话（JWT）并要求二次确认；通用设置写入拒绝这个键（IsW6ProtectedSettingKey）；
//   - 在同一个事务里对所有 v2 白名单分组按「改后的名单」做保存时校验：因此变成违规的单元格会被列出来并阻止写入
//     （删除之前就已经违规的单元格不算，避免被历史问题卡住）；
//   - 写入前先锁住这些白名单分组的配置行（与单元格写入、阶段切换互斥），所以并发的单元格写入读到的是写入之后的名单。

// ReasonKnownFreeListInvalid 名单内容不合法。
const ReasonKnownFreeListInvalid = "KNOWN_FREE_LIST_INVALID"

// MaxKnownFreeListEntries 名单最多的条目数。
const MaxKnownFreeListEntries = 2000

// KnownFreeListStore 名单的存取与白名单分组的 open 单元格读取，都在调用方的事务里。
type KnownFreeListStore interface {
	// GetKnownFreeList 读名单原文，不加锁，给只读路径（读接口与预览）用；没有这一行返回空串。
	GetKnownFreeList(ctx context.Context, exec MatrixExecutor) (string, error)
	// GetKnownFreeListTx 读名单原文并锁住这一行，给写入事务用；没有这一行返回空串。
	GetKnownFreeListTx(ctx context.Context, exec MatrixExecutor) (string, error)
	// SetKnownFreeListTx 写名单原文（upsert）。必须在事务里调用。
	SetKnownFreeListTx(ctx context.Context, tx MatrixTx, raw string) error
	// AllowlistOpenCellsTx 返回所有 v2 白名单分组里 open 的单元格；lock 为真时先按分组 id 升序锁住这些分组的配置行。
	AllowlistOpenCellsTx(ctx context.Context, exec MatrixExecutor, lock bool) ([]ExposureCell, error)
}

// KnownFreeListService 已知免费名单的写入口。
type KnownFreeListService struct {
	store  PriceWriteStore
	list   KnownFreeListStore
	prices OfficialPriceStateSource
	// invalidator 名单变了以后失效哪些分组的快照缓存：快照里不含名单，所以不需要；保留为 nil。
}

// NewKnownFreeListService 创建服务。prices 为 nil 时官方价一律按「没有」处理（与保存时校验一致，失败关闭）。
func NewKnownFreeListService(store PriceWriteStore, list KnownFreeListStore, prices OfficialPriceStateSource) *KnownFreeListService {
	return &KnownFreeListService{store: store, list: list, prices: prices}
}

// KnownFreeListChange 一次名单变更的预览或结果。
type KnownFreeListChange struct {
	Before []BillingKnownFreeEntry `json:"before"`
	After  []BillingKnownFreeEntry `json:"after"`
	// Removed 被删掉的条目。
	Removed []BillingKnownFreeEntry `json:"removed"`
	// NewViolations 改后会新增的违规（白名单分组里因此无价或 0 元的 open 单元格）；非空时写入被阻止。
	NewViolations []ExposureViolation `json:"new_violations"`
	Changed       bool                `json:"changed"`
	// PreviousInvalid 现有名单的原文写坏了（不是合法 JSON）：保存时校验把它当空名单。此时任何提交（包括空名单）都算变更，
	// 会用规范化的内容覆盖坏值，管理员才有办法清掉它。
	PreviousInvalid bool `json:"previous_invalid"`
}

// staticKnownFreeSettings 只回答已知免费名单这一个键，值是调用方给定的原文。
type staticKnownFreeSettings struct {
	SettingRepository
	raw string
}

func (s staticKnownFreeSettings) GetValue(_ context.Context, key string) (string, error) {
	if key != SettingKeyBillingKnownFreeList {
		return "", ErrSettingNotFound
	}
	return s.raw, nil
}

// NormalizeKnownFreeList 校验并规范名单：model 必填且不重复（同一分组内忽略大小写），group_id 不能为负，
// 条数有上限；规范成固定顺序（分组、模型）。名单里的 model 保持字面（保存时校验按字面比较，忽略大小写）。
func NormalizeKnownFreeList(in []BillingKnownFreeEntry) ([]BillingKnownFreeEntry, error) {
	if len(in) > MaxKnownFreeListEntries {
		return nil, infraerrors.BadRequest(ReasonKnownFreeListInvalid, "too many entries").
			WithMetadata(map[string]string{"max": strconv.Itoa(MaxKnownFreeListEntries)})
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]BillingKnownFreeEntry, 0, len(in))
	for _, e := range in {
		e.Model = strings.TrimSpace(e.Model)
		e.Note = strings.TrimSpace(e.Note)
		if e.Model == "" || len(e.Model) > 200 || len(e.Note) > 500 || e.GroupID < 0 {
			return nil, infraerrors.BadRequest(ReasonKnownFreeListInvalid, "an entry needs a model (at most 200 characters), a note of at most 500 characters and a non-negative group_id")
		}
		key := strconv.FormatInt(e.GroupID, 10) + "/" + strings.ToLower(e.Model)
		if _, dup := seen[key]; dup {
			return nil, infraerrors.BadRequest(ReasonKnownFreeListInvalid, "duplicate entry").
				WithMetadata(map[string]string{"model": e.Model, "group_id": strconv.FormatInt(e.GroupID, 10)})
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return strings.ToLower(out[i].Model) < strings.ToLower(out[j].Model)
	})
	return out, nil
}

func marshalKnownFreeList(entries []BillingKnownFreeEntry) (string, error) {
	if len(entries) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Current 读当前名单（读失败或内容写坏时返回错误，而不是悄悄当成空名单：管理员要看到真实的原文）。
func (s *KnownFreeListService) Current(ctx context.Context) ([]BillingKnownFreeEntry, error) {
	raw, err := s.list.GetKnownFreeList(ctx, s.store.Reader())
	if err != nil {
		return nil, err
	}
	list, err := parseBillingKnownFreeList(raw)
	if err != nil {
		return nil, err
	}
	// 没配置过时解析结果是 nil；返回空切片，接口的 entries 才永远是数组。
	return nonNilEntries(list), nil
}

// Preview 预览一次名单变更：列出删掉的条目，以及改后会变成违规的白名单单元格。不写入、不加锁。
func (s *KnownFreeListService) Preview(ctx context.Context, entries []BillingKnownFreeEntry) (*KnownFreeListChange, error) {
	norm, err := NormalizeKnownFreeList(entries)
	if err != nil {
		return nil, err
	}
	reader := s.store.Reader()
	raw, err := s.list.GetKnownFreeList(ctx, reader)
	if err != nil {
		return nil, err
	}
	return s.diff(ctx, reader, raw, norm, false)
}

// diff 计算变更：当前名单（原文）与目标名单之间，哪些条目被删、哪些单元格会新增违规。
func (s *KnownFreeListService) diff(ctx context.Context, exec MatrixExecutor, currentRaw string, target []BillingKnownFreeEntry, lock bool) (*KnownFreeListChange, error) {
	current, err := parseBillingKnownFreeList(currentRaw)
	invalid := err != nil
	if invalid {
		// 现有名单写坏了：保存时校验把它当空名单；这里也一样，但要标出来，并且提交任何内容都算变更（见 PreviousInvalid）。
		current = nil
	}
	targetRaw, err := marshalKnownFreeList(target)
	if err != nil {
		return nil, err
	}
	cells, err := s.list.AllowlistOpenCellsTx(ctx, exec, lock)
	if err != nil {
		return nil, err
	}
	now := NewExposureValidator(s.prices, staticKnownFreeSettings{raw: targetRaw}).Check(ctx, cells)
	was := NewExposureValidator(s.prices, staticKnownFreeSettings{raw: mustMarshalKnownFree(current)}).Check(ctx, cells)
	existing := make(map[string]struct{}, len(was))
	for _, v := range was {
		existing[violationKey(v)] = struct{}{}
	}
	newViolations := []ExposureViolation{}
	for _, v := range now {
		if _, old := existing[violationKey(v)]; !old {
			newViolations = append(newViolations, v)
		}
	}
	return &KnownFreeListChange{
		Before: nonNilEntries(current), After: nonNilEntries(target), Removed: removedKnownFree(current, target),
		NewViolations: newViolations, Changed: invalid || marshalOrEmpty(current) != targetRaw, PreviousInvalid: invalid,
	}, nil
}

func nonNilEntries(in []BillingKnownFreeEntry) []BillingKnownFreeEntry {
	if in == nil {
		return []BillingKnownFreeEntry{}
	}
	return in
}

func mustMarshalKnownFree(entries []BillingKnownFreeEntry) string {
	raw, err := marshalKnownFreeList(entries)
	if err != nil {
		return "[]"
	}
	return raw
}

func marshalOrEmpty(entries []BillingKnownFreeEntry) string {
	norm, err := NormalizeKnownFreeList(entries)
	if err != nil {
		return mustMarshalKnownFree(entries)
	}
	return mustMarshalKnownFree(norm)
}

func violationKey(v ExposureViolation) string {
	return strconv.FormatInt(v.GroupID, 10) + "/" + v.ModelKey + "/" + string(v.Reason)
}

func removedKnownFree(current, target []BillingKnownFreeEntry) []BillingKnownFreeEntry {
	keep := make(map[string]struct{}, len(target))
	for _, e := range target {
		keep[strconv.FormatInt(e.GroupID, 10)+"/"+strings.ToLower(strings.TrimSpace(e.Model))] = struct{}{}
	}
	out := []BillingKnownFreeEntry{}
	for _, e := range current {
		if _, ok := keep[strconv.FormatInt(e.GroupID, 10)+"/"+strings.ToLower(strings.TrimSpace(e.Model))]; !ok {
			out = append(out, e)
		}
	}
	return out
}

// Update 写入新名单。只允许交互式管理员会话并要求二次确认；在同一个事务里先锁住白名单分组、
// 再按改后的名单做保存时校验，有新增违规就阻止并列出（事务回滚，名单不变）。
func (s *KnownFreeListService) Update(ctx context.Context, actor PriceWriteActor, entries []BillingKnownFreeEntry, confirm bool) (*KnownFreeListChange, error) {
	if actor.ID <= 0 {
		return nil, infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	if !actor.Interactive {
		return nil, infraerrors.Forbidden(ReasonPriceWriteInteractive, "the known-free list can only be changed in an interactive administrator session")
	}
	if !confirm {
		return nil, infraerrors.BadRequest(ReasonPriceWriteConfirm, "the write must be confirmed a second time")
	}
	norm, err := NormalizeKnownFreeList(entries)
	if err != nil {
		return nil, err
	}
	targetRaw, err := marshalKnownFreeList(norm)
	if err != nil {
		return nil, err
	}
	var change *KnownFreeListChange
	err = s.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		raw, err := s.list.GetKnownFreeListTx(ctx, tx)
		if err != nil {
			return err
		}
		c, err := s.diff(ctx, tx, raw, norm, true)
		if err != nil {
			return err
		}
		if len(c.NewViolations) > 0 {
			return exposureError(c.NewViolations)
		}
		if c.Changed {
			if err := s.list.SetKnownFreeListTx(ctx, tx, targetRaw); err != nil {
				return err
			}
		}
		change = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}
