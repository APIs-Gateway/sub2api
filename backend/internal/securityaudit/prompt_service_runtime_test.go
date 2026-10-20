package securityaudit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPromptServiceHasExplicitIdempotentLifecycle(t *testing.T) {
	config := NewConfigManager(nil, &configTestSettings{values: map[string]string{
		SettingKeyPromptAuditConfig: "",
		SettingKeyRiskControl:       "false",
	}}, nil, prefixEncryptor{})
	service := NewPromptService(
		config,
		NewPostgreSQLRepository(nil),
		NewRedisPayloadStore(nil),
		NewOpenAICompatibleScanner(),
		NewAtomicMetrics(),
	)

	require.Nil(t, service.cancel)
	require.NoError(t, service.Start(context.Background()))
	require.NotNil(t, service.cancel)
	require.NoError(t, service.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, service.Shutdown(ctx))
	require.Nil(t, service.cancel)
	require.NoError(t, service.Shutdown(ctx))
}

func TestPromptServiceStartReportsMissingDependencies(t *testing.T) {
	service := &PromptService{}
	require.Error(t, service.Start(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, service.Shutdown(ctx))
}

func TestPromptServiceEvaluateFailClosedAndSkipsNonBlockingRequests(t *testing.T) {
	request := Request{Protocol: "openai_chat_completions", Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}

	config := &enqueueTestConfig{active: ActiveConfig{RiskControlEnabled: true, Enabled: true, BlockingEnabled: false, AllGroups: true}, ok: true}
	service := &PromptService{config: config, evaluator: NewGuardEvaluator(nil, nil, NewAtomicMetrics())}
	decision, err := service.Evaluate(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.True(t, decision.AllowNextStage)

	config.active.BlockingEnabled = true
	decision, err = service.Evaluate(context.Background(), request)
	require.Error(t, err)
	require.Nil(t, decision)
	var guardErr *GuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)
}

func TestPromptServiceEvaluateAllowsNoPromptText(t *testing.T) {
	config := &enqueueTestConfig{active: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, BlockingEnabled: true,
	}, ok: true}
	service := &PromptService{config: config, evaluator: NewGuardEvaluator(nil, nil, NewAtomicMetrics())}

	decision, err := service.Evaluate(context.Background(), Request{Protocol: "openai_chat_completions", Body: []byte(`{"messages":[{"role":"function","content":"tool result"}]}`)})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
}

func TestPromptServiceDelegatesConfigAndReportsDegradedRuntime(t *testing.T) {
	config := &enqueueTestConfig{active: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, BlockingEnabled: false,
		WorkerCount: 2, QueueCapacity: 9, ConfigVersion: 3,
	}, ok: true}
	metrics := NewAtomicMetrics()
	service := &PromptService{
		config: config, payload: NewRedisPayloadStore(nil),
		runner: &Runner{}, metrics: metrics, clock: fixedTestClock{now: time.Unix(1700000000, 0).UTC()},
		probes: map[string]ProbeResult{},
	}

	require.Equal(t, config.Public(), service.GetConfig())
	_, err := service.SaveConfig(context.Background(), UpdateConfigRequest{}, 7)
	require.NoError(t, err)
	runtime := service.Runtime(context.Background())
	require.Equal(t, ModeAsync, runtime.EffectiveMode)
	require.Equal(t, "degraded", runtime.ProcessStatus)
	require.Equal(t, int64(3), runtime.ActiveConfigVersion)
	require.Equal(t, 2, runtime.WorkerTotal)
	require.Equal(t, "error", runtime.DatabaseStatus)
	require.Equal(t, "error", runtime.RedisStatus)
	require.Equal(t, "payload_store_unavailable", runtime.LastErrorCode)

	var nilService *PromptService
	require.Equal(t, ModeOff, nilService.EffectiveMode())
}

func TestPromptServiceEnqueueRequiresLifecycleAndCopiesWork(t *testing.T) {
	config := asyncEnqueueConfig()
	metrics := NewAtomicMetrics()
	repo := &enqueueTestRepo{}
	payload := &enqueueTestPayload{}
	service := &PromptService{
		config: config, enqueuer: NewEnqueuer(config, repo, payload, metrics), metrics: metrics,
		enqueueSlots: make(chan struct{}, 1),
	}
	request := enqueueRequest(`{"messages":[{"role":"user","content":"hello"}]}`)
	require.Error(t, service.Enqueue(context.Background(), request))

	service.background = context.Background()
	require.NoError(t, service.Enqueue(context.Background(), request))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, service.Shutdown(ctx))
	require.Equal(t, int64(1), metrics.AuditSnapshot().Enqueued)
}

type degradedPromptConfig struct{ *enqueueTestConfig }

func (degradedPromptConfig) BlockingActivationDegraded() bool { return true }

func TestPromptServiceEnqueueAndEvaluateGuardBranches(t *testing.T) {
	request := enqueueRequest(`{"messages":[{"role":"user","content":"hello"}]}`)
	metrics := NewAtomicMetrics()
	config := asyncEnqueueConfig()
	service := &PromptService{
		config: config, enqueuer: NewEnqueuer(config, &enqueueTestRepo{}, &enqueueTestPayload{}, metrics),
		metrics: metrics, enqueueSlots: make(chan struct{}, 1),
	}
	service.enqueueSlots <- struct{}{}
	require.NoError(t, service.Enqueue(context.Background(), request))
	require.Equal(t, int64(1), metrics.AuditSnapshot().Dropped)
	<-service.enqueueSlots

	config.active.Enabled = false
	require.NoError(t, service.Enqueue(context.Background(), request))
	var nilService *PromptService
	require.NoError(t, nilService.Enqueue(context.Background(), request))

	var guardErr *GuardError
	_, err := nilService.Evaluate(context.Background(), request)
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)

	service.config = degradedPromptConfig{enqueueTestConfig: config}
	_, err = service.Evaluate(context.Background(), request)
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)

	service.config = &enqueueTestConfig{active: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, BlockingEnabled: true, AllGroups: false, GroupIDs: []int64{9},
	}, ok: true}
	service.evaluator = NewGuardEvaluator(nil, nil, NewAtomicMetrics())
	groupID := int64(8)
	decision, err := service.Evaluate(context.Background(), Request{GroupID: &groupID, Protocol: request.Protocol, Body: request.Body})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)

	service.config = &enqueueTestConfig{active: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, BlockingEnabled: true, AllGroups: true,
	}, ok: true}
	_, err = service.Evaluate(context.Background(), Request{Protocol: request.Protocol, Body: []byte("not-json")})
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeInvalidResponse, guardErr.Code)
}

func TestPromptServiceProbeUsesSafeStoredTokenAndTracksFailures(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = writer.Write([]byte(`{"data":[{"id":"guard-model"}]}`))
		}
	}))
	defer server.Close()

	config := &enqueueTestConfig{active: ActiveConfig{Endpoints: []ActiveEndpoint{{
		ID: "guard", BaseURL: server.URL, Token: "stored-secret", Enabled: true,
	}}}, ok: true}
	service := &PromptService{
		config: config, clock: fixedTestClock{now: time.Unix(1700000000, 0).UTC()},
		probes: map[string]ProbeResult{},
	}
	request := ProbeRequest{Endpoint: UpdateEndpoint{ID: "guard", BaseURL: server.URL, Model: "guard-model", TimeoutMS: 1000, InputLimit: 128}}
	result := service.Probe(context.Background(), request)
	require.True(t, result.OK)
	require.True(t, result.TokenApplied)
	require.Equal(t, "healthy", result.Status)
	require.Equal(t, result, service.probeSnapshot()["guard"])

	status = http.StatusUnauthorized
	result = service.Probe(context.Background(), request)
	require.False(t, result.OK)
	require.Equal(t, "authentication_failed", result.ErrorCode)

	status = http.StatusInternalServerError
	result = service.Probe(context.Background(), request)
	require.False(t, result.OK)
	require.Equal(t, "probe_http_error", result.ErrorCode)
	require.True(t, result.Retryable)

	invalid := service.Probe(context.Background(), ProbeRequest{Endpoint: UpdateEndpoint{ID: "guard", BaseURL: "https://metadata.google.internal"}})
	require.Equal(t, "endpoint_invalid", invalid.ErrorCode)
	require.False(t, invalid.TokenApplied)
	require.False(t, modelsResponseReady([]byte("not-json"), "guard-model"))
	require.True(t, modelsResponseReady([]byte(`{"data":[{"id":"guard-model"}]}`), ""))
	require.False(t, modelsResponseReady([]byte(`{"data":[{"id":"other"}]}`), "guard-model"))
}

func TestPromptServiceProbeConnectionAndScannerFailures(t *testing.T) {
	service := &PromptService{
		config:  &enqueueTestConfig{active: ActiveConfig{AllGroups: true}, ok: true},
		scanner: NewOpenAICompatibleScanner(), clock: fixedTestClock{now: time.Unix(1700000000, 0).UTC()},
		probes: map[string]ProbeResult{},
	}
	result := service.Probe(context.Background(), ProbeRequest{Endpoint: UpdateEndpoint{
		ID: "down", BaseURL: "http://127.0.0.1:1", Model: "guard-model", TimeoutMS: 100,
	}})
	require.Equal(t, "connection_failed", result.ErrorCode)
	require.True(t, result.Retryable)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = writer.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	result = service.Probe(context.Background(), ProbeRequest{Endpoint: UpdateEndpoint{
		ID: "scanner", BaseURL: server.URL, Model: "guard-model", TimeoutMS: 1000, InputLimit: 128,
	}})
	require.False(t, result.OK)
	require.Equal(t, ErrorCodeInvalidResponse, result.ErrorCode)

	largeServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", int(maxGuardResponseBytes)+1)))
	}))
	defer largeServer.Close()
	result = service.Probe(context.Background(), ProbeRequest{Endpoint: UpdateEndpoint{
		ID: "large", BaseURL: largeServer.URL, Model: "guard-model", TimeoutMS: 1000, InputLimit: 128,
	}})
	require.Equal(t, "response_too_large", result.ErrorCode)
}

func TestPromptServiceEvaluateInactiveModesAndProbeDefaults(t *testing.T) {
	request := enqueueRequest(`{"messages":[{"role":"user","content":"hello"}]}`)
	config := &enqueueTestConfig{ok: false}
	service := &PromptService{config: config, evaluator: NewGuardEvaluator(nil, nil, NewAtomicMetrics())}

	decision, err := service.Evaluate(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)

	config.active = ActiveConfig{RiskControlEnabled: true, Enabled: true, BlockingEnabled: true}
	_, err = service.Evaluate(context.Background(), request)
	var guardErr *GuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)

	config.ok = true
	config.active = ActiveConfig{Endpoints: []ActiveEndpoint{{
		ID: "guard", BaseURL: "https://guard.example", Token: "stored-token", Enabled: true,
	}}}
	endpoint, tokenApplied, err := service.resolveProbeEndpoint(UpdateEndpoint{ID: "guard", BaseURL: "https://guard.example"})
	require.NoError(t, err)
	require.True(t, tokenApplied)
	require.Equal(t, "stored-token", endpoint.Token)
	require.Equal(t, DefaultGuardModel, endpoint.Model)
	require.Equal(t, DefaultTimeoutMS, endpoint.TimeoutMS)
	require.Equal(t, DefaultInputLimit, endpoint.InputLimit)

	endpoint, tokenApplied, err = service.resolveProbeEndpoint(UpdateEndpoint{ID: "guard", BaseURL: "https://other.example/v1"})
	require.NoError(t, err)
	require.False(t, tokenApplied)
	require.Empty(t, endpoint.Token)

	_, _, err = service.resolveProbeEndpoint(UpdateEndpoint{BaseURL: "https://metadata.google.internal"})
	require.Error(t, err)
}
