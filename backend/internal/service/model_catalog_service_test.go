//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type mxCatalogRepo struct {
	entries    []ModelCatalogEntry
	created    []*ModelCatalogEntry
	listErr    error
	createErr  error
	lastFilter ModelCatalogFilter
	listCalls  int
}

func (r *mxCatalogRepo) List(_ context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error) {
	r.listCalls++
	r.lastFilter = filter
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []ModelCatalogEntry
	for _, e := range r.entries {
		if filter.Platform != "" && e.Platform != filter.Platform {
			continue
		}
		if filter.Status != "" && e.Status != filter.Status {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (r *mxCatalogRepo) Create(_ context.Context, entry *ModelCatalogEntry) error {
	if r.createErr != nil {
		return r.createErr
	}
	entry.ID = int64(len(r.entries) + 1)
	r.created = append(r.created, entry)
	r.entries = append(r.entries, *entry)
	return nil
}

func mxCatalogEntry(platform, key string, aliases ...string) ModelCatalogEntry {
	return ModelCatalogEntry{Platform: platform, ModelKey: key, Aliases: aliases, Status: ModelCatalogActive}
}

func TestNormalizeCatalogModelKey(t *testing.T) {
	require.Equal(t, "gpt-5.6-luna", NormalizeCatalogModelKey("  GPT-5.6-Luna "))
	require.Equal(t, "claude-opus-4-5", NormalizeCatalogModelKey("Claude-Opus-4.5"), "claude 的点号统一成连字符")
	require.Equal(t, "gemini-2.5-pro", NormalizeCatalogModelKey("Gemini-2.5-Pro"), "只有 claude-* 做点号归一化")
	require.Equal(t, NormalizeCatalogModelKey("Claude-Opus-4.5"), normalizeChannelPricingModelName("Claude-Opus-4.5"), "与渠道定价缓存键同一条规则")
	require.Empty(t, NormalizeCatalogModelKey("   "))
}

func TestModelCatalogService_CreateNormalizesAndStores(t *testing.T) {
	repo := &mxCatalogRepo{}
	svc := NewModelCatalogService(repo)
	by := int64(7)
	ref := " GPT-5.5 "

	entry, err := svc.Create(context.Background(), CreateModelCatalogInput{
		ModelKey:       " GPT-5.6-Luna ",
		Platform:       " openai ",
		DisplayName:    "  GPT 5.6 Luna ",
		Aliases:        []string{"Luna-B", "luna-a"},
		ReferenceModel: &ref,
		Note:           "n",
		CreatedBy:      &by,
	})
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-luna", entry.ModelKey)
	require.Equal(t, "openai", entry.Platform)
	require.Equal(t, "GPT 5.6 Luna", entry.DisplayName)
	require.Equal(t, []string{"luna-a", "luna-b"}, entry.Aliases, "别名规范化并排序")
	require.Equal(t, "gpt-5.5", *entry.ReferenceModel)
	require.Equal(t, ModelCatalogDraft, entry.Status, "新建的模型默认 draft")
	require.Equal(t, &by, entry.CreatedBy)
	require.Len(t, repo.created, 1)
	require.Equal(t, int64(1), entry.ID)
}

func TestModelCatalogService_CreateDefaultsAndReferenceHandling(t *testing.T) {
	svc := NewModelCatalogService(&mxCatalogRepo{})
	blank := "   "
	entry, err := svc.Create(context.Background(), CreateModelCatalogInput{ModelKey: "m", Platform: "openai", Status: ModelCatalogActive, ReferenceModel: &blank})
	require.NoError(t, err)
	require.Equal(t, ModelCatalogActive, entry.Status)
	require.Nil(t, entry.ReferenceModel, "空的参考模型视为没有")
	require.NotNil(t, entry.Aliases)
}

func TestModelCatalogService_CreateValidation(t *testing.T) {
	long := strings.Repeat("a", catalogModelKeyMaxLen+1)
	cases := []struct {
		name  string
		in    CreateModelCatalogInput
		param string
	}{
		{"model_key 为空", CreateModelCatalogInput{ModelKey: "  ", Platform: "openai"}, "model_key"},
		{"model_key 带通配符", CreateModelCatalogInput{ModelKey: "gpt-5*", Platform: "openai"}, "model_key"},
		{"model_key 过长", CreateModelCatalogInput{ModelKey: long, Platform: "openai"}, "model_key"},
		{"platform 为空", CreateModelCatalogInput{ModelKey: "m", Platform: " "}, "platform"},
		{"platform 过长", CreateModelCatalogInput{ModelKey: "m", Platform: strings.Repeat("p", catalogPlatformMaxLen+1)}, "platform"},
		{"status 非法", CreateModelCatalogInput{ModelKey: "m", Platform: "openai", Status: "weird"}, "status"},
		{"别名为空", CreateModelCatalogInput{ModelKey: "m", Platform: "openai", Aliases: []string{" "}}, "aliases"},
		{"别名过长", CreateModelCatalogInput{ModelKey: "m", Platform: "openai", Aliases: []string{long}}, "aliases"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &mxCatalogRepo{}
			_, err := NewModelCatalogService(repo).Create(context.Background(), c.in)
			require.ErrorIs(t, err, ErrModelCatalogInvalid)
			require.Empty(t, repo.created)
			require.Zero(t, repo.listCalls, "输入校验先于任何数据库读取")
			require.Equal(t, c.param, infraerrors.FromError(err).Metadata["param"])
		})
	}
}

func TestModelCatalogService_CreateAliasAndKeyConflicts(t *testing.T) {
	existing := []ModelCatalogEntry{
		mxCatalogEntry("openai", "gpt-5.5", "gpt-5-5"),
		mxCatalogEntry("anthropic", "claude-opus-4-5"),
	}
	cases := []struct {
		name string
		in   CreateModelCatalogInput
		want error
	}{
		{"key 与已有 key 相同", CreateModelCatalogInput{ModelKey: "GPT-5.5", Platform: "openai"}, ErrModelCatalogExists},
		{"key 等于已有别名", CreateModelCatalogInput{ModelKey: "gpt-5-5", Platform: "openai"}, ErrModelCatalogAliasConflict},
		{"别名等于已有 key", CreateModelCatalogInput{ModelKey: "new", Platform: "openai", Aliases: []string{"gpt-5.5"}}, ErrModelCatalogAliasConflict},
		{"别名等于已有别名", CreateModelCatalogInput{ModelKey: "new", Platform: "openai", Aliases: []string{"GPT-5-5"}}, ErrModelCatalogAliasConflict},
		{"别名等于自己的 key", CreateModelCatalogInput{ModelKey: "new", Platform: "openai", Aliases: []string{"NEW"}}, ErrModelCatalogAliasConflict},
		{"别名重复", CreateModelCatalogInput{ModelKey: "new", Platform: "openai", Aliases: []string{"x", "X"}}, ErrModelCatalogAliasConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &mxCatalogRepo{entries: append([]ModelCatalogEntry(nil), existing...)}
			_, err := NewModelCatalogService(repo).Create(context.Background(), c.in)
			require.ErrorIs(t, err, c.want)
			require.Empty(t, repo.created)
		})
	}

	t.Run("同名模型在另一个平台可以登记", func(t *testing.T) {
		repo := &mxCatalogRepo{entries: append([]ModelCatalogEntry(nil), existing...)}
		_, err := NewModelCatalogService(repo).Create(context.Background(), CreateModelCatalogInput{ModelKey: "gpt-5.5", Platform: "gemini", Aliases: []string{"gpt-5-5"}})
		require.NoError(t, err)
		require.Equal(t, "gemini", repo.lastFilter.Platform, "冲突只在同平台内检查")
	})
	t.Run("仓库层的唯一冲突原样返回（并发建同一个）", func(t *testing.T) {
		repo := &mxCatalogRepo{createErr: ErrModelCatalogExists}
		_, err := NewModelCatalogService(repo).Create(context.Background(), CreateModelCatalogInput{ModelKey: "m", Platform: "openai"})
		require.ErrorIs(t, err, ErrModelCatalogExists)
	})
	t.Run("读取失败", func(t *testing.T) {
		repo := &mxCatalogRepo{listErr: errors.New("db down")}
		_, err := NewModelCatalogService(repo).Create(context.Background(), CreateModelCatalogInput{ModelKey: "m", Platform: "openai"})
		require.ErrorContains(t, err, "db down")
	})
}

func TestModelCatalogService_List(t *testing.T) {
	repo := &mxCatalogRepo{entries: []ModelCatalogEntry{
		mxCatalogEntry("openai", "a"),
		{Platform: "openai", ModelKey: "b", Status: ModelCatalogRetired},
		mxCatalogEntry("gemini", "c"),
	}}
	svc := NewModelCatalogService(repo)

	all, err := svc.List(context.Background(), ModelCatalogFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)

	got, err := svc.List(context.Background(), ModelCatalogFilter{Platform: "openai", Status: ModelCatalogRetired})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "b", got[0].ModelKey)

	empty, err := svc.List(context.Background(), ModelCatalogFilter{Platform: "none"})
	require.NoError(t, err)
	require.Equal(t, []ModelCatalogEntry{}, empty, "没有数据时返回空数组而不是 null")

	_, err = svc.List(context.Background(), ModelCatalogFilter{Status: "weird"})
	require.ErrorIs(t, err, ErrModelCatalogInvalid)

	repo.listErr = errors.New("db down")
	_, err = svc.List(context.Background(), ModelCatalogFilter{})
	require.ErrorContains(t, err, "db down")
}

func TestModelCatalogService_Resolve(t *testing.T) {
	repo := &mxCatalogRepo{entries: []ModelCatalogEntry{
		mxCatalogEntry("openai", "gpt-5.5", "gpt5"),
		mxCatalogEntry("openai", "gpt5"), // 别名与另一个 key 同名（历史数据）：key 优先
		mxCatalogEntry("gemini", "other"),
	}}
	svc := NewModelCatalogService(repo)

	e, err := svc.Resolve(context.Background(), "openai", " GPT-5.5 ")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", e.ModelKey)

	e, err = svc.Resolve(context.Background(), "openai", "GPT5")
	require.NoError(t, err)
	require.Equal(t, "gpt5", e.ModelKey, "先匹配 model_key，再匹配别名")

	e, err = svc.Resolve(context.Background(), "openai", "other")
	require.NoError(t, err)
	require.Nil(t, e, "按平台查找，不会跨平台命中")

	e, err = svc.Resolve(context.Background(), "openai", "  ")
	require.NoError(t, err)
	require.Nil(t, e)

	repo.listErr = errors.New("db down")
	_, err = svc.Resolve(context.Background(), "openai", "x")
	require.Error(t, err)
}

func TestResolveCatalogEntry_ByAlias(t *testing.T) {
	entries := []ModelCatalogEntry{mxCatalogEntry("openai", "gpt-5.5", "gpt-5-5", "five")}
	require.Equal(t, "gpt-5.5", ResolveCatalogEntry(entries, "FIVE").ModelKey)
	require.Nil(t, ResolveCatalogEntry(entries, "six"))
	require.Nil(t, ResolveCatalogEntry(nil, "five"))
}

func TestModelCatalogStatus_Valid(t *testing.T) {
	for _, s := range []ModelCatalogStatus{ModelCatalogDraft, ModelCatalogActive, ModelCatalogRetired} {
		require.True(t, s.valid(), s)
	}
	for _, s := range []ModelCatalogStatus{"", "weird", "ACTIVE"} {
		require.False(t, s.valid(), s)
	}
}

// ---------------------------------------------------------------------------
// 种子计划（纯函数）
// ---------------------------------------------------------------------------

func TestPlanModelCatalogSeed(t *testing.T) {
	long := strings.Repeat("a", catalogModelKeyMaxLen+1)
	candidates := []ModelCatalogSeedCandidate{
		{Platform: "openai", Model: " GPT-5.5 ", DisplayName: "", Source: SeedSourceUsageLogs},
		{Platform: "openai", Model: "gpt-5.5", DisplayName: " GPT 5.5 ", Source: SeedSourceDefaultModels},
		{Platform: "openai", Model: "gpt-5.5", DisplayName: "ignored later name", Source: SeedSourceChannelPricing},
		{Platform: "anthropic", Model: "Claude-Opus-4.5", DisplayName: "Opus", Source: SeedSourceDefaultModels},
		{Platform: "openai", Model: "already", Source: SeedSourceChannelPricing},
		{Platform: "openai", Model: "ALREADY", Source: SeedSourceUsageLogs},
		{Platform: "openai", Model: "gpt-5*", Source: SeedSourceChannelPricing},
		{Platform: "openai", Model: "  ", Source: SeedSourceChannelPricing},
		{Platform: "openai", Model: long, Source: SeedSourceChannelPricing},
		{Platform: " ", Model: "x", Source: SeedSourceChannelPricing},
		{Platform: strings.Repeat("p", catalogPlatformMaxLen+1), Model: "x", Source: SeedSourceChannelPricing},
	}
	existing := map[string]struct{}{CatalogSeedKey("openai", "already"): {}}

	plan := PlanModelCatalogSeed(candidates, existing)
	require.Equal(t, 5, plan.Invalid)
	require.Equal(t, 1, plan.AlreadyRegistered, "同一个已登记条目被多个候选命中也只算一个")
	require.Equal(t, []ModelCatalogSeedItem{
		{Platform: "anthropic", ModelKey: "claude-opus-4-5", DisplayName: "Opus", Sources: []string{SeedSourceDefaultModels}},
		{Platform: "openai", ModelKey: "gpt-5.5", DisplayName: "GPT 5.5", Sources: []string{SeedSourceChannelPricing, SeedSourceDefaultModels, SeedSourceUsageLogs}},
	}, plan.Insert, "合并来源、取第一个非空显示名、按平台与 model_key 排序")

	// 幂等：登记之后再算一遍，名单为空。
	registered := map[string]struct{}{}
	for _, it := range plan.Insert {
		registered[CatalogSeedKey(it.Platform, it.ModelKey)] = struct{}{}
	}
	for k := range existing {
		registered[k] = struct{}{}
	}
	again := PlanModelCatalogSeed(candidates, registered)
	require.Empty(t, again.Insert)
	require.Equal(t, 3, again.AlreadyRegistered)
}

func TestPlanModelCatalogSeed_EmptyInputGivesEmptyPlan(t *testing.T) {
	plan := PlanModelCatalogSeed(nil, nil)
	require.Equal(t, []ModelCatalogSeedItem{}, plan.Insert)
	require.Zero(t, plan.Invalid+plan.AlreadyRegistered)
}

func TestDefaultModelCatalogSeedCandidates(t *testing.T) {
	candidates := DefaultModelCatalogSeedCandidates()
	require.NotEmpty(t, candidates)
	platforms := map[string]bool{}
	for _, c := range candidates {
		require.Equal(t, SeedSourceDefaultModels, c.Source)
		platforms[c.Platform] = true
	}
	for _, p := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
		require.True(t, platforms[p], p)
	}
	plan := PlanModelCatalogSeed(candidates, nil)
	require.NotEmpty(t, plan.Insert)
	for _, it := range plan.Insert {
		require.NotEmpty(t, it.ModelKey)
		require.Equal(t, NormalizeCatalogModelKey(it.ModelKey), it.ModelKey)
	}
}

func TestModelCatalogSeedItem_SeedEntry(t *testing.T) {
	e := ModelCatalogSeedItem{Platform: "openai", ModelKey: "gpt-5.5", DisplayName: "GPT", Sources: []string{"a", "b"}}.SeedEntry()
	require.Equal(t, ModelCatalogActive, e.Status, "种子登记的一律是 active（未登记视同 active，保持现状）")
	require.Equal(t, "seed: a,b", e.Note)
	require.Equal(t, []string{}, e.Aliases)
	require.Equal(t, "gpt-5.5", e.ModelKey)
}
