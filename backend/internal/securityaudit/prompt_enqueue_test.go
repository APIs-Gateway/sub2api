package securityaudit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type enqueueTestConfig struct {
	active ActiveConfig
	ok     bool
}

func (c *enqueueTestConfig) Start(context.Context) error      { return nil }
func (c *enqueueTestConfig) Shutdown(context.Context) error   { return nil }
func (c *enqueueTestConfig) Active() (ActiveConfig, bool)     { return c.active, c.ok }
func (c *enqueueTestConfig) EffectiveMode() Mode              { return c.active.EffectiveMode() }
func (c *enqueueTestConfig) BlockingActivationDegraded() bool { return false }
func (c *enqueueTestConfig) Public() PublicConfig {
	return PublicFromStorage(DefaultStorageConfig(), c.active.RiskControlEnabled, nil)
}
func (c *enqueueTestConfig) Save(context.Context, UpdateConfigRequest, int64) (PublicConfig, error) {
	return PublicConfig{}, nil
}
func (c *enqueueTestConfig) RuntimeState() (int64, int64, *time.Time, string) {
	return c.active.ConfigVersion, c.active.ConfigVersion, nil, ""
}
func (c *enqueueTestConfig) Encrypt(value string) (string, error) { return value, nil }
func (c *enqueueTestConfig) Decrypt(value string) (string, error) { return value, nil }

type enqueueTestRepo struct {
	createErr  error
	create     func(PromptSnapshot) *Job
	publishErr error
	markErr    error
	created    int
	marked     int
}

func (r *enqueueTestRepo) CreateStagingWithCapacity(_ context.Context, snapshot PromptSnapshot, version int64, maxAttempts, capacity int) (*Job, error) {
	r.created++
	if r.createErr != nil {
		return nil, r.createErr
	}
	if r.create != nil {
		return r.create(snapshot), nil
	}
	return &Job{ID: 7, Snapshot: snapshot, ConfigVersion: version, MaxAttempts: maxAttempts}, nil
}
func (r *enqueueTestRepo) PublishQueued(context.Context, int64) error { return r.publishErr }
func (r *enqueueTestRepo) MarkStagingFailed(context.Context, int64, string, string) error {
	r.marked++
	return r.markErr
}
func (*enqueueTestRepo) ClaimNextJob(context.Context, time.Time) (*Job, bool, error) {
	return nil, false, nil
}
func (*enqueueTestRepo) RefreshLease(context.Context, int64, int64, time.Time) error { return nil }
func (*enqueueTestRepo) Complete(context.Context, *Job, *NormalizedResult, bool) (*Event, error) {
	return nil, nil
}
func (*enqueueTestRepo) Retry(context.Context, int64, int64, time.Time, string, string) error {
	return nil
}
func (*enqueueTestRepo) Fail(context.Context, int64, int64, string, string) error { return nil }
func (*enqueueTestRepo) ReclaimStale(context.Context, time.Time, time.Time, int) (int64, error) {
	return 0, nil
}
func (*enqueueTestRepo) QueueStats(context.Context) (QueueStats, error) { return QueueStats{}, nil }
func (*enqueueTestRepo) RecordBlocking(context.Context, PromptSnapshot, int64, *NormalizedResult, bool) (*Event, error) {
	return nil, nil
}

type enqueueTestPayload struct {
	setErr    error
	deleteErr error
	setText   string
	deleted   bool
}

func (p *enqueueTestPayload) Set(context.Context, int64, string, time.Duration) error {
	if p.setErr != nil {
		return p.setErr
	}
	p.setText = "raw prompt payload"
	return nil
}
func (*enqueueTestPayload) Get(context.Context, int64) (string, error) { return "", nil }
func (p *enqueueTestPayload) Delete(context.Context, int64) error {
	p.deleted = true
	return p.deleteErr
}
func (*enqueueTestPayload) Ping(context.Context) error { return nil }

func asyncEnqueueConfig() *enqueueTestConfig {
	return &enqueueTestConfig{ok: true, active: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, Strategy: "priority", WorkerCount: 1, QueueCapacity: 16,
		AllGroups: true, Scanners: append([]string(nil), AllScannerIDs...), ConfigVersion: 3,
		Endpoints: []ActiveEndpoint{{ID: "guard", Enabled: true}},
	}}
}

func enqueueRequest(body string) Request {
	return Request{RequestID: "req-enqueue", Protocol: "openai_chat", Body: []byte(body)}
}

func TestEnqueuerGuardBranchesAndNoResponseMutation(t *testing.T) {
	err := NewEnqueuer(nil, nil, nil).Enqueue(context.Background(), enqueueRequest(`{}`))
	require.Error(t, err)

	config := asyncEnqueueConfig()
	repo := &enqueueTestRepo{}
	payload := &enqueueTestPayload{}
	metrics := NewAtomicMetrics()
	enqueuer := NewEnqueuer(config, repo, payload, metrics)
	config.ok = false
	require.NoError(t, enqueuer.Enqueue(context.Background(), enqueueRequest(`{"messages":[{"role":"user","content":"skip"}]}`)))
	config.ok = true
	config.active.AllGroups = false
	config.active.GroupIDs = []int64{9}
	groupID := int64(8)
	require.NoError(t, enqueuer.Enqueue(context.Background(), Request{GroupID: &groupID, Protocol: "openai_chat", Body: []byte(`{"messages":[{"role":"user","content":"skip"}]}`)}))
	config.active.GroupIDs = nil
	config.active.AllGroups = true
	config.active.Endpoints = nil
	require.NoError(t, enqueuer.Enqueue(context.Background(), enqueueRequest(`{"messages":[{"role":"user","content":"skip"}]}`)))
	require.Equal(t, int64(1), metrics.AuditSnapshot().Dropped)

	config.active.Endpoints = []ActiveEndpoint{{ID: "guard", Enabled: true}}
	require.NoError(t, enqueuer.Enqueue(context.Background(), enqueueRequest(`{"messages":[{"role":"function","content":"none"}]}`)))
	require.NoError(t, enqueuer.Enqueue(context.Background(), enqueueRequest("{")))
	require.Equal(t, int64(2), metrics.AuditSnapshot().Dropped)
}

func TestEnqueuerSuccessStoresRawTextOnlyInPayloadAndCounts(t *testing.T) {
	config := asyncEnqueueConfig()
	repo := &enqueueTestRepo{}
	payload := &enqueueTestPayload{}
	metrics := NewAtomicMetrics()
	enqueuer := NewEnqueuer(config, repo, payload, metrics)
	req := enqueueRequest(`{"messages":[{"role":"user","content":"PROMPT_CANARY_RAW"}]}`)
	require.NoError(t, enqueuer.Enqueue(context.Background(), req))
	require.Equal(t, 1, repo.created)
	require.Equal(t, "raw prompt payload", payload.setText)
	require.Equal(t, int64(1), metrics.AuditSnapshot().Enqueued)
}

func TestEnqueuerRecordsQueuePayloadAndPublishFailures(t *testing.T) {
	config := asyncEnqueueConfig()
	valid := enqueueRequest(`{"messages":[{"role":"user","content":"hello"}]}`)
	metrics := NewAtomicMetrics()
	queueRepo := &enqueueTestRepo{createErr: ErrQueueFull}
	require.ErrorIs(t, NewEnqueuer(config, queueRepo, &enqueueTestPayload{}, metrics).Enqueue(context.Background(), valid), ErrQueueFull)
	require.Equal(t, int64(1), metrics.AuditSnapshot().Dropped)

	payloadFailure := &enqueueTestPayload{setErr: errors.New("redis unavailable")}
	payloadRepo := &enqueueTestRepo{}
	require.Error(t, NewEnqueuer(config, payloadRepo, payloadFailure, metrics).Enqueue(context.Background(), valid))
	require.Equal(t, 1, payloadRepo.marked)

	publishFailure := &enqueueTestRepo{publishErr: errors.New("publish failed")}
	publishPayload := &enqueueTestPayload{}
	require.Error(t, NewEnqueuer(config, publishFailure, publishPayload, metrics).Enqueue(context.Background(), valid))
	require.True(t, publishPayload.deleted)
	require.Equal(t, 1, publishFailure.marked)
}
