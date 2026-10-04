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

// 零行为变化的静态证据：W6 PR2 新增的表与代码没有任何读取方。
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
	"internal/service/pricing_matrix_types.go":   {},
	"internal/service/pricing_matrix_derive.go":  {},
	"internal/service/pricing_matrix_plan.go":    {},
	"internal/service/pricing_matrix_service.go": {},
	"internal/service/pricing_matrix_facts.go":   {},
	"internal/service/model_catalog_service.go":  {},
	"internal/service/channel_save_hook.go":      {},

	"internal/repository/pricing_matrix_repo.go": {},
	"internal/repository/model_catalog_repo.go":  {},
	"internal/repository/model_catalog_seed.go":  {},

	"internal/handler/admin/pricing_matrix_handler.go": {},
	"cmd/server/model_catalog_cmd.go":                  {},
}

// matrixWiringFiles 只做依赖注入接线的文件。
var matrixWiringFiles = map[string]struct{}{
	"internal/service/wire.go":    {},
	"internal/repository/wire.go": {},
	"cmd/server/wire_gen.go":      {},
}

var matrixTableNames = regexp.MustCompile(`model_group_prices|model_group_price_history|group_model_config|cost_accounting_rule|model_catalog`)

var matrixEntryPoints = regexp.MustCompile(`\b(` + strings.Join([]string{
	"DeriveGroupState", "PlanGroupApply", "DerivedGroupState", "GroupStateSnapshot", "GroupApplyPlan",
	"PricingDerivationService", "NewPricingDerivationService", "ProvidePricingDerivationService",
	"PricingMatrixRepository", "NewPricingMatrixRepository",
	"ModelCatalogService", "NewModelCatalogService", "ModelCatalogRepository", "NewModelCatalogRepository",
	"ResolveCatalogEntry", "OfficialPriceFactSource", "LookupOfficialPriceFact",
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
