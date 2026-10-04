//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// W6 PR2 的矩阵表、模型目录与种子命令的集成测试（真实 PostgreSQL，迁移 200 至 202 已执行）。
// 这些表没有外键，夹具直接按分组 id 写入，结束时手工清理。

func requireMatrixPGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "expected pq error, got %v", err)
	require.Equal(t, code, string(pqErr.Code))
}

// mxIntGroup 建一个 openai 分组，结束时清理它在三张矩阵表里的行。
func mxIntGroup(t *testing.T) int64 {
	t.Helper()
	// 同一个测试里会建多个分组，名字用 uuid 区分（uniqueTestValue 只按测试名区分）。
	g := mustCreateGroup(t, testEntClient(t), &service.Group{Name: "mxg-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	gid := g.ID
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_group_prices WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_group_price_history WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM group_model_config WHERE group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM cost_accounting_rules WHERE scope_group_id = $1`, gid)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, gid)
	})
	return gid
}

func mxF(v float64) *float64 { return &v }

// ---------------------------------------------------------------------------
// 表约束
// ---------------------------------------------------------------------------

func TestMatrixTables_GroupModelConfigConstraints(t *testing.T) {
	ctx := context.Background()
	gid := mxIntGroup(t)
	exec := func(q string, args ...any) error {
		_, err := integrationDB.ExecContext(ctx, q, args...)
		return err
	}

	require.NoError(t, exec(`INSERT INTO group_model_config (group_id) VALUES ($1)`, gid))
	requireMatrixPGCode(t, exec(`INSERT INTO group_model_config (group_id) VALUES ($1)`, gid), "23505")

	var (
		access, cost, stage string
		bms                 sql.NullString
		mapping, features   string
		revision            int64
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT access_mode, billing_model_source, model_mapping::text, features::text, cost_mode, pricing_stage, revision
		 FROM group_model_config WHERE group_id = $1`, gid).Scan(&access, &bms, &mapping, &features, &cost, &stage, &revision))
	require.Equal(t, "open", access)
	require.False(t, bms.Valid, "billing_model_source 默认 NULL（无渠道）")
	require.Equal(t, "[]", mapping)
	require.Equal(t, "{}", features)
	require.Equal(t, "account_rate", cost)
	require.Equal(t, "legacy", stage)
	require.Equal(t, int64(1), revision)

	requireMatrixPGCode(t, exec(`UPDATE group_model_config SET access_mode = 'x' WHERE group_id = $1`, gid), "23514")
	requireMatrixPGCode(t, exec(`UPDATE group_model_config SET pricing_stage = 'x' WHERE group_id = $1`, gid), "23514")
	requireMatrixPGCode(t, exec(`UPDATE group_model_config SET cost_mode = 'x' WHERE group_id = $1`, gid), "23514")
	requireMatrixPGCode(t, exec(`UPDATE group_model_config SET billing_model_source = 'x' WHERE group_id = $1`, gid), "23514")
	require.NoError(t, exec(`UPDATE group_model_config SET billing_model_source = 'requested' WHERE group_id = $1`, gid))
	require.NoError(t, exec(`UPDATE group_model_config SET billing_model_source = NULL, pricing_stage = 'v2' WHERE group_id = $1`, gid))
}

func TestMatrixTables_ModelGroupPricesConstraints(t *testing.T) {
	ctx := context.Background()
	gid := mxIntGroup(t)
	insert := func(key string, pattern bool, mode string, extra any, custom any, source string) error {
		_, err := integrationDB.ExecContext(ctx,
			`INSERT INTO model_group_prices (group_id, model_key, is_pattern, price_mode, extra_multiplier, custom_price, source)
			 VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)`, gid, key, pattern, mode, extra, custom, source)
		return err
	}

	require.NoError(t, insert("m1", false, "inherit", nil, nil, "manual"))
	requireMatrixPGCode(t, insert("m1", false, "inherit", nil, nil, "manual"), "23505")
	require.NoError(t, insert("m1", true, "inherit", nil, nil, "manual"), "同一个名字的精确单元格与通配符单元格是两个键")

	requireMatrixPGCode(t, insert("m2", false, "extra", nil, nil, "manual"), "23514")
	requireMatrixPGCode(t, insert("m2", false, "extra", 0, nil, "manual"), "23514")
	require.NoError(t, insert("m2", false, "extra", 1.5, nil, "manual"))
	requireMatrixPGCode(t, insert("m3", false, "custom", nil, nil, "manual"), "23514")
	require.NoError(t, insert("m3", false, "custom", nil, `{"billing_mode":"token"}`, "manual"))
	requireMatrixPGCode(t, insert("m4", false, "inherit", nil, `{"billing_mode":"token"}`, "manual"), "23514")
	requireMatrixPGCode(t, insert("m4", false, "inherit", 2.0, nil, "manual"), "23514")
	requireMatrixPGCode(t, insert("m4", false, "weird", nil, nil, "manual"), "23514")
	requireMatrixPGCode(t, insert("m4", false, "inherit", nil, nil, "other"), "23514")
	for _, src := range []string{"manual", "copied", "legacy_derived", "legacy_frozen"} {
		require.NoError(t, insert("src-"+src, false, "inherit", nil, nil, src))
	}

	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO model_group_prices (group_id, model_key, effective_from, effective_to) VALUES ($1, 'window', '2026-10-02', '2026-10-01')`, gid)
	requireMatrixPGCode(t, err, "23514")
}

func TestMatrixTables_CostAccountingRulesConstraintsAndCascade(t *testing.T) {
	ctx := context.Background()
	gid := mxIntGroup(t)
	insertRule := func(source string, channel, ordinal any) (int64, error) {
		var id int64
		err := integrationDB.QueryRowContext(ctx,
			`INSERT INTO cost_accounting_rules (name, scope_group_id, source, source_channel_id, source_ordinal)
			 VALUES ('r', $1, $2, $3, $4) RETURNING id`, gid, source, channel, ordinal).Scan(&id)
		return id, err
	}

	manual, err := insertRule("manual", nil, nil)
	require.NoError(t, err)
	_, err = insertRule("legacy_derived", nil, nil)
	requireMatrixPGCode(t, err, "23514")
	_, err = insertRule("legacy_derived", int64(5), nil)
	requireMatrixPGCode(t, err, "23514")
	_, err = insertRule("legacy_frozen", nil, 1)
	requireMatrixPGCode(t, err, "23514")
	_, err = insertRule("other", int64(5), 1)
	requireMatrixPGCode(t, err, "23514")
	derived, err := insertRule("legacy_derived", int64(5), 1)
	require.NoError(t, err)

	for _, rid := range []int64{manual, derived} {
		_, err = integrationDB.ExecContext(ctx,
			`INSERT INTO cost_accounting_rule_prices (rule_id, platform, models, price) VALUES ($1, '', '["a"]', '{"billing_mode":"token"}')`, rid)
		require.NoError(t, err)
	}
	_, err = integrationDB.ExecContext(ctx,
		`INSERT INTO cost_accounting_rule_prices (rule_id, platform, models, price) VALUES ($1, '', '[]', '{}')`, int64(987654321))
	requireMatrixPGCode(t, err, "23503")

	_, err = integrationDB.ExecContext(ctx, `DELETE FROM cost_accounting_rules WHERE id = $1`, derived)
	require.NoError(t, err)
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cost_accounting_rule_prices WHERE rule_id = $1`, derived).Scan(&n))
	require.Zero(t, n, "价格行随规则行级联删除")
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cost_accounting_rule_prices WHERE rule_id = $1`, manual).Scan(&n))
	require.Equal(t, 1, n)
}

func TestMatrixTables_ModelCatalogConstraints(t *testing.T) {
	ctx := context.Background()
	prefix := "mxtest-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_catalog WHERE model_key LIKE $1`, prefix+"%")
	})
	insert := func(platform, key, status string) error {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO model_catalog (model_key, platform, status) VALUES ($1, $2, $3)`, key, platform, status)
		return err
	}
	require.NoError(t, insert("openai", prefix+"-a", "draft"))
	requireMatrixPGCode(t, insert("openai", prefix+"-a", "active"), "23505")
	require.NoError(t, insert("gemini", prefix+"-a", "active"), "同名模型可以在不同平台各登记一次")
	requireMatrixPGCode(t, insert("openai", prefix+"-b", "weird"), "23514")
}

// ---------------------------------------------------------------------------
// 仓储：读取、落库、幂等、阶段
// ---------------------------------------------------------------------------

func TestPricingMatrixRepo_GetGroupMetaIntegration(t *testing.T) {
	ctx := context.Background()
	repo := NewPricingMatrixRepository(integrationDB)
	live := mxIntGroup(t)
	gone := mxIntGroup(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at = NOW() WHERE id = $1`, gone)
	require.NoError(t, err)

	got, err := repo.GetGroupMeta(ctx, []int64{live, gone, 987654321})
	require.NoError(t, err)
	require.Equal(t, map[int64]service.DeriveGroup{
		live: {ID: live, Platform: service.PlatformOpenAI},
		gone: {ID: gone, Platform: service.PlatformOpenAI, Deleted: true},
	}, got)
}

func mxRichChannel(gid, channelID int64, price float64) *service.Channel {
	maxTokens := 200000
	ch := &service.Channel{
		ID: channelID, Name: "mx-rich", Status: service.StatusActive,
		BillingModelSource: service.BillingModelSourceRequested, RestrictModels: true, ApplyPricingToAccountStats: true,
		GroupIDs: []int64{gid},
		ModelMapping: map[string]map[string]string{service.PlatformOpenAI: {
			"alias-a": "gpt-5.5", "Gpt-4*": "gpt-5.4", "gpt-4.1*": "",
		}},
		FeaturesConfig: map[string]any{
			"web_search_emulation":          map[string]any{"openai": true},
			"bedrock_cc_compat":             false,
			"codex_image_generation_bridge": true,
		},
	}
	ch.ModelPricing = []service.ChannelModelPricing{
		{Platform: service.PlatformOpenAI, Models: []string{"gpt-5.5", "gpt-5.6*"}, BillingMode: service.BillingModeToken,
			InputPrice: mxF(price), OutputPrice: mxF(price * 4), CacheReadPrice: mxF(0),
			Intervals: []service.PricingInterval{
				{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: mxF(price), SortOrder: 0},
				{MinTokens: maxTokens, InputPrice: mxF(price * 2), OutputPrice: mxF(price * 8), SortOrder: 1},
			}},
		{Platform: service.PlatformOpenAI, Models: []string{"gpt-5.4-mini"}, BillingMode: service.BillingModeToken},
		{Platform: service.PlatformOpenAI, Models: []string{"gpt-image-2"}, BillingMode: service.BillingModeImage,
			Intervals: []service.PricingInterval{{TierLabel: "1K", PerRequestPrice: mxF(price * 1000), SortOrder: 0}}},
	}
	ch.AccountStatsPricingRules = []service.AccountStatsPricingRule{
		{ID: 1, ChannelID: channelID, Name: "r-second", GroupIDs: []int64{gid, 99999}, AccountIDs: []int64{11, 12}, SortOrder: 2,
			Pricing: []service.ChannelModelPricing{
				{Platform: service.PlatformOpenAI, Models: []string{"gpt-5.5"}, BillingMode: service.BillingModeToken, InputPrice: mxF(price)},
				{Platform: "", Models: []string{"img-*"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: mxF(0.5)},
			}},
		{ID: 2, ChannelID: channelID, Name: "r-first", SortOrder: 1},
	}
	return ch
}

func mxApplyDerived(t *testing.T, repo service.PricingMatrixRepository, gid int64, derived service.DerivedGroupState) {
	t.Helper()
	err := repo.ApplyPlans(context.Background(), []int64{gid}, func(snaps map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
		return []service.GroupApplyPlan{service.PlanGroupApply(derived, snaps[gid])}, nil
	})
	require.NoError(t, err)
}

func mxLoad(t *testing.T, repo service.PricingMatrixRepository, gid int64) service.GroupStateSnapshot {
	t.Helper()
	snaps, err := repo.LoadGroupSnapshots(context.Background(), []int64{gid})
	require.NoError(t, err)
	return snaps[gid]
}

func TestPricingMatrixRepo_ApplyThenLoadIsInSyncAndIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := NewPricingMatrixRepository(integrationDB)
	gid := mxIntGroup(t)
	channelID := gid + 7_000_000
	group := service.DeriveGroup{ID: gid, Platform: service.PlatformOpenAI}

	// 手工单元格与手工成本核算规则：派生钩子不能碰。
	_, err := integrationDB.ExecContext(ctx,
		`INSERT INTO group_model_config (group_id, pricing_stage) VALUES ($1, 'shadow')`, gid)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx,
		`INSERT INTO model_group_prices (group_id, model_key, price_mode, extra_multiplier, source) VALUES ($1, 'manual-model', 'extra', 1.2, 'manual')`, gid)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx,
		`INSERT INTO cost_accounting_rules (name, scope_group_id, source) VALUES ('manual-rule', $1, 'manual')`, gid)
	require.NoError(t, err)

	derived := service.DeriveGroupState(mxRichChannel(gid, channelID, 1e-6), group, nil)
	require.NotEmpty(t, derived.Cells)
	require.Len(t, derived.CostRules, 2)
	mxApplyDerived(t, repo, gid, derived)

	snap := mxLoad(t, repo, gid)
	require.NotNil(t, snap.Config)
	require.Equal(t, service.PricingStageShadow, snap.Config.PricingStage, "钩子不改阶段")
	require.Equal(t, int64(2), snap.Config.Revision, "内容变化时 revision 加一")
	require.Equal(t, derived.Config, snap.Config.MatrixGroupConfig, "配置往返一致（映射、功能开关、可空的 billing_model_source）")

	var derivedCells, manualCells int
	for _, c := range snap.Cells {
		if c.Source == service.MatrixSourceLegacyDerived {
			derivedCells++
		} else {
			manualCells++
			require.Equal(t, "manual-model", c.ModelKey)
		}
	}
	require.Equal(t, len(derived.Cells), derivedCells)
	require.Equal(t, 1, manualCells)

	var derivedRules, manualRules int
	for _, r := range snap.Rules {
		if r.Source == service.MatrixSourceLegacyDerived {
			derivedRules++
		} else {
			manualRules++
		}
	}
	require.Equal(t, 2, derivedRules)
	require.Equal(t, 1, manualRules)
	require.True(t, service.PlanGroupApply(derived, snap).Empty(), "落库再读回来，再规划一次必须什么都不做")

	// 幂等：再执行一次，所有行（含时间戳与 revision）保持不变。
	mxApplyDerived(t, repo, gid, derived)
	require.Equal(t, snap, mxLoad(t, repo, gid))

	// 渠道变了：只改变化的行。
	changed := mxRichChannel(gid, channelID, 3e-6)
	changed.ModelPricing = changed.ModelPricing[:1]
	changed.ModelMapping = nil
	changed.AccountStatsPricingRules = changed.AccountStatsPricingRules[:1]
	derived2 := service.DeriveGroupState(changed, group, nil)
	mxApplyDerived(t, repo, gid, derived2)

	snap2 := mxLoad(t, repo, gid)
	require.Equal(t, int64(3), snap2.Config.Revision)
	require.Empty(t, snap2.Config.ModelMapping)
	byKey := map[string]service.StoredMatrixCell{}
	for _, c := range snap2.Cells {
		byKey[c.ModelKey] = c
	}
	require.NotContains(t, byKey, "gpt-5.4-mini", "渠道里没有了的派生单元格被删除")
	require.NotContains(t, byKey, "gpt-image-2")
	require.Contains(t, byKey, "manual-model", "手工单元格保留")
	require.Equal(t, int64(2), byKey["gpt-5.5"].Revision, "价格变了：原地更新，revision 加一")
	require.Equal(t, mxF(3e-6), byKey["gpt-5.5"].CustomPrice.InputPrice)

	var derivedRules2 int
	for _, r := range snap2.Rules {
		if r.Source == service.MatrixSourceLegacyDerived {
			derivedRules2++
			require.Equal(t, "r-second", r.Name)
			require.Len(t, r.Prices, 2, "价格行随规则行重建")
		}
	}
	require.Equal(t, 1, derivedRules2)
	var orphanPrices int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cost_accounting_rule_prices p WHERE NOT EXISTS (SELECT 1 FROM cost_accounting_rules r WHERE r.id = p.rule_id)`).Scan(&orphanPrices))
	require.Zero(t, orphanPrices)
	require.True(t, service.PlanGroupApply(derived2, snap2).Empty())

	// 渠道解绑：回到默认状态，派生行清空，手工行与阶段保留。
	derived3 := service.DeriveGroupState(nil, group, nil)
	mxApplyDerived(t, repo, gid, derived3)
	snap3 := mxLoad(t, repo, gid)
	require.Nil(t, snap3.Config.BillingModelSource)
	require.Equal(t, service.PricingStageShadow, snap3.Config.PricingStage)
	require.Len(t, snap3.Cells, 1)
	require.Len(t, snap3.Rules, 1)

	ids, err := repo.ListDerivedRuleGroupIDs(ctx, channelID)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestPricingMatrixRepo_ListDerivedRuleGroupIDsIntegration(t *testing.T) {
	repo := NewPricingMatrixRepository(integrationDB)
	g1, g2 := mxIntGroup(t), mxIntGroup(t)
	channelID := g1 + 8_000_000
	for _, gid := range []int64{g1, g2} {
		derived := service.DeriveGroupState(mxRichChannel(gid, channelID, 1e-6), service.DeriveGroup{ID: gid, Platform: service.PlatformOpenAI}, nil)
		mxApplyDerived(t, repo, gid, derived)
	}
	ids, err := repo.ListDerivedRuleGroupIDs(context.Background(), channelID)
	require.NoError(t, err)
	require.Equal(t, []int64{g1, g2}, ids)
	none, err := repo.ListDerivedRuleGroupIDs(context.Background(), channelID+1)
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestPricingMatrixRepo_V2GroupIsNeverWritten(t *testing.T) {
	ctx := context.Background()
	repo := NewPricingMatrixRepository(integrationDB)
	gid := mxIntGroup(t)
	group := service.DeriveGroup{ID: gid, Platform: service.PlatformOpenAI}

	mxApplyDerived(t, repo, gid, service.DeriveGroupState(mxRichChannel(gid, gid+7_000_000, 1e-6), group, nil))
	_, err := integrationDB.ExecContext(ctx, `UPDATE group_model_config SET pricing_stage = 'v2' WHERE group_id = $1`, gid)
	require.NoError(t, err)
	before := mxLoad(t, repo, gid)
	require.NotEmpty(t, before.Cells)

	// 渠道大改：v2 分组的配置、单元格、成本核算规则一律不动。
	changed := mxRichChannel(gid, gid+7_000_000, 9e-6)
	changed.RestrictModels = false
	changed.ModelPricing = nil
	changed.AccountStatsPricingRules = nil
	mxApplyDerived(t, repo, gid, service.DeriveGroupState(changed, group, nil))
	require.Equal(t, before, mxLoad(t, repo, gid))

	// 分组离开渠道：同样不动。
	mxApplyDerived(t, repo, gid, service.DeriveGroupState(nil, group, nil))
	require.Equal(t, before, mxLoad(t, repo, gid))
}

// 与阶段切换互斥：别的事务持有 group_model_config 的行锁时，钩子等待；
// 锁释放后才读取阶段，所以对方刚切到 v2，钩子就必须跳过这个分组。
func TestPricingMatrixRepo_WaitsForStageSwitchRowLock(t *testing.T) {
	ctx := context.Background()
	repo := NewPricingMatrixRepository(integrationDB)
	gid := mxIntGroup(t)
	group := service.DeriveGroup{ID: gid, Platform: service.PlatformOpenAI}

	mxApplyDerived(t, repo, gid, service.DeriveGroupState(mxRichChannel(gid, gid+7_000_000, 1e-6), group, nil))
	before := mxLoad(t, repo, gid)

	switcher, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = switcher.Rollback() })
	var locked int64
	require.NoError(t, switcher.QueryRowContext(ctx, `SELECT group_id FROM group_model_config WHERE group_id = $1 FOR UPDATE`, gid).Scan(&locked))

	changed := mxRichChannel(gid, gid+7_000_000, 5e-6)
	done := make(chan error, 1)
	go func() {
		done <- repo.ApplyPlans(ctx, []int64{gid}, func(snaps map[int64]service.GroupStateSnapshot) ([]service.GroupApplyPlan, error) {
			return []service.GroupApplyPlan{service.PlanGroupApply(service.DeriveGroupState(changed, group, nil), snaps[gid])}, nil
		})
	}()

	select {
	case err := <-done:
		t.Fatalf("钩子没有等待阶段切换持有的行锁：%v", err)
	case <-time.After(400 * time.Millisecond):
	}

	_, err = switcher.ExecContext(ctx, `UPDATE group_model_config SET pricing_stage = 'v2' WHERE group_id = $1`, gid)
	require.NoError(t, err)
	require.NoError(t, switcher.Commit())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("钩子在锁释放后没有结束")
	}

	after := mxLoad(t, repo, gid)
	require.Equal(t, service.PricingStageV2, after.Config.PricingStage)
	require.Equal(t, before.Cells, after.Cells, "切换先拿到锁：钩子看到 v2，整体跳过")
	require.Equal(t, before.Rules, after.Rules)
	require.Equal(t, before.Config.MatrixGroupConfig, after.Config.MatrixGroupConfig)
}

// ---------------------------------------------------------------------------
// 端到端：真实的渠道服务 + 钩子 + 矩阵仓储
// ---------------------------------------------------------------------------

func TestPricingDerivationHook_EndToEndWithChannelService(t *testing.T) {
	ctx := context.Background()
	gid := mxIntGroup(t)
	channelRepo := NewChannelRepository(integrationDB)
	groupRepo := NewGroupRepository(testEntClient(t), integrationDB)
	matrix := NewPricingMatrixRepository(integrationDB)
	derive := service.NewPricingDerivationService(matrix, channelRepo, nil)
	svc := service.NewChannelService(channelRepo, groupRepo, nil, nil, nil)
	svc.SetSaveHook(derive)

	priced := func(price float64) []service.ChannelModelPricing {
		return []service.ChannelModelPricing{{Platform: service.PlatformOpenAI, Models: []string{"gpt-5.5"}, BillingMode: service.BillingModeToken, InputPrice: mxF(price)}}
	}
	ch, err := svc.Create(ctx, &service.CreateChannelInput{
		Name: "mx-hook-" + uuid.NewString(), GroupIDs: []int64{gid}, ModelPricing: priced(1e-6),
		AccountStatsPricingRules: []service.AccountStatsPricingRule{{Name: "r", SortOrder: 1, GroupIDs: []int64{gid}, AccountIDs: []int64{}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, ch.ID) })

	snap := mxLoad(t, matrix, gid)
	require.NotNil(t, snap.Config, "创建渠道之后钩子写入了该分组的配置")
	require.Equal(t, service.PricingStageLegacy, snap.Config.PricingStage)
	require.Len(t, snap.Cells, 1)
	require.Equal(t, "gpt-5.5", snap.Cells[0].ModelKey)
	require.Len(t, snap.Rules, 1)
	require.Equal(t, ch.ID, snap.Rules[0].SourceChannelID)

	// 更新定价：钩子原地更新单元格。
	pricing := priced(2e-6)
	_, err = svc.Update(ctx, ch.ID, &service.UpdateChannelInput{ModelPricing: &pricing})
	require.NoError(t, err)
	snap = mxLoad(t, matrix, gid)
	require.Equal(t, mxF(2e-6), snap.Cells[0].CustomPrice.InputPrice)

	// 分组切到 v2 之后，渠道保存照常成功，矩阵一个字节都不动。
	_, err = integrationDB.ExecContext(ctx, `UPDATE group_model_config SET pricing_stage = 'v2' WHERE group_id = $1`, gid)
	require.NoError(t, err)
	frozen := mxLoad(t, matrix, gid)
	pricing = priced(3e-6)
	updated, err := svc.Update(ctx, ch.ID, &service.UpdateChannelInput{ModelPricing: &pricing})
	require.NoError(t, err)
	require.Equal(t, mxF(3e-6), updated.ModelPricing[0].InputPrice, "渠道本身照常保存")
	require.Equal(t, frozen, mxLoad(t, matrix, gid))
	_, err = integrationDB.ExecContext(ctx, `UPDATE group_model_config SET pricing_stage = 'legacy' WHERE group_id = $1`, gid)
	require.NoError(t, err)

	// 分组移出渠道：回到默认状态，派生行清空。
	empty := []int64{}
	_, err = svc.Update(ctx, ch.ID, &service.UpdateChannelInput{GroupIDs: &empty})
	require.NoError(t, err)
	snap = mxLoad(t, matrix, gid)
	require.Nil(t, snap.Config.BillingModelSource)
	require.Empty(t, snap.Cells)
	require.Empty(t, snap.Rules)

	// 再放回去，然后删除渠道：同样清空。
	back := []int64{gid}
	_, err = svc.Update(ctx, ch.ID, &service.UpdateChannelInput{GroupIDs: &back})
	require.NoError(t, err)
	require.Len(t, mxLoad(t, matrix, gid).Cells, 1)
	require.NoError(t, svc.Delete(ctx, ch.ID))
	snap = mxLoad(t, matrix, gid)
	require.Empty(t, snap.Cells)
	require.Empty(t, snap.Rules)

	require.Zero(t, derive.Stats().Failures)
	require.Equal(t, int64(6), derive.Stats().Runs)
}

// 钩子彻底失败（连数据库都连不上）也不影响渠道保存。
func TestPricingDerivationHook_FailureDoesNotAffectChannelSave(t *testing.T) {
	ctx := context.Background()
	gid := mxIntGroup(t)
	channelRepo := NewChannelRepository(integrationDB)
	groupRepo := NewGroupRepository(testEntClient(t), integrationDB)

	broken, err := sql.Open("postgres", integrationPostgresDSN)
	require.NoError(t, err)
	require.NoError(t, broken.Close())

	derive := service.NewPricingDerivationService(NewPricingMatrixRepository(broken), channelRepo, nil)
	svc := service.NewChannelService(channelRepo, groupRepo, nil, nil, nil)
	svc.SetSaveHook(derive)

	ch, err := svc.Create(ctx, &service.CreateChannelInput{Name: "mx-broken-" + uuid.NewString(), GroupIDs: []int64{gid}})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, ch.ID) })

	desc := "changed"
	_, err = svc.Update(ctx, ch.ID, &service.UpdateChannelInput{Description: &desc})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, ch.ID))
	require.Equal(t, int64(3), derive.Stats().Failures)

	_, err = channelRepo.GetByID(ctx, ch.ID)
	require.ErrorIs(t, err, service.ErrChannelNotFound, "渠道确实已经删除")
}

// ---------------------------------------------------------------------------
// 模型目录与种子命令
// ---------------------------------------------------------------------------

func TestModelCatalogRepoAndService_Integration(t *testing.T) {
	ctx := context.Background()
	prefix := "mxtest-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_catalog WHERE model_key LIKE $1`, prefix+"%")
	})
	svc := service.NewModelCatalogService(NewModelCatalogRepository(integrationDB))

	by := int64(1)
	ref := prefix + "-ref"
	a, err := svc.Create(ctx, service.CreateModelCatalogInput{
		ModelKey: strings.ToUpper(prefix) + "-A", Platform: "openai", DisplayName: "A", Aliases: []string{prefix + "-alias-b", prefix + "-alias-a"},
		ReferenceModel: &ref, Status: service.ModelCatalogActive, Note: "n", CreatedBy: &by,
	})
	require.NoError(t, err)
	require.Equal(t, prefix+"-a", a.ModelKey, "model_key 规范化为小写")
	require.NotZero(t, a.ID)

	_, err = svc.Create(ctx, service.CreateModelCatalogInput{ModelKey: prefix + "-a", Platform: "openai"})
	require.ErrorIs(t, err, service.ErrModelCatalogExists)
	_, err = svc.Create(ctx, service.CreateModelCatalogInput{ModelKey: prefix + "-x", Platform: "openai", Aliases: []string{prefix + "-alias-a"}})
	require.ErrorIs(t, err, service.ErrModelCatalogAliasConflict)
	other, err := svc.Create(ctx, service.CreateModelCatalogInput{ModelKey: prefix + "-a", Platform: "gemini", Aliases: []string{prefix + "-alias-a"}})
	require.NoError(t, err, "同名与同别名在另一个平台可以再登记")
	require.Equal(t, service.ModelCatalogDraft, other.Status)

	list, err := svc.List(ctx, service.ModelCatalogFilter{Platform: "openai", Status: service.ModelCatalogActive})
	require.NoError(t, err)
	var mine []service.ModelCatalogEntry
	for _, e := range list {
		if strings.HasPrefix(e.ModelKey, prefix) {
			mine = append(mine, e)
		}
	}
	require.Len(t, mine, 1)
	require.Equal(t, []string{prefix + "-alias-a", prefix + "-alias-b"}, mine[0].Aliases)
	require.Equal(t, ref, *mine[0].ReferenceModel)
	require.Equal(t, by, *mine[0].CreatedBy)

	resolved, err := svc.Resolve(ctx, "openai", strings.ToUpper(prefix)+"-ALIAS-B")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, a.ID, resolved.ID)
}

func mxCatalogCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM model_catalog`).Scan(&n))
	return n
}

func TestRunModelCatalogSeed_Integration(t *testing.T) {
	ctx := context.Background()
	seedModel := "mx-seed-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	channelRepo := NewChannelRepository(integrationDB)
	ch := &service.Channel{
		Name: "mx-seed-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceChannelMapped,
		ModelPricing: []service.ChannelModelPricing{
			{Platform: service.PlatformOpenAI, Models: []string{seedModel, seedModel + "-wild*"}, BillingMode: service.BillingModeToken, InputPrice: mxF(1e-6)},
		},
	}
	require.NoError(t, channelRepo.Create(ctx, ch))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, ch.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_catalog WHERE note LIKE 'seed:%'`)
	})

	before := mxCatalogCount(t)
	var out strings.Builder
	require.NoError(t, RunModelCatalogSeed(ctx, integrationDB, ModelCatalogSeedOptions{UsageDays: 1}, &out))
	text := out.String()
	require.Contains(t, text, "mode=dry-run")
	require.Contains(t, text, "phase=read_catalog elapsed=")
	require.Contains(t, text, "phase=read_channel_pricing elapsed=")
	require.Contains(t, text, "phase=read_usage_logs elapsed=", "usage_logs 的查询在真实库里可以执行")
	require.Contains(t, text, "+ openai/"+seedModel+" sources=[channel_pricing]")
	require.NotContains(t, text, seedModel+"-wild", "通配符不登记")
	require.Contains(t, text, "nothing was written")
	require.Equal(t, before, mxCatalogCount(t), "dry-run 不写任何东西")

	out.Reset()
	require.NoError(t, RunModelCatalogSeed(ctx, integrationDB, ModelCatalogSeedOptions{Apply: true}, &out))
	require.Contains(t, out.String(), "phase=insert elapsed=")
	var status, note string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT status, note FROM model_catalog WHERE platform = 'openai' AND model_key = $1`, seedModel).Scan(&status, &note))
	require.Equal(t, "active", status, "种子登记的一律是 active")
	require.Equal(t, "seed: channel_pricing", note)
	require.Greater(t, mxCatalogCount(t), before)

	// 幂等：再跑一遍什么都不登记。
	after := mxCatalogCount(t)
	out.Reset()
	require.NoError(t, RunModelCatalogSeed(ctx, integrationDB, ModelCatalogSeedOptions{Apply: true}, &out))
	require.Contains(t, out.String(), "plan: insert=0")
	require.Contains(t, out.String(), "inserted=0")
	require.Equal(t, after, mxCatalogCount(t))
}
