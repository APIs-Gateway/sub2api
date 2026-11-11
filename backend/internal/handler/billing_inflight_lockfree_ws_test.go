//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// WebSocket 多轮：握手时只做一次 CheckBillingEligibility，第 2 轮起唯一的余额 / 订阅限额准入是
// reserveWSTurn -> ReserveBillingInflight。特权用户跳过预占后，这一半准入由只读准入顶上：
// 第 1 轮放行；第 2 轮前把余额改成 <=0，第 2 轮必须被拒、连接被关闭，且客户端看到的拒绝与无特权用户逐字相同。

const lockFreeWSUserID = int64(9811)

// lockFreeWSRepo 是 WS 测试用的计费 repo：余额可变；只读准入与预占用同一个余额判定。
type lockFreeWSRepo struct {
	*lockFreeHandlerBillingRepo
	bmu     sync.Mutex
	balance float64
	admits  int
}

func (r *lockFreeWSRepo) setBalance(v float64) {
	r.bmu.Lock()
	r.balance = v
	r.bmu.Unlock()
}

func (r *lockFreeWSRepo) funded() bool {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	return r.balance > 0
}

func (r *lockFreeWSRepo) admitCount() int {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	return r.admits
}

func (r *lockFreeWSRepo) AdmitBillingLockFree(context.Context, int64) (bool, error) {
	r.bmu.Lock()
	r.admits++
	r.bmu.Unlock()
	return r.funded(), nil
}

func (r *lockFreeWSRepo) ReserveBillingInflight(ctx context.Context, userID int64, owner string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	_, _ = r.lockFreeHandlerBillingRepo.ReserveBillingInflight(ctx, userID, owner, amount, exclusive, ttl)
	return r.funded(), nil
}

type lockFreeWSResult struct {
	turn1Err, turn2Err error
}

type lockFreeWSObserved struct {
	payload  []byte
	closeErr coderws.CloseError
	result   lockFreeWSResult
	repo     *lockFreeWSRepo
}

// runLockFreeWSTwoTurns 跑一条两轮的 WS 连接：第 1 轮余额 100，第 2 轮前改成 balanceBeforeTurn2。
// privileged 决定请求所属用户是否在特权名单里；返回客户端看到的第 2 轮拒绝（若有）和 repo 的计数。
func runLockFreeWSTwoTurns(t *testing.T, privileged bool, balanceBeforeTurn2 float64) lockFreeWSObserved {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := lockFreeConfig()
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	repo := &lockFreeWSRepo{lockFreeHandlerBillingRepo: newLockFreeHandlerBillingRepo(), balance: 100}
	account := service.Account{
		ID: 9451, Name: "openai-ws-lockfree", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-lockfree", "base_url": "https://api.openai.example"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
		},
	}
	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	billingCache := service.NewBillingCacheService(nil, &lockFreeUserRepo{balance: 100}, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	gateway := service.NewOpenAIGatewayService(
		accountRepo, nil, repo, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	if privileged {
		gateway.SetBillingLockFreeUsers(newLockFreeHandlerResolver(t, lockFreeWSUserID))
	}

	turnErrs := make(chan lockFreeWSResult, 1)
	proxy := func(_ context.Context, _ *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, firstMessage []byte, hooks *service.OpenAIWSIngressHooks) error {
		var res lockFreeWSResult
		res.turn1Err = hooks.BeforeUpstreamTurn(1, firstMessage, "gpt-5.4")
		repo.setBalance(balanceBeforeTurn2) // 第 1 轮跑完后，余额在两轮之间被扣到这个值
		res.turn2Err = hooks.BeforeUpstreamTurn(2, firstMessage, "gpt-5.4")
		turnErrs <- res
		if res.turn2Err != nil {
			return res.turn2Err
		}
		return nil
	}
	reports := make(chan bool, 4)
	h := &OpenAIGatewayHandler{
		gatewayService:                gateway,
		billingCacheService:           billingCache,
		apiKeyService:                 &service.APIKeyService{},
		concurrencyHelper:             NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                           cfg,
		responsesWebSocketProxy:       proxy,
		onOpenAIAccountScheduleResult: func(_ int64, success bool) { reports <- success },
	}

	group := chainRespGroup(1, 1, 0)
	groupID := group.ID
	apiKey := &service.APIKey{
		ID: lockFreeWSUserID, UserID: lockFreeWSUserID, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: lockFreeWSUserID, Status: service.StatusActive, Balance: 100},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: lockFreeWSUserID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()

	client := dialAndSendFirstResponseCreate(t, server.URL)
	defer func() { _ = client.CloseNow() }()

	obs := lockFreeWSObserved{repo: repo}
	select {
	case obs.result = <-turnErrs:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish both turns")
	}
	if obs.result.turn2Err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		typ, payload, err := client.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, coderws.MessageText, typ)
		obs.payload = payload
		_, _, err = client.Read(ctx)
		require.ErrorAs(t, err, &obs.closeErr)
	}
	return obs
}

// 第 1 轮放行；第 2 轮前余额 <=0，特权用户第 2 轮被拒并关闭连接，没有 lease、没有预占调用。
func TestLockFreeWS_PrivilegedUserRejectedOnLaterTurnWhenBalanceExhausted(t *testing.T) {
	for _, balance := range []float64{0, -5, -0.00000001} {
		obs := runLockFreeWSTwoTurns(t, true, balance)
		require.NoError(t, obs.result.turn1Err, "第 1 轮：有余额，放行")
		require.Error(t, obs.result.turn2Err, "第 2 轮：余额 %v 必须被拒", balance)
		require.Equal(t, 2, obs.repo.admitCount(), "每一轮各做一次只读准入")
		require.Equal(t, lockFreeCounts{}, obs.repo.counts(), "特权用户：预占 repo 零调用（没有 lease、没有锁）")

		require.Equal(t, "error", gjson.GetBytes(obs.payload, "type").String(), string(obs.payload))
		require.Equal(t, int64(http.StatusForbidden), gjson.GetBytes(obs.payload, "status").Int(), string(obs.payload))
		require.Contains(t, gjson.GetBytes(obs.payload, "error.message").String(), "nsufficient")
		require.Equal(t, coderws.StatusPolicyViolation, obs.closeErr.Code, "连接被关闭")
		require.Equal(t, "billing check failed", obs.closeErr.Reason)
	}
}

// 对照：无特权用户同样场景走预占，客户端看到的拒绝与特权用户逐字相同。
func TestLockFreeWS_RejectionMatchesUnprivilegedReservationRejection(t *testing.T) {
	priv := runLockFreeWSTwoTurns(t, true, -5)
	unpriv := runLockFreeWSTwoTurns(t, false, -5)

	require.NoError(t, unpriv.result.turn1Err)
	require.Error(t, unpriv.result.turn2Err)
	require.Equal(t, 2, unpriv.repo.counts().reserves, "对照组：每一轮各预占一次")
	require.Zero(t, unpriv.repo.admitCount())

	require.Equal(t, string(unpriv.payload), string(priv.payload), "拒绝消息逐字相同")
	require.Equal(t, unpriv.closeErr.Code, priv.closeErr.Code)
	require.Equal(t, unpriv.closeErr.Reason, priv.closeErr.Reason)
}

// 余额始终为正：特权用户两轮都放行，且一次预占也没有。
func TestLockFreeWS_PrivilegedUserWithBalanceStaysAdmittedWithoutReservation(t *testing.T) {
	obs := runLockFreeWSTwoTurns(t, true, 50)
	require.NoError(t, obs.result.turn1Err)
	require.NoError(t, obs.result.turn2Err)
	require.Equal(t, 2, obs.repo.admitCount())
	require.Equal(t, lockFreeCounts{}, obs.repo.counts())
}
