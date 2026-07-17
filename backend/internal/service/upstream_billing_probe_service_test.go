package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type billingProbeSettingRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	getErr error
}

func (r *billingProbeSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *billingProbeSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

type billingProbeAccountRepo struct {
	AccountRepository
	mu          sync.Mutex
	accounts    map[int64]*Account
	due         []Account
	snapshots   map[int64]*UpstreamBillingProbeSnapshot
	updated     map[int64]map[string]any
	writerError error
	dueLimit    int
}

func (r *billingProbeAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account, ok := r.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (r *billingProbeAccountRepo) FindByExtraField(_ context.Context, key string, value any) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	accounts := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Extra[key] == value {
			accounts = append(accounts, *account)
		}
	}
	return accounts, nil
}

func (r *billingProbeAccountRepo) ListDueUpstreamBillingProbeAccounts(_ context.Context, _ time.Time, limit int) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dueLimit = limit
	if len(r.due) == 0 {
		return []Account{}, nil
	}
	if len(r.due) <= limit {
		return append([]Account(nil), r.due...), nil
	}
	return append([]Account(nil), r.due[:limit]...), nil
}

func (r *billingProbeAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updated == nil {
		r.updated = make(map[int64]map[string]any)
	}
	r.updated[id] = updates
	return nil
}

func (r *billingProbeAccountRepo) UpdateUpstreamBillingProbeSnapshot(_ context.Context, account *Account, snapshot *UpstreamBillingProbeSnapshot) error {
	if r.writerError != nil {
		return r.writerError
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshots == nil {
		r.snapshots = make(map[int64]*UpstreamBillingProbeSnapshot)
	}
	copyOfSnapshot := *snapshot
	if snapshot.Data != nil {
		copyOfSnapshot.Data = make(map[string]any, len(snapshot.Data))
		for key, value := range snapshot.Data {
			copyOfSnapshot.Data[key] = value
		}
	}
	r.snapshots[account.ID] = &copyOfSnapshot
	return nil
}

type billingProbeHTTPUpstream struct {
	handler       func(*http.Request, int) *http.Response
	delay         time.Duration
	calls         atomic.Int32
	active        atomic.Int32
	maxConcurrent atomic.Int32
}

func (u *billingProbeHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.do(req)
}

func (u *billingProbeHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.do(req)
}

func (u *billingProbeHTTPUpstream) do(req *http.Request) (*http.Response, error) {
	call := int(u.calls.Add(1))
	active := u.active.Add(1)
	defer u.active.Add(-1)
	for {
		max := u.maxConcurrent.Load()
		if active <= max || u.maxConcurrent.CompareAndSwap(max, active) {
			break
		}
	}
	if u.delay > 0 {
		time.Sleep(u.delay)
	}
	if u.handler == nil {
		return nil, nil
	}
	return u.handler(req, call), nil
}

type billingProbeLeaderCache struct {
	mu     sync.Mutex
	owners map[string]string
}

func (c *billingProbeLeaderCache) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners == nil {
		c.owners = make(map[string]string)
	}
	if _, held := c.owners[key]; held {
		return false, nil
	}
	c.owners[key] = owner
	return true, nil
}

func (c *billingProbeLeaderCache) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners[key] == owner {
		delete(c.owners, key)
	}
	return nil
}

func newBillingProbeAccount(id int64, baseURL string, enabled bool) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": baseURL},
		Extra:       map[string]any{UpstreamBillingProbeEnabledExtraKey: enabled},
	}
}

func newBillingProbeService(repo *billingProbeAccountRepo, upstream HTTPUpstream, settings *billingProbeSettingRepo, now time.Time) *UpstreamBillingProbeService {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	testService := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          cfg,
	}
	settingService := &SettingService{settingRepo: settings}
	service := NewUpstreamBillingProbeService(repo, testService, settingService)
	service.now = func() time.Time { return now }
	return service
}

func billingProbeResponse(t *testing.T, status int, body string, headers http.Header) *http.Response {
	t.Helper()
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func validBillingProbeBody(t *testing.T, observedAt time.Time) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"object":                    "sub2api.key_billing",
		"schema_version":            1,
		"billing_scope":             "token",
		"group_rate_multiplier":     1.2,
		"resolved_rate_multiplier":  1.2,
		"peak_rate_enabled":         false,
		"effective_rate_multiplier": 1.2,
		"observed_at":               observedAt.UTC().Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	return string(body)
}

func TestUpstreamBillingProbeSettingsDefaultsAndValidation(t *testing.T) {
	repo := &billingProbeSettingRepo{values: map[string]string{}}
	service := &SettingService{settingRepo: repo}

	settings, err := service.GetUpstreamBillingProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, &UpstreamBillingProbeSettings{Enabled: true, IntervalMinutes: 30}, settings)
	require.Error(t, service.SetUpstreamBillingProbeSettings(context.Background(), nil))
	require.Error(t, service.SetUpstreamBillingProbeSettings(context.Background(), &UpstreamBillingProbeSettings{IntervalMinutes: 4}))

	require.NoError(t, service.SetUpstreamBillingProbeSettings(context.Background(), &UpstreamBillingProbeSettings{
		Enabled:         false,
		IntervalMinutes: 15,
	}))
	settings, err = service.GetUpstreamBillingProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, &UpstreamBillingProbeSettings{Enabled: false, IntervalMinutes: 15}, settings)
}

func TestUpstreamBillingProbeServiceProbeAccountPersistsSnapshot(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	repo := &billingProbeAccountRepo{accounts: map[int64]*Account{}}
	repo.accounts[1] = newBillingProbeAccount(1, "https://api.example.com", true)
	upstream := &billingProbeHTTPUpstream{handler: func(req *http.Request, _ int) *http.Response {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/v1/sub2api/billing", req.URL.Path)
		require.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
		require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(req.Context()))
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
	}}
	service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)

	snapshot, err := service.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Status)
	require.Equal(t, 1.2, snapshot.Data["effective_rate_multiplier"])
	require.Equal(t, int32(1), upstream.calls.Load())
	require.Equal(t, snapshot, repo.snapshots[1])
}

func TestUpstreamBillingProbeServiceFailureHonorsRetryAfterAndPreservesData(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	previous := &UpstreamBillingProbeSnapshot{
		Status:       UpstreamBillingProbeStatusOK,
		Data:         map[string]any{"resolved_rate_multiplier": 1.1},
		ReceivedAt:   probeTimePtr(now.Add(-time.Hour)),
		FailureCount: 1,
		NextProbeAt:  now.Add(-time.Minute),
	}
	raw, err := json.Marshal(previous)
	require.NoError(t, err)
	var extraValue any
	require.NoError(t, json.Unmarshal(raw, &extraValue))
	repo := &billingProbeAccountRepo{accounts: map[int64]*Account{
		1: newBillingProbeAccount(1, "https://api.example.com", true),
	}}
	repo.accounts[1].Extra[UpstreamBillingProbeExtraKey] = extraValue
	upstream := &billingProbeHTTPUpstream{handler: func(req *http.Request, _ int) *http.Response {
		return billingProbeResponse(t, http.StatusTooManyRequests, `{"error":"rate limited"}`, http.Header{"Retry-After": []string{"86400"}})
	}}
	service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)

	snapshot, err := service.ProbeAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, UpstreamBillingProbeStatusFailed, snapshot.Status)
	require.Equal(t, 2, snapshot.FailureCount)
	require.Equal(t, http.StatusTooManyRequests, snapshot.HTTPStatus)
	require.Equal(t, "http_error", snapshot.LastError)
	require.Equal(t, 1.1, snapshot.Data["resolved_rate_multiplier"])
	require.Equal(t, now.Add(24*time.Hour), snapshot.NextProbeAt)
}

func TestUpstreamBillingProbeServiceProbeAccountsBoundsConcurrency(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	repo := &billingProbeAccountRepo{accounts: make(map[int64]*Account)}
	for id := int64(1); id <= UpstreamBillingProbeMaxBatchSize+1; id++ {
		repo.accounts[id] = newBillingProbeAccount(id, "https://api.example.com", true)
	}
	upstream := &billingProbeHTTPUpstream{
		delay: 10 * time.Millisecond,
		handler: func(_ *http.Request, _ int) *http.Response {
			return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
		},
	}
	service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)
	ids := make([]int64, UpstreamBillingProbeMaxBatchSize+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	results := service.ProbeAccounts(context.Background(), ids)
	require.Len(t, results, UpstreamBillingProbeMaxBatchSize)
	require.Equal(t, int32(UpstreamBillingProbeMaxBatchSize), upstream.calls.Load())
	require.LessOrEqual(t, upstream.maxConcurrent.Load(), int32(upstreamBillingProbeConcurrency))
	for _, result := range results {
		require.Empty(t, result.Error)
		require.NotNil(t, result.Snapshot)
	}
}

func TestUpstreamBillingProbeServiceRunDueUsesLeaderAndBoundedLister(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	repo := &billingProbeAccountRepo{accounts: make(map[int64]*Account)}
	for id := int64(1); id <= UpstreamBillingProbeMaxBatchSize+1; id++ {
		account := newBillingProbeAccount(id, "https://api.example.com", true)
		repo.accounts[id] = account
		repo.due = append(repo.due, *account)
	}
	upstream := &billingProbeHTTPUpstream{handler: func(_ *http.Request, _ int) *http.Response {
		return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
	}}
	service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)
	service.SetLeaderLock(&billingProbeLeaderCache{}, nil)

	require.NoError(t, service.RunDue(context.Background()))
	require.Equal(t, UpstreamBillingProbeMaxBatchSize, repo.dueLimit)
	require.Equal(t, int32(UpstreamBillingProbeMaxBatchSize), upstream.calls.Load())
	require.Len(t, repo.snapshots, UpstreamBillingProbeMaxBatchSize)
}

func TestUpstreamBillingProbeServiceCASConflictAndLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	repo := &billingProbeAccountRepo{
		accounts:    map[int64]*Account{1: newBillingProbeAccount(1, "https://api.example.com", true)},
		writerError: ErrUpstreamBillingProbeIdentityChanged,
	}
	upstream := &billingProbeHTTPUpstream{handler: func(_ *http.Request, _ int) *http.Response {
		return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
	}}
	service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)

	_, err := service.ProbeAccount(context.Background(), 1)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeIdentityChanged)

	service.Start()
	service.Start()
	service.Stop()
	service.Stop()
}

func TestUpstreamBillingProbeServiceSetAccountEnabled(t *testing.T) {
	repo := &billingProbeAccountRepo{accounts: map[int64]*Account{
		1: newBillingProbeAccount(1, "https://api.example.com", true),
		2: {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
	}}
	service := newBillingProbeService(repo, nil, &billingProbeSettingRepo{values: map[string]string{}}, time.Now())

	require.NoError(t, service.SetAccountEnabled(context.Background(), 1, false))
	require.Equal(t, false, repo.updated[1][UpstreamBillingProbeEnabledExtraKey])
	require.ErrorIs(t, service.SetAccountEnabled(context.Background(), 2, true), ErrUpstreamBillingProbeAccountInvalid)
}
