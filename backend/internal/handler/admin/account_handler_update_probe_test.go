//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// probeSignalRepo 只实现探测入口会调用的 GetByID：探测 goroutine 一启动就会先读账号，
// 以此作为「探测被调度」的确定性信号（账号无 api_key，读完即返回，不发任何网络请求）。
type probeSignalRepo struct {
	service.AccountRepository
	signal chan int64
}

func (r *probeSignalRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	select {
	case r.signal <- id:
	default:
	}
	return &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}, nil
}

// probeAdminStub 在通用 stub 之上控制更新前后的账号。
type probeAdminStub struct {
	*stubAdminService
	before   *service.Account
	getErr   error
	after    *service.Account
	getCalls atomic.Int32
}

func (s *probeAdminStub) GetAccount(_ context.Context, _ int64) (*service.Account, error) {
	s.getCalls.Add(1)
	if s.getErr != nil {
		return nil, s.getErr
	}
	cp := *s.before
	return &cp, nil
}

func (s *probeAdminStub) UpdateAccount(_ context.Context, _ int64, _ *service.UpdateAccountInput) (*service.Account, error) {
	cp := *s.after
	return &cp, nil
}

func probeTestAccount(baseURL, apiKey string, extra map[string]any) *service.Account {
	return &service.Account{
		ID:          42,
		Name:        "relay",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Credentials: map[string]any{"base_url": baseURL, "api_key": apiKey},
		Extra:       extra,
	}
}

// runUpdateProbeCase 发起 PUT 并断言探测是否被调度。期望探测时最多等 5s（收到信号即返回，
// 兼容 -race/繁忙 CI）；期望不探测时等 300ms 并断言没有信号。
func runUpdateProbeCase(t *testing.T, stub *probeAdminStub, body map[string]any, wantProbe bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &probeSignalRepo{signal: make(chan int64, 4)}
	testSvc := service.NewAccountTestService(repo, nil, nil, nil, nil, nil, nil)
	h := NewAccountHandler(stub, nil, nil, nil, nil, nil, nil, testSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.PUT("/api/v1/admin/accounts/:id", h.Update)

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/42", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	wait := 300 * time.Millisecond
	if wantProbe {
		wait = 5 * time.Second
	}
	select {
	case <-repo.signal:
		require.True(t, wantProbe, "probe was scheduled but should not have been")
	case <-time.After(wait):
		require.False(t, wantProbe, "probe was expected but not scheduled within %s", wait)
	}
}

func TestAccountHandlerUpdate_ProbeScheduling(t *testing.T) {
	known := map[string]any{"openai_responses_supported": true}
	creds := func(baseURL, apiKey string) map[string]any {
		m := map[string]any{"base_url": baseURL, "model_mapping": map[string]any{"a": "b"}}
		if apiKey != "" {
			m["api_key"] = apiKey
		}
		return m
	}

	t.Run("endpoint unchanged and capability known: no probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			before:           probeTestAccount("https://relay.example.com/v1", "sk-old", known),
			after:            probeTestAccount("https://relay.example.com/v1", "sk-old", known),
		}
		runUpdateProbeCase(t, stub, map[string]any{"credentials": creds("https://relay.example.com/v1", "")}, false)
		require.EqualValues(t, 1, stub.getCalls.Load())
	})

	t.Run("api_key changed: probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			before:           probeTestAccount("https://relay.example.com/v1", "sk-old", known),
			after:            probeTestAccount("https://relay.example.com/v1", "sk-new", known),
		}
		runUpdateProbeCase(t, stub, map[string]any{"credentials": creds("https://relay.example.com/v1", "sk-new")}, true)
	})

	t.Run("base_url changed: probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			before:           probeTestAccount("https://relay.example.com/v1", "sk-old", known),
			after:            probeTestAccount("https://other.example.com/v1", "sk-old", known),
		}
		runUpdateProbeCase(t, stub, map[string]any{"credentials": creds("https://other.example.com/v1", "")}, true)
	})

	t.Run("capability unknown, only model_mapping: probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			before:           probeTestAccount("https://relay.example.com/v1", "sk-old", nil),
			after:            probeTestAccount("https://relay.example.com/v1", "sk-old", nil),
		}
		runUpdateProbeCase(t, stub, map[string]any{"credentials": creds("https://relay.example.com/v1", "")}, true)
	})

	t.Run("GetAccount fails: still probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			getErr:           errors.New("db down"),
			after:            probeTestAccount("https://relay.example.com/v1", "sk-old", known),
		}
		runUpdateProbeCase(t, stub, map[string]any{"credentials": creds("https://relay.example.com/v1", "")}, true)
		require.EqualValues(t, 1, stub.getCalls.Load())
	})

	t.Run("no credentials: no GetAccount, no probe", func(t *testing.T) {
		stub := &probeAdminStub{
			stubAdminService: newStubAdminService(),
			before:           probeTestAccount("https://relay.example.com/v1", "sk-old", known),
			after:            probeTestAccount("https://relay.example.com/v1", "sk-old", known),
		}
		runUpdateProbeCase(t, stub, map[string]any{"name": "renamed"}, false)
		require.EqualValues(t, 0, stub.getCalls.Load())
	})
}
