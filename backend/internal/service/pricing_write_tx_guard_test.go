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
// 只要出现 `.ApplyTx(` 调用，文件就必须在白名单里（今天只有 pricing_write_tx.go）。
// 不按「文件里有没有提到写入器类型」来猜：同一个包里经未导出字段（g.tx.cells.ApplyTx）调用，文件里不会出现类型名。
// W5 的 change-set 动作拿着 *ent.Tx 写价格时，同样只能调 MatrixTxWriter 的两个入口；
// 那个动作文件要登记进 applyTxAllowedFiles 才能出现 ApplyTx 调用。
//
// 第二条守卫：Interactive 能放行涉价写入，只能由鉴权方式得出。非测试代码里，除 pricing_write_tx.go
// （PriceWriteActorFromAuthMethod）以外，不能构造 PriceWriteActor 字面量，也不能给 Interactive 字段赋值。

var applyTxCall = regexp.MustCompile(`\.ApplyTx\(`)

// matrixTxEntryFile 唯一允许调用写入器 ApplyTx 的文件。
const matrixTxEntryFile = "internal/service/pricing_write_tx.go"

// applyTxAllowedFiles 允许出现 `.ApplyTx(` 的文件（相对 backend）。
var applyTxAllowedFiles = map[string]bool{matrixTxEntryFile: true}

// interactiveSetters 构造 PriceWriteActor 或写 Interactive 字段的写法。
var interactiveSetters = regexp.MustCompile(`PriceWriteActor\{|\bInteractive\s*:|\.Interactive\s*=[^=]`)

// interactiveAllowedFiles 允许这样写的文件（相对 backend）。
var interactiveAllowedFiles = map[string]bool{matrixTxEntryFile: true}

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
		} else if calls > 0 && !applyTxAllowedFiles[rel] {
			violations = append(violations, rel+" 直接调用了 ApplyTx，应改调 MatrixTxWriter.ApplyCellWritesTx / ApplyGroupConfigTx")
		}
		if interactiveSetters.MatchString(text) && !interactiveAllowedFiles[rel] {
			violations = append(violations, rel+" 自己构造了 PriceWriteActor 或给 Interactive 赋值，应改用 PriceWriteActorFromAuthMethod")
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "扫描范围不对：应当覆盖 backend 下的全部 Go 源码")
	require.Equal(t, 2, entryCalls, "入口文件里应当正好有两处写入器 ApplyTx 调用（单元格与分组配置各一处）")

	sort.Strings(violations)
	require.Empty(t, violations)
}

// 守卫的正则本身要能抓到各种写法（包括同包里经未导出字段的调用）。
func TestWriteTxGuardPatterns(t *testing.T) {
	for _, line := range []string{"res, err := w.cells.ApplyTx(ctx, tx, req)", "g.tx.cells.ApplyTx(ctx, tx, req)", "x.ApplyTx(a)"} {
		require.True(t, applyTxCall.MatchString(line), line)
	}
	require.False(t, applyTxCall.MatchString("ApplyTx(ctx context.Context, tx MatrixTx, req CellWriteRequest)"), "接口方法的声明不算调用")
	for _, line := range []string{"PriceWriteActor{ID: 1}", "x := PriceWriteActor{ID: 1, Interactive: true}", "a.Interactive = true", "Interactive:true"} {
		require.True(t, interactiveSetters.MatchString(line), line)
	}
	require.False(t, interactiveSetters.MatchString("if !actor.Interactive {"), "读取不算")
	require.False(t, interactiveSetters.MatchString("actor.Interactive == true"), "比较不算")
}
