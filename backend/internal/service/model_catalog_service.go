package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	geminipkg "github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// 模型目录（设计文档 2.2）。
//
// 本 PR 里目录只有两个使用方：只读管理 API 与种子命令；计费、调度、准入都不读它。
// 语义（给后续 PR 的读取方）：未登记的模型视同 active，只有显式 draft / retired 才挡（S-3、Q2）。

var (
	ErrModelCatalogNotFound      = infraerrors.NotFound("MODEL_CATALOG_NOT_FOUND", "model catalog entry not found")
	ErrModelCatalogExists        = infraerrors.Conflict("MODEL_CATALOG_EXISTS", "model catalog entry already exists")
	ErrModelCatalogInvalid       = infraerrors.BadRequest("MODEL_CATALOG_INVALID", "invalid model catalog entry")
	ErrModelCatalogAliasConflict = infraerrors.Conflict("MODEL_CATALOG_ALIAS_CONFLICT", "alias conflicts with another model or alias")
)

const (
	catalogModelKeyMaxLen = 200
	catalogPlatformMaxLen = 32
)

// ModelCatalogEntry model_catalog 的一行。
type ModelCatalogEntry struct {
	ID             int64              `json:"id"`
	ModelKey       string             `json:"model_key"`
	Platform       string             `json:"platform"`
	DisplayName    string             `json:"display_name"`
	Aliases        []string           `json:"aliases"`
	ReferenceModel *string            `json:"reference_model"`
	Status         ModelCatalogStatus `json:"status"`
	Note           string             `json:"note"`
	CreatedBy      *int64             `json:"created_by"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// ModelCatalogFilter 列表过滤条件；空值表示不过滤。
type ModelCatalogFilter struct {
	Platform string
	Status   ModelCatalogStatus
}

// ModelCatalogRepository 模型目录的数据访问。
type ModelCatalogRepository interface {
	List(ctx context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error)
	// Create 插入一行；(platform, model_key) 已存在时返回 ErrModelCatalogExists。
	Create(ctx context.Context, entry *ModelCatalogEntry) error
}

// CreateModelCatalogInput 新建目录条目的输入。
type CreateModelCatalogInput struct {
	ModelKey       string
	Platform       string
	DisplayName    string
	Aliases        []string
	ReferenceModel *string
	Status         ModelCatalogStatus // 空值按 draft（只有此后新建的模型才默认 draft）
	Note           string
	CreatedBy      *int64
}

// ModelCatalogService 模型目录服务。
type ModelCatalogService struct {
	repo ModelCatalogRepository
}

// NewModelCatalogService 创建模型目录服务。
func NewModelCatalogService(repo ModelCatalogRepository) *ModelCatalogService {
	return &ModelCatalogService{repo: repo}
}

// NormalizeCatalogModelKey 目录里 model_key 的规范写法：与 normalizeChannelPricingModelName 同一条规则
// （去空白、小写，claude-* 的点号统一成连字符）。
func NormalizeCatalogModelKey(model string) string {
	return normalizeChannelPricingModelName(model)
}

// List 列出目录条目（按平台、model_key 排序）。
func (s *ModelCatalogService) List(ctx context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error) {
	if filter.Status != "" && !filter.Status.valid() {
		return nil, ErrModelCatalogInvalid.WithMetadata(map[string]string{"param": "status"})
	}
	entries, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list model catalog: %w", err)
	}
	if entries == nil {
		entries = []ModelCatalogEntry{}
	}
	return entries, nil
}

// Resolve 按 model_key 或别名查找同平台的目录条目；未登记返回 nil。
func (s *ModelCatalogService) Resolve(ctx context.Context, platform, model string) (*ModelCatalogEntry, error) {
	entries, err := s.repo.List(ctx, ModelCatalogFilter{Platform: platform})
	if err != nil {
		return nil, fmt.Errorf("list model catalog: %w", err)
	}
	return ResolveCatalogEntry(entries, model), nil
}

// ResolveCatalogEntry 在条目里按 model_key 或别名查找；先匹配 model_key，再匹配别名。
func ResolveCatalogEntry(entries []ModelCatalogEntry, model string) *ModelCatalogEntry {
	key := NormalizeCatalogModelKey(model)
	if key == "" {
		return nil
	}
	for i := range entries {
		if entries[i].ModelKey == key {
			return &entries[i]
		}
	}
	for i := range entries {
		for _, alias := range entries[i].Aliases {
			if alias == key {
				return &entries[i]
			}
		}
	}
	return nil
}

func (s ModelCatalogStatus) valid() bool {
	switch s {
	case ModelCatalogDraft, ModelCatalogActive, ModelCatalogRetired:
		return true
	}
	return false
}

// Create 新建目录条目。别名唯一性在这里校验：同平台内别名不得等于另一个 model_key 或另一个别名。
func (s *ModelCatalogService) Create(ctx context.Context, in CreateModelCatalogInput) (*ModelCatalogEntry, error) {
	entry, err := buildCatalogEntry(in)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.List(ctx, ModelCatalogFilter{Platform: entry.Platform})
	if err != nil {
		return nil, fmt.Errorf("list model catalog: %w", err)
	}
	if err := checkCatalogConflicts(existing, entry); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// buildCatalogEntry 校验并规范化输入。
func buildCatalogEntry(in CreateModelCatalogInput) (*ModelCatalogEntry, error) {
	invalid := func(param, msg string) error {
		return ErrModelCatalogInvalid.WithMetadata(map[string]string{"param": param, "detail": msg})
	}
	key := NormalizeCatalogModelKey(in.ModelKey)
	if key == "" || len(key) > catalogModelKeyMaxLen || strings.HasSuffix(key, "*") {
		return nil, invalid("model_key", "model_key must be a non-empty exact name of at most 200 characters")
	}
	platform := strings.TrimSpace(in.Platform)
	if platform == "" || len(platform) > catalogPlatformMaxLen {
		return nil, invalid("platform", "platform must be non-empty and at most 32 characters")
	}
	status := in.Status
	if status == "" {
		status = ModelCatalogDraft
	}
	if !status.valid() {
		return nil, invalid("status", "status must be draft, active or retired")
	}

	aliases := make([]string, 0, len(in.Aliases))
	seen := map[string]struct{}{key: {}}
	for _, raw := range in.Aliases {
		alias := NormalizeCatalogModelKey(raw)
		if alias == "" || len(alias) > catalogModelKeyMaxLen {
			return nil, invalid("aliases", "aliases must be non-empty names of at most 200 characters")
		}
		if _, dup := seen[alias]; dup {
			return nil, ErrModelCatalogAliasConflict.WithMetadata(map[string]string{"alias": alias})
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	var reference *string
	if in.ReferenceModel != nil {
		ref := NormalizeCatalogModelKey(*in.ReferenceModel)
		if ref != "" {
			reference = &ref
		}
	}
	return &ModelCatalogEntry{
		ModelKey:       key,
		Platform:       platform,
		DisplayName:    strings.TrimSpace(in.DisplayName),
		Aliases:        aliases,
		ReferenceModel: reference,
		Status:         status,
		Note:           in.Note,
		CreatedBy:      in.CreatedBy,
	}, nil
}

// checkCatalogConflicts 检查新条目与同平台已有条目的冲突。
func checkCatalogConflicts(existing []ModelCatalogEntry, entry *ModelCatalogEntry) error {
	keys := make(map[string]struct{}, len(existing))
	aliases := make(map[string]struct{})
	for _, e := range existing {
		keys[e.ModelKey] = struct{}{}
		for _, a := range e.Aliases {
			aliases[a] = struct{}{}
		}
	}
	if _, ok := keys[entry.ModelKey]; ok {
		return ErrModelCatalogExists
	}
	if _, ok := aliases[entry.ModelKey]; ok {
		return ErrModelCatalogAliasConflict.WithMetadata(map[string]string{"alias": entry.ModelKey})
	}
	for _, a := range entry.Aliases {
		_, isKey := keys[a]
		_, isAlias := aliases[a]
		if isKey || isAlias {
			return ErrModelCatalogAliasConflict.WithMetadata(map[string]string{"alias": a})
		}
	}
	return nil
}

// ---- 种子（设计文档 2.2「种子」，S-3、REVIEW_OPUS_2 S-13）----
//
// 种子不进迁移：迁移在启动时执行，而扫 usage_logs 要读数百万行。由管理员在部署后手动执行
// `model-catalog seed`（默认 dry-run，只打印将登记的名单）。登记的一律是 active。

// 种子候选的来源。
const (
	SeedSourceDefaultModels  = "default_models"
	SeedSourceChannelPricing = "channel_pricing"
	SeedSourceUsageLogs      = "usage_logs"
)

// ModelCatalogSeedCandidate 一个种子候选。
type ModelCatalogSeedCandidate struct {
	Platform    string
	Model       string
	DisplayName string
	Source      string
}

// ModelCatalogSeedItem 合并后的种子条目。
type ModelCatalogSeedItem struct {
	Platform    string   `json:"platform"`
	ModelKey    string   `json:"model_key"`
	DisplayName string   `json:"display_name"`
	Sources     []string `json:"sources"`
}

// ModelCatalogSeedPlan 种子计划。
type ModelCatalogSeedPlan struct {
	// Insert 将登记的条目（按平台、model_key 排序）。
	Insert []ModelCatalogSeedItem `json:"insert"`
	// AlreadyRegistered 候选里已经在目录里的条目数。
	AlreadyRegistered int `json:"already_registered"`
	// Invalid 被丢弃的候选数（名字为空、过长、带通配符、平台为空或过长）。
	Invalid int `json:"invalid"`
}

// CatalogSeedKey 种子计划里「已登记」集合的键。
func CatalogSeedKey(platform, modelKey string) string {
	return platform + "\x00" + modelKey
}

// DefaultModelCatalogSeedCandidates 各平台默认模型列表里的模型。
func DefaultModelCatalogSeedCandidates() []ModelCatalogSeedCandidate {
	var out []ModelCatalogSeedCandidate
	add := func(platform, id, display string) {
		out = append(out, ModelCatalogSeedCandidate{Platform: platform, Model: id, DisplayName: display, Source: SeedSourceDefaultModels})
	}
	for _, m := range claude.DefaultModels {
		add(PlatformAnthropic, m.ID, m.DisplayName)
	}
	for _, m := range openai.DefaultModels {
		add(PlatformOpenAI, m.ID, m.DisplayName)
	}
	for _, m := range geminicli.DefaultModels {
		add(PlatformGemini, m.ID, m.DisplayName)
	}
	for _, m := range geminipkg.DefaultModels() {
		add(PlatformGemini, strings.TrimPrefix(m.Name, "models/"), m.DisplayName)
	}
	for _, m := range xai.DefaultModels() {
		add(PlatformGrok, m.ID, m.DisplayName)
	}
	for _, m := range antigravity.DefaultModels() {
		add(PlatformAntigravity, m.ID, m.DisplayName)
	}
	return out
}

// PlanModelCatalogSeed 把候选合并、规范化、去掉已登记的，得到将登记的名单（纯函数）。
// existing 的键是 CatalogSeedKey(platform, model_key)。
func PlanModelCatalogSeed(candidates []ModelCatalogSeedCandidate, existing map[string]struct{}) ModelCatalogSeedPlan {
	type acc struct {
		display string
		sources map[string]struct{}
	}
	merged := make(map[string]*acc)
	order := make(map[string]ModelCatalogSeedItem)
	plan := ModelCatalogSeedPlan{Insert: []ModelCatalogSeedItem{}}
	registered := make(map[string]struct{})

	for _, c := range candidates {
		platform := strings.TrimSpace(c.Platform)
		key := NormalizeCatalogModelKey(c.Model)
		if platform == "" || len(platform) > catalogPlatformMaxLen ||
			key == "" || len(key) > catalogModelKeyMaxLen || strings.Contains(key, "*") {
			plan.Invalid++
			continue
		}
		mk := CatalogSeedKey(platform, key)
		if _, ok := existing[mk]; ok {
			registered[mk] = struct{}{}
			continue
		}
		a, ok := merged[mk]
		if !ok {
			a = &acc{sources: make(map[string]struct{})}
			merged[mk] = a
			order[mk] = ModelCatalogSeedItem{Platform: platform, ModelKey: key}
		}
		if a.display == "" {
			a.display = strings.TrimSpace(c.DisplayName)
		}
		a.sources[c.Source] = struct{}{}
	}
	plan.AlreadyRegistered = len(registered)

	for mk, a := range merged {
		item := order[mk]
		item.DisplayName = a.display
		for src := range a.sources {
			item.Sources = append(item.Sources, src)
		}
		sort.Strings(item.Sources)
		plan.Insert = append(plan.Insert, item)
	}
	sort.Slice(plan.Insert, func(i, j int) bool {
		if plan.Insert[i].Platform != plan.Insert[j].Platform {
			return plan.Insert[i].Platform < plan.Insert[j].Platform
		}
		return plan.Insert[i].ModelKey < plan.Insert[j].ModelKey
	})
	return plan
}

// SeedEntry 把种子条目转成目录条目：登记的一律是 active，note 记录来源。
func (i ModelCatalogSeedItem) SeedEntry() ModelCatalogEntry {
	return ModelCatalogEntry{
		ModelKey:    i.ModelKey,
		Platform:    i.Platform,
		DisplayName: i.DisplayName,
		Aliases:     []string{},
		Status:      ModelCatalogActive,
		Note:        "seed: " + strings.Join(i.Sources, ","),
	}
}
