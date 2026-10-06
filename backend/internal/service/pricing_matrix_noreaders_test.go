//go:build unit

package service

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 零行为变化的静态证据：W6 PR2、PR4b-1 新增的表与代码没有任何读取方；PR5 之后唯一的读取方是 stagedPolicy
// 与它的比对代码（上面 matrixOwnFiles 里标注的文件），而且它只比对、不路由。
//
// 这个测试扫描 backend 下全部非测试 Go 源码：
//  1. 矩阵表与模型目录表的名字只能出现在本 PR 自己的文件里（也就是说，计费、调度、准入、
//     用量记录等现有路径的任何 SQL 都不会碰到这些表）；
//  2. 派生与目录的入口（DeriveGroupState、PlanGroupApply、PricingDerivationService、
//     ModelCatalogService 等）只能被本 PR 自己的文件与依赖注入的接线文件引用。
//
// 后续 PR 一旦让某个现有路径读取这些表，这个测试会失败，提醒审查者逐处确认。

// matrixOwnFiles 本 PR 新增的非测试文件（相对 backend 的路径）。
var matrixOwnFiles = map[string]struct{}{
	"internal/service/pricing_matrix_types.go":           {},
	"internal/service/pricing_matrix_derive.go":          {},
	"internal/service/pricing_matrix_plan.go":            {},
	"internal/service/pricing_matrix_service.go":         {},
	"internal/service/pricing_matrix_facts.go":           {},
	"internal/service/model_catalog_service.go":          {},
	"internal/service/channel_save_hook.go":              {},
	"internal/service/group_policy_matrix.go":            {}, // W6 PR4-1：matrixPolicy，目前只被测试驱动，没有生产路径构造它
	"internal/service/pricing_write_types.go":            {}, // W6 PR4b-1：价格写入路径（CellWriter、PriceWriteGate），没有生产路径构造它
	"internal/service/pricing_write_plan.go":             {},
	"internal/service/pricing_write_gate.go":             {},
	"internal/service/pricing_exposure.go":               {}, // W6 PR4b-2a：保存时校验（ExposureValidator / ExposureGuard），没有生产路径构造它
	"internal/service/pricing_group_config_write.go":     {}, // W6 PR4b-2a：分组配置写路径，没有生产路径构造它
	"internal/service/pricing_price_diff.go":             {}, // W6 PR4b-2a：PriceDiff，纯函数
	"internal/service/user_price_catalog.go":             {}, // W6 PR8a：用户价格页，v2 分组的模型清单读模型目录（第一个读取方）
	"internal/service/pricing_snapshot_types.go":         {}, // W6 PR9b：快照批准事务里的校验回调类型（MatrixExecutor），只在管理员批准时执行
	"internal/service/pricing_snapshot_admin_service.go": {}, // W6 PR9b：快照批准，auto 模式下不会被调用
	"internal/service/pricing_snapshot_exposure.go":      {}, // W6 PR9b：批准时对白名单分组跑 CheckGroups
	"internal/service/pricing_write_tx.go":               {}, // W6 PR4b-2b-1：写入与保存时校验的唯一事务入口（MatrixTxWriter），没有生产路径构造它
	"internal/service/pricing_estimator.go":              {}, // W6 PR4b-2b-1：价格方向估算器，没有生产路径构造它
	"internal/service/price_quoter_overlay.go":           {}, // W6 PR4b-2b-1：QuoteWith / BatchQuoteWith，没有生产路径调用
	"internal/service/pricing_w5_registry.go":            {}, // W6 PR4b-2b-2：W5 注册表里 W6 的设置键与动作登记，受保护键的通用写入拒绝
	"internal/service/pricing_open_precheck.go":          {}, // W6 PR4b-2b-2：开放时预检（含映射目标校验、发布预检）
	"internal/service/pricing_known_free_list.go":        {}, // W6 PR4b-2b-2：已知免费名单的写入口（C 档）
	"internal/service/model_catalog_transition.go":       {}, // W6 PR4b-2b-2：目录状态转换与 7 天用量检查
	"internal/service/pricing_cost_rule_write.go":        {}, // W6 PR4b-2b-2：成本核算规则写服务
	"internal/service/pricing_write_services.go":         {}, // W6 PR4b-2b-2：价格写入路径的装配（所有分组默认 legacy，写入器只写 v2）

	// W6 PR5：stagedPolicy 与影子比对。它们读矩阵快照，但只用来比对：阶段为 shadow 的分组才会比对，
	// 真实请求不会被路由到矩阵（见下面的 TestStagedPolicyNeverRoutesToV2InProduction）。
	"internal/service/group_policy_staged.go":         {},
	"internal/service/group_policy_staged_runtime.go": {}, // W6 PR7a：v2 运行时目录与未定价检查
	"internal/service/group_policy_matrix_preload.go": {}, // W6 PR7a：启动预加载
	"internal/service/billing_unpriced_runtime.go":    {}, // W6 PR7a：billing_unpriced_policy
	"internal/service/pricing_shadow.go":              {},
	"internal/service/pricing_shadow_ctx.go":          {},
	"internal/service/pricing_shadow_session.go":      {},
	"internal/service/pricing_stage_service.go":       {},

	"internal/repository/pricing_matrix_repo.go":              {},
	"internal/repository/model_catalog_repo.go":               {},
	"internal/repository/model_catalog_seed.go":               {},
	"internal/repository/pricing_cell_writer.go":              {},
	"internal/repository/pricing_write_store.go":              {},
	"internal/repository/pricing_group_config_writer.go":      {},
	"internal/repository/pricing_snapshot_exposure_source.go": {}, // W6 PR9b：批准时列出白名单分组
	"internal/repository/pricing_stage_repo.go":               {}, // W6 PR5：阶段切换与影子样本的存储
	"internal/repository/pricing_matrix_configured_groups.go": {}, // W6 PR7a：启动预加载列出有配置行的分组
	"internal/repository/pricing_known_free_store.go":         {}, // W6 PR4b-2b-2：已知免费名单与目录状态、用量统计的存储
	"internal/repository/pricing_cost_rule_writer.go":         {}, // W6 PR4b-2b-2：成本核算规则写入器

	"internal/handler/admin/pricing_matrix_handler.go": {},
	"internal/handler/admin/pricing_write_handler.go":  {}, // W6 PR4b-2b-2：价格写入的管理接口（提交类接口要 JWT 会话）
	"cmd/server/model_catalog_cmd.go":                  {},

	// W6 PR6：离线只读的价格回放命令与引擎。只在运维命令里被构造，不被任何请求路径引用。
	"cmd/server/pricing_replay_cmd.go":           {},
	"internal/service/pricing_replay.go":         {},
	"internal/service/pricing_replay_run.go":     {},
	"internal/service/pricing_replay_source.go":  {},
	"internal/repository/pricing_replay_repo.go": {},

	// W6 派生 CLI（#1653）：运维命令里手动触发的批量派生（默认 dry-run），只在命令行构造，不被任何请求路径引用。
	"cmd/server/pricing_matrix_cmd.go":         {},
	"internal/service/pricing_matrix_batch.go": {},
}

// matrixWiringFiles 只做依赖注入接线的文件。
var matrixWiringFiles = map[string]struct{}{
	"internal/service/wire.go":    {},
	"internal/repository/wire.go": {},
	"cmd/server/wire_gen.go":      {},
}

var matrixTableNames = regexp.MustCompile(`model_group_prices|model_group_price_history|group_model_config|cost_accounting_rule|model_catalog|pricing_write_approvals`)

var matrixEntryPoints = regexp.MustCompile(`\b(` + strings.Join([]string{
	"DeriveGroupState", "PlanGroupApply", "DerivedGroupState", "GroupStateSnapshot", "GroupApplyPlan",
	"PricingDerivationService", "NewPricingDerivationService", "ProvidePricingDerivationService",
	"PricingMatrixRepository", "NewPricingMatrixRepository",
	"ModelCatalogService", "NewModelCatalogService", "ModelCatalogRepository", "NewModelCatalogRepository",
	"ResolveCatalogEntry", "OfficialPriceFactSource", "LookupOfficialPriceFact",
	"matrixPolicy", "NewMatrixGroupPolicy", "MatrixSnapshotSource", "MatrixSnapshotInvalidator",
	"CellWriter", "NewPricingCellWriter", "PriceWriteGate", "InterimPriceWriteGate", "NewInterimPriceWriteGate",
	"PriceWriteStore", "NewPricingWriteStore", "MatrixExecutor", "NormalizeCellWriteRequest", "PlanCellWrites",
	"MatrixTx", "ExposureValidator", "NewExposureValidator", "ExposureGuard", "NewExposureGuard", "ExposureReader",
	"GroupConfigWriter", "GroupConfigService", "NewGroupConfigService", "NewPricingGroupConfigWriter", "NewPricingExposureReader",
	"OfficialPriceStateSource", "LookupOfficialPriceState",
	"MatrixTxWriter", "NewMatrixTxWriter", "PriceDeltaEstimator", "PriceEstimator", "NewPriceEstimator",
	"CellOverlay", "OverlayFromPlanned",
	"OpenPrechecker", "NewOpenPrechecker", "KnownFreeListService", "NewKnownFreeListService", "KnownFreeListStore",
	"NewPricingKnownFreeStore", "ModelCatalogTransitionService", "NewModelCatalogTransitionService",
	"ModelCatalogStatusStore", "NewModelCatalogStatusStore", "CostRuleService", "NewCostRuleService", "CostRuleWriter",
	"NewPricingCostRuleWriter", "PricingWriteServices", "ProvidePricingWriteServices",
}, "|") + `)\b`)

func TestMatrixTablesAndDerivationHaveNoReaders(t *testing.T) {
	backendRoot, err := filepath.Abs("../..") // 测试在 internal/service 下运行
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(backendRoot, "internal"))

	var violations []string
	scanned := 0
	err = filepath.Walk(backendRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(backendRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, own := matrixOwnFiles[rel]; own {
			return nil
		}
		scanned++
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		if m := matrixTableNames.FindString(text); m != "" {
			violations = append(violations, rel+" 引用了矩阵表 "+m)
		}
		if _, wiring := matrixWiringFiles[rel]; !wiring {
			if m := matrixEntryPoints.FindString(text); m != "" {
				violations = append(violations, rel+" 引用了派生入口 "+m)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "扫描范围不对：应当覆盖 backend 下的全部 Go 源码")

	sort.Strings(violations)
	require.Empty(t, violations, "现有路径不得读取 W6 的新表或派生入口（本 PR 零行为变化）")
}

// W6 PR7a 的零行为变化证据：阶段 API 仍不开放 v2，所以任何分组都不可能被写成 v2，
// 生产环境所有分组的计费、准入、映射继续走 legacy（shadow 只旁路比对）。
// PR7b 开放 v2 时，要把这条守卫改成对切换闸门的检查。
func TestStagedPolicyV2StageStillClosedInProduction(t *testing.T) {
	require.False(t, pricingStageAllowed(PricingStageV2), "PR7a 不开放 v2 阶段")
	require.True(t, pricingStageAllowed(PricingStageLegacy))
	require.True(t, pricingStageAllowed(PricingStageShadow))

	backendRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	writer := regexp.MustCompile(`SET\s+pricing_stage\s*=`)
	var violations []string
	err = filepath.Walk(backendRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(backendRoot, path)
		rel = filepath.ToSlash(rel)
		if writer.Match(raw) && rel != "internal/repository/pricing_stage_repo.go" {
			violations = append(violations, rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, violations, "只有阶段仓储可以写 pricing_stage")
}
