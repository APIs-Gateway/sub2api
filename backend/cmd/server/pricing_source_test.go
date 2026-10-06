package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDBSettingReader_GetValue(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := dbSettingReader{db: db}

	mock.ExpectQuery(`SELECT value FROM settings WHERE key = \$1`).WithArgs(service.SettingKeyPricingSnapshotMode).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("pinned"))
	v, err := r.GetValue(context.Background(), service.SettingKeyPricingSnapshotMode)
	require.NoError(t, err)
	require.Equal(t, "pinned", v)

	mock.ExpectQuery(`SELECT value FROM settings`).WillReturnRows(sqlmock.NewRows([]string{"value"}))
	_, err = r.GetValue(context.Background(), "missing")
	require.ErrorIs(t, err, service.ErrSettingNotFound)

	mock.ExpectQuery(`SELECT value FROM settings`).WillReturnError(errors.New("boom"))
	_, err = r.GetValue(context.Background(), "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrSettingNotFound, "读失败不能当成没设置而按 auto 处理")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveDataDirPricingFile_NeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	fallback := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(fallback, []byte("{}"), 0o600))
	cfg := &config.Config{}
	cfg.Pricing.FallbackFile = fallback
	cfg.Pricing.DataDir = filepath.Join(dir, "missing")

	got, err := resolveDataDirPricingFile("explicit.json", cfg)
	require.NoError(t, err)
	require.Equal(t, "explicit.json", got)

	_, err = resolveDataDirPricingFile("", cfg)
	require.Error(t, err, "数据目录没有价格文件时不退回内置文件")

	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	local := filepath.Join(dataDir, "model_pricing.json")
	require.NoError(t, os.WriteFile(local, []byte("{}"), 0o600))
	cfg.Pricing.DataDir = dataDir
	got, err = resolveDataDirPricingFile("", cfg)
	require.NoError(t, err)
	require.Equal(t, local, got)
}

func TestLoadServicePricing_PinnedModeReadsSnapshotThroughTheDatabase(t *testing.T) {
	// 设置表读到 pinned，但没有生效快照：必须报错，不能退回数据目录里的文件。
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model_pricing.json"), []byte(`{"gpt-5.1":{"input_cost_per_token":1e-6,"litellm_provider":"openai","mode":"chat"}}`), 0o600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir = dir

	mock.ExpectQuery(`SELECT value FROM settings`).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("pinned"))
	mock.ExpectQuery(`pricing_snapshots`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, _, err = loadServicePricing(context.Background(), db, cfg, "", true)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDescribePricingSource(t *testing.T) {
	require.Contains(t, describePricingSource(service.PricingDataInfo{Source: service.PricingSourceSnapshot, SnapshotID: 7, SHA256: "abcdef0123456789"}), "snapshot id=7 sha256=abcdef012345")
	require.Contains(t, describePricingSource(service.PricingDataInfo{Source: service.PricingSourceFile, Path: "/d/model_pricing.json"}), "file path=/d/model_pricing.json")
}

func TestLoadServicePricing_ExplicitFileIsAnOverrideAndSkipsTheDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	path := filepath.Join(t.TempDir(), "p.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"gpt-5.1":{"input_cost_per_token":1e-6,"output_cost_per_token":1e-5,"litellm_provider":"openai","mode":"chat"}}`), 0o600))

	_, info, err := loadServicePricing(context.Background(), db, &config.Config{}, path, false)
	require.NoError(t, err)
	require.Equal(t, service.PricingSourceFile, info.Source)
	require.Equal(t, path, info.Path)
	require.NoError(t, mock.ExpectationsWereMet(), "显式覆盖不读设置与快照表")
}
