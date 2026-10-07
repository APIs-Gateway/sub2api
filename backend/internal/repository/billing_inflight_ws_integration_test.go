//go:build integration

package repository

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	gatewayhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Drive real ingress, real Redis slots/cache and PostgreSQL reservation/settlement.
// Only the provider is controlled: a gate keeps its response pending while a
// second HTTP request competes for the same user's actual available funds.
type wsInflightProviderTurn struct {
	payload        []byte
	effectiveModel string
	release        chan struct{}
}
type wsInflightProvider struct {
	turns    chan wsInflightProviderTurn
	stop     chan struct{}
	stopOnce sync.Once
	calls    atomic.Int64
	replay   atomic.Bool
	fault    atomic.Value
	server   *httptest.Server
}

func (p *wsInflightProvider) faultMode() string {
	if v := p.fault.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func newWSInflightProvider(t *testing.T) *wsInflightProvider {
	t.Helper()
	p := &wsInflightProvider{turns: make(chan wsInflightProviderTurn, 16), stop: make(chan struct{})}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := coderws.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			sessionImage := false
			sessionModel := ""
			for {
				_, body, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				if gjson.GetBytes(body, "type").String() == "session.update" {
					if model := gjson.GetBytes(body, "session.model"); model.Exists() {
						sessionModel = model.String()
					}
					if tools := gjson.GetBytes(body, "session.tools"); tools.Exists() {
						sessionImage = strings.Contains(tools.Raw, "image_generation")
					}
					continue
				}
				n := p.calls.Add(1)
				effectiveModel := gjson.GetBytes(body, "model").String()
				if effectiveModel == "" {
					effectiveModel = sessionModel
				}
				turn := wsInflightProviderTurn{payload: body, effectiveModel: effectiveModel, release: make(chan struct{})}
				select {
				case p.turns <- turn:
				case <-r.Context().Done():
					return
				}
				select {
				case <-turn.release:
				case <-p.stop:
					return
				case <-r.Context().Done():
					return
				}
				image := sessionImage
				if tools := gjson.GetBytes(body, "tools"); tools.Exists() {
					image = strings.Contains(tools.Raw, "image_generation")
				}
				image = image || strings.HasPrefix(effectiveModel, "gpt-image")
				responseNumber := n
				if p.replay.Load() {
					responseNumber = 1
				}
				responseModel := effectiveModel
				if p.faultMode() == "mismatch" && n == 1 {
					responseModel = "gpt-5-mini"
				}
				event := wsInflightCompleted(responseNumber, body, image, responseModel)
				if err := conn.Write(r.Context(), coderws.MessageText, event); err != nil {
					return
				}
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		n := p.calls.Add(1)
		turn := wsInflightProviderTurn{payload: body, effectiveModel: gjson.GetBytes(body, "model").String(), release: make(chan struct{})}
		select {
		case p.turns <- turn:
		case <-r.Context().Done():
			return
		}
		select {
		case <-turn.release:
		case <-p.stop:
			return
		case <-r.Context().Done():
			return
		}

		fault := p.faultMode()
		if fault == "401" || fault == "403" {
			status := http.StatusUnauthorized
			code := "invalid_api_key"
			if fault == "403" {
				status = http.StatusForbidden
				code = "permission_denied"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"type":%q,"code":%q,"message":"provider denied authentication"}}`, code, code)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if fault == "scanner" {
			_, _ = io.WriteString(w, "data: "+strings.Repeat("x", 2<<20)+"\n\n")
			return
		}
		responseNumber := n
		if p.replay.Load() {
			responseNumber = 1
		}
		responseModel := turn.effectiveModel
		if fault == "mismatch" && n == 1 {
			responseModel = "gpt-5-mini"
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", wsInflightCompleted(responseNumber, body, strings.Contains(string(body), "image_generation") || strings.HasPrefix(responseModel, "gpt-image"), responseModel))
	}))
	t.Cleanup(func() { p.stopOnce.Do(func() { close(p.stop) }); p.server.Close() })
	return p
}
func wsInflightCompleted(n int64, body []byte, image bool, model string) []byte {
	if model == "" {
		model = gjson.GetBytes(body, "model").String()
	}
	if model == "" {
		model = "gpt-5.4"
	}
	output := `[]`
	if image {
		output = `[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"aGVsbG8="}]`
	}
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_funding_%d","status":"completed","model":%q,"output":%s,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`, n, model, output))
}
func (p *wsInflightProvider) next(t *testing.T) wsInflightProviderTurn {
	t.Helper()
	select {
	case v := <-p.turns:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive a real outbound turn")
		return wsInflightProviderTurn{}
	}
}

type wsInflightHTTPTransport struct {
	service.HTTPUpstream
	client *http.Client
}

func (h wsInflightHTTPTransport) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return h.client.Do(r)
}
func (h wsInflightHTTPTransport) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return h.Do(r, p, a, c)
}

type wsInflightBillingObserver struct {
	service.UsageBillingRepository
	service.BillingInflightRepository
	commands       chan service.UsageBillingCommand
	firstApplyGate chan struct{}
	afterStage     func(context.Context, int64, string, string, string)
	afterApply     func(context.Context, *service.UsageBillingCommand, *service.UsageBillingApplyResult, error)
	calls          atomic.Int64
}

func (b *wsInflightBillingObserver) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	n := b.calls.Add(1)
	b.commands <- *cmd
	if n == 1 && b.firstApplyGate != nil {
		select {
		case <-b.firstApplyGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result, err := b.UsageBillingRepository.Apply(ctx, cmd)
	if b.afterApply != nil {
		b.afterApply(ctx, cmd, result, err)
	}
	return result, err
}

func (b *wsInflightBillingObserver) StageBillingInflight(ctx context.Context, userID int64, ownerID, attemptID string, cmd *service.UsageBillingCommand, ttl time.Duration) (string, error) {
	id, err := b.BillingInflightRepository.StageBillingInflight(ctx, userID, ownerID, attemptID, cmd, ttl)
	if err == nil && b.afterStage != nil {
		b.afterStage(ctx, userID, ownerID, attemptID, id)
	}
	return id, err
}

type wsInflightFixture struct {
	pool        *service.UsageRecordWorkerPool
	accounts    service.AccountRepository
	accountID   int64
	groupID     int64
	userID      int64
	key         *service.APIKey
	gateway     *service.OpenAIGatewayService
	billing     *service.BillingCacheService
	router      *gin.Engine
	server      *httptest.Server
	provider    *wsInflightProvider
	httpClient  *http.Client
	billingRepo *wsInflightBillingObserver
}

func newWSInflightFixture(t *testing.T, mode string, source string, prices map[string]float64, billingOverride ...*service.BillingService) *wsInflightFixture {
	t.Helper()
	logger.InitBootstrap()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	client := inflightTestEntClient(t)
	rdb := testRedis(t)
	p := newWSInflightProvider(t)
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	cfg.Billing.InflightReservation.DefaultMaxOutputTokens = 8
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	cfg.Gateway.MaxLineSize = 1 << 20
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 15
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 5
	ingress := service.OpenAIWSIngressModeCtxPool
	if mode == "passthrough" {
		ingress = service.OpenAIWSIngressModePassthrough
	}
	if mode == "bridge" {
		cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
		cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	}
	user := mustCreateUser(t, client, &service.User{Email: "ws-funding-" + uuid.NewString() + "@example.com", Balance: .75, Concurrency: 10})
	group := mustCreateGroup(t, client, &service.Group{Name: "ws-funding-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 1})
	_, err := client.Group.UpdateOneID(group.ID).SetAllowImageGeneration(true).Save(ctx)
	require.NoError(t, err)
	channel := &service.Channel{Name: "ws-funding-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: source, GroupIDs: []int64{group.ID}}
	for model, price := range prices {
		priceCopy := price
		pricing := service.ChannelModelPricing{Platform: service.PlatformOpenAI, Models: []string{model}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &priceCopy}
		if strings.HasPrefix(model, "token:") {
			zero := 0.0
			pricing.Models = []string{strings.TrimPrefix(model, "token:")}
			pricing.BillingMode = service.BillingModeToken
			pricing.PerRequestPrice = nil
			pricing.InputPrice = &zero
			pricing.OutputPrice = &priceCopy
			pricing.CacheWritePrice = &zero
			pricing.CacheReadPrice = &zero
		}
		channel.ModelPricing = append(channel.ModelPricing, pricing)
	}
	channelRepo := NewChannelRepository(inflightTestDB(t))
	require.NoError(t, channelRepo.Create(ctx, channel))
	groups := NewGroupRepository(client, inflightTestDB(t))
	channels := service.NewChannelService(channelRepo, groups, nil, nil, nil)
	account := mustCreateAccount(t, client, &service.Account{Name: "ws-funding-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Concurrency: 10, Credentials: map[string]any{"api_key": "sk-provider", "base_url": p.server.URL}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": ingress}})
	schedulerCache := NewSchedulerCache(rdb)
	accounts := NewAccountRepository(client, inflightTestDB(t), schedulerCache)
	require.NoError(t, accounts.BindGroups(ctx, account.ID, []int64{group.ID}))
	users := NewUserRepository(client, inflightTestDB(t))
	subs := NewUserSubscriptionRepository(client)
	rates := NewUserGroupRateRepository(inflightTestDB(t))
	keys := NewAPIKeyRepository(client, inflightTestDB(t))
	saved := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-ws-" + uuid.NewString()})
	apiKeys := service.NewAPIKeyService(keys, users, groups, subs, rates, NewAPIKeyCache(rdb), cfg)
	key, err := apiKeys.GetByKey(ctx, saved.Key)
	require.NoError(t, err)
	settings := service.NewSettingService(NewSettingRepository(client), cfg)
	billing := service.NewBillingCacheService(NewBillingCache(rdb), users, subs, keys, nil, rates, cfg, nil, settings)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(NewConcurrencyCache(rdb, 15, 30))
	snapshots := service.NewSchedulerSnapshotService(schedulerCache, NewSchedulerOutboxRepository(inflightTestDB(t)), accounts, groups, cfg)
	t.Cleanup(snapshots.Stop)
	billService := service.NewBillingService(cfg, nil)
	if len(billingOverride) > 0 {
		billService = billingOverride[0]
	}
	httpClient := &http.Client{}
	t.Cleanup(httpClient.CloseIdleConnections)
	realBilling := NewUsageBillingRepository(client, inflightTestDB(t))
	observedBilling := &wsInflightBillingObserver{UsageBillingRepository: realBilling, BillingInflightRepository: realBilling.(service.BillingInflightRepository), commands: make(chan service.UsageBillingCommand, 16)}
	wheel, err := service.NewTimingWheelService()
	require.NoError(t, err)
	t.Cleanup(wheel.Stop)
	deferred := service.NewDeferredService(accounts, wheel, time.Minute)
	t.Cleanup(deferred.Stop)
	gateway := service.NewOpenAIGatewayService(accounts, NewUsageLogRepository(client, inflightTestDB(t)), observedBilling, users, subs, rates, NewGatewayCache(rdb), cfg, snapshots, concurrency, billService, service.NewRateLimitService(accounts, nil, cfg, nil, nil), billing, wsInflightHTTPTransport{client: httpClient}, deferred, nil, nil, service.NewModelPricingResolver(channels, billService), channels, nil, settings, nil, nil, groups)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 16, TaskTimeout: 10 * time.Second})
	t.Cleanup(pool.Stop)
	handler := gatewayhandler.NewOpenAIGatewayHandler(gateway, concurrency, billing, apiKeys, pool, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: 10})
		c.Next()
	})
	wsDone := make(chan struct{})
	var wsStarted atomic.Bool
	router.GET("/v1/responses", func(c *gin.Context) { wsStarted.Store(true); defer close(wsDone); handler.ResponsesWebSocket(c) })
	router.POST("/v1/responses", handler.Responses)
	router.POST("/v1/chat/completions", handler.ChatCompletions)
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		p.stopOnce.Do(func() { close(p.stop) })
		server.Close()
		if wsStarted.Load() {
			select {
			case <-wsDone:
			case <-time.After(5 * time.Second):
				t.Error("real WS handler did not release its resources before fixture cleanup")
			}
		}
	})
	return &wsInflightFixture{pool: pool, accounts: accounts, accountID: account.ID, groupID: group.ID, userID: user.ID, key: key, gateway: gateway, billing: billing, router: router, server: server, provider: p, httpClient: httpClient, billingRepo: observedBilling}
}
func (f *wsInflightFixture) dial(t *testing.T) *coderws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(coderws.StatusNormalClosure, "test complete"); _ = conn.CloseNow() })
	return conn
}
func wsInflightWrite(t *testing.T, conn *coderws.Conn, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
}
func wsInflightReadCompleted(t *testing.T, conn *coderws.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		_, body, err := conn.Read(ctx)
		require.NoError(t, err)
		if gjson.GetBytes(body, "type").String() == "response.completed" {
			return
		}
		require.NotEqual(t, "error", gjson.GetBytes(body, "type").String(), string(body))
	}
}
func (f *wsInflightFixture) held(t *testing.T) float64 {
	t.Helper()
	var amount float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COALESCE(SUM(amount),0) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.userID).Scan(&amount))
	return amount
}
func (f *wsInflightFixture) waitUsage(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int
		err := inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&count)
		return err == nil && count == n && f.held(t) == 0
	}, 10*time.Second, 20*time.Millisecond)
}
func (f *wsInflightFixture) wallet(t *testing.T) float64 {
	t.Helper()
	var amount float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.userID).Scan(&amount))
	return amount
}
func (f *wsInflightFixture) denyConcurrentHTTP(t *testing.T, model string) {
	t.Helper()
	before := f.provider.calls.Load()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(fmt.Sprintf(`{"model":%q,"input":"competing request","max_output_tokens":8}`, model)))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	f.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, before, f.provider.calls.Load(), "funds denied before provider dispatch")
}

func TestBillingInflightWS_RealTurnReservesBillsOnceAndReleases(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			conn := f.dial(t)
			for turn := 1; turn <= 2; turn++ {
				if turn == 2 {
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
				}
				wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"paid turn","max_output_tokens":8}`)
				pending := f.provider.next(t)
				require.Equal(t, "gpt-5.4", gjson.GetBytes(pending.payload, "model").String())
				require.InDelta(t, .5, f.held(t), 1e-9)
				f.denyConcurrentHTTP(t, "gpt-5.4")
				close(pending.release)
				wsInflightReadCompleted(t, conn)
				f.waitUsage(t, turn)
				require.InDelta(t, .25, f.wallet(t), 1e-9, "one real turn must settle exactly once")
			}
			require.EqualValues(t, 2, f.provider.calls.Load())
		})
	}
}

func TestBillingInflightWS_LocalBridgePrewarmDoesNotReserveOrBill(t *testing.T) {
	f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
	conn := f.dial(t)
	wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","generate":false,"input":[]}`)
	wsInflightReadCompleted(t, conn)
	require.EqualValues(t, 0, f.provider.calls.Load())
	require.Zero(t, f.held(t))
	require.InDelta(t, .75, f.wallet(t), 1e-9)
	var count int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&count))
	require.Zero(t, count)
	wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"real turn"}`)
	pending := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9)
	close(pending.release)
	wsInflightReadCompleted(t, conn)
	f.waitUsage(t, 1)
	require.InDelta(t, .25, f.wallet(t), 1e-9)
}

func TestBillingInflightWS_LaterModelUsesActualBillingSource(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		for _, source := range []string{service.BillingModelSourceRequested, service.BillingModelSourceUpstream} {
			inheritedModes := []bool{false}
			if mode == "passthrough" {
				inheritedModes = append(inheritedModes, true)
			}
			for _, inherited := range inheritedModes {
				name := fmt.Sprintf("%s/%s/session_%t", mode, source, inherited)
				t.Run(name, func(t *testing.T) {
					f := newWSInflightFixture(t, mode, source, map[string]float64{"gpt-5.4": .5, "gpt-5.1": 0})
					conn := f.dial(t)
					wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"initial paid A"}`)
					first := f.provider.next(t)
					require.InDelta(t, .5, f.held(t), 1e-9)
					close(first.release)
					wsInflightReadCompleted(t, conn)
					f.waitUsage(t, 1)
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
					secondPayload := `{"type":"response.create","model":"gpt-5.1","input":"later B"}`
					if inherited {
						wsInflightWrite(t, conn, `{"type":"session.update","session":{"model":"gpt-5.1"}}`)
						secondPayload = `{"type":"response.create","input":"inherited later B"}`
					}
					wsInflightWrite(t, conn, secondPayload)
					second := f.provider.next(t)
					require.Equal(t, "gpt-5.1", second.effectiveModel, "provider uses actual B, including preserved session inheritance")
					expected := 0.0
					if source == service.BillingModelSourceRequested || mode == "passthrough" {
						expected = .5
					}
					require.InDelta(t, expected, f.held(t), 1e-9, "reservation must follow existing completion billing source, not only latest draft model")
					if expected > 0 {
						f.denyConcurrentHTTP(t, "gpt-5.4")
					}
					close(second.release)
					wsInflightReadCompleted(t, conn)
					f.waitUsage(t, 2)
					require.InDelta(t, .75-expected, f.wallet(t), 1e-9)
					var amount float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT actual_cost FROM usage_logs WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, f.userID).Scan(&amount))
					require.InDelta(t, expected, amount, 1e-9)
				})
			}
		}
	}
}

func TestBillingInflightWS_ImageToolsMatchExistingSettlementWhenTextIsFree(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": 0, "gpt-image-2": .5})
			conn := f.dial(t)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"free text"}`)
			first := f.provider.next(t)
			require.Zero(t, f.held(t))
			close(first.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			require.InDelta(t, .75, f.wallet(t), 1e-9)
			imagePayload := `{"type":"response.create","model":"gpt-5.4","input":"draw a cat","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`
			if mode == "passthrough" {
				wsInflightWrite(t, conn, `{"type":"session.update","session":{"tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}}`)
				imagePayload = `{"type":"response.create","model":"gpt-5.4","input":"draw a cat"}`
			}
			wsInflightWrite(t, conn, imagePayload)
			second := f.provider.next(t)
			expectedCost := .5
			expectedImageCount := 1
			if mode == "passthrough" {
				// Existing v2 adapter supplies no ImageCount/BillingModel to
				// settlement. Reservation must match that unchanged zero cost.
				expectedCost = 0
				expectedImageCount = 0
			}
			require.InDelta(t, expectedCost, f.held(t), 1e-9, "reservation follows each adapter's existing image metadata")
			close(second.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 2)
			require.InDelta(t, .75-expectedCost, f.wallet(t), 1e-9, "actual image follows existing settlement")
			var count int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT image_count FROM usage_logs WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, f.userID).Scan(&count))
			require.Equal(t, expectedImageCount, count)
		})
	}
}

func TestBillingInflightWS_UnknownBridgeProcessingRetainsBoundedHold(t *testing.T) {
	for _, failure := range []string{"header_timeout", "scanner"} {
		t.Run(failure, func(t *testing.T) {
			f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			if failure == "header_timeout" {
				f.httpClient.Timeout = 150 * time.Millisecond
			} else {
				f.provider.fault.Store("scanner")
			}
			conn := f.dial(t)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"processing outcome remains unknown"}`)
			pending := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			if failure == "scanner" {
				close(pending.release)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for {
				_, event, err := conn.Read(ctx)
				if err != nil {
					require.NotEqual(t, context.DeadlineExceeded, ctx.Err(), "gateway must terminate failed turn")
					break
				}
				require.NotEqual(t, "response.completed", gjson.GetBytes(event, "type").String(), string(event))
			}
			require.InDelta(t, .5, f.held(t), 1e-9, "provider processing is not provably free, even with no usage result")
			require.InDelta(t, .75, f.wallet(t), 1e-9)
			f.denyConcurrentHTTP(t, "gpt-5.4")
			var logs, dedup int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&logs))
			require.Zero(t, logs)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Zero(t, dedup)
			var seconds float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT EXTRACT(EPOCH FROM MAX(expires_at)-clock_timestamp()) FROM billing_inflight_leases WHERE user_id=$1 AND phase='attempt'`, f.userID).Scan(&seconds))
			require.Greater(t, seconds, 850.0)
			require.LessOrEqual(t, seconds, 900.0)
		})
	}
}

func TestBillingInflightWS_QueuedTurnsCannotConsumeAnotherAttempt(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"token:gpt-5.4": .0625})
			gate := make(chan struct{})
			f.billingRepo.firstApplyGate = gate
			released := false
			t.Cleanup(func() {
				if !released {
					close(gate)
				}
			})
			conn := f.dial(t)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"first delayed settlement","max_output_tokens":8}`)
			first := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(first.release)
			wsInflightReadCompleted(t, conn)
			var firstCommand service.UsageBillingCommand
			select {
			case firstCommand = <-f.billingRepo.commands:
			case <-time.After(5 * time.Second):
				t.Fatal("real first Apply did not start")
			}
			require.NotEmpty(t, firstCommand.InflightObligationID)
			require.InDelta(t, .0625, f.held(t), 1e-9, "known cost replaces estimate before worker Apply")
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"second live attempt","max_output_tokens":8}`)
			second := f.provider.next(t)
			require.InDelta(t, .5625, f.held(t), 1e-9, "pending first actual plus second attempt estimate")
			f.denyConcurrentHTTP(t, "gpt-5.4")
			close(gate)
			released = true
			require.Eventually(t, func() bool { return f.wallet(t) == .6875 && f.held(t) == .5 }, 5*time.Second, 20*time.Millisecond, "old queued task must consume only its immutable obligation")
			var oldPending int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE id=$1 AND phase='pending'`, firstCommand.InflightObligationID).Scan(&oldPending))
			require.Zero(t, oldPending)
			close(second.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 2)
			secondCommand := <-f.billingRepo.commands
			require.NotEmpty(t, secondCommand.InflightObligationID)
			require.NotEqual(t, firstCommand.InflightObligationID, secondCommand.InflightObligationID)
			require.InDelta(t, .625, f.wallet(t), 1e-9)
			require.EqualValues(t, 2, f.billingRepo.calls.Load())
		})
	}
}

func TestBillingInflightWS_ProviderReplayDoesNotBillOrRetainAnExtraObligation(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			f.provider.replay.Store(true)
			conn := f.dial(t)
			payload := `{"type":"response.create","model":"gpt-5.4","input":"same logical provider turn","max_output_tokens":8}`
			wsInflightWrite(t, conn, payload)
			first := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(first.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			firstCommand := <-f.billingRepo.commands
			require.NotEmpty(t, firstCommand.InflightObligationID)
			wsInflightWrite(t, conn, payload)
			second := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			f.denyConcurrentHTTP(t, "gpt-5.4")
			close(second.release)
			wsInflightReadCompleted(t, conn)
			require.Eventually(t, func() bool { return f.billingRepo.calls.Load() == 2 && f.held(t) == 0 }, 5*time.Second, 20*time.Millisecond, "idempotent provider replay must release its own newly staged obligation")
			secondCommand := <-f.billingRepo.commands
			require.NotEqual(t, firstCommand.InflightObligationID, secondCommand.InflightObligationID)
			require.InDelta(t, .25, f.wallet(t), 1e-9, "same provider response id and payload must bill once")
			var logs, dedup int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&logs))
			require.Equal(t, 1, logs)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Equal(t, 1, dedup)
		})
	}
}

func (f *wsInflightFixture) addAccount(t *testing.T) {
	t.Helper()
	original, err := f.accounts.GetByID(context.Background(), f.accountID)
	require.NoError(t, err)
	another := *original
	another.ID = 0
	another.Name = "ws-funding-second-" + uuid.NewString()
	require.NoError(t, f.accounts.Create(context.Background(), &another))
	require.NoError(t, f.accounts.BindGroups(context.Background(), another.ID, []int64{f.groupID}))
}
func TestBillingInflightWS_CanonicalBridgeAuthRefusalReleasesItsTurn(t *testing.T) {
	for _, failure := range []string{"401", "403"} {
		t.Run(failure, func(t *testing.T) {
			f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			f.addAccount(t)
			conn := f.dial(t)
			// A local prewarm is excluded from the upstream ordinal. Finish
			// one actual healthy turn to exercise a terminal turn-two refusal.
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"healthy first upstream turn"}`)
			healthy := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(healthy.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			require.InDelta(t, .25, f.wallet(t), 1e-9)
			_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
			require.NoError(t, err)
			require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
			f.provider.fault.Store(failure)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"rejected authentication"}`)
			first := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(first.release)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for {
				_, event, err := conn.Read(ctx)
				if err != nil {
					require.NotEqual(t, context.DeadlineExceeded, ctx.Err())
					break
				}
				require.NotEqual(t, "response.completed", gjson.GetBytes(event, "type").String())
			}
			require.Eventually(t, func() bool { return f.held(t) == 0 }, 5*time.Second, 20*time.Millisecond, "complete canonical authentication rejection must release this WS turn lease")
			require.InDelta(t, .75, f.wallet(t), 1e-9)
			require.EqualValues(t, 1, f.billingRepo.calls.Load(), "rejected turn must not add a billing command")
			var logs int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&logs))
			require.Equal(t, 1, logs, "only the preceding successful turn has usage")
			f.provider.fault.Store("")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"another funded request"}`))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				f.router.ServeHTTP(rec, req.WithContext(ctx))
				done <- rec
			}()
			next := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(next.release)
			select {
			case rec := <-done:
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("independent HTTP request did not finish")
			}
			f.waitUsage(t, 2)
			require.InDelta(t, .25, f.wallet(t), 1e-9)
		})
	}
}

func TestBillingInflightWS_BlockedModelMismatchAuditDoesNotStrandFunding(t *testing.T) {
	for _, mode := range []string{"native", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			f.addAccount(t)
			f.provider.fault.Store("mismatch")
			conn := f.dial(t)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"must receive requested model"}`)
			first := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(first.release)
			second := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9, "blocked zero-cost attempt must not stack a second estimate on replay")
			close(second.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 2)
			require.InDelta(t, .25, f.wallet(t), 1e-9, "only accepted response is billed")
			var audit, normal, dedup int
			var auditCost float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1 AND upstream_model_mismatch=true`, f.userID).Scan(&audit, &auditCost))
			require.Equal(t, 1, audit)
			require.Zero(t, auditCost)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1 AND upstream_model_mismatch=false`, f.userID).Scan(&normal))
			require.Equal(t, 1, normal)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Equal(t, 1, dedup, "zero-cost audit must not claim settlement idempotency")
			_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
			require.NoError(t, err)
			require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"third logical owner after safe replay"}`))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				f.router.ServeHTTP(rec, req.WithContext(ctx))
				done <- rec
			}()
			third := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			close(third.release)
			select {
			case rec := <-done:
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("third owner did not finish")
			}
			f.waitUsage(t, 3)
			require.InDelta(t, .25, f.wallet(t), 1e-9)
			require.EqualValues(t, 2, f.billingRepo.calls.Load())
		})
	}
}

// Native/bridge already inherit the first model when a later response.create
// omits it. This does not add session.update support to either adapter.
func TestBillingInflightWS_InitialImageModelInheritanceRemainsPaid(t *testing.T) {
	for _, mode := range []string{"native", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-image-2": .5})
			conn := f.dial(t)
			for turn := 1; turn <= 2; turn++ {
				payload := `{"type":"response.create","model":"gpt-image-2","input":"draw cat","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`
				if turn == 2 {
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
					payload = `{"type":"response.create","input":"draw dog"}`
				}
				wsInflightWrite(t, conn, payload)
				pending := f.provider.next(t)
				require.Equal(t, "gpt-image-2", pending.effectiveModel)
				require.InDelta(t, .5, f.held(t), 1e-9)
				f.denyConcurrentHTTP(t, "gpt-image-2")
				close(pending.release)
				wsInflightReadCompleted(t, conn)
				f.waitUsage(t, turn)
				require.InDelta(t, .25, f.wallet(t), 1e-9)
			}
			var images int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT sum(image_count) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&images))
			require.Equal(t, 2, images)
			require.EqualValues(t, 2, f.billingRepo.calls.Load())
		})
	}
}

// V2 had no upstream-model mismatch filtering before this PR. Keep its actual
// normal settlement, rather than introducing a new audit/retry policy here.
func TestBillingInflightWS_PassthroughBaselineMismatchRemainsNormalSettlement(t *testing.T) {
	f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
	f.addAccount(t)
	f.provider.fault.Store("mismatch")
	conn := f.dial(t)
	wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"existing v2 metadata"}`)
	pending := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9)
	close(pending.release)
	wsInflightReadCompleted(t, conn)
	f.waitUsage(t, 1)
	require.InDelta(t, .25, f.wallet(t), 1e-9)
	require.EqualValues(t, 1, f.provider.calls.Load(), "v2 baseline does not retry model mismatch")
	var audit, dedup int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1 AND upstream_model_mismatch=true`, f.userID).Scan(&audit))
	require.Zero(t, audit)
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
	require.Equal(t, 1, dedup)
}
