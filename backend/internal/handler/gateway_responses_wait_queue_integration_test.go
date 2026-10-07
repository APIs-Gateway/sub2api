//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// These public-handler fixtures use an isolated real Redis and real selector,
// conversion and HTTP wire. Simple mode deliberately makes no wallet/card claim.
type responsesQueueSnapshot struct {
	service.SchedulerCache
	account *service.Account
	onRead  func()
}

func (s *responsesQueueSnapshot) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return []*service.Account{s.account}, true, nil
}

func (s *responsesQueueSnapshot) GetAccount(context.Context, int64) (*service.Account, error) {
	if s.onRead != nil {
		s.onRead()
	}
	return s.account, nil
}

type responsesQueueGroup struct {
	service.GroupRepository
	group *service.Group
}

func (g *responsesQueueGroup) GetByID(context.Context, int64) (*service.Group, error) {
	return g.group, nil
}

func (g *responsesQueueGroup) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return g.group, nil
}

type responsesQueueUsage struct {
	service.UsageLogRepository
	created atomic.Int32
}

func (r *responsesQueueUsage) Create(context.Context, *service.UsageLog) (bool, error) {
	r.created.Add(1)
	return true, nil
}

type responsesQueueWire struct {
	server *httptest.Server
	calls  atomic.Int32
	failed atomic.Bool
	bad    atomic.Bool
}

func (u *responsesQueueWire) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.DoWithTLS(req, "", 0, 0, nil)
}

func (u *responsesQueueWire) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	forward := req.Clone(req.Context())
	target, err := url.Parse(u.server.URL)
	if err != nil {
		return nil, err
	}
	forward.URL.Scheme, forward.URL.Host = target.Scheme, target.Host
	forward.Host = target.Host
	return u.server.Client().Do(forward)
}

type responsesQueueCache struct {
	service.ConcurrencyCache
	service.APIKeyConcurrencyCache
	keyTracks     atomic.Int32
	keyReleases   atomic.Int32
	registrations atomic.Int32
	decrements    atomic.Int32
	acquisitions  atomic.Int32
	onIncrement   func(context.Context, int64, int) (bool, error)
}

func (c *responsesQueueCache) TrackAPIKeySlot(ctx context.Context, id int64, requestID string) error {
	c.keyTracks.Add(1)
	return c.APIKeyConcurrencyCache.TrackAPIKeySlot(ctx, id, requestID)
}

func (c *responsesQueueCache) ReleaseAPIKeySlot(ctx context.Context, id int64, requestID string) error {
	c.keyReleases.Add(1)
	return c.APIKeyConcurrencyCache.ReleaseAPIKeySlot(ctx, id, requestID)
}

type responsesQueueFlushWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func (w *responsesQueueFlushWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.flushed) })
}

func (c *responsesQueueCache) IncrementAccountWaitCount(ctx context.Context, id int64, max int) (bool, error) {
	c.registrations.Add(1)
	if c.onIncrement != nil {
		return c.onIncrement(ctx, id, max)
	}
	return c.ConcurrencyCache.IncrementAccountWaitCount(ctx, id, max)
}

func (c *responsesQueueCache) DecrementAccountWaitCount(ctx context.Context, id int64) error {
	c.decrements.Add(1)
	return c.ConcurrencyCache.DecrementAccountWaitCount(ctx, id)
}

func (c *responsesQueueCache) AcquireAccountSlot(ctx context.Context, id int64, max int, requestID string) (bool, error) {
	c.acquisitions.Add(1)
	return c.ConcurrencyCache.AcquireAccountSlot(ctx, id, max, requestID)
}

type responsesQueueFixture struct {
	rdb             *redis.Client
	cache           *responsesQueueCache
	service         *service.ConcurrencyService
	snapshot        *responsesQueueSnapshot
	wire            *responsesQueueWire
	usage           *responsesQueueUsage
	handler         *GatewayHandler
	group           *service.Group
	account         *service.Account
	sticky          service.GatewayCache
	userConcurrency int
	flushed         chan struct{}
}

func newResponsesQueueFixture(t *testing.T, rdb *redis.Client) *responsesQueueFixture {
	t.Helper()
	require.NoError(t, rdb.FlushDB(context.Background()).Err(), "only the dedicated fixture container is flushed")
	f := &responsesQueueFixture{rdb: rdb, userConcurrency: 10}
	f.group = &service.Group{ID: 9755, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	f.account = &service.Account{ID: 9756, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "fixture-only", "pool_mode": false},
		AccountGroups: []service.AccountGroup{{AccountID: 9756, GroupID: 9755}}}
	f.snapshot = &responsesQueueSnapshot{account: f.account}
	realCache := repository.NewConcurrencyCache(rdb, 1, 60)
	f.cache = &responsesQueueCache{ConcurrencyCache: realCache, APIKeyConcurrencyCache: realCache.(service.APIKeyConcurrencyCache)}
	f.service = service.NewConcurrencyService(f.cache)
	f.sticky = repository.NewGatewayCache(rdb)
	f.usage = &responsesQueueUsage{}
	f.wire = &responsesQueueWire{}
	f.wire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.wire.calls.Add(1)
		var body map[string]any
		if r.URL.Path != "/v1/messages" || json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "claude-sonnet-4-5" {
			f.wire.bad.Store(true)
		}
		if f.wire.failed.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"type":"overloaded_error","message":"fixture unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"queue_message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"queue control success\"}}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(f.wire.server.Close)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 1
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = time.Second
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billing.Stop)
	snapshot := service.NewSchedulerSnapshotService(f.snapshot, nil, nil, nil, nil)
	gateway := service.NewGatewayService(nil, &responsesQueueGroup{group: f.group}, f.usage, nil, nil, nil, nil, f.sticky, cfg,
		snapshot, f.service, service.NewBillingService(cfg, nil), nil, billing, nil, f.wire,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	f.handler = &GatewayHandler{gatewayService: gateway, billingCacheService: billing, cfg: cfg,
		concurrencyHelper: NewConcurrencyHelper(f.service, SSEPingFormatClaude, time.Hour)}
	return f
}

func (f *responsesQueueFixture) sessionHash(userID int64) string {
	body := service.NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","input":"hello queue","stream":false}`))
	parsed, _ := service.ParseGatewayRequest(body, "responses")
	parsed.SessionContext = &service.SessionContext{ClientIP: "192.0.2.1", APIKeyID: userID + 100}
	return f.handler.gatewayService.GenerateSessionHash(parsed)
}

func (f *responsesQueueFixture) request(ctx context.Context, userID int64) (*httptest.ResponseRecorder, <-chan struct{}) {
	return f.requestStream(ctx, userID, false)
}

func (f *responsesQueueFixture) requestStream(ctx context.Context, userID int64, stream bool) (*httptest.ResponseRecorder, <-chan struct{}) {
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		key := &service.APIKey{ID: userID + 100, UserID: userID, GroupID: &f.group.ID, Group: f.group, Status: service.StatusActive,
			User: &service.User{ID: userID, Balance: 100, Concurrency: f.userConcurrency, Status: service.StatusActive}}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID, Concurrency: f.userConcurrency})
		f.handler.Responses(c)
	})
	recorder := httptest.NewRecorder()
	var writer http.ResponseWriter = recorder
	if f.flushed != nil {
		writer = &responsesQueueFlushWriter{ResponseRecorder: recorder, flushed: f.flushed}
	}
	requestBody := `{"model":"claude-sonnet-4-5","input":"hello queue","stream":false}`
	if stream {
		requestBody = `{"model":"claude-sonnet-4-5","input":"hello queue","stream":true}`
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
		req = req.WithContext(context.WithValue(ctx, ctxkey.Group, f.group))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(writer, req)
	}()
	return recorder, done
}

func responsesQueueDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("public Responses handler did not finish")
	}
}

func (f *responsesQueueFixture) foreignSlot(t *testing.T) func() {
	t.Helper()
	result, err := f.service.AcquireAccountSlot(context.Background(), f.account.ID, 1)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	return result.ReleaseFunc
}

func (f *responsesQueueFixture) count(t *testing.T) int {
	t.Helper()
	n, err := f.rdb.Get(context.Background(), "wait:account:9756").Int()
	if err == redis.Nil {
		return 0
	}
	require.NoError(t, err)
	return n
}

func (f *responsesQueueFixture) assertForeignSlot(t *testing.T) {
	t.Helper()
	n, err := f.cache.ConcurrencyCache.GetAccountConcurrency(context.Background(), f.account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the external owner's active slot must remain")
}

func TestGenericResponsesWaitQueue_RealRedis(t *testing.T) {
	gin.SetMode(gin.TestMode)
	container, err := tcredis.Run(context.Background(), "redis:8.4-alpine")
	require.NoError(t, err, "the queue contract requires actual Redis")
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	address, err := container.ConnectionString(context.Background())
	require.NoError(t, err)
	options, err := redis.ParseURL(address)
	require.NoError(t, err)
	options.ContextTimeoutEnabled = true
	rdb := redis.NewClient(options)
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(context.Background()).Err())

	t.Run("one_waiter_then_overflow_before_provider", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		selection, err := f.handler.gatewayService.SelectAccountWithLoadAwareness(
			context.WithValue(context.Background(), ctxkey.Group, f.group), &f.group.ID, f.sessionHash(1), "claude-sonnet-4-5", nil, "", 0)
		require.NoError(t, err)
		require.False(t, selection.Acquired)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated, "the public handler fixture reaches actual Layer3")
		require.Equal(t, 1, selection.WaitPlan.MaxWaiting)
		baselineAttempts := f.cache.acquisitions.Load()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first, firstDone := f.request(ctx, 1)
		defer func() { cancel(); responsesQueueDone(t, firstDone) }()
		require.Eventually(t, func() bool { return f.cache.acquisitions.Load() >= baselineAttempts+2 }, time.Second, time.Millisecond)
		assert.Equal(t, 1, f.count(t), "the admitted public-handler waiter must be registered")
		second, secondDone := f.request(context.Background(), 2)
		responsesQueueDone(t, secondDone)
		assert.Equal(t, http.StatusTooManyRequests, second.Code)
		assert.Contains(t, second.Body.String(), "Too many pending requests, please retry later")
		assert.Zero(t, f.wire.calls.Load(), "no provider request before a slot is acquired")
		f.assertForeignSlot(t)
		foreign()
		responsesQueueDone(t, firstDone)
		assert.Equal(t, http.StatusOK, first.Code)
		assert.Contains(t, first.Body.String(), "queue control success")
		assert.EqualValues(t, 1, f.wire.calls.Load())
		assert.False(t, f.wire.bad.Load())
		assert.EqualValues(t, 1, f.usage.created.Load(), "real conversion and metering completed, not a swallowed usage panic")
		assert.Zero(t, f.count(t))
		assert.EqualValues(t, 1, f.cache.decrements.Load(), "owned registration is cleaned once")
	})

	for _, ending := range []string{"cancel", "timeout"} {
		t.Run(ending+"_retains_foreign_slot", func(t *testing.T) {
			f := newResponsesQueueFixture(t, rdb)
			foreign := f.foreignSlot(t)
			defer foreign()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, done := f.request(ctx, 3)
			defer func() { cancel(); responsesQueueDone(t, done) }()
			require.Eventually(t, func() bool { return f.cache.acquisitions.Load() >= 3 }, time.Second, time.Millisecond)
			assert.Equal(t, 1, f.count(t))
			if ending == "cancel" {
				cancel()
			}
			responsesQueueDone(t, done)
			assert.Zero(t, f.count(t))
			assert.EqualValues(t, 1, f.cache.decrements.Load())
			assert.Zero(t, f.wire.calls.Load())
			f.assertForeignSlot(t)
		})
	}

	t.Run("full_queue_preserves_existing_sticky_owner", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		require.NoError(t, rdb.Set(context.Background(), "wait:account:9756", 1, time.Minute).Err())
		session := f.sessionHash(9)
		require.NotEmpty(t, session)
		require.NoError(t, f.sticky.SetSessionAccountID(context.Background(), f.group.ID, session, f.account.ID, time.Minute))
		recorder, done := f.request(context.Background(), 9)
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "Too many pending requests, please retry later")
		owner, err := f.sticky.GetSessionAccountID(context.Background(), f.group.ID, session)
		require.NoError(t, err)
		assert.Equal(t, f.account.ID, owner)
		assert.Equal(t, 1, f.count(t))
		assert.Zero(t, f.cache.decrements.Load())
		assert.Zero(t, f.wire.calls.Load())
		f.assertForeignSlot(t)
	})

	t.Run("owned_cancel_preserves_foreign_waiter", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		f.handler.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
		foreign := f.foreignSlot(t)
		defer foreign()
		require.NoError(t, rdb.Set(context.Background(), "wait:account:9756", 1, time.Minute).Err())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, done := f.request(ctx, 10)
		defer func() { cancel(); responsesQueueDone(t, done) }()
		require.Eventually(t, func() bool { return f.cache.acquisitions.Load() >= 3 }, time.Second, time.Millisecond)
		assert.Equal(t, 2, f.count(t))
		cancel()
		responsesQueueDone(t, done)
		assert.Equal(t, 1, f.count(t))
		assert.EqualValues(t, 1, f.cache.decrements.Load())
		assert.Zero(t, f.wire.calls.Load())
		f.assertForeignSlot(t)
	})

	t.Run("preacquired_slot_ignores_full_queue", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		require.NoError(t, rdb.Set(context.Background(), "wait:account:9756", 1, time.Minute).Err())
		recorder, done := f.request(context.Background(), 4)
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, 1, f.count(t))
		assert.Zero(t, f.cache.registrations.Load())
		assert.Zero(t, f.cache.decrements.Load())
		assert.EqualValues(t, 1, f.wire.calls.Load())
	})

	t.Run("capacity_freed_after_selection_ignores_full_queue", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		selection, err := f.handler.gatewayService.SelectAccountWithLoadAwareness(
			context.WithValue(context.Background(), ctxkey.Group, f.group), &f.group.ID, f.sessionHash(5), "claude-sonnet-4-5", nil, "", 0)
		require.NoError(t, err)
		require.False(t, selection.Acquired)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
		baselineAttempts := f.cache.acquisitions.Load()
		var once sync.Once
		var hydrationAttempts int32
		var hydrationSlots int
		var hydrationErr error
		f.snapshot.onRead = func() {
			once.Do(func() {
				// newSelectionResult hydrates only after its Acquired/WaitPlan
				// decision. The foreign slot is still busy at this boundary.
				hydrationAttempts = f.cache.acquisitions.Load()
				hydrationSlots, hydrationErr = f.cache.ConcurrencyCache.GetAccountConcurrency(context.Background(), f.account.ID)
				foreign()
				if hydrationErr == nil {
					hydrationErr = rdb.Set(context.Background(), "wait:account:9756", 1, time.Minute).Err()
				}
			})
		}
		recorder, done := f.request(context.Background(), 5)
		responsesQueueDone(t, done)
		require.NoError(t, hydrationErr)
		assert.Equal(t, 1, hydrationSlots, "actual public selector decided while the foreign slot was busy")
		assert.Equal(t, baselineAttempts, hydrationAttempts, "Layer3 hydration had no preacquired selector slot")
		assert.Equal(t, baselineAttempts+1, f.cache.acquisitions.Load(), "the handler's immediate Try acquired the freed slot")
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, 1, f.count(t))
		assert.Zero(t, f.cache.registrations.Load())
		assert.Zero(t, f.cache.decrements.Load())
		assert.EqualValues(t, 1, f.wire.calls.Load())
	})

	t.Run("capacity_between_registration_and_second_try", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		f.cache.onIncrement = func(ctx context.Context, id int64, max int) (bool, error) {
			foreign()
			return f.cache.ConcurrencyCache.IncrementAccountWaitCount(ctx, id, max)
		}
		recorder, done := f.request(context.Background(), 6)
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Zero(t, f.count(t))
		assert.EqualValues(t, 1, f.cache.decrements.Load())
		assert.EqualValues(t, 1, f.wire.calls.Load())
	})

	t.Run("provider_failure_after_acquire_releases_queue", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		f.wire.failed.Store(true)
		f.cache.onIncrement = func(ctx context.Context, id int64, max int) (bool, error) {
			foreign()
			return f.cache.ConcurrencyCache.IncrementAccountWaitCount(ctx, id, max)
		}
		recorder, done := f.request(context.Background(), 7)
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusBadGateway, recorder.Code, "preserve the existing 503 upstream to 502 client mapping")
		var response map[string]map[string]string
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "upstream_error", response["error"]["code"])
		assert.Equal(t, "Upstream service temporarily unavailable", response["error"]["message"])
		assert.False(t, f.wire.bad.Load(), "the actual failed wire still used the authorized account model and Messages path")
		assert.Zero(t, f.count(t))
		assert.EqualValues(t, 1, f.cache.decrements.Load())
		assert.EqualValues(t, 1, f.wire.calls.Load())
	})

	t.Run("stream_timeout_keeps_ping_terminal_and_owned_cleanup", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		f.handler.concurrencyHelper = NewConcurrencyHelper(f.service, SSEPingFormatClaude, 20*time.Millisecond)
		foreign := f.foreignSlot(t)
		defer foreign()
		recorder, done := f.requestStream(context.Background(), 11, true)
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusOK, recorder.Code, "a flushed ping fixes the transport status at 200")
		assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
		assert.Contains(t, recorder.Body.String(), string(SSEPingFormatClaude), "the public wait path actually flushed inherited ping frames")
		assert.Contains(t, recorder.Body.String(), "event: response.failed")
		assert.Contains(t, recorder.Body.String(), `"code":"rate_limit_exceeded"`)
		assert.Zero(t, f.count(t))
		assert.EqualValues(t, 1, f.cache.decrements.Load())
		assert.Zero(t, f.wire.calls.Load())
		f.assertForeignSlot(t)
	})

	t.Run("user_wait_ping_then_full_account_queue_has_one_Responses_terminal", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		f.userConcurrency = 1
		f.flushed = make(chan struct{})
		f.handler.concurrencyHelper = NewConcurrencyHelper(f.service, SSEPingFormatClaude, 20*time.Millisecond)
		foreign := f.foreignSlot(t)
		defer foreign()
		require.NoError(t, rdb.Set(context.Background(), "wait:account:9756", 1, time.Minute).Err())
		foreignUser, err := f.service.AcquireUserSlot(context.Background(), 12, 1)
		require.NoError(t, err)
		require.True(t, foreignUser.Acquired)
		defer foreignUser.ReleaseFunc()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		recorder, done := f.requestStream(ctx, 12, true)
		defer func() { cancel(); responsesQueueDone(t, done) }()
		select {
		case <-f.flushed:
		case <-time.After(time.Second):
			t.Fatal("the real public user-slot wait did not flush a ping")
		}
		// Only the Flush notification crosses goroutines. Recorder reads are
		// deferred until handler done, so the race detector can guard the wire.
		foreignUser.ReleaseFunc()
		responsesQueueDone(t, done)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), string(SSEPingFormatClaude))
		assert.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.failed"))
		assert.Contains(t, recorder.Body.String(), `"code":"rate_limit_exceeded"`)
		assert.Contains(t, recorder.Body.String(), "Too many pending requests, please retry later")
		for _, frame := range strings.Split(recorder.Body.String(), "\n\n") {
			assert.NotContains(t, frame, `{"error":{"code":`, "no bare JSON may follow the inherited SSE ping")
		}
		assert.Equal(t, 1, f.count(t), "the foreign account waiter remains")
		assert.Zero(t, f.cache.decrements.Load())
		assert.Zero(t, f.wire.calls.Load())
		f.assertForeignSlot(t)
		userSlots, err := f.cache.GetUserConcurrency(context.Background(), 12)
		require.NoError(t, err)
		assert.Zero(t, userSlots, "the admitted user's own slot is released")
		keySlots, err := f.cache.GetAPIKeyConcurrencyBatch(context.Background(), []int64{112})
		require.NoError(t, err)
		assert.Zero(t, keySlots[112])
		assert.EqualValues(t, 1, f.cache.keyTracks.Load(), "the public handler really acquired a key slot")
		assert.EqualValues(t, 1, f.cache.keyReleases.Load())
	})

	t.Run("Redis_timeout_failopen_never_decrements_recovered_foreign_wait", func(t *testing.T) {
		f := newResponsesQueueFixture(t, rdb)
		foreign := f.foreignSlot(t)
		defer foreign()
		failed := make(chan error, 1)
		continueFailopen := make(chan struct{})
		var continueOnce sync.Once
		resume := func() { continueOnce.Do(func() { close(continueFailopen) }) }
		defer resume()
		f.cache.onIncrement = func(ctx context.Context, id int64, max int) (bool, error) {
			pauseErr := rdb.Do(ctx, "CLIENT", "PAUSE", 300, "ALL").Err()
			if pauseErr != nil {
				failed <- pauseErr
				return false, pauseErr
			}
			short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			_, incrementErr := f.cache.ConcurrencyCache.IncrementAccountWaitCount(short, id, max)
			failed <- incrementErr
			<-continueFailopen
			return false, incrementErr
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, done := f.request(ctx, 8)
		defer func() { cancel(); resume(); responsesQueueDone(t, done) }()
		var incrementErr error
		select {
		case incrementErr = <-failed:
		case <-time.After(2 * time.Second):
			cancel()
			resume()
			responsesQueueDone(t, done)
			t.Fatal("public handler never registered a waiter")
		}
		require.Error(t, incrementErr, "actual Redis pause must invalidate registration acknowledgement")
		require.NoError(t, rdb.Ping(context.Background()).Err(), "real Redis is available again")
		// An unacknowledged Lua may execute after the timeout. Replace that
		// uncertain aggregate with a distinct recovered owner's registration.
		require.NoError(t, rdb.Del(context.Background(), "wait:account:9756").Err())
		registered, err := f.cache.ConcurrencyCache.IncrementAccountWaitCount(context.Background(), f.account.ID, 1)
		require.NoError(t, err)
		require.True(t, registered)
		resume()
		require.Eventually(t, func() bool { return f.cache.acquisitions.Load() >= 4 }, time.Second, time.Millisecond)
		cancel()
		responsesQueueDone(t, done)
		assert.Equal(t, 1, f.count(t), "failed-open registration does not own the recovered counter")
		assert.Zero(t, f.cache.decrements.Load())
		assert.Zero(t, f.wire.calls.Load())
		f.assertForeignSlot(t)
	})
}
