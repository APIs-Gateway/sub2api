package service

import (
	"context"
	"encoding/json"
	"errors"
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
	setErr error
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
	if r.setErr != nil {
		return r.setErr
	}
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
	dueError    error
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
	if r.dueError != nil {
		return nil, r.dueError
	}
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
	err           error
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
	if u.err != nil {
		return nil, u.err
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
	var accountRepo AccountRepository
	if repo != nil {
		accountRepo = repo
	}
	testService := &AccountTestService{
		accountRepo:  accountRepo,
		httpUpstream: upstream,
		cfg:          cfg,
	}
	var settingService *SettingService
	if settings != nil {
		settingService = &SettingService{settingRepo: settings}
	}
	service := NewUpstreamBillingProbeService(accountRepo, testService, settingService)
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

type billingProbeFallbackRepo struct {
	AccountRepository
	account  *Account
	accounts []Account
}

func (r *billingProbeFallbackRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, ErrAccountNotFound
}

func (r *billingProbeFallbackRepo) FindByExtraField(_ context.Context, _ string, _ any) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type billingProbeNoWriterRepo struct {
	AccountRepository
	account *Account
}

func (r *billingProbeNoWriterRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, ErrAccountNotFound
}

type billingProbeReadCloser struct{}

func (billingProbeReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (billingProbeReadCloser) Close() error             { return nil }

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

func TestUpstreamBillingProbeSettingsErrorsNormalizationAndServiceFacade(t *testing.T) {
	dbErr := errors.New("settings database unavailable")
	repo := &billingProbeSettingRepo{values: map[string]string{}, getErr: dbErr}
	settingService := &SettingService{settingRepo: repo}

	_, err := settingService.GetUpstreamBillingProbeSettings(context.Background())
	require.ErrorIs(t, err, dbErr)
	repo.getErr = nil
	repo.values[SettingKeyUpstreamBillingProbeSettings] = `{"enabled":true,"interval_minutes":1}`
	settings, err := settingService.GetUpstreamBillingProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, settings.IntervalMinutes)
	repo.values[SettingKeyUpstreamBillingProbeSettings] = "not-json"
	_, err = settingService.GetUpstreamBillingProbeSettings(context.Background())
	require.Error(t, err)

	repo.values[SettingKeyUpstreamBillingProbeSettings] = ""
	settings, err = settingService.GetUpstreamBillingProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultUpstreamBillingProbeSettings(), settings)
	repo.setErr = dbErr
	err = settingService.SetUpstreamBillingProbeSettings(context.Background(), &UpstreamBillingProbeSettings{Enabled: true, IntervalMinutes: 10})
	require.ErrorIs(t, err, dbErr)

	service := NewUpstreamBillingProbeService(nil, nil, settingService)
	settings, err = service.GetSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultUpstreamBillingProbeSettings(), settings)
	require.ErrorIs(t, service.UpdateSettings(context.Background(), &UpstreamBillingProbeSettings{Enabled: true, IntervalMinutes: 10}), dbErr)
	var nilService *UpstreamBillingProbeService
	require.NoError(t, nilService.RunDue(context.Background()))
	nilService.SetLeaderLock(nil, nil)
}

func TestUpstreamBillingProbeServiceRunDueSkipsDisabledHeldAndNonDueAccounts(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	future := newBillingProbeAccount(5, "https://api.example.com", true)
	futureSnapshot := &UpstreamBillingProbeSnapshot{Status: UpstreamBillingProbeStatusOK, NextProbeAt: now.Add(time.Hour)}
	raw, err := json.Marshal(futureSnapshot)
	require.NoError(t, err)
	future.Extra[UpstreamBillingProbeExtraKey] = raw
	inactive := newBillingProbeAccount(2, "https://api.example.com", true)
	inactive.Status = StatusDisabled
	nonOpenAI := newBillingProbeAccount(3, "https://api.example.com", true)
	nonOpenAI.Platform = PlatformAnthropic
	disabled := newBillingProbeAccount(4, "https://api.example.com", false)

	repo := &billingProbeAccountRepo{
		accounts: map[int64]*Account{1: newBillingProbeAccount(1, "https://api.example.com", true)},
		due:      []Account{*inactive, *nonOpenAI, *disabled, *future},
	}
	// Keep one genuinely due account separate from the filtered fixtures above.
	due := newBillingProbeAccount(6, "https://api.example.com", true)
	repo.accounts[6] = due
	repo.due = append(repo.due, *due)
	upstream := &billingProbeHTTPUpstream{handler: func(_ *http.Request, _ int) *http.Response {
		return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
	}}
	settingsRepo := &billingProbeSettingRepo{values: map[string]string{
		SettingKeyUpstreamBillingProbeSettings: `{"enabled":true,"interval_minutes":30}`,
	}}
	service := newBillingProbeService(repo, upstream, settingsRepo, now)
	service.SetLeaderLock(&billingProbeLeaderCache{owners: map[string]string{upstreamBillingProbeLeaderLockKey: "peer"}}, nil)
	require.NoError(t, service.RunDue(context.Background()))
	require.Zero(t, upstream.calls.Load())

	settingsRepo.values[SettingKeyUpstreamBillingProbeSettings] = `{"enabled":false,"interval_minutes":30}`
	service.SetLeaderLock(nil, nil)
	require.NoError(t, service.RunDue(context.Background()))
	require.Zero(t, upstream.calls.Load())
}

func TestUpstreamBillingProbeServiceRunDueFallbackListerAndErrors(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	account := newBillingProbeAccount(1, "https://api.example.com", true)
	fallback := &billingProbeFallbackRepo{account: account, accounts: []Account{*account}}
	settingsRepo := &billingProbeSettingRepo{values: map[string]string{}}
	service := NewUpstreamBillingProbeService(fallback, nil, &SettingService{settingRepo: settingsRepo})
	accounts, err := service.listDueAccounts(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, accounts, 1)

	repo := &billingProbeAccountRepo{accounts: map[int64]*Account{1: account}, dueError: errors.New("due query failed")}
	service = newBillingProbeService(repo, nil, settingsRepo, now)
	_, err = service.listDueAccounts(context.Background(), now)
	require.Error(t, err)
	require.Error(t, service.RunDue(context.Background()))

	settingsRepo.getErr = errors.New("settings read failed")
	require.Error(t, service.RunDue(context.Background()))
}

func TestUpstreamBillingProbeServiceAccountModesAndBatchErrors(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	invalid := newBillingProbeAccount(2, "https://api.example.com", true)
	invalid.Platform = PlatformAnthropic
	account := newBillingProbeAccount(1, "https://api.example.com", true)
	repo := &billingProbeAccountRepo{accounts: map[int64]*Account{1: account, 2: invalid}}
	service := newBillingProbeService(repo, nil, &billingProbeSettingRepo{values: map[string]string{}}, now)
	_, err := service.ProbeAccount(context.Background(), 2)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)

	account.Extra[UpstreamBillingProbeEnabledExtraKey] = false
	result, err := service.probeScheduledAccount(context.Background(), 1, 30)
	require.NoError(t, err)
	require.Nil(t, result)
	account.Extra[UpstreamBillingProbeEnabledExtraKey] = true
	account.Extra[UpstreamBillingProbeExtraKey] = map[string]any{
		"status":        UpstreamBillingProbeStatusOK,
		"next_probe_at": now.Add(time.Hour),
	}
	result, err = service.probeScheduledAccount(context.Background(), 1, 30)
	require.NoError(t, err)
	require.Nil(t, result)

	service = newBillingProbeService(repo, nil, &billingProbeSettingRepo{getErr: errors.New("settings unavailable")}, now)
	results := service.ProbeAccounts(context.Background(), []int64{1, 2})
	require.Equal(t, "probe_failed", results[0].Error)
	require.Equal(t, "probe_failed", results[1].Error)

	service = newBillingProbeService(nil, nil, nil, now)
	results = service.ProbeAccounts(context.Background(), []int64{1})
	require.Equal(t, ErrUpstreamBillingProbeUnavailable.Error(), results[0].Error)

	service = newBillingProbeService(repo, nil, &billingProbeSettingRepo{values: map[string]string{}}, now)
	for i := 0; i < upstreamBillingProbeConcurrency; i++ {
		service.probeSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.probeAccount(ctx, 1, 30)
	require.ErrorIs(t, err, context.Canceled)
	for i := 0; i < upstreamBillingProbeConcurrency; i++ {
		<-service.probeSlots
	}
}

func TestUpstreamBillingProbeServiceProbeFailureReasons(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	base := newBillingProbeAccount(1, "https://api.example.com", true)
	tests := []struct {
		name       string
		account    *Account
		response   *http.Response
		httpErr    error
		wantReason string
		wantStatus string
	}{
		{name: "transport unavailable", account: base, wantReason: "transport_unavailable"},
		{name: "missing api key", account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{}}, wantReason: "missing_api_key"},
		{name: "invalid base url", account: func() *Account { a := newBillingProbeAccount(1, "://bad", true); return a }(), wantReason: "invalid_base_url"},
		{name: "proxy unavailable", account: func() *Account {
			a := newBillingProbeAccount(1, "https://api.example.com", true)
			id := int64(7)
			a.ProxyID = &id
			return a
		}(), wantReason: "proxy_unavailable"},
		{name: "proxy identity changed", account: func() *Account {
			a := newBillingProbeAccount(1, "https://api.example.com", true)
			id := int64(7)
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: 8}
			return a
		}(), wantStatus: "identity_changed"},
		{name: "request failed", account: base, httpErr: errors.New("network failed"), wantReason: "request_failed"},
		{name: "empty response", account: base, response: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, wantReason: "empty_response"},
		{name: "read failed", account: base, response: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: billingProbeReadCloser{}}, wantReason: "response_read_failed"},
		{name: "response too large", account: base, response: billingProbeResponse(t, http.StatusOK, strings.Repeat("x", upstreamBillingProbeMaxBodyBytes+1), nil), wantReason: "response_too_large"},
		{name: "unsupported", account: base, response: billingProbeResponse(t, http.StatusNotFound, "", nil), wantReason: "unsupported", wantStatus: UpstreamBillingProbeStatusUnsupported},
		{name: "http error", account: base, response: billingProbeResponse(t, http.StatusInternalServerError, "", nil), wantReason: "http_error"},
		{name: "invalid response", account: base, response: billingProbeResponse(t, http.StatusOK, `{}`, nil), wantReason: "invalid_response"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := test.account
			repo := &billingProbeAccountRepo{accounts: map[int64]*Account{1: account}}
			upstream := &billingProbeHTTPUpstream{err: test.httpErr, handler: func(_ *http.Request, _ int) *http.Response {
				return test.response
			}}
			service := newBillingProbeService(repo, upstream, &billingProbeSettingRepo{values: map[string]string{}}, now)
			if test.name == "transport unavailable" {
				service.accountTestService.httpUpstream = nil
			}
			if test.name == "proxy identity changed" {
				_, err := service.ProbeAccount(context.Background(), 1)
				require.ErrorIs(t, err, ErrUpstreamBillingProbeIdentityChanged)
				return
			}
			snapshot, err := service.ProbeAccount(context.Background(), 1)
			require.NoError(t, err)
			require.NotNil(t, snapshot)
			require.Equal(t, test.wantReason, snapshot.LastError)
			if test.wantStatus != "" {
				require.Equal(t, test.wantStatus, snapshot.Status)
			}
		})
	}

	noWriter := &billingProbeNoWriterRepo{account: newBillingProbeAccount(1, "https://api.example.com", true)}
	service := NewUpstreamBillingProbeService(noWriter, &AccountTestService{
		httpUpstream: &billingProbeHTTPUpstream{handler: func(_ *http.Request, _ int) *http.Response {
			return billingProbeResponse(t, http.StatusOK, validBillingProbeBody(t, now), nil)
		}},
		cfg: &config.Config{},
	}, &SettingService{})
	_, err := service.ProbeAccount(context.Background(), 1)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeUnavailable)
}

func TestUpstreamBillingProbeHelpersAndPublicSettings(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	require.Equal(t, time.Hour, retryAfter(http.Header{"Retry-After": []string{"3600"}}, now))
	require.Equal(t, time.Minute, retryAfter(http.Header{"Retry-After": []string{now.Add(time.Minute).UTC().Format(http.TimeFormat)}}, now))
	require.Zero(t, retryAfter(http.Header{"Retry-After": []string{"-1"}}, now))
	require.Zero(t, retryAfter(http.Header{"Retry-After": []string{"invalid"}}, now))
	require.Equal(t, "", safeProbeError(nil))
	require.Equal(t, ErrUpstreamBillingProbeAccountInvalid.Error(), safeProbeError(ErrUpstreamBillingProbeAccountInvalid))
	require.Equal(t, ErrUpstreamBillingProbeUnavailable.Error(), safeProbeError(ErrUpstreamBillingProbeUnavailable))
	require.Equal(t, "probe_failed", safeProbeError(errors.New("internal")))

	service := NewUpstreamBillingProbeService(nil, nil, nil)
	settings, err := service.GetSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultUpstreamBillingProbeSettings(), settings)
	require.ErrorIs(t, service.UpdateSettings(context.Background(), &UpstreamBillingProbeSettings{Enabled: true, IntervalMinutes: 10}), ErrUpstreamBillingProbeUnavailable)

	service.Start()
	service.Start()
	service.Stop()
	service.Stop()
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
