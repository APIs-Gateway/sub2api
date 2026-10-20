//go:build unit

package service

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitBackupFile_ReassemblesExactBytes(t *testing.T) {
	src := writeBackupArchiveFixture(t, []byte("0123456789abcdefg"))
	parts, err := splitBackupFile(src, 5)
	require.NoError(t, err)
	require.Len(t, parts, 4)

	var got bytes.Buffer
	for i, part := range parts {
		require.Equal(t, i+1, part.Index)
		require.LessOrEqual(t, part.SizeBytes, int64(5))
		data, readErr := os.ReadFile(part.Path)
		require.NoError(t, readErr)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(data)), part.SHA256)
		got.Write(data)
	}
	require.Equal(t, []byte("0123456789abcdefg"), got.Bytes())
}

func TestSplitBackupFile_RejectsInvalidInput(t *testing.T) {
	src := writeBackupArchiveFixture(t, []byte("data"))

	_, err := splitBackupFile(src, 0)
	require.Error(t, err)

	empty := writeBackupArchiveFixture(t, nil)
	_, err = splitBackupFile(empty, 5)
	require.Error(t, err)

	_, err = splitBackupFile(filepathForMissingBackupArchive(t), 5)
	require.Error(t, err)
}

// TestSplitBackupFile_CreateTempFailurePropagates 通过把 TMPDIR 指向一个不存在的目录，
// 让分卷临时文件的 os.CreateTemp 失败，验证错误会带着 part 序号往外传播。
func TestSplitBackupFile_CreateTempFailurePropagates(t *testing.T) {
	src := writeBackupArchiveFixture(t, []byte("0123456789"))
	t.Setenv("TMPDIR", "/nonexistent-xyz-dir-for-split-test")

	_, err := splitBackupFile(src, 4)
	require.ErrorContains(t, err, "create backup part")
}

// TestSplitBackupFile_ReadFailurePropagates 用目录路径当作源文件：os.Open 对目录会成功、
// Stat().Size() 也非 0，但真正读取时会返回 "is a directory"，用于覆盖写分卷失败的清理分支。
func TestSplitBackupFile_ReadFailurePropagates(t *testing.T) {
	dir := t.TempDir()

	_, err := splitBackupFile(dir, 4)
	require.ErrorContains(t, err, "write backup part")
}

func TestCleanupBackupFiles_SkipsEmptyPaths(t *testing.T) {
	// 空字符串路径应被跳过，不应尝试 os.Remove("")
	require.NoError(t, cleanupBackupFiles("", ""))
}

func TestCleanupBackupFiles_IgnoresNotExist(t *testing.T) {
	require.NoError(t, cleanupBackupFiles(filepathForMissingBackupArchive(t)))
}

func TestCleanupBackupFiles_AggregatesRemovalErrors(t *testing.T) {
	// 移除一个非空目录会失败（不是 ErrNotExist），应被聚合进返回的错误里。
	dir := t.TempDir()
	nested := dir + "/nested"
	require.NoError(t, os.Mkdir(nested, 0o700))
	require.NoError(t, os.WriteFile(nested+"/file", []byte("x"), 0o600))

	err := cleanupBackupFiles(dir)
	require.Error(t, err)
}

func writeBackupArchiveFixture(t *testing.T, content []byte) string {
	t.Helper()
	path := filepathForBackupArchive(t)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

func filepathForBackupArchive(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/archive.gz"
}

func filepathForMissingBackupArchive(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/missing.gz"
}
