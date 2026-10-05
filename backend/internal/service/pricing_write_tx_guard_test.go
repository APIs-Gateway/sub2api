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

// W6 PR4b-2b-1：价格写入的事务入口只留一个。
//
// CellWriter.ApplyTx 与 GroupConfigWriter.ApplyTx 只负责写；「写完以后保存时校验必须通过」由
// MatrixTxWriter.ApplyCellWritesTx、ApplyGroupConfigTx 把写入与校验做成一体。直接调用写入器的 ApplyTx 就绕过了校验
// （白名单分组里会出现无价或 0 元的开放单元格），所以这个守卫扫描 backend 下全部非测试 Go 源码：
// 凡是能接触到写入器类型的文件，除了 pricing_write_tx.go，都不能出现 `.ApplyTx(` 调用。
// W5 的 change-set 动作拿着 *ent.Tx 写价格时，同样只能调 MatrixTxWriter 的两个入口。

// writerAwareTypes 提到写入器类型或构造函数的文件才会被检查：别的包里同名的 ApplyTx（例如 W5 的 Action 接口）不受影响。
var writerAwareTypes = regexp.MustCompile(`\b(CellWriter|GroupConfigWriter|NewPricingCellWriter|NewPricingGroupConfigWriter)\b`)

var applyTxCall = regexp.MustCompile(`\.ApplyTx\(`)

// matrixTxEntryFile 唯一允许调用写入器 ApplyTx 的文件。
const matrixTxEntryFile = "internal/service/pricing_write_tx.go"

func TestWriterApplyTxIsOnlyCalledFromTheTxEntryPoint(t *testing.T) {
	backendRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(backendRoot, "internal"))

	var violations []string
	scanned, entryCalls := 0, 0
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
		scanned++
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		calls := len(applyTxCall.FindAllString(text, -1))
		if rel == matrixTxEntryFile {
			entryCalls = calls
			return nil
		}
		if calls > 0 && writerAwareTypes.MatchString(text) {
			violations = append(violations, rel+" 直接调用了写入器的 ApplyTx，应改调 MatrixTxWriter.ApplyCellWritesTx / ApplyGroupConfigTx")
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "扫描范围不对：应当覆盖 backend 下的全部 Go 源码")
	require.Equal(t, 2, entryCalls, "入口文件里应当正好有两处写入器 ApplyTx 调用（单元格与分组配置各一处）")

	sort.Strings(violations)
	require.Empty(t, violations)
}
