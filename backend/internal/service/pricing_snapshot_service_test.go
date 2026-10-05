//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---- 夹具 ----

type pssSettings struct {
	mu     sync.Mutex
	values map[string]string
	getErr error
	setErr error
	sets   []string
}

func newPSSSettings(mode string) *pssSettings {
	s := &pssSettings{values: map[string]string{}}
	if mode != "" {
		s.values[SettingKeyPricingSnapshotMode] = mode
	}
	return s
}

func (s *pssSettings) setMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[SettingKeyPricingSnapshotMode] = mode
}

func (s *pssSettings) Get(_ context.Context, key string) (*Setting, error) {
	v, err := s.GetValue(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: v}, nil
}

func (s *pssSettings) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	v, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (s *pssSettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	s.sets = append(s.sets, key+"="+value)
	return nil
}

func (s *pssSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unused")
}
func (s *pssSettings) SetMultiple(context.Context, map[string]string) error {
	return errors.New("unused")
}
func (s *pssSettings) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unused")
}
func (s *pssSettings) Delete(context.Context, string) error { return errors.New("unused") }

type pssStore struct {
	mu            sync.Mutex
	active        *PricingSnapshotMeta
	payloads      map[int64][]byte
	getActiveErr  error
	getPayloadErr error
	activateErr   error
	activated     []NewPricingSnapshot
	metaCalls     int
	nextID        int64
}

func newPSSStore() *pssStore {
	return &pssStore{payloads: map[int64][]byte{}, nextID: 100}
}

func pssSHA(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (f *pssStore) setActive(id int64, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = &PricingSnapshotMeta{ID: id, Label: fmt.Sprintf("snap-%d", id), Source: PricingSnapshotSourceBootstrap,
		ContentSHA256: pssSHA(payload), Status: PricingSnapshotStatusActive, FetchedAt: time.Unix(1700000000, 0)}
	f.payloads[id] = payload
}

func (f *pssStore) GetActiveMeta(context.Context) (*PricingSnapshotMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metaCalls++
	if f.getActiveErr != nil {
		return nil, f.getActiveErr
	}
	if f.active == nil {
		return nil, ErrPricingSnapshotNotFound
	}
	m := *f.active
	return &m, nil
}

func (f *pssStore) GetPayload(_ context.Context, id int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getPayloadErr != nil {
		return nil, f.getPayloadErr
	}
	p, ok := f.payloads[id]
	if !ok {
		return nil, ErrPricingSnapshotNotFound
	}
	return p, nil
}

func (f *pssStore) ActivateNew(_ context.Context, in NewPricingSnapshot) (*PricingSnapshotMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activateErr != nil {
		return nil, f.activateErr
	}
	f.nextID++
	f.activated = append(f.activated, in)
	f.payloads[f.nextID] = in.Payload
	f.active = &PricingSnapshotMeta{ID: f.nextID, Label: in.Label, Source: in.Source, ContentSHA256: in.ContentSHA256,
		ModelCount: in.ModelCount, Status: PricingSnapshotStatusActive, FetchedAt: time.Unix(1700000000, 0)}
	m := *f.active
	return &m, nil
}

type pssPubSub struct {
	mu       sync.Mutex
	handler  func()
	notified int
}

func (p *pssPubSub) NotifyUpdate(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notified++
	return nil
}

func (p *pssPubSub) SubscribeUpdates(_ context.Context, handler func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = handler
}

func (p *pssPubSub) fire() {
	p.mu.Lock()
	h := p.handler
	p.mu.Unlock()
	if h != nil {
		h()
	}
}

func (p *pssPubSub) notifications() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.notified
}

// pssPayload 生成一份最小的 LiteLLM 价格 JSON；gptInput 是 gpt-5.1 的输入单价，用来区分两份快照。
func pssPayload(gptInput string) []byte {
	return []byte(fmt.Sprintf(`{
  "sample_spec": {"input_cost_per_token": 0},
  "gpt-5.1": {"input_cost_per_token": %s, "output_cost_per_token": 0.00001, "litellm_provider": "openai", "mode": "chat"},
  "claude-sonnet-4": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "cache_read_input_token_cost": 3e-7, "litellm_provider": "anthropic", "mode": "chat"},
  "gemini-2.5-pro": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001, "litellm_provider": "vertex_ai", "mode": "chat"}
}`, gptInput))
}

var pssModelNames = []string{"gpt-5.1", "gpt-5.1-2026-01-01", "claude-sonnet-4", "claude-sonnet-4-20250514", "gemini-2.5-pro", "gpt-5.6-sol", "no-such-model"}

func newPSSService(t *testing.T, dir string) *PricingService {
	t.Helper()
	return NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dir, UpdateIntervalHours: 24 * 365}}, nil)
}

func pssWriteFile(t *testing.T, dir string, payload []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model_pricing.json"), payload, 0o644))
}

func pssLookups(svc *PricingService) map[string]*LiteLLMModelPricing {
	out := map[string]*LiteLLMModelPricing{}
	for _, name := range pssModelNames {
		out[name] = svc.GetModelPricing(name)
	}
	return out
}

func pssInputCost(t *testing.T, svc *PricingService) float64 {
	t.Helper()
	p := svc.GetModelPricing("gpt-5.1")
	require.NotNil(t, p)
	return p.InputCostPerToken
}

// ---- auto 模式：零行为变化 ----

func TestPricingSnapshot_AutoModeMatchesServiceWithoutSnapshots(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))

	plain := newPSSService(t, dir)
	require.NoError(t, plain.Initialize())
	defer plain.Stop()

	store := newPSSStore()
	snap := newPSSService(t, dir)
	snap.ConfigureSnapshots(newPSSSettings(""), store, &pssPubSub{})
	require.NoError(t, snap.Initialize())
	defer snap.Stop()

	require.False(t, snap.isPinned())
	require.Equal(t, pssLookups(plain), pssLookups(snap), "auto 模式下价格查找结果必须与没有快照存储时完全一致")
	require.Equal(t, plain.GetStatus()["model_count"], snap.GetStatus()["model_count"])
	require.Zero(t, store.metaCalls, "auto 模式不访问快照表")
	require.Empty(t, store.activated)
}

func TestPricingSnapshot_AutoModeExplicitValueAndBlankAreAuto(t *testing.T) {
	for _, value := range []string{"auto", "  AUTO ", ""} {
		dir := t.TempDir()
		pssWriteFile(t, dir, pssPayload("0.000001"))
		svc := newPSSService(t, dir)
		settings := newPSSSettings("")
		settings.values[SettingKeyPricingSnapshotMode] = value
		svc.ConfigureSnapshots(settings, newPSSStore(), nil)
		require.NoError(t, svc.Initialize(), value)
		require.False(t, svc.isPinned(), value)
		svc.Stop()
	}
}

func TestPricingSnapshot_AutoModeStillDownloadsAndSwapsMemory(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	remote := pssPayload("0.000002")
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
		json: func(context.Context) ([]byte, error) { return remote, nil },
	}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: dir, RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 24 * 365,
	}}, client)
	svc.ConfigureSnapshots(newPSSSettings(""), newPSSStore(), nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
	require.NoError(t, svc.ForceUpdate())
	require.InDelta(t, 0.000002, pssInputCost(t, svc), 1e-12, "auto 模式的下载照旧换入内存")
	b, err := os.ReadFile(filepath.Join(dir, "model_pricing.json"))
	require.NoError(t, err)
	require.Equal(t, remote, b)
}

func TestPricingSnapshot_ConfigureWithMissingDependenciesKeepsPlainBehavior(t *testing.T) {
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(nil, newPSSStore(), nil)
	require.Nil(t, svc.snap)
	svc.ConfigureSnapshots(newPSSSettings(""), nil, nil)
	require.Nil(t, svc.snap)
	var nilSvc *PricingService
	nilSvc.ConfigureSnapshots(newPSSSettings(""), newPSSStore(), nil)
	require.False(t, nilSvc.isPinned())
	require.False(t, svc.isPinned())

	_, err := svc.PinCurrentPricing(context.Background(), 1)
	require.ErrorIs(t, err, ErrPricingSnapshotsUnavailable)
	svc.stopSnapshots() // snap 为 nil 时是空操作
}

// ---- pinned 模式：启动加载与 fail-closed ----

func TestPricingSnapshot_PinnedStartupLoadsActiveSnapshotNotTheFile(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000009")) // 文件里是另一份价格，pinned 时不能读它
	store := newPSSStore()
	store.setActive(5, pssPayload("0.000001"))

	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, &pssPubSub{})
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	require.True(t, svc.isPinned())
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
	require.Equal(t, int64(5), svc.snap.activeID)
	require.Equal(t, 3, svc.GetStatus()["model_count"])
}

func TestPricingSnapshot_PinnedStartupFailuresAreFailClosed(t *testing.T) {
	good := pssPayload("0.000001")
	cases := []struct {
		name  string
		setup func(*pssSettings, *pssStore)
	}{
		{"no active snapshot", func(_ *pssSettings, _ *pssStore) {}},
		{"meta query fails", func(_ *pssSettings, st *pssStore) { st.getActiveErr = errors.New("db down") }},
		{"payload query fails", func(_ *pssSettings, st *pssStore) {
			st.setActive(1, good)
			st.getPayloadErr = errors.New("db down")
		}},
		{"payload hash mismatch", func(_ *pssSettings, st *pssStore) {
			st.setActive(1, good)
			st.payloads[1] = pssPayload("0.000002")
		}},
		{"payload not json", func(_ *pssSettings, st *pssStore) {
			bad := []byte("not json")
			st.setActive(1, bad)
		}},
		{"payload has no prices", func(_ *pssSettings, st *pssStore) {
			st.setActive(1, []byte(`{"sample_spec": {}}`))
		}},
		{"mode read fails", func(se *pssSettings, st *pssStore) {
			st.setActive(1, good)
			se.getErr = errors.New("db down")
		}},
		{"mode value is corrupt", func(se *pssSettings, st *pssStore) {
			st.setActive(1, good)
			se.setMode("pinnd")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pssWriteFile(t, dir, pssPayload("0.000009")) // 文件可用也不能退回去
			settings, store := newPSSSettings("pinned"), newPSSStore()
			tc.setup(settings, store)
			svc := newPSSService(t, dir)
			svc.ConfigureSnapshots(settings, store, nil)
			err := svc.Initialize()
			require.ErrorIs(t, err, ErrPricingSnapshotStartup)
			require.Zero(t, svc.GetStatus()["model_count"], "加载失败时不能退回文件或内置数据")
			require.False(t, svc.isPinned())
		})
	}
}

func TestProvidePricingService_FailsClosedOnlyForSnapshotStartup(t *testing.T) {
	cfg := &config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir(), FallbackFile: filepath.Join(t.TempDir(), "missing.json")}}

	svc, err := ProvidePricingService(cfg, nil, newPSSSettings("pinned"), newPSSStore(), nil)
	require.ErrorIs(t, err, ErrPricingSnapshotStartup)
	require.Nil(t, svc)

	// auto 模式：没有文件、没有兜底文件时 Initialize 也失败，但保持原有行为——只告警，不阻止启动。
	svc, err = ProvidePricingService(cfg, nil, newPSSSettings(""), newPSSStore(), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
	svc.Stop()
}

// ---- 运行时重载 ----

func TestPricingSnapshot_RuntimeReloadFailureKeepsCurrentSnapshot(t *testing.T) {
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	settings := newPSSSettings("pinned")
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	ctx := context.Background()

	// 新的生效快照损坏、查询失败、模式读取失败：都保留 A。
	store.setActive(2, pssPayload("0.000002"))
	store.payloads[2] = []byte("garbage")
	require.Error(t, svc.refreshSnapshotState(ctx))
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)

	store.getActiveErr = errors.New("db down")
	require.Error(t, svc.refreshSnapshotState(ctx))
	store.getActiveErr = nil
	settings.getErr = errors.New("db down")
	require.Error(t, svc.refreshSnapshotState(ctx))
	settings.getErr = nil
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
	require.True(t, svc.isPinned())
	require.Equal(t, int64(1), svc.snap.activeID)

	// 数据库恢复后收敛到 B；同一份再次重载不重复读取 payload。
	store.setActive(2, pssPayload("0.000002"))
	require.NoError(t, svc.refreshSnapshotState(ctx))
	require.InDelta(t, 0.000002, pssInputCost(t, svc), 1e-12)
	require.Equal(t, int64(2), svc.snap.activeID)
	store.getPayloadErr = errors.New("must not be read again")
	require.NoError(t, svc.refreshSnapshotState(ctx))
}

func TestPricingSnapshot_PubSubNotificationTriggersReload(t *testing.T) {
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	ps := &pssPubSub{}
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, ps)
	svc.snap.pollInterval = time.Hour
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	store.setActive(2, pssPayload("0.000002"))
	ps.fire()
	ps.fire() // 重复通知被合并，不会阻塞
	require.Eventually(t, func() bool { return pssInputCost(t, svc) == 0.000002 }, 3*time.Second, 5*time.Millisecond)
}

func TestPricingSnapshot_PollingConvergesWithoutNotification(t *testing.T) {
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
	svc.snap.pollInterval = 10 * time.Millisecond
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	store.setActive(2, pssPayload("0.000002"))
	require.Eventually(t, func() bool { return pssInputCost(t, svc) == 0.000002 }, 3*time.Second, 5*time.Millisecond)

	// 轮询里的失败只告警，当前快照照常服务。
	store.getActiveErr = errors.New("db down")
	time.Sleep(50 * time.Millisecond)
	require.InDelta(t, 0.000002, pssInputCost(t, svc), 1e-12)
}

func TestPricingSnapshot_AutoToPinnedAtRuntimeLoadsSnapshotAndBackToAuto(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	settings := newPSSSettings("")
	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	ctx := context.Background()
	require.False(t, svc.isPinned())

	// 另一个实例完成了固定：本实例读到 pinned 与生效快照。
	store.setActive(3, pssPayload("0.000001"))
	settings.setMode("pinned")
	require.NoError(t, svc.refreshSnapshotState(ctx))
	require.True(t, svc.isPinned())

	// 模式改回 auto：不再 pinned，内存数据保持到下一次远程同步。
	settings.setMode("auto")
	require.NoError(t, svc.refreshSnapshotState(ctx))
	require.False(t, svc.isPinned())
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
}

// ---- pinned 模式下旧的更新通道被拦住 ----

func TestPricingSnapshot_PinnedBlocksRemoteDownloadIntoLiveData(t *testing.T) {
	calls := 0
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
		json: func(context.Context) ([]byte, error) {
			calls++
			return pssPayload("0.000099"), nil
		},
	}
	dir := t.TempDir()
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: dir, RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 1,
	}}, client)
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	require.ErrorIs(t, svc.ForceUpdate(), ErrPricingPinned)
	require.NoError(t, svc.syncWithRemote(), "pinned 时定时同步是空操作")
	require.Zero(t, calls, "pinned 时不访问远程")

	// 下载进行到一半才变成 pinned：解析之后、写文件之前被拦下。
	svc.mu.Lock()
	svc.snap.pinned = false
	svc.mu.Unlock()
	client.json = func(context.Context) ([]byte, error) {
		svc.mu.Lock()
		svc.snap.pinned = true
		svc.mu.Unlock()
		return pssPayload("0.000099"), nil
	}
	svc.remoteClient = client
	require.ErrorIs(t, svc.downloadPricingData(), ErrPricingPinned)
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
	_, err := os.Stat(filepath.Join(dir, "model_pricing.json"))
	require.True(t, os.IsNotExist(err), "pinned 时不改写价格文件")
}

// ---- 启动引导：固定当前价格 ----

func TestPricingSnapshot_PinCurrentPricingImportsFileAndKeepsPricesIdentical(t *testing.T) {
	dir := t.TempDir()
	file := pssPayload("0.000001")
	pssWriteFile(t, dir, file)
	store := newPSSStore()
	settings := newPSSSettings("")
	ps := &pssPubSub{}

	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(settings, store, ps)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	before := pssLookups(svc)

	res, err := svc.PinCurrentPricing(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, PricingSnapshotSourceBootstrap, res.Snapshot.Source)
	require.Len(t, store.activated, 1)
	in := store.activated[0]
	require.Equal(t, file, in.Payload, "导入的是价格文件原文")
	require.Equal(t, pssSHA(file), in.ContentSHA256)
	require.Equal(t, 3, in.ModelCount)
	require.Equal(t, int64(42), *in.ApprovedBy)
	require.Equal(t, int64(42), *in.FetchedBy)
	require.Contains(t, in.Label, "bootstrap-")
	require.Contains(t, in.Label, pssSHA(file)[:8])
	require.Equal(t, []string{SettingKeyPricingSnapshotMode + "=pinned"}, settings.sets)
	require.Equal(t, 1, ps.notifications())
	require.True(t, svc.isPinned())
	require.Equal(t, before, pssLookups(svc), "切换瞬间内存价格不变")

	// 模拟重启：新实例没有价格文件，只能读快照，结果与切换前逐个相同。
	restarted := newPSSService(t, t.TempDir())
	restarted.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, restarted.Initialize())
	defer restarted.Stop()
	require.True(t, restarted.isPinned())
	require.Equal(t, before, pssLookups(restarted), "重启后从快照加载的价格与切换前完全相同")

	// 已是 pinned 时再次固定被拒绝。
	_, err = svc.PinCurrentPricing(context.Background(), 42)
	require.ErrorIs(t, err, ErrPricingAlreadyPinned)
}

func TestPricingSnapshot_PinCurrentPricingReusesIdenticalActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	file := pssPayload("0.000001")
	pssWriteFile(t, dir, file)
	store := newPSSStore()
	store.setActive(7, file)
	settings := newPSSSettings("")
	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	res, err := svc.PinCurrentPricing(context.Background(), 0)
	require.NoError(t, err)
	require.False(t, res.Created)
	require.Equal(t, int64(7), res.Snapshot.ID)
	require.Empty(t, store.activated)
	require.Equal(t, int64(7), svc.snap.activeID)
	require.Equal(t, "pinned", settings.values[SettingKeyPricingSnapshotMode])
}

func TestPricingSnapshot_PinCurrentPricingReplacesDifferentActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	store.setActive(7, pssPayload("0.000005")) // 旧的生效快照与线上数据不同
	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(newPSSSettings(""), store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	res, err := svc.PinCurrentPricing(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Len(t, store.activated, 1)
}

func TestPricingSnapshot_PinCurrentPricingRefusesWhenFileDiffersFromMemory(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	settings := newPSSSettings("")
	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	pssWriteFile(t, dir, pssPayload("0.000002")) // 文件被换掉，但内存还是旧价
	_, err := svc.PinCurrentPricing(context.Background(), 1)
	require.ErrorIs(t, err, ErrPricingBootstrapMismatch)
	require.False(t, svc.isPinned())
	require.Empty(t, store.activated)
	require.Empty(t, settings.sets)
}

func TestPricingSnapshot_PinCurrentPricingFailuresRollBackToAuto(t *testing.T) {
	setup := func(t *testing.T) (*PricingService, *pssSettings, *pssStore) {
		dir := t.TempDir()
		pssWriteFile(t, dir, pssPayload("0.000001"))
		store, settings := newPSSStore(), newPSSSettings("")
		svc := newPSSService(t, dir)
		svc.ConfigureSnapshots(settings, store, nil)
		require.NoError(t, svc.Initialize())
		t.Cleanup(svc.Stop)
		return svc, settings, store
	}
	ctx := context.Background()

	t.Run("price file unreadable", func(t *testing.T) {
		svc, _, _ := setup(t)
		require.NoError(t, os.Remove(svc.getPricingFilePath()))
		_, err := svc.PinCurrentPricing(ctx, 1)
		require.Error(t, err)
		require.False(t, svc.isPinned())
	})
	t.Run("price file not parseable", func(t *testing.T) {
		svc, _, _ := setup(t)
		require.NoError(t, os.WriteFile(svc.getPricingFilePath(), []byte("[]"), 0o644))
		_, err := svc.PinCurrentPricing(ctx, 1)
		require.Error(t, err)
		require.False(t, svc.isPinned())
	})
	t.Run("active snapshot lookup fails", func(t *testing.T) {
		svc, settings, store := setup(t)
		store.getActiveErr = errors.New("db down")
		_, err := svc.PinCurrentPricing(ctx, 1)
		require.Error(t, err)
		require.False(t, svc.isPinned())
		require.Empty(t, settings.sets)
	})
	t.Run("activation fails", func(t *testing.T) {
		svc, settings, store := setup(t)
		store.activateErr = ErrPricingSnapshotConflict
		_, err := svc.PinCurrentPricing(ctx, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotConflict)
		require.False(t, svc.isPinned())
		require.Empty(t, settings.sets)
	})
	t.Run("mode write fails", func(t *testing.T) {
		svc, settings, _ := setup(t)
		settings.setErr = errors.New("db down")
		_, err := svc.PinCurrentPricing(ctx, 1)
		require.Error(t, err)
		require.False(t, svc.isPinned(), "模式没写成，必须回到 auto，下载通道不能被永久拦住")
		require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
	})
}

func TestPricingSnapshot_OptionalAdminID(t *testing.T) {
	require.Nil(t, optionalAdminID(0))
	require.Nil(t, optionalAdminID(-3))
	require.Equal(t, int64(9), *optionalAdminID(9))
}
