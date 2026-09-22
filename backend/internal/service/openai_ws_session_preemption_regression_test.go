package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSPreemptionCloseRecorder struct {
	mu       sync.Mutex
	watchCtx context.Context
	codes    []coderws.StatusCode
	reason   string
	closed   chan struct{}
}

func (r *openAIWSPreemptionCloseRecorder) Close(code coderws.StatusCode, reason string) error {
	r.mu.Lock()
	r.codes = append(r.codes, code)
	r.reason = reason
	r.mu.Unlock()
	close(r.closed)
	return nil
}

func newOpenAIWSPreemptionContext(apiKeyID int64, threadID, requestKind string) *gin.Context {
	groupID := int64(7)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "shared-session")
	if threadID != "" {
		metadata := `{"session_id":"shared-session","thread_id":"` + threadID + `"`
		if requestKind != "" {
			metadata += `,"request_kind":"` + requestKind + `"`
		}
		c.Request.Header.Set(openAIWSTurnMetadataHeader, metadata+`}`)
	}
	c.Set("api_key", &APIKey{ID: apiKeyID, GroupID: &groupID})
	return c
}

func TestOpenAIWSSessionPreemptionClosesThenCancelsSameScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	closer := &openAIWSPreemptionCloseRecorder{closed: make(chan struct{})}
	first, cleanupFirst, armed := svc.BeginOpenAIWSIngressSessionPreemptionWithClient(context.Background(), newOpenAIWSPreemptionContext(11, "thread-a", ""), account, []byte(`{"input":"hello"}`), closer)
	require.True(t, armed)
	defer cleanupFirst()
	closer.watchCtx = first
	_, cleanupSecond, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newOpenAIWSPreemptionContext(11, "thread-a", ""), account, []byte(`{"input":"again"}`))
	require.True(t, armed)
	defer cleanupSecond()
	require.True(t, isOpenAIWSSessionPreempted(first))
	select {
	case <-closer.closed:
	case <-time.After(time.Second):
		t.Fatal("preempted websocket did not receive a close frame")
	}
	closer.mu.Lock()
	require.Equal(t, []coderws.StatusCode{coderws.StatusTryAgainLater}, closer.codes)
	require.Equal(t, openAIWSSessionPreemptedCloseReason, closer.reason)
	closer.mu.Unlock()
	select {
	case <-first.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("preempted websocket was not cancelled")
	}
	require.ErrorIs(t, context.Cause(first), errOpenAIWSSessionPreempted)
}

func TestOpenAIWSSessionPreemptionRegistryAndKeys(t *testing.T) {
	var registry openAIWSSessionPreemptRegistry
	key := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 11, sessionHash: "scope"}
	other := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 12, sessionHash: "scope"}
	first, cancelFirst := context.WithCancel(context.Background())
	cleanupFirst, replaced := registry.Begin(key, cancelFirst)
	require.False(t, replaced)
	second, cancelSecond := context.WithCancel(context.Background())
	cleanupSecond, replaced := registry.Begin(other, cancelSecond)
	require.False(t, replaced)
	third, cancelThird := context.WithCancel(context.Background())
	cleanupThird, replaced := registry.Begin(key, cancelThird)
	require.True(t, replaced)
	require.ErrorIs(t, first.Err(), context.Canceled)
	require.NoError(t, second.Err())
	require.NoError(t, third.Err())
	cleanupFirst()
	require.NoError(t, third.Err())
	cleanupSecond()
	cleanupThird()

	_, ok := newOpenAIWSSessionPreemptKey(0, 11, "scope")
	require.False(t, ok)
	_, ok = newOpenAIWSSessionPreemptKey(7, 0, "scope")
	require.False(t, ok)
	_, ok = newOpenAIWSSessionPreemptKey(7, 11, " ")
	require.False(t, ok)
	trimmed, ok := newOpenAIWSSessionPreemptKey(7, 11, " scope ")
	require.True(t, ok)
	require.Equal(t, "scope", trimmed.sessionHash)
	require.Equal(t, "wspreempt:11:scope", openAIWSSessionPreemptCacheHash(11, " scope "))
}

type openAIWSPreemptionCacheStub struct {
	GatewayCache
	mu     sync.Mutex
	owners map[string][]byte
}

func (c *openAIWSPreemptionCacheStub) key(groupID int64, hash string) string {
	return fmt.Sprintf("%d:%s", groupID, hash)
}

func (c *openAIWSPreemptionCacheStub) ClaimOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, owner []byte, _ time.Duration) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owners == nil {
		c.owners = make(map[string][]byte)
	}
	key := c.key(groupID, hash)
	previous := append([]byte(nil), c.owners[key]...)
	c.owners[key] = append([]byte(nil), owner...)
	return previous, nil
}

func (c *openAIWSPreemptionCacheStub) CompareAndRefreshOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, expected []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.owners[c.key(groupID, hash)]) == string(expected), nil
}

func (c *openAIWSPreemptionCacheStub) CompareAndDeleteOpenAIResponsesSessionWindow(_ context.Context, groupID int64, hash string, expected []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := c.key(groupID, hash)
	if string(c.owners[key]) != string(expected) {
		return false, nil
	}
	delete(c.owners, key)
	return true, nil
}

func TestOpenAIWSSessionPreemptionCacheAndEligibility(t *testing.T) {
	cache := &openAIWSPreemptionCacheStub{}
	svc := &OpenAIGatewayService{cache: cache, openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	key := openAIWSSessionPreemptKey{groupID: 7, apiKeyID: 11, sessionHash: "scope"}
	previous, claimed := svc.claimOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-a")
	require.True(t, claimed)
	require.Empty(t, previous)
	previous, claimed = svc.claimOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-b")
	require.True(t, claimed)
	require.Equal(t, "owner-a", previous)
	svc.releaseOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-a")
	cache.mu.Lock()
	require.Equal(t, "owner-b", string(cache.owners[cache.key(7, openAIWSSessionPreemptCacheHash(11, "scope"))]))
	cache.mu.Unlock()
	svc.releaseOpenAIWSSessionPreemptOwner(context.Background(), key, "owner-b")

	oauth := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, cleanup, armed, _ := svc.beginOpenAIWSSessionPreemptContext(context.Background(), apiKey, 7, 11, "scope", nil)
	cleanup()
	require.False(t, armed)
	first, cleanupFirst, armed, replaced := svc.beginOpenAIWSSessionPreemptContext(context.Background(), oauth, 7, 11, "scope", nil)
	require.True(t, armed)
	require.False(t, replaced)
	svc.getOpenAIWSStateStore().BindSessionTurnState(7, "scope", "state", time.Hour)
	svc.getOpenAIWSStateStore().BindSessionConn(7, "scope", "conn", time.Hour)
	_, cleanupSecond, armed, replaced := svc.beginOpenAIWSSessionPreemptContext(context.Background(), oauth, 7, 11, "scope", nil)
	require.True(t, armed)
	require.True(t, replaced)
	require.True(t, isOpenAIWSSessionPreempted(first))
	_, foundState := svc.getOpenAIWSStateStore().GetSessionTurnState(7, "scope")
	_, foundConn := svc.getOpenAIWSStateStore().GetSessionConn(7, "scope")
	require.False(t, foundState)
	require.False(t, foundConn)
	cleanupFirst()
	cleanupSecond()
}

func TestOpenAIWSIngressPreemptionModeAndScopeIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	passthroughCfg := &config.Config{}
	passthroughCfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	passthroughCfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
	svc := &OpenAIGatewayService{cfg: passthroughCfg}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 11, GroupID: &groupID})
	ctx, cleanup, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), c, account, []byte(`{"prompt_cache_key":"session"}`))
	defer cleanup()
	require.False(t, armed)
	require.NoError(t, ctx.Err())

	svc = &OpenAIGatewayService{}
	root, cleanupRoot, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newOpenAIWSPreemptionContext(11, "root", "turn"), account, []byte(`{"input":"one"}`))
	require.True(t, armed)
	defer cleanupRoot()
	child, cleanupChild, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newOpenAIWSPreemptionContext(11, "child", "turn"), account, []byte(`{"input":"one"}`))
	require.True(t, armed)
	defer cleanupChild()
	memory, cleanupMemory, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newOpenAIWSPreemptionContext(11, "root", "memory"), account, []byte(`{"input":"one"}`))
	require.True(t, armed)
	defer cleanupMemory()
	require.NoError(t, root.Err())
	require.NoError(t, child.Err())
	require.NoError(t, memory.Err())
	_, cleanupReplacement, armed := svc.BeginOpenAIWSIngressSessionPreemption(context.Background(), newOpenAIWSPreemptionContext(11, "root", "prewarm"), account, []byte(`{"input":"two"}`))
	require.True(t, armed)
	defer cleanupReplacement()
	require.True(t, IsOpenAIWSSessionPreemptedError(context.Cause(root)))
	require.NoError(t, child.Err())
	require.NoError(t, memory.Err())
	require.True(t, IsOpenAIWSSessionPreemptedError(NewOpenAIWSSessionPreemptedError()))
	require.False(t, IsOpenAIWSSessionPreemptedError(context.Canceled))
}
