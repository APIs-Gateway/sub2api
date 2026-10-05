//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const accountTestPrivateBody = `{"error":{"message":"private provider payload sk-do-not-log","access_token":"private-access-token","password":"private-password"}}`

type accountTestIndexedLog struct {
	accountID                                                    int64
	level, component, message, requestID, platform, model, extra string
}

func TestAccountTestLogging_ActualLoggerOpsPostgres(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []string{"manual", "background"} {
		for _, phase := range []string{"mapped", "default", "before_model", "not_found"} {
			t.Run(source+"/"+phase, func(t *testing.T) {
				db := inflightTestDB(t)
				client := inflightTestEntClient(t)
				require.NoError(t, logger.Init(logger.InitOptions{Level: "debug", Format: "json", Sampling: logger.SamplingOptions{Enabled: false}, Output: logger.OutputOptions{ToStdout: true}}))
				sink := service.NewOpsSystemLogSink(NewOpsRepository(db))
				logger.SetSink(sink)
				t.Cleanup(func() { logger.SetSink(nil) })
				sink.Start()
				t.Cleanup(sink.Stop)
				var calls atomic.Int32
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					require.Equal(t, "/v1/responses", r.URL.Path)
					var payload map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					expected := "provider-model"
					if phase == "default" {
						expected = openai.DefaultTestModel
					}
					require.Equal(t, expected, payload["model"])
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, accountTestPrivateBody)
				}))
				t.Cleanup(provider.Close)
				credentials := map[string]any{"api_key": "sk-private-test-credential", "base_url": provider.URL, "model_mapping": map[string]any{"client-alias": "provider-model"}}
				if phase == "before_model" {
					delete(credentials, "api_key")
				}
				account := mustCreateAccount(t, client, &service.Account{Name: "account-log-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: credentials, Extra: map[string]any{"openai_responses_supported": true}})
				accounts := NewAccountRepository(client, db, nil)
				accountID := account.ID
				requested := "client-alias"
				if phase == "default" {
					requested = ""
				}
				if phase == "not_found" {
					accountID += 1000000
				}
				cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
				svc := service.NewAccountTestService(accounts, nil, nil, nil, wsInflightHTTPTransport{client: provider.Client()}, cfg, nil)
				requestID := ""
				expectedError := "API returned 403: " + accountTestPrivateBody
				if phase == "before_model" {
					expectedError = "No API key available"
				}
				if phase == "not_found" {
					expectedError = "Account not found"
				}
				if source == "manual" {
					router := gin.New()
					router.Use(middleware.RequestLogger())
					handler := adminhandler.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, nil, nil)
					router.POST("/api/v1/admin/accounts/:id/test", handler.Test)
					rec := httptest.NewRecorder()
					requestID = "manual-" + uuid.NewString()
					req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/"+strconv.FormatInt(accountID, 10)+"/test", strings.NewReader(fmt.Sprintf(`{"model_id":%q,"prompt":"private-test-prompt"}`, requested)))
					req.Header.Set("X-Request-ID", requestID)
					req.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(rec, req)
					require.Equal(t, requestID, rec.Header().Get("X-Request-ID"))
					var errorEvents int
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						var event service.TestEvent
						require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
						if event.Type == "error" {
							errorEvents++
							require.Equal(t, expectedError, event.Error)
						}
					}
					require.Equal(t, 1, errorEvents, "client error remains exact and single")
				} else {
					result, err := svc.RunTestBackground(context.Background(), accountID, requested)
					require.NoError(t, err)
					require.Equal(t, "failed", result.Status)
					require.Equal(t, expectedError, result.ErrorMessage)
					require.GreaterOrEqual(t, result.LatencyMs, int64(0))
				}
				sink.Stop() // Drain the actual logger->sink queue before SQL assertions.
				rows, err := db.Query(`SELECT COALESCE(account_id,0),level,component,message,COALESCE(request_id,''),COALESCE(platform,''),COALESCE(model,''),extra::text FROM ops_system_logs ORDER BY id`)
				require.NoError(t, err)
				defer func() { _ = rows.Close() }()
				var logs []accountTestIndexedLog
				for rows.Next() {
					var log accountTestIndexedLog
					require.NoError(t, rows.Scan(&log.accountID, &log.level, &log.component, &log.message, &log.requestID, &log.platform, &log.model, &log.extra))
					logs = append(logs, log)
				}
				require.NoError(t, rows.Err())
				require.Len(t, logs, 1, "one failure, not duplicate stdlog+structured events")
				log := logs[0]
				require.Equal(t, accountID, log.accountID)
				require.Equal(t, "error", log.level, "preserve old stdlog ERROR severity")
				require.Equal(t, "service.account_test", log.component)
				require.Equal(t, "account_test.failed", log.message)
				if source == "manual" {
					require.Equal(t, requestID, log.requestID)
				} else {
					_, err = uuid.Parse(log.requestID)
					require.NoError(t, err)
				}
				expectedPlatform, expectedModel := service.PlatformOpenAI, "provider-model"
				if phase == "default" {
					expectedModel = openai.DefaultTestModel
				}
				if phase == "before_model" {
					expectedModel = ""
				}
				if phase == "not_found" {
					expectedPlatform = ""
					expectedModel = ""
				}
				require.Equal(t, expectedPlatform, log.platform)
				require.Equal(t, expectedModel, log.model)
				var fields map[string]any
				require.NoError(t, json.Unmarshal([]byte(log.extra), &fields))
				require.Equal(t, source, fields["test_source"])
				if requested != "" {
					require.Equal(t, requested, fields["requested_model"])
				} else {
					require.NotContains(t, fields, "requested_model")
				}
				for _, secret := range []string{"private provider payload", "sk-do-not-log", "private-access-token", "private-password", "sk-private-test-credential", "private-test-prompt"} {
					require.NotContains(t, log.message+log.extra, secret)
				}
				expectedCalls := int32(1)
				if phase == "before_model" || phase == "not_found" {
					expectedCalls = 0
				}
				require.Equal(t, expectedCalls, calls.Load())
			})
		}
	}
	t.Run("four_concurrent_invocations", func(t *testing.T) {
		db := inflightTestDB(t)
		client := inflightTestEntClient(t)
		require.NoError(t, logger.Init(logger.InitOptions{Level: "debug", Format: "json", Sampling: logger.SamplingOptions{Enabled: false}, Output: logger.OutputOptions{ToStdout: true}}))
		sink := service.NewOpsSystemLogSink(NewOpsRepository(db))
		logger.SetSink(sink)
		t.Cleanup(func() { logger.SetSink(nil) })
		sink.Start()
		t.Cleanup(sink.Stop)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, accountTestPrivateBody)
		}))
		t.Cleanup(provider.Close)
		type expectedLog struct{ source, model, requestID string }
		expected := map[int64]expectedLog{}
		var ids []int64
		for i := 0; i < 4; i++ {
			account := mustCreateAccount(t, client, &service.Account{Name: "concurrent-log-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-secret", "base_url": provider.URL}, Extra: map[string]any{"openai_responses_supported": true}})
			source := "manual"
			id := "manual-" + uuid.NewString()
			if i%2 == 1 {
				source = "background"
				id = ""
			}
			expected[account.ID] = expectedLog{source: source, model: fmt.Sprintf("model-%d", i), requestID: id}
			ids = append(ids, account.ID)
		}
		svc := service.NewAccountTestService(NewAccountRepository(client, db, nil), nil, nil, nil, wsInflightHTTPTransport{client: provider.Client()}, &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}, nil)
		parent := logger.IntoContext(context.Background(), logger.L().With(zap.String("request_id", "shared-parent-must-not-leak")))
		router := gin.New()
		router.Use(middleware.RequestLogger())
		handler := adminhandler.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, nil, nil)
		router.POST("/api/v1/admin/accounts/:id/test", handler.Test)
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for _, id := range ids {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				e := expected[id]
				if e.source == "background" {
					result, err := svc.RunTestBackground(parent, id, e.model)
					if err != nil {
						errs <- err
						return
					}
					if result.Status != "failed" || result.ErrorMessage != "API returned 403: "+accountTestPrivateBody {
						errs <- fmt.Errorf("unexpected background result: %+v", result)
					}
					return
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/test", strings.NewReader(fmt.Sprintf(`{"model_id":%q}`, e.model)))
				req.Header.Set("X-Request-ID", e.requestID)
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(rec, req)
				if !strings.Contains(rec.Body.String(), "API returned 403") {
					errs <- fmt.Errorf("missing client SSE: %s", rec.Body.String())
				}
			}(id)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		sink.Stop()
		rows, err := db.Query(`SELECT COALESCE(account_id,0),COALESCE(request_id,''),model,extra->>'test_source' FROM ops_system_logs WHERE message='account_test.failed'`)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		seen := map[int64]bool{}
		requestIDs := map[string]bool{}
		for rows.Next() {
			var id int64
			var requestID, model, source string
			require.NoError(t, rows.Scan(&id, &requestID, &model, &source))
			e, ok := expected[id]
			require.True(t, ok)
			require.False(t, seen[id])
			seen[id] = true
			require.Equal(t, e.model, model)
			require.Equal(t, e.source, source)
			require.NotEqual(t, "shared-parent-must-not-leak", requestID)
			require.False(t, requestIDs[requestID], "correlation IDs must stay invocation-local")
			requestIDs[requestID] = true
			if source == "manual" {
				require.Equal(t, e.requestID, requestID)
			} else {
				_, err = uuid.Parse(requestID)
				require.NoError(t, err)
			}
		}
		require.NoError(t, rows.Err())
		require.Len(t, seen, 4)
	})
}
