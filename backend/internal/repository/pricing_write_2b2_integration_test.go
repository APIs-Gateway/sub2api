//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-2b-2 的集成测试（真实 PostgreSQL）：已知免费名单与白名单分组读取、目录状态与 7 天用量、
// 成本核算规则写入器、受保护设置键。

func pw2Tx(t *testing.T, fn func(ctx context.Context, tx service.MatrixTx) error) error {
	t.Helper()
	return NewPricingWriteStore(integrationDB).WithTx(context.Background(), fn)
}

func TestPricingKnownFreeStore_Integration(t *testing.T) {
	ctx := context.Background()
	store := NewPricingKnownFreeStore()
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key = $1`, service.SettingKeyBillingKnownFreeList)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key = $1`, service.SettingKeyBillingKnownFreeList)
	})

	// 没有这一行：空串；写入是 upsert。
	require.NoError(t, pw2Tx(t, func(ctx context.Context, tx service.MatrixTx) error {
		raw, err := store.GetKnownFreeListTx(ctx, tx)
		require.NoError(t, err)
		require.Empty(t, raw)
		return store.SetKnownFreeListTx(ctx, tx, `[{"group_id":1,"model":"a"}]`)
	}))
	require.NoError(t, pw2Tx(t, func(ctx context.Context, tx service.MatrixTx) error {
		raw, err := store.GetKnownFreeListTx(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, `[{"group_id":1,"model":"a"}]`, raw)
		return store.SetKnownFreeListTx(ctx, tx, `[]`)
	}))
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyBillingKnownFreeList).Scan(&raw))
	require.Equal(t, `[]`, raw)

	// 只读路径不加锁：写事务持有这一行的锁时也能读到（不会阻塞）。
	require.NoError(t, pw2Tx(t, func(ctx context.Context, tx service.MatrixTx) error {
		_, err := store.GetKnownFreeListTx(ctx, tx)
		require.NoError(t, err)
		got, err := store.GetKnownFreeList(ctx, NewPricingWriteStore(integrationDB).Reader())
		require.NoError(t, err)
		require.Equal(t, `[]`, got)
		return nil
	}))

	// 只返回 v2 白名单分组的 open 单元格：v2 开放分组、legacy 白名单分组都不算。
	allow := pwiV2Group(t)
	pwiSetAccess(t, allow, "allowlist")
	open := pwiV2Group(t)
	legacyAllow := pwiGroup(t, "legacy", 3)
	pwiSetAccess(t, legacyAllow, "allowlist")
	for _, gid := range []int64{allow, open} {
		_, err := pwiApply(NewPricingWriteStore(integrationDB), service.CellWriteRequest{
			Ops:            []service.CellOp{pwiUpsert(gid, "pw2-open", true, service.MatrixPriceInherit, 0), pwiUpsert(gid, "pw2-closed", false, service.MatrixPriceInherit, 0)},
			GroupRevisions: map[int64]int64{gid: 3},
		})
		require.NoError(t, err)
	}
	// 通配符单元格由派生产生：直接写一行 open 的，验证它也被读到（失败关闭要看见它）。
	_, err = integrationDB.ExecContext(ctx,
		`INSERT INTO model_group_prices (group_id, model_key, is_pattern, pattern_order, open, price_mode, source)
		 VALUES ($1, 'pw2-*', TRUE, 1, TRUE, 'inherit', 'legacy_derived')`, allow)
	require.NoError(t, err)

	for _, lock := range []bool{false, true} {
		require.NoError(t, pw2Tx(t, func(ctx context.Context, tx service.MatrixTx) error {
			cells, err := store.AllowlistOpenCellsTx(ctx, tx, lock)
			require.NoError(t, err)
			got := map[string]bool{}
			for _, c := range cells {
				if c.GroupID == open || c.GroupID == legacyAllow {
					t.Errorf("group %d must not be listed", c.GroupID)
				}
				if c.GroupID == allow {
					got[c.Cell.ModelKey] = c.Cell.IsPattern
				}
			}
			require.Equal(t, map[string]bool{"pw2-open": false, "pw2-*": true}, got)
			return nil
		}))
	}
}

func TestSettingRepository_RejectsProtectedKeys_Integration(t *testing.T) {
	ctx := context.Background()
	repo := NewSettingRepository(testEntClient(t))
	for _, key := range []string{
		service.SettingKeyBillingKnownFreeList, "billing.known_free_list", service.SettingKeyBillingUnpricedPolicy, service.SettingKeyPricingDefaultStage,
	} {
		require.Equal(t, service.ReasonSettingKeyProtected, pwiReason(t, repo.Set(ctx, key, "x")), key)
		require.Equal(t, service.ReasonSettingKeyProtected, pwiReason(t, repo.SetMultiple(ctx, map[string]string{"pw2_ok": "1", key: "x"})), key)
		require.Equal(t, service.ReasonSettingKeyProtected, pwiReason(t, repo.Delete(ctx, key)), key)
	}
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = 'pw2_ok'`).Scan(&n))
	require.Zero(t, n, "整批拒绝，普通键也没有写进去")

	// 普通键照常可写可删。
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key = 'pw2_ok'`) })
	require.NoError(t, repo.Set(ctx, "pw2_ok", "1"))
	require.NoError(t, repo.SetMultiple(ctx, map[string]string{"pw2_ok": "2"}))
	require.NoError(t, repo.Delete(ctx, "pw2_ok"))
}

func TestModelCatalogStatusStore_Integration(t *testing.T) {
	ctx := context.Background()
	store := NewModelCatalogStatusStore(integrationDB)
	catalog := NewModelCatalogRepository(integrationDB)

	key := "pw2-" + uuid.NewString()
	alias := key + "-alias"
	ref := "gpt-5"
	entry := &service.ModelCatalogEntry{
		ModelKey: key, Platform: "openai", DisplayName: "d", Aliases: []string{alias}, ReferenceModel: &ref,
		Status: service.ModelCatalogActive, Note: "n",
	}
	require.NoError(t, catalog.Create(ctx, entry))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM model_catalog WHERE id = $1`, entry.ID) })

	got, err := store.GetByID(ctx, entry.ID)
	require.NoError(t, err)
	require.Equal(t, key, got.ModelKey)
	require.Equal(t, []string{alias}, got.Aliases)
	require.Equal(t, "gpt-5", *got.ReferenceModel)
	require.Equal(t, service.ModelCatalogActive, got.Status)
	require.Nil(t, got.CreatedBy)

	_, err = store.GetByID(ctx, -1)
	require.Equal(t, service.ErrModelCatalogNotFound.Reason, pwiReason(t, err))

	// 状态不是 from 时不改。
	ok, err := store.UpdateStatus(ctx, entry.ID, service.ModelCatalogDraft, service.ModelCatalogRetired)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.UpdateStatus(ctx, entry.ID, service.ModelCatalogActive, service.ModelCatalogRetired)
	require.NoError(t, err)
	require.True(t, ok)
	got, err = store.GetByID(ctx, entry.ID)
	require.NoError(t, err)
	require.Equal(t, service.ModelCatalogRetired, got.Status)

	// 7 天用量：按 model 或 requested_model 命中，窗口外的不算。
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "pw2-" + uuid.NewString() + "@example.com"})
	account := mustCreateAccount(t, client, &service.Account{Name: "pw2-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-pw2-" + uuid.NewString(), Name: "pw2"})
	// 用量日志是共享表：测试结束就删，不能留下「今天」的行去影响仪表盘统计类测试。
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id = $1`, user.ID) })
	now := time.Now().UTC().Truncate(time.Second)
	add := func(model, requested string, at time.Time) {
		b := client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(apiKey.ID).SetAccountID(account.ID).
			SetRequestID(uuid.NewString()).SetModel(model).SetCreatedAt(at)
		if requested != "" {
			b.SetRequestedModel(requested)
		}
		_, err := b.Save(ctx)
		require.NoError(t, err)
	}
	add(key, "", now.Add(-time.Hour))
	add("upstream-x", alias, now.Add(-2*time.Hour))
	add(key, "", now.Add(-10*24*time.Hour))
	add("someone-else", "", now.Add(-time.Hour))
	// model 与 requested_model 同时命中的行只算一次；历史日志里大小写不同的名字也算。
	add(key, key, now.Add(-90*time.Minute))
	add(strings.ToUpper(key), "", now.Add(-3*time.Hour))

	usage, err := store.CountModelUsageSince(ctx, []string{strings.ToUpper(key), alias}, now.Add(-service.CatalogUsageWindow))
	require.NoError(t, err)
	require.Equal(t, int64(4), usage.Requests)
	require.NotNil(t, usage.LastUsedAt)
	require.WithinDuration(t, now.Add(-time.Hour), *usage.LastUsedAt, time.Second)

	usage, err = store.CountModelUsageSince(ctx, []string{"pw2-no-such-model"}, now.Add(-service.CatalogUsageWindow))
	require.NoError(t, err)
	require.Zero(t, usage.Requests)
	require.Nil(t, usage.LastUsedAt)
	usage, err = store.CountModelUsageSince(ctx, nil, now)
	require.NoError(t, err)
	require.Zero(t, usage.Requests)
}

func pw2RuleCount(t *testing.T, gid int64) (rules, prices int) {
	t.Helper()
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM cost_accounting_rules WHERE scope_group_id = $1`, gid).Scan(&rules))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM cost_accounting_rule_prices p JOIN cost_accounting_rules r ON r.id = p.rule_id WHERE r.scope_group_id = $1`, gid).Scan(&prices))
	return rules, prices
}

func pw2Spec(name string) service.CostRuleSpec {
	one, two := 1e-6, 2e-6
	return service.CostRuleSpec{
		Name: name, GroupIDs: []int64{1}, AccountIDs: []int64{2}, SortOrder: 3, Enabled: true,
		Prices: []service.MatrixCostRulePrice{{
			Platform: "openai", Models: []string{"pw2-model"}, Price: service.MatrixCustomPrice{InputPrice: &one, OutputPrice: &two},
		}},
	}
}

func TestCostRuleWriter_Integration(t *testing.T) {
	ctx := context.Background()
	inv := &pwiInvalidator{}
	svc := service.NewCostRuleService(NewPricingWriteStore(integrationDB), NewPricingCostRuleWriter(), inv)
	gid := pwiV2Group(t) // revision 3

	// 创建：写规则与价格行，revision 加一，提交后失效缓存。
	created, err := svc.Create(ctx, 7, gid, 3, pw2Spec("first"))
	require.NoError(t, err)
	require.Equal(t, int64(4), created.Revision)
	require.Equal(t, int64(4), pwiConfigRevision(t, gid))
	require.Equal(t, []int64{gid}, inv.groups)
	snap := mxLoad(t, NewPricingMatrixRepository(integrationDB), gid)
	require.Len(t, snap.Rules, 1)
	require.Equal(t, service.MatrixSourceManual, snap.Rules[0].Source)
	require.Equal(t, "first", snap.Rules[0].Name)
	require.Equal(t, []int64{1}, snap.Rules[0].GroupIDs)
	require.Equal(t, 3, snap.Rules[0].SortOrder)
	require.Len(t, snap.Rules[0].Prices, 1)
	require.Equal(t, []string{"pw2-model"}, snap.Rules[0].Prices[0].Models)
	require.Equal(t, service.BillingModeToken, snap.Rules[0].Prices[0].Price.BillingMode)

	// 基线过期：拒绝，什么都不写。
	_, err = svc.Create(ctx, 7, gid, 3, pw2Spec("stale"))
	require.Equal(t, service.ReasonPriceBaselineChanged, pwiReason(t, err))
	rules, _ := pw2RuleCount(t, gid)
	require.Equal(t, 1, rules)

	// 更新：整个替换（价格行也换掉）。
	spec := pw2Spec("renamed")
	spec.Enabled = false
	spec.Prices = append(spec.Prices, service.MatrixCostRulePrice{Models: []string{"pw2-other"}, Price: service.MatrixCustomPrice{BillingMode: service.BillingModePerRequest, PerRequestPrice: pwF2(0.5)}})
	updated, err := svc.Update(ctx, 7, gid, 4, created.RuleID, spec)
	require.NoError(t, err)
	require.Equal(t, int64(5), updated.Revision)
	rules, prices := pw2RuleCount(t, gid)
	require.Equal(t, 1, rules)
	require.Equal(t, 2, prices)
	snap = mxLoad(t, NewPricingMatrixRepository(integrationDB), gid)
	require.Equal(t, "renamed", snap.Rules[0].Name)
	require.False(t, snap.Rules[0].Enabled)

	// 渠道派生的规则只读；别的分组的规则找不到。
	var derived int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO cost_accounting_rules (name, scope_group_id, source, source_channel_id, source_ordinal)
		 VALUES ('derived', $1, 'legacy_derived', $2, 1) RETURNING id`, gid, gid+8_000_000).Scan(&derived))
	_, err = svc.Update(ctx, 7, gid, 5, derived, pw2Spec("x"))
	require.Equal(t, service.ReasonCostRuleReadonly, pwiReason(t, err))
	_, err = svc.Delete(ctx, 7, gid, 5, derived)
	require.Equal(t, service.ReasonCostRuleReadonly, pwiReason(t, err))
	other := pwiV2Group(t)
	_, err = svc.Delete(ctx, 7, other, 3, created.RuleID)
	require.Equal(t, service.ReasonCostRuleNotFound, pwiReason(t, err))
	require.Equal(t, int64(5), pwiConfigRevision(t, gid), "被拒绝的写入不动 revision")

	// 删除。
	deleted, err := svc.Delete(ctx, 7, gid, 5, created.RuleID)
	require.NoError(t, err)
	require.Equal(t, int64(6), deleted.Revision)
	rules, prices = pw2RuleCount(t, gid)
	require.Equal(t, 1, rules, "只剩派生行")
	require.Zero(t, prices)

	// legacy 分组与没有配置行的分组：拒绝。
	legacy := pwiGroup(t, "legacy", 3)
	_, err = svc.Create(ctx, 7, legacy, 3, pw2Spec("x"))
	require.Equal(t, service.ReasonGroupConfigNotV2, pwiReason(t, err))
	_, err = svc.Create(ctx, 7, mxIntGroup(t), 1, pw2Spec("x"))
	require.Equal(t, service.ReasonCostRuleNotFound, pwiReason(t, err))
}

func pwF2(v float64) *float64 { return &v }
