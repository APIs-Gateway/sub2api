//go:build unit

package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ─── Mocks ───

type mockSettingRepo struct {
	mu          sync.Mutex
	data        map[string]string
	failSetKeys map[string]error // key -> 注入的 Set 失败，测试保存失败分支用
}

func newMockSettingRepo() *mockSettingRepo {
	return &mockSettingRepo{data: make(map[string]string)}
}

// failSet 让后续对指定 key 的 Set 调用返回 err，用于覆盖保存失败的错误处理分支。
func (m *mockSettingRepo) failSet(key string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSetKeys == nil {
		m.failSetKeys = make(map[string]error)
	}
	m.failSetKeys[key] = err
}

func (m *mockSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: v}, nil
}

func (m *mockSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return "", nil
	}
	return v, nil
}

func (m *mockSettingRepo) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.failSetKeys[key]; ok {
		return err
	}
	m.data[key] = value
	return nil
}

func (m *mockSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := m.data[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range settings {
		m.data[k] = v
	}
	return nil
}

func (m *mockSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string, len(m.data))
	for k, v := range m.data {
		result[k] = v
	}
	return result, nil
}

func (m *mockSettingRepo) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// plainEncryptor 仅做 base64-like 包装，用于测试
type plainEncryptor struct{}

func (e *plainEncryptor) Encrypt(plaintext string) (string, error) {
	return "ENC:" + plaintext, nil
}

func (e *plainEncryptor) Decrypt(ciphertext string) (string, error) {
	if strings.HasPrefix(ciphertext, "ENC:") {
		return strings.TrimPrefix(ciphertext, "ENC:"), nil
	}
	return ciphertext, fmt.Errorf("not encrypted")
}

type mockDumper struct {
	dumpData []byte
	dumpErr  error
	restored []byte
	restErr  error
}

func (m *mockDumper) Dump(_ context.Context) (io.ReadCloser, error) {
	if m.dumpErr != nil {
		return nil, m.dumpErr
	}
	return io.NopCloser(bytes.NewReader(m.dumpData)), nil
}

func (m *mockDumper) Restore(_ context.Context, data io.Reader) error {
	if m.restErr != nil {
		return m.restErr
	}
	d, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	m.restored = d
	return nil
}

// closeErrDumper 的 Dump() 返回的 ReadCloser 在 Close() 时报错，
// 用于覆盖 createCompressedBackupFile 里 dumpReader.Close() 失败的分支。
type closeErrDumper struct {
	data     []byte
	closeErr error
}

func (d *closeErrDumper) Dump(_ context.Context) (io.ReadCloser, error) {
	return &closeErrReadCloser{Reader: bytes.NewReader(d.data), closeErr: d.closeErr}, nil
}

func (d *closeErrDumper) Restore(_ context.Context, data io.Reader) error {
	_, err := io.ReadAll(data)
	return err
}

// blockingDumper 可控延迟的 dumper，用于测试异步行为
type blockingDumper struct {
	blockCh chan struct{}
	data    []byte
	restErr error
}

func (d *blockingDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	select {
	case <-d.blockCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return io.NopCloser(bytes.NewReader(d.data)), nil
}

func (d *blockingDumper) Restore(_ context.Context, data io.Reader) error {
	if d.restErr != nil {
		return d.restErr
	}
	_, _ = io.ReadAll(data)
	return nil
}

type mockObjectStore struct {
	objects        map[string][]byte
	mu             sync.Mutex
	failUploadAt   int // 第几次 Upload 调用注入失败，0=不注入
	uploadCalls    int
	deletedKeys    []string
	failDeleteKeys map[string]error
}

// cancelingUploadFailureStore 模拟"对象已经在存储端落地、但客户端因 ctx 取消/超时收到错误"的场景，
// 用于验证失败清理必须使用独立于请求 ctx 的 context，否则清理请求会被同一次取消打断。
type cancelingUploadFailureStore struct {
	*mockObjectStore
	cancel context.CancelFunc
}

func (m *cancelingUploadFailureStore) Upload(_ context.Context, key string, body io.Reader, _ string) (int64, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.mu.Unlock()
	m.cancel()
	return 0, fmt.Errorf("injected upload failure after object landed")
}

func (m *cancelingUploadFailureStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.mockObjectStore.Delete(ctx, key)
}

func newMockObjectStore() *mockObjectStore {
	return &mockObjectStore{objects: make(map[string][]byte), failDeleteKeys: make(map[string]error)}
}

func (m *mockObjectStore) Upload(_ context.Context, key string, body io.Reader, _ string) (int64, error) {
	m.mu.Lock()
	m.uploadCalls++
	call := m.uploadCalls
	failAt := m.failUploadAt
	m.mu.Unlock()
	if failAt > 0 && call == failAt {
		_, _ = io.Copy(io.Discard, body)
		return 0, fmt.Errorf("injected upload failure at call %d", call)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.mu.Unlock()
	return int64(len(data)), nil
}

func (m *mockObjectStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	m.deletedKeys = append(m.deletedKeys, key)
	if err, ok := m.failDeleteKeys[key]; ok {
		m.mu.Unlock()
		return err
	}
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

func (m *mockObjectStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://presigned.example.com/" + key, nil
}

func (m *mockObjectStore) HeadBucket(_ context.Context) error {
	return nil
}

// closeErrReadCloser 包一个 io.Reader，让 Close() 返回可配置的错误；
// 用来覆盖各处 "读取成功但 Close 失败" 的错误处理分支。
type closeErrReadCloser struct {
	io.Reader
	closeErr error
}

func (c *closeErrReadCloser) Close() error { return c.closeErr }

// erroringDownloadStore 包一个 mockObjectStore，让指定 key 的 Download 要么读取失败、
// 要么返回后 Close 失败，用于覆盖 downloadBackupParts 里对应的错误处理分支。
type erroringDownloadStore struct {
	*mockObjectStore
	readErrAtKey  string
	closeErrAtKey string
}

func (s *erroringDownloadStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.readErrAtKey != "" && key == s.readErrAtKey {
		return io.NopCloser(iotest.ErrReader(fmt.Errorf("simulated read failure"))), nil
	}
	body, err := s.mockObjectStore.Download(ctx, key)
	if err != nil {
		return nil, err
	}
	if s.closeErrAtKey != "" && key == s.closeErrAtKey {
		data, readErr := io.ReadAll(body)
		if readErr != nil {
			return nil, readErr
		}
		return &closeErrReadCloser{Reader: bytes.NewReader(data), closeErr: fmt.Errorf("simulated close failure")}, nil
	}
	return body, nil
}

// writeTempBackupFile 写一个临时文件供直接调用 uploadBackupArchive/restoreArchive 等私有方法测试用。
func writeTempBackupFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "backup-svc-test-*.gz")
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func newTestBackupService(repo *mockSettingRepo, dumper DBDumper, store *mockObjectStore) *BackupService {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Host:   "localhost",
			Port:   5432,
			User:   "test",
			DBName: "testdb",
		},
		Totp: config.TotpConfig{EncryptionKeyConfigured: true},
	}
	factory := func(_ context.Context, _ *BackupS3Config) (BackupObjectStore, error) {
		return store, nil
	}
	return NewBackupService(repo, cfg, &plainEncryptor{}, factory, dumper)
}

func newTestBackupServiceEphemeralKey(repo *mockSettingRepo) *BackupService {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Host:   "localhost",
			Port:   5432,
			User:   "test",
			DBName: "testdb",
		},
		Totp: config.TotpConfig{EncryptionKeyConfigured: false},
	}
	factory := func(_ context.Context, _ *BackupS3Config) (BackupObjectStore, error) {
		return newMockObjectStore(), nil
	}
	return NewBackupService(repo, cfg, &plainEncryptor{}, factory, &mockDumper{})
}

func seedS3Config(t *testing.T, repo *mockSettingRepo) {
	t.Helper()
	cfg := BackupS3Config{
		Bucket:          "test-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "ENC:secret123",
		Prefix:          "backups",
	}
	data, _ := json.Marshal(cfg)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, string(data)))
}

// ─── Tests ───

func TestBackupService_S3ConfigEncryption(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 保存配置 -> SecretAccessKey 应被加密
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "my-secret",
		Prefix:          "backups",
	})
	require.NoError(t, err)

	// 直接读取数据库中存储的值，应该是加密后的
	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	var stored BackupS3Config
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, "ENC:my-secret", stored.SecretAccessKey)

	// 通过 GetS3Config 获取应该脱敏
	cfg, err := svc.GetS3Config(context.Background())
	require.NoError(t, err)
	require.Empty(t, cfg.SecretAccessKey)
	require.Equal(t, "my-bucket", cfg.Bucket)

	// loadS3Config 内部应解密
	internal, err := svc.loadS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "my-secret", internal.SecretAccessKey)
}

func TestBackupService_S3ConfigKeepExistingSecret(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 先保存一个有 secret 的配置
	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "original-secret",
	})
	require.NoError(t, err)

	// 再更新时不提供 secret，应保留原值
	_, err = svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:      "my-bucket",
		AccessKeyID: "AKID-NEW",
	})
	require.NoError(t, err)

	internal, err := svc.loadS3Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "original-secret", internal.SecretAccessKey)
	require.Equal(t, "AKID-NEW", internal.AccessKeyID)
}

func TestBackupService_UpdateS3Config_RejectsEphemeralKey(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupServiceEphemeralKey(repo)

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:          "my-bucket",
		AccessKeyID:     "AKID",
		SecretAccessKey: "my-secret",
	})
	require.ErrorIs(t, err, ErrSecretEncryptionKeyNotConfigured)

	raw, _ := repo.GetValue(context.Background(), settingKeyBackupS3Config)
	require.Empty(t, raw)
}

func TestBackupService_UpdateS3Config_NoSecretAllowedWithEphemeralKey(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupServiceEphemeralKey(repo)

	_, err := svc.UpdateS3Config(context.Background(), BackupS3Config{
		Bucket:      "my-bucket",
		AccessKeyID: "AKID",
	})
	require.NoError(t, err)
}

func TestBackupService_EncryptionKeyConfigured(t *testing.T) {
	repo := newMockSettingRepo()
	require.True(t, newTestBackupService(repo, &mockDumper{}, newMockObjectStore()).EncryptionKeyConfigured())
	require.False(t, newTestBackupServiceEphemeralKey(repo).EncryptionKeyConfigured())
}

func TestBackupService_SaveRecordConcurrency(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			record := &BackupRecord{
				ID:        fmt.Sprintf("rec-%d", idx),
				Status:    "completed",
				StartedAt: time.Now().Format(time.RFC3339),
			}
			_ = svc.saveRecord(context.Background(), record)
		}(i)
	}
	wg.Wait()

	records, err := svc.loadRecords(context.Background())
	require.NoError(t, err)
	require.Len(t, records, n)
}

func TestBackupService_LoadRecords_Empty(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	records, err := svc.loadRecords(context.Background())
	require.NoError(t, err)
	require.Nil(t, records) // 无数据时返回 nil
}

func TestBackupService_LoadRecords_Corrupted(t *testing.T) {
	repo := newMockSettingRepo()
	_ = repo.Set(context.Background(), settingKeyBackupRecords, "not valid json{{{")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	records, err := svc.loadRecords(context.Background())
	require.Error(t, err) // 损坏数据应返回错误
	require.Nil(t, records)
}

func TestBackupService_CreateBackup_Streaming(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	require.Greater(t, record.SizeBytes, int64(0))
	require.NotEmpty(t, record.S3Key)

	// 验证 S3 上确实有文件
	store.mu.Lock()
	require.Len(t, store.objects, 1)
	store.mu.Unlock()
}

func TestBackupService_CreateBackup_SplitsCompressedArchive(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumpContent := entropyBackupFixture(512)
	dumper := &mockDumper{dumpData: dumpContent}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)
	svc.partSizeBytes = 32

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	require.Greater(t, len(record.Parts), 1)
	require.Empty(t, record.S3Key)

	var compressed bytes.Buffer
	store.mu.Lock()
	for _, part := range record.Parts {
		data, ok := store.objects[part.S3Key]
		require.True(t, ok)
		require.LessOrEqual(t, len(data), 32)
		compressed.Write(data)
	}
	store.mu.Unlock()

	gzReader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	require.NoError(t, err)
	decompressed, err := io.ReadAll(gzReader)
	require.NoError(t, err)
	require.NoError(t, gzReader.Close())
	require.Equal(t, dumpContent, decompressed)
}

func TestBackupService_StartBackup_SplitsCompressedArchive(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{dumpData: entropyBackupFixture(512)}, store)
	svc.partSizeBytes = 32

	record, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.Status)
	require.Greater(t, len(final.Parts), 1)
	require.Empty(t, final.S3Key)
}

func TestBackupService_StartBackup_UploadFailureCleansUploadedParts(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	store.failUploadAt = 2
	svc := newTestBackupService(repo, &mockDumper{dumpData: entropyBackupFixture(512)}, store)
	svc.partSizeBytes = 32

	record, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", final.Status)
	require.NotEmpty(t, final.Parts)
	store.mu.Lock()
	deletedKeys := append([]string(nil), store.deletedKeys...)
	store.mu.Unlock()
	for _, part := range final.Parts {
		require.Contains(t, deletedKeys, part.S3Key)
	}
}

func TestBackupService_UploadFailureCleanupUsesDetachedContext(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.partSizeBytes = 4

	archive, err := os.CreateTemp("", "backup-upload-context-*.gz")
	require.NoError(t, err)
	archivePath := archive.Name()
	defer func() { _ = os.Remove(archivePath) }()
	_, err = archive.Write([]byte("0123456789"))
	require.NoError(t, err)
	require.NoError(t, archive.Close())

	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelingUploadFailureStore{
		mockObjectStore: newMockObjectStore(),
		cancel:          cancel,
	}
	record := &BackupRecord{ID: "cancel-cleanup", S3Key: "backups/cancel-cleanup.sql.gz"}

	err = svc.uploadBackupArchive(ctx, record, store, &BackupS3Config{Prefix: "backups"}, archivePath)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "context canceled")

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, part := range record.Parts {
		require.Contains(t, store.deletedKeys, part.S3Key)
		require.NotContains(t, store.objects, part.S3Key)
	}
}

// TestBackupService_CreateCompressedBackupFile_CreateTempFailure 通过把 TMPDIR 指向一个
// 不存在的目录，让 createCompressedBackupFile 里创建本地归档临时文件失败。
func TestBackupService_CreateCompressedBackupFile_CreateTempFailure(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, newMockObjectStore())
	t.Setenv("TMPDIR", "/nonexistent-xyz-dir-for-compress-test")

	_, _, err := svc.createCompressedBackupFile(context.Background())
	require.ErrorContains(t, err, "create backup archive")
}

// TestBackupService_CreateCompressedBackupFile_DumpCloseError 让 dumper 返回的 ReadCloser
// 在 Close() 时报错，覆盖 gzip/dump 失败后清理归档并返回错误的分支。
func TestBackupService_CreateCompressedBackupFile_DumpCloseError(t *testing.T) {
	repo := newMockSettingRepo()
	dumper := &closeErrDumper{data: []byte("data"), closeErr: fmt.Errorf("dump close boom")}
	svc := newTestBackupService(repo, dumper, newMockObjectStore())

	_, _, err := svc.createCompressedBackupFile(context.Background())
	require.ErrorContains(t, err, "gzip/dump failed")
}

// TestBackupService_UploadBackupArchive_StatError 覆盖 uploadBackupArchive 里
// 对本地归档文件 Stat 失败的分支（比如上一步产物已经不存在）。
func TestBackupService_UploadBackupArchive_StatError(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	record := &BackupRecord{ID: "stat-err", S3Key: "backups/stat-err.sql.gz"}

	err := svc.uploadBackupArchive(context.Background(), record, store, &BackupS3Config{Prefix: "backups"}, "/nonexistent/path/x.gz")
	require.ErrorContains(t, err, "stat backup archive")
}

// TestBackupService_UploadBackupArchive_ZeroPartSizeDefaultsToConstant 验证
// partSizeBytes<=0 时会回退到默认的 4GiB 阈值，小文件仍按单对象上传。
func TestBackupService_UploadBackupArchive_ZeroPartSizeDefaultsToConstant(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.partSizeBytes = 0
	archivePath := writeTempBackupFile(t, []byte("hello"))
	record := &BackupRecord{ID: "zero-partsize", S3Key: "backups/zero.sql.gz"}

	err := svc.uploadBackupArchive(context.Background(), record, store, &BackupS3Config{Prefix: "backups"}, archivePath)
	require.NoError(t, err)
	require.Empty(t, record.Parts)
	store.mu.Lock()
	require.Contains(t, store.objects, record.S3Key)
	store.mu.Unlock()
}

// TestBackupService_UploadBackupArchive_SingleFileUploadFailureCleansUp 覆盖单对象
// (未拆分)上传失败时清理已上传对象并返回聚合错误的分支。
func TestBackupService_UploadBackupArchive_SingleFileUploadFailureCleansUp(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	store.failUploadAt = 1
	svc := newTestBackupService(repo, &mockDumper{}, store)
	archivePath := writeTempBackupFile(t, []byte("hello"))
	record := &BackupRecord{ID: "single-fail", S3Key: "backups/single-fail.sql.gz"}

	err := svc.uploadBackupArchive(context.Background(), record, store, &BackupS3Config{Prefix: "backups"}, archivePath)
	require.ErrorContains(t, err, "backup upload")
	store.mu.Lock()
	require.Contains(t, store.deletedKeys, record.S3Key)
	store.mu.Unlock()
}

// TestBackupService_UploadBackupArchive_SplitFailurePropagates 让拆分阶段本身失败
// (临时目录不可用)，验证错误会被包装成 "split backup archive" 往外传播。
func TestBackupService_UploadBackupArchive_SplitFailurePropagates(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.partSizeBytes = 4
	archivePath := writeTempBackupFile(t, []byte("0123456789"))
	record := &BackupRecord{ID: "split-fail", S3Key: "backups/split-fail.sql.gz"}

	t.Setenv("TMPDIR", "/nonexistent-xyz-dir-for-upload-split-test")

	err := svc.uploadBackupArchive(context.Background(), record, store, &BackupS3Config{Prefix: "backups"}, archivePath)
	require.ErrorContains(t, err, "split backup archive")
}

// TestBackupService_UploadBackupArchive_SplitRequiresConfig 覆盖拆分上传但 S3 配置
// 缺失（cfg==nil）时的显式报错分支。
func TestBackupService_UploadBackupArchive_SplitRequiresConfig(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.partSizeBytes = 4
	archivePath := writeTempBackupFile(t, []byte("0123456789"))
	record := &BackupRecord{ID: "nil-cfg", S3Key: "backups/nil-cfg.sql.gz"}

	err := svc.uploadBackupArchive(context.Background(), record, store, nil, archivePath)
	require.ErrorContains(t, err, "backup S3 config is unavailable")
}

// TestBackupService_UploadBackupArchive_SaveSplitPlanFailure 覆盖拆分计划保存
// (saveRecord)失败时的错误分支。
func TestBackupService_UploadBackupArchive_SaveSplitPlanFailure(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	svc.partSizeBytes = 4
	archivePath := writeTempBackupFile(t, []byte("0123456789"))
	record := &BackupRecord{ID: "save-plan-fail", S3Key: "backups/save-plan-fail.sql.gz"}

	repo.failSet(settingKeyBackupRecords, fmt.Errorf("boom"))

	err := svc.uploadBackupArchive(context.Background(), record, store, &BackupS3Config{Prefix: "backups"}, archivePath)
	require.ErrorContains(t, err, "save split backup plan")
}

// TestBackupService_UploadBackupFile_OpenError 覆盖 uploadBackupFile 打开本地文件失败的分支。
func TestBackupService_UploadBackupFile_OpenError(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.uploadBackupFile(context.Background(), newMockObjectStore(), "key", "/nonexistent/path.gz", "application/gzip")
	require.ErrorContains(t, err, "open backup file")
}

func entropyBackupFixture(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + 17) % 251)
	}
	return data
}

func TestBackupService_CreateBackup_DumpFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &mockDumper{dumpErr: fmt.Errorf("pg_dump failed")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.Error(t, err)
	require.Equal(t, "failed", record.Status)
	require.Contains(t, record.ErrorMsg, "pg_dump")
}

// TestBackupService_CreateBackup_SaveInitialRecordFailure 覆盖 CreateBackup 里
// "保存初始记录失败" 的错误分支：dump 成功后但落盘 running 记录失败应直接报错返回。
func TestBackupService_CreateBackup_SaveInitialRecordFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, newMockObjectStore())

	repo.failSet(settingKeyBackupRecords, fmt.Errorf("boom"))

	_, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.ErrorContains(t, err, "save initial record")
}

// TestBackupService_CreateBackup_UploadFailure 覆盖 CreateBackup 里上传失败后
// 标记记录为 failed 并保存的分支（区别于 dump 失败分支）。
func TestBackupService_CreateBackup_UploadFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	store.failUploadAt = 1
	svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.Error(t, err)
	require.Equal(t, "failed", record.Status)
	require.NotEmpty(t, record.ErrorMsg)
}

func TestBackupService_CreateBackup_NoS3Config(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	_, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupS3NotConfigured)
}

func TestBackupService_CreateBackup_ConcurrentBlocked(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	// 使用一个慢速 dumper 来模拟正在进行的备份
	dumper := &mockDumper{dumpData: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 手动设置 backingUp 标志
	svc.opMu.Lock()
	svc.backingUp = true
	svc.opMu.Unlock()

	_, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupInProgress)
}

// TestBackupService_RunScheduledBackup_LeaderElection verifies the scheduled
// backup is gated by a cross-instance leader lock: a non-leader instance skips
// the dump entirely so a clustered deployment does not run N identical backups
// against the same database, while the leader runs it and releases the lock
// afterward. Manual backups (CreateBackup/StartBackup) are intentionally left
// ungated and are covered by the other tests.
func TestBackupService_RunScheduledBackup_LeaderElection(t *testing.T) {
	t.Run("non-leader skips", func(t *testing.T) {
		repo := newMockSettingRepo()
		seedS3Config(t, repo)
		store := newMockObjectStore()
		svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, store)

		// A peer already owns the lock, so this instance is not the leader.
		cache := &fakeLeaderLockCache{}
		peerRelease, ok := tryAcquireSingletonLeaderLock(context.Background(), cache, nil, backupScheduledLeaderLockKey, "peer", time.Minute)
		require.True(t, ok)
		defer peerRelease()

		svc.SetLeaderLock(cache, nil)
		svc.runScheduledBackup()

		store.mu.Lock()
		require.Empty(t, store.objects, "non-leader must not upload a backup")
		store.mu.Unlock()

		records, err := svc.ListBackups(context.Background())
		require.NoError(t, err)
		require.Empty(t, records, "non-leader must not create a backup record")
		require.Equal(t, "peer", cache.heldBy(backupScheduledLeaderLockKey), "peer keeps the lock")
	})

	t.Run("leader runs and releases", func(t *testing.T) {
		repo := newMockSettingRepo()
		seedS3Config(t, repo)
		store := newMockObjectStore()
		svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("-- dump\n")}, store)

		cache := &fakeLeaderLockCache{}
		svc.SetLeaderLock(cache, nil)
		svc.runScheduledBackup()

		records, err := svc.ListBackups(context.Background())
		require.NoError(t, err)
		require.Len(t, records, 1, "leader creates exactly one backup record")
		require.Equal(t, "completed", records[0].Status)
		require.Empty(t, cache.heldBy(backupScheduledLeaderLockKey), "leader releases the lock when done")
	})

	t.Run("SetLeaderLock on nil receiver is a no-op", func(t *testing.T) {
		// wire.go calls svc.SetLeaderLock(...) unconditionally right after
		// construction; mirror the nil-receiver safety net already covered for
		// the other leader-locked periodic services (see the nilService.SetLeaderLock
		// assertion in TestUpstreamBillingProbeSettingsErrorsNormalizationAndServiceFacade,
		// upstream_billing_probe_service_test.go).
		var nilService *BackupService
		nilService.SetLeaderLock(nil, nil)
	})
}

func TestBackupService_RestoreBackup_Streaming(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 先创建一个备份
	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 恢复
	err = svc.RestoreBackup(context.Background(), record.ID)
	require.NoError(t, err)

	// 验证 psql 收到的数据是否与原始 dump 内容一致
	require.Equal(t, dumpContent, string(dumper.restored))
}

func TestBackupService_RestoreBackup_SplitParts(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumpContent := entropyBackupFixture(512)
	dumper := &mockDumper{}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	compressed := gzipBackupBytes(t, dumpContent)
	parts := splitBackupBytes(compressed, 11)
	recordParts := make([]BackupPart, 0, len(parts))
	for i, data := range parts {
		key := fmt.Sprintf("backups/split-1/payload.part-%06d", i+1)
		store.objects[key] = data
		recordParts = append(recordParts, BackupPart{
			Index:     i + 1,
			S3Key:     key,
			SizeBytes: int64(len(data)),
			SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	record := &BackupRecord{
		ID:        "split-1",
		Status:    "completed",
		Parts:     recordParts,
		SizeBytes: int64(len(compressed)),
	}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	require.NoError(t, svc.RestoreBackup(context.Background(), record.ID))
	require.Equal(t, dumpContent, dumper.restored)
}

func TestBackupService_RestoreBackup_SplitPartsMissingPartDoesNotRestore(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumpContent := entropyBackupFixture(256)
	dumper := &mockDumper{}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	compressed := gzipBackupBytes(t, dumpContent)
	parts := splitBackupBytes(compressed, 11)
	recordParts := make([]BackupPart, 0, len(parts))
	for i, data := range parts {
		key := fmt.Sprintf("backups/split-missing/payload.part-%06d", i+1)
		store.objects[key] = data
		recordParts = append(recordParts, BackupPart{
			Index:     i + 1,
			S3Key:     key,
			SizeBytes: int64(len(data)),
			SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	delete(store.objects, recordParts[1].S3Key)
	record := &BackupRecord{ID: "split-missing", Status: "completed", Parts: recordParts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	require.Error(t, svc.RestoreBackup(context.Background(), record.ID))
	require.Empty(t, dumper.restored)
}

func TestBackupService_DownloadBackupPartsRejectsMismatchedMetadata(t *testing.T) {
	tests := []struct {
		name string
		part BackupPart
		want string
	}{
		{
			name: "size",
			part: BackupPart{Index: 1, S3Key: "backups/mismatch/size", SizeBytes: 4},
			want: "size mismatch",
		},
		{
			name: "checksum",
			part: BackupPart{Index: 1, S3Key: "backups/mismatch/checksum", SizeBytes: 3, SHA256: "bad-checksum"},
			want: "checksum mismatch",
		},
		{
			name: "invalid-index",
			part: BackupPart{Index: 2, S3Key: "backups/mismatch/bad-index", SizeBytes: 3},
			want: "invalid backup part metadata",
		},
		{
			name: "empty-key",
			part: BackupPart{Index: 1, S3Key: "", SizeBytes: 3},
			want: "invalid backup part metadata",
		},
		{
			name: "non-positive-size",
			part: BackupPart{Index: 1, S3Key: "backups/mismatch/zero-size", SizeBytes: 0},
			want: "invalid backup part metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockSettingRepo()
			seedS3Config(t, repo)
			store := newMockObjectStore()
			store.objects[tt.part.S3Key] = []byte("abc")
			svc := newTestBackupService(repo, &mockDumper{}, store)

			_, err := svc.downloadBackupParts(context.Background(), store, []BackupPart{tt.part})
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestBackupService_DownloadBackupParts_EmptyPartsRejected(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	_, err := svc.downloadBackupParts(context.Background(), newMockObjectStore(), nil)
	require.ErrorContains(t, err, "backup parts are empty")
}

// TestBackupService_DownloadBackupParts_CreateTempFailure 通过把 TMPDIR 指向一个不存在的
// 目录，让拼接分卷用的本地临时文件创建失败。
func TestBackupService_DownloadBackupParts_CreateTempFailure(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	t.Setenv("TMPDIR", "/nonexistent-xyz-dir-for-download-test")

	parts := []BackupPart{{Index: 1, S3Key: "backups/x", SizeBytes: 3}}
	_, err := svc.downloadBackupParts(context.Background(), newMockObjectStore(), parts)
	require.ErrorContains(t, err, "create restore archive")
}

// TestBackupService_DownloadBackupParts_ReadFailure 覆盖读取分卷内容中途失败的分支。
func TestBackupService_DownloadBackupParts_ReadFailure(t *testing.T) {
	repo := newMockSettingRepo()
	base := newMockObjectStore()
	store := &erroringDownloadStore{mockObjectStore: base, readErrAtKey: "backups/read-fail/payload.part-000001"}
	svc := newTestBackupService(repo, &mockDumper{}, base)

	parts := []BackupPart{{Index: 1, S3Key: "backups/read-fail/payload.part-000001", SizeBytes: 3}}
	_, err := svc.downloadBackupParts(context.Background(), store, parts)
	require.ErrorContains(t, err, "read backup part")
}

// TestBackupService_DownloadBackupParts_CloseFailure 覆盖分卷 body Close() 失败的分支。
func TestBackupService_DownloadBackupParts_CloseFailure(t *testing.T) {
	repo := newMockSettingRepo()
	base := newMockObjectStore()
	base.objects["backups/close-fail/payload.part-000001"] = []byte("abc")
	store := &erroringDownloadStore{mockObjectStore: base, closeErrAtKey: "backups/close-fail/payload.part-000001"}
	svc := newTestBackupService(repo, &mockDumper{}, base)

	parts := []BackupPart{{Index: 1, S3Key: "backups/close-fail/payload.part-000001", SizeBytes: 3}}
	_, err := svc.downloadBackupParts(context.Background(), store, parts)
	require.ErrorContains(t, err, "close backup part")
}

// TestBackupService_RestoreArchive_OpenError 覆盖 restoreArchive 打开本地归档失败的分支。
func TestBackupService_RestoreArchive_OpenError(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.restoreArchive(context.Background(), "/nonexistent/path.gz")
	require.ErrorContains(t, err, "open restore archive")
}

// TestBackupService_RestoreArchive_GzipReaderError 覆盖归档内容不是合法 gzip 流的分支。
func TestBackupService_RestoreArchive_GzipReaderError(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	path := writeTempBackupFile(t, []byte("not a gzip stream"))

	err := svc.restoreArchive(context.Background(), path)
	require.ErrorContains(t, err, "gzip reader")
}

// TestBackupService_RestoreArchive_DumperRestoreError 覆盖 gzip 流合法但 pg restore 本身
// 失败的分支。
func TestBackupService_RestoreArchive_DumperRestoreError(t *testing.T) {
	repo := newMockSettingRepo()
	dumper := &mockDumper{restErr: fmt.Errorf("restore boom")}
	svc := newTestBackupService(repo, dumper, newMockObjectStore())
	path := writeTempBackupFile(t, gzipBackupBytes(t, []byte("payload")))

	err := svc.restoreArchive(context.Background(), path)
	require.ErrorContains(t, err, "pg restore")
}

func gzipBackupBytes(t *testing.T, content []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	_, err := writer.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return out.Bytes()
}

func splitBackupBytes(data []byte, partSize int) [][]byte {
	parts := make([][]byte, 0, (len(data)+partSize-1)/partSize)
	for len(data) > 0 {
		size := partSize
		if len(data) < size {
			size = len(data)
		}
		parts = append(parts, append([]byte(nil), data[:size]...))
		data = data[size:]
	}
	return parts
}

func TestBackupService_RestoreBackup_NotCompleted(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 手动插入一条 failed 记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:     "fail-1",
		Status: "failed",
	})

	err := svc.RestoreBackup(context.Background(), "fail-1")
	require.Error(t, err)
}

func TestBackupService_DeleteBackup(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "data"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// S3 中应有文件
	store.mu.Lock()
	require.Len(t, store.objects, 1)
	store.mu.Unlock()

	// 删除
	err = svc.DeleteBackup(context.Background(), record.ID)
	require.NoError(t, err)

	// S3 中文件应被删除
	store.mu.Lock()
	require.Len(t, store.objects, 0)
	store.mu.Unlock()

	// 记录应不存在
	_, err = svc.GetBackupRecord(context.Background(), record.ID)
	require.ErrorIs(t, err, ErrBackupNotFound)
}

func TestBackupService_DeleteBackup_RunningKeepsUploadObjects(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	parts := []BackupPart{
		{Index: 1, S3Key: "backups/running/payload.part-000001", SizeBytes: 3},
		{Index: 2, S3Key: "backups/running/payload.part-000002", SizeBytes: 3},
	}
	for _, part := range parts {
		store.objects[part.S3Key] = []byte("abc")
	}
	record := &BackupRecord{ID: "running-delete", Status: "running", Parts: parts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	err := svc.DeleteBackup(context.Background(), record.ID)
	require.ErrorIs(t, err, ErrBackupInProgress)
	store.mu.Lock()
	require.Empty(t, store.deletedKeys)
	for _, part := range parts {
		require.Contains(t, store.objects, part.S3Key)
	}
	store.mu.Unlock()
	got, getErr := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, getErr)
	require.Equal(t, "running", got.Status)
}

func TestBackupService_DeleteBackup_SplitPartsFailureKeepsRecord(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	parts := []BackupPart{
		{Index: 1, S3Key: "backups/split/payload.part-000001", SizeBytes: 3},
		{Index: 2, S3Key: "backups/split/payload.part-000002", SizeBytes: 3},
		{Index: 3, S3Key: "backups/split/payload.part-000003", SizeBytes: 3},
	}
	for _, part := range parts {
		store.objects[part.S3Key] = []byte("abc")
	}
	store.failDeleteKeys[parts[1].S3Key] = fmt.Errorf("delete failed")
	record := &BackupRecord{ID: "split-delete", Status: "completed", Parts: parts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	err := svc.DeleteBackup(context.Background(), record.ID)
	require.Error(t, err)

	store.mu.Lock()
	deleted := append([]string(nil), store.deletedKeys...)
	store.mu.Unlock()
	for _, part := range parts {
		require.Contains(t, deleted, part.S3Key)
	}
	got, getErr := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, getErr)
	require.Equal(t, record.ID, got.ID)
	store.mu.Lock()
	require.Contains(t, store.objects, parts[1].S3Key)
	store.mu.Unlock()
}

func TestBackupService_GetDownloadURL(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &mockDumper{dumpData: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	url, err := svc.GetBackupDownloadURL(context.Background(), record.ID)
	require.NoError(t, err)
	require.Contains(t, url, "https://presigned.example.com/")
}

// GetBackupDownloadURL 目前对拆分为多个分卷的备份不支持单文件下载（需要 handler/frontend
// 一并调整响应结构才能暴露多个分卷 URL，留给后续处理 repository/handler/frontend 的批次）。
// 这里验证的是明确的业务错误，而不是用空 S3Key 去预签名出一个无效链接。
func TestBackupService_GetDownloadURL_SplitParts_ReturnsExplicitUnsupportedError(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	parts := []BackupPart{
		{Index: 2, S3Key: "backups/split/payload.part-000002", SizeBytes: 7},
		{Index: 1, S3Key: "backups/split/payload.part-000001", SizeBytes: 5},
	}
	record := &BackupRecord{ID: "split-download", Status: "completed", Parts: parts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	url, err := svc.GetBackupDownloadURL(context.Background(), record.ID)
	require.Error(t, err)
	require.Empty(t, url)
}

// TestBackupService_GetDownloadURL_EmptyS3KeyReturnsError 覆盖既没有 Parts 也没有
// S3Key 的异常记录（理论上不应出现，但防御性拒绝而不是签出一个无效链接）。
func TestBackupService_GetDownloadURL_EmptyS3KeyReturnsError(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	record := &BackupRecord{ID: "empty-key", Status: "completed"}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	url, err := svc.GetBackupDownloadURL(context.Background(), record.ID)
	require.ErrorContains(t, err, "backup object key is empty")
	require.Empty(t, url)
}

func TestBackupService_CleanupOldBackups_SplitParts(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	now := time.Now()
	parts := []BackupPart{
		{Index: 1, S3Key: "backups/old/payload.part-000001", SizeBytes: 3},
		{Index: 2, S3Key: "backups/old/payload.part-000002", SizeBytes: 3},
	}
	for _, part := range parts {
		store.objects[part.S3Key] = []byte("abc")
	}
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "new",
		Status:    "completed",
		StartedAt: now.Format(time.RFC3339),
	}))
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "old",
		Status:    "completed",
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		Parts:     parts,
	}))

	err := svc.cleanupOldBackups(context.Background(), &BackupScheduleConfig{RetainCount: 1})
	require.NoError(t, err)
	_, err = svc.GetBackupRecord(context.Background(), "old")
	require.ErrorIs(t, err, ErrBackupNotFound)
	store.mu.Lock()
	for _, part := range parts {
		require.NotContains(t, store.objects, part.S3Key)
	}
	store.mu.Unlock()
}

// TestBackupService_CleanupOldBackups_DeleteFailureKeepsRecord 覆盖清理过期备份时
// 对象删除失败应该保留记录（便于重试）并聚合错误的分支。
func TestBackupService_CleanupOldBackups_DeleteFailureKeepsRecord(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	now := time.Now()
	part := BackupPart{Index: 1, S3Key: "backups/cleanup-fail/payload.part-000001", SizeBytes: 3}
	store.objects[part.S3Key] = []byte("abc")
	store.failDeleteKeys[part.S3Key] = fmt.Errorf("delete failed")

	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "new",
		Status:    "completed",
		StartedAt: now.Format(time.RFC3339),
	}))
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "old-fail",
		Status:    "completed",
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		Parts:     []BackupPart{part},
	}))

	err := svc.cleanupOldBackups(context.Background(), &BackupScheduleConfig{RetainCount: 1})
	require.ErrorContains(t, err, "cleanup backup")

	got, getErr := svc.GetBackupRecord(context.Background(), "old-fail")
	require.NoError(t, getErr)
	require.Equal(t, "old-fail", got.ID)
}

// TestBackupService_CleanupOldBackups_SaveAfterCleanupFailure 覆盖成功删除对象后
// 保存清理结果（saveRecordsLocked）本身失败的分支。
func TestBackupService_CleanupOldBackups_SaveAfterCleanupFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	now := time.Now()

	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "new",
		Status:    "completed",
		StartedAt: now.Format(time.RFC3339),
	}))
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "old",
		Status:    "completed",
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
	}))

	repo.failSet(settingKeyBackupRecords, fmt.Errorf("save failed"))

	err := svc.cleanupOldBackups(context.Background(), &BackupScheduleConfig{RetainCount: 1})
	require.ErrorContains(t, err, "save backup records after cleanup")
}

func TestBackupService_ListBackups_Sorted(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = svc.saveRecord(context.Background(), &BackupRecord{
			ID:        fmt.Sprintf("rec-%d", i),
			Status:    "completed",
			StartedAt: now.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}

	records, err := svc.ListBackups(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 3)
	// 最新在前
	require.Equal(t, "rec-2", records[0].ID)
	require.Equal(t, "rec-0", records[2].ID)
}

func TestBackupService_TestS3Connection(t *testing.T) {
	repo := newMockSettingRepo()
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket:          "test",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	})
	require.NoError(t, err)
}

func TestBackupService_TestS3Connection_Incomplete(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.TestS3Connection(context.Background(), BackupS3Config{
		Bucket: "test",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "incomplete")
}

func TestBackupService_Schedule_CronValidation(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	svc.cronSched = nil // 未初始化 cron

	// 启用但 cron 为空
	_, err := svc.UpdateSchedule(context.Background(), BackupScheduleConfig{
		Enabled:  true,
		CronExpr: "",
	})
	require.Error(t, err)

	// 无效的 cron 表达式
	_, err = svc.UpdateSchedule(context.Background(), BackupScheduleConfig{
		Enabled:  true,
		CronExpr: "invalid",
	})
	require.Error(t, err)
}

func TestBackupService_LoadS3Config_Corrupted(t *testing.T) {
	repo := newMockSettingRepo()
	_ = repo.Set(context.Background(), settingKeyBackupS3Config, "not json!!!!")
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	cfg, err := svc.loadS3Config(context.Background())
	require.Error(t, err)
	require.Nil(t, cfg)
}

// ─── Async Backup Tests ───

func TestStartBackup_ReturnsImmediately(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "running", record.Status)
	require.NotEmpty(t, record.ID)

	// 释放 dumper 让后台完成
	close(dumper.blockCh)
	svc.wg.Wait()

	// 验证最终状态
	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.Status)
	require.Greater(t, final.SizeBytes, int64(0))
}

func TestStartBackup_ConcurrentBlocked(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 第一次启动
	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 第二次应被阻塞
	_, err = svc.StartBackup(context.Background(), "manual", 14)
	require.ErrorIs(t, err, ErrBackupInProgress)

	close(dumper.blockCh)
	svc.wg.Wait()
}

func TestStartBackup_ShuttingDown(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{dumpData: []byte("data")}, newMockObjectStore())

	svc.shuttingDown.Store(true)

	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutting down")
}

// TestStartBackup_DumpFailure 覆盖异步备份(executeBackup)里 pg_dump/压缩阶段失败的分支，
// 与同步 CreateBackup 的 DumpFailure 用例分别对应两条独立的错误处理代码路径。
func TestStartBackup_DumpFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumper := &mockDumper{dumpErr: fmt.Errorf("pg_dump failed")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	record, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)
	require.Equal(t, "running", record.Status)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", final.Status)
	require.Contains(t, final.ErrorMsg, "pg_dump")
}

func TestRecoverStaleRecords(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	// 模拟一条孤立的 running 记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "stale-1",
		Status:    "running",
		StartedAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	})
	// 模拟一条孤立的恢复中记录
	_ = svc.saveRecord(context.Background(), &BackupRecord{
		ID:            "stale-2",
		Status:        "completed",
		RestoreStatus: "running",
		StartedAt:     time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	})

	svc.recoverStaleRecords()

	r1, _ := svc.GetBackupRecord(context.Background(), "stale-1")
	require.Equal(t, "failed", r1.Status)
	require.Contains(t, r1.ErrorMsg, "server restart")

	r2, _ := svc.GetBackupRecord(context.Background(), "stale-2")
	require.Equal(t, "failed", r2.RestoreStatus)
	require.Contains(t, r2.RestoreError, "server restart")
}

func TestBackupService_RecoverStaleRecords_CleansBackupObjects(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	parts := []BackupPart{
		{Index: 1, S3Key: "backups/stale/payload.part-000001", SizeBytes: 3},
		{Index: 2, S3Key: "backups/stale/payload.part-000002", SizeBytes: 3},
	}
	for _, part := range parts {
		store.objects[part.S3Key] = []byte("abc")
	}
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "stale-parts",
		Status:    "running",
		Parts:     parts,
		StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}))

	svc.recoverStaleRecords()

	record, err := svc.GetBackupRecord(context.Background(), "stale-parts")
	require.NoError(t, err)
	require.Equal(t, "failed", record.Status)
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, part := range parts {
		require.Contains(t, store.deletedKeys, part.S3Key)
		require.NotContains(t, store.objects, part.S3Key)
	}
}

// TestBackupService_RecoverStaleRecords_LogsSaveFailure 验证恢复孤立记录时，即便保存
// 恢复后状态失败（saveRecoveredRecord 内部错误分支），也只是记日志，不会 panic 或中断恢复流程。
func TestBackupService_RecoverStaleRecords_LogsSaveFailure(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "stale-save-fail",
		Status:    "running",
		StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}))

	repo.failSet(settingKeyBackupRecords, fmt.Errorf("boom"))

	require.NotPanics(t, func() { svc.recoverStaleRecords() })

	// Set 一直失败，保存后的状态没能落盘，读回来仍是恢复前的原始记录。
	rec, err := svc.GetBackupRecord(context.Background(), "stale-save-fail")
	require.NoError(t, err)
	require.Equal(t, "running", rec.Status)
}

func TestBackupService_RecoverStaleRecords_PreservesKeysWhenCleanupFails(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)
	part := BackupPart{Index: 1, S3Key: "backups/stale-failed/payload.part-000001", SizeBytes: 3}
	store.objects[part.S3Key] = []byte("abc")
	store.failDeleteKeys[part.S3Key] = fmt.Errorf("delete failed")
	require.NoError(t, svc.saveRecord(context.Background(), &BackupRecord{
		ID:        "stale-cleanup-failed",
		Status:    "running",
		Parts:     []BackupPart{part},
		StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}))

	svc.recoverStaleRecords()

	record, err := svc.GetBackupRecord(context.Background(), "stale-cleanup-failed")
	require.NoError(t, err)
	require.Equal(t, "failed", record.Status)
	require.Contains(t, record.ErrorMsg, "cleanup failed")
	require.Equal(t, part.S3Key, record.Parts[0].S3Key)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Contains(t, store.objects, part.S3Key)
}

func TestGracefulShutdown(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumper := &blockingDumper{blockCh: make(chan struct{}), data: []byte("data")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	_, err := svc.StartBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// Stop 应该等待备份完成
	done := make(chan struct{})
	go func() {
		svc.Stop()
		close(done)
	}()

	// 短暂等待确认 Stop 还在等待
	select {
	case <-done:
		t.Fatal("Stop returned before backup finished")
	case <-time.After(100 * time.Millisecond):
		// 预期：Stop 还在等待
	}

	// 释放备份
	close(dumper.blockCh)

	// 现在 Stop 应该完成
	select {
	case <-done:
		// 预期
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after backup finished")
	}
}

func TestStartRestore_Async(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)

	dumpContent := "-- PostgreSQL dump\nCREATE TABLE test (id int);\n"
	dumper := &mockDumper{dumpData: []byte(dumpContent)}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	// 先创建一个备份（同步方式）
	record, err := svc.CreateBackup(context.Background(), "manual", 14)
	require.NoError(t, err)

	// 异步恢复
	restored, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "running", restored.RestoreStatus)

	svc.wg.Wait()

	// 验证最终状态
	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.RestoreStatus)
}

func TestBackupService_StartRestore_SplitParts(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumpContent := entropyBackupFixture(384)
	dumper := &mockDumper{}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	compressed := gzipBackupBytes(t, dumpContent)
	parts := splitBackupBytes(compressed, 13)
	recordParts := make([]BackupPart, 0, len(parts))
	for i, data := range parts {
		key := fmt.Sprintf("backups/split-async/payload.part-%06d", i+1)
		store.objects[key] = data
		recordParts = append(recordParts, BackupPart{
			Index:     i + 1,
			S3Key:     key,
			SizeBytes: int64(len(data)),
			SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	record := &BackupRecord{ID: "split-async", Status: "completed", Parts: recordParts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	started, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "running", started.RestoreStatus)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", final.RestoreStatus)
	require.Equal(t, dumpContent, dumper.restored)
}

// TestBackupService_StartRestore_SplitParts_DownloadFailure 覆盖异步恢复(executeRestore)
// 分卷路径里下载某个分卷失败的分支——与同步 RestoreBackup 的等价用例是两条独立代码路径。
func TestBackupService_StartRestore_SplitParts_DownloadFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	store := newMockObjectStore()
	svc := newTestBackupService(repo, &mockDumper{}, store)

	parts := []BackupPart{{Index: 1, S3Key: "backups/async-missing/payload.part-000001", SizeBytes: 3}}
	// 故意不在 store 中放入对应对象，触发 Download 失败
	record := &BackupRecord{ID: "async-missing", Status: "completed", Parts: parts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	started, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "running", started.RestoreStatus)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", final.RestoreStatus)
	require.Contains(t, final.RestoreError, "download backup part")
}

// TestBackupService_StartRestore_SplitParts_RestoreFailure 覆盖异步恢复分卷路径里
// pg restore 本身失败的分支。
func TestBackupService_StartRestore_SplitParts_RestoreFailure(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumper := &mockDumper{restErr: fmt.Errorf("pg restore boom")}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	dumpContent := entropyBackupFixture(64)
	compressed := gzipBackupBytes(t, dumpContent)
	parts := splitBackupBytes(compressed, 11)
	recordParts := make([]BackupPart, 0, len(parts))
	for i, data := range parts {
		key := fmt.Sprintf("backups/async-restore-fail/payload.part-%06d", i+1)
		store.objects[key] = data
		recordParts = append(recordParts, BackupPart{
			Index:     i + 1,
			S3Key:     key,
			SizeBytes: int64(len(data)),
			SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	record := &BackupRecord{ID: "async-restore-fail", Status: "completed", Parts: recordParts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	started, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "running", started.RestoreStatus)
	svc.wg.Wait()

	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", final.RestoreStatus)
	require.Contains(t, final.RestoreError, "pg restore")
}

// TestBackupService_StartRestore_SplitParts_FinalSaveFailureIsLogged 覆盖分卷恢复
// 成功后、保存 completed 状态失败时只记日志不 panic 的分支。
func TestBackupService_StartRestore_SplitParts_FinalSaveFailureIsLogged(t *testing.T) {
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumper := &mockDumper{}
	store := newMockObjectStore()
	svc := newTestBackupService(repo, dumper, store)

	dumpContent := entropyBackupFixture(64)
	compressed := gzipBackupBytes(t, dumpContent)
	parts := splitBackupBytes(compressed, 11)
	recordParts := make([]BackupPart, 0, len(parts))
	for i, data := range parts {
		key := fmt.Sprintf("backups/async-save-fail/payload.part-%06d", i+1)
		store.objects[key] = data
		recordParts = append(recordParts, BackupPart{
			Index:     i + 1,
			S3Key:     key,
			SizeBytes: int64(len(data)),
			SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	record := &BackupRecord{ID: "async-save-fail", Status: "completed", Parts: recordParts}
	require.NoError(t, svc.saveRecord(context.Background(), record))

	repo.failSet(settingKeyBackupRecords, fmt.Errorf("boom"))

	started, err := svc.StartRestore(context.Background(), record.ID)
	require.NoError(t, err) // StartRestore 里保存 running 状态失败的错误被忽略
	require.Equal(t, "running", started.RestoreStatus)
	svc.wg.Wait()

	// Set 一直失败，completed 状态没能落盘；读回来的仍是保存失败前的原始记录。
	final, err := svc.GetBackupRecord(context.Background(), record.ID)
	require.NoError(t, err)
	require.Equal(t, "", final.RestoreStatus)
}

// ─── backupObjectKeys / deleteBackupObjects 直接单测 ───

func TestBackupObjectKeys_NilRecordReturnsNil(t *testing.T) {
	require.Nil(t, backupObjectKeys(nil))
}

func TestBackupObjectKeys_DeduplicatesRepeatedKeys(t *testing.T) {
	record := &BackupRecord{
		S3Key: "backups/dup/payload.part-000001",
		Parts: []BackupPart{
			{Index: 1, S3Key: "backups/dup/payload.part-000001"},
			{Index: 2, S3Key: "backups/dup/payload.part-000001"}, // 与 S3Key 及第一个分卷重复
		},
	}
	keys := backupObjectKeys(record)
	require.Equal(t, []string{"backups/dup/payload.part-000001"}, keys)
}

// TestBackupService_DeleteBackupObjects_NoKeysReturnsNilWithoutS3Lookup 覆盖记录既没有
// S3Key 也没有 Parts 时提前返回、不查 S3 配置的分支。
func TestBackupService_DeleteBackupObjects_NoKeysReturnsNilWithoutS3Lookup(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.deleteBackupObjects(context.Background(), &BackupRecord{ID: "no-keys"})
	require.NoError(t, err)
}

// TestBackupService_DeleteBackupObjects_LoadS3ConfigError 覆盖 S3 配置数据损坏导致
// loadS3Config 报错时的分支。
func TestBackupService_DeleteBackupObjects_LoadS3ConfigError(t *testing.T) {
	repo := newMockSettingRepo()
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupS3Config, "not json!!!"))
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.deleteBackupObjects(context.Background(), &BackupRecord{ID: "corrupt-cfg", S3Key: "backups/corrupt.sql.gz"})
	require.ErrorIs(t, err, ErrBackupS3ConfigCorrupt)
}

// TestBackupService_DeleteBackupObjects_NoS3ConfigSkipsDeletion 覆盖旧记录带 S3Key
// 但完全没有配置对象存储时，兼容性跳过删除的分支。
func TestBackupService_DeleteBackupObjects_NoS3ConfigSkipsDeletion(t *testing.T) {
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())

	err := svc.deleteBackupObjects(context.Background(), &BackupRecord{ID: "legacy", S3Key: "backups/legacy.sql.gz"})
	require.NoError(t, err)
}

func TestDeleteBackupObjectKeys_EmptyKeysNoOp(t *testing.T) {
	store := newMockObjectStore()

	err := deleteBackupObjectKeys(context.Background(), store, &BackupRecord{})
	require.NoError(t, err)
	require.Empty(t, store.deletedKeys)
}
