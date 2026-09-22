package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var errOpenAIWSSessionPreempted = errors.New("openai ws session preempted by newer request")

const (
	openAIWSSessionPreemptOwnerTTL      = 2 * time.Hour
	openAIWSSessionPreemptWatchInterval = 2 * time.Second
	openAIWSSessionPreemptCachePrefix   = "wspreempt:"
	openAIWSSessionPreemptCloseGrace    = time.Second
	openAIWSSessionPreemptedCloseReason = "session preempted by a newer connection"
)

type openAIWSPreemptClientCloser interface {
	Close(code coderws.StatusCode, reason string) error
}

type openAIWSSessionPreemptState struct {
	preempted atomic.Bool
}

// OpenAIWSSessionPreemptionCache is implemented by the Redis GatewayCache.
// The conditional methods ensure a stale connection cannot delete a newer
// owner's cross-instance registration.
type OpenAIWSSessionPreemptionCache interface {
	ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error)
	CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error)
	CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error)
}

func NewOpenAIWSSessionPreemptedError() error { return errOpenAIWSSessionPreempted }

type openAIWSSessionPreemptKey struct {
	groupID, apiKeyID int64
	sessionHash       string
}

type openAIWSSessionPreemptContextKey struct{}

func (s *OpenAIGatewayService) BeginOpenAIWSIngressSessionPreemption(ctx context.Context, c *gin.Context, account *Account, firstClientMessage []byte) (context.Context, func(), bool) {
	return s.BeginOpenAIWSIngressSessionPreemptionWithClient(ctx, c, account, firstClientMessage, nil)
}

func (s *OpenAIGatewayService) BeginOpenAIWSIngressSessionPreemptionWithClient(ctx context.Context, c *gin.Context, account *Account, firstClientMessage []byte, clientConn openAIWSPreemptClientCloser) (context.Context, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if state, _ := ctx.Value(openAIWSSessionPreemptContextKey{}).(*openAIWSSessionPreemptState); state != nil {
		return ctx, func() {}, true
	}
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled && account != nil {
		switch account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault) {
		case OpenAIWSIngressModePassthrough:
			return ctx, func() {}, false
		}
	}
	var notifyPreempted func()
	if clientConn != nil {
		notifyPreempted = func() {
			_ = clientConn.Close(coderws.StatusTryAgainLater, openAIWSSessionPreemptedCloseReason)
		}
	}
	groupID, apiKeyID := getOpenAIGroupIDFromContext(c), getAPIKeyIDFromContext(c)
	scope, threadID := "", ""
	if account != nil && account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
		scope, threadID = resolveOpenAIWSExecutionScope(c, firstClientMessage, apiKeyID)
	}
	preemptCtx, cleanup, armed, preemptedPrevious := s.beginOpenAIWSSessionPreemptContext(ctx, account, groupID, apiKeyID, scope, notifyPreempted)
	if !armed {
		return ctx, func() {}, false
	}
	if preemptedPrevious {
		if store := s.getOpenAIWSStateStore(); store != nil {
			store.DeleteSessionTurnState(groupID, scope)
			store.DeleteSessionConn(groupID, scope)
		}
		lane := resolveOpenAIWSExecutionLane(c, firstClientMessage)
		if lane == "" {
			lane = "main"
		}
		logOpenAIWSModeInfo("ingress_ws_session_preempted account_id=%d group_id=%d api_key_id=%d scope=%s thread_id=%s lane=%s", account.ID, groupID, apiKeyID, truncateOpenAIWSLogValue(scope, 12), truncateOpenAIWSLogValue(threadID, openAIWSIDValueMaxLen), truncateOpenAIWSLogValue(lane, openAIWSIDValueMaxLen))
	}
	return preemptCtx, cleanup, true
}

func newOpenAIWSSessionPreemptKey(groupID, apiKeyID int64, sessionHash string) (openAIWSSessionPreemptKey, bool) {
	sessionHash = strings.TrimSpace(sessionHash)
	if groupID <= 0 || apiKeyID <= 0 || sessionHash == "" {
		return openAIWSSessionPreemptKey{}, false
	}
	return openAIWSSessionPreemptKey{groupID: groupID, apiKeyID: apiKeyID, sessionHash: sessionHash}, true
}

func openAIWSSessionPreemptCacheHash(apiKeyID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", openAIWSSessionPreemptCachePrefix, apiKeyID, strings.TrimSpace(sessionHash))
}

type openAIWSSessionPreemptEntry struct {
	generation uint64
	cancel     func()
}

type openAIWSSessionPreemptRegistry struct {
	mu     sync.Mutex
	next   uint64
	active map[openAIWSSessionPreemptKey]openAIWSSessionPreemptEntry
}

func (r *openAIWSSessionPreemptRegistry) Begin(key openAIWSSessionPreemptKey, cancel func()) (func(), bool) {
	if r == nil || key.sessionHash == "" {
		return func() {}, false
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[openAIWSSessionPreemptKey]openAIWSSessionPreemptEntry)
	}
	r.next++
	generation := r.next
	previous, hadPrevious := r.active[key]
	r.active[key] = openAIWSSessionPreemptEntry{generation: generation, cancel: cancel}
	r.mu.Unlock()
	if hadPrevious && previous.cancel != nil {
		previous.cancel()
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if current, ok := r.active[key]; ok && current.generation == generation {
			delete(r.active, key)
		}
	}, hadPrevious
}

func (s *OpenAIGatewayService) beginOpenAIWSSessionPreemptContext(ctx context.Context, account *Account, groupID, apiKeyID int64, sessionHash string, notifyPreempted func()) (context.Context, func(), bool, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return ctx, func() {}, false, false
	}
	key, ok := newOpenAIWSSessionPreemptKey(groupID, apiKeyID, sessionHash)
	if !ok {
		return ctx, func() {}, false, false
	}
	state := &openAIWSSessionPreemptState{}
	preemptCtx, cancel := context.WithCancelCause(context.WithValue(ctx, openAIWSSessionPreemptContextKey{}, state))
	ownerToken := uuid.NewString()
	var once sync.Once
	preempt := func() {
		once.Do(func() {
			state.preempted.Store(true)
			if store := s.getOpenAIWSStateStore(); store != nil {
				store.DeleteSessionTurnState(key.groupID, key.sessionHash)
				store.DeleteSessionConn(key.groupID, key.sessionHash)
			}
			if notifyPreempted == nil {
				cancel(errOpenAIWSSessionPreempted)
				return
			}
			notified := make(chan struct{})
			go func() {
				defer close(notified)
				notifyPreempted()
			}()
			go func() {
				select {
				case <-notified:
				case <-time.After(openAIWSSessionPreemptCloseGrace):
				}
				cancel(errOpenAIWSSessionPreempted)
			}()
		})
	}
	previousRemoteOwner, remoteClaimed := s.claimOpenAIWSSessionPreemptOwner(ctx, key, ownerToken)
	cleanupLocal, hadLocalPrevious := s.openaiWSSessionPreemptions.Begin(key, preempt)
	preemptedPrevious := hadLocalPrevious || (remoteClaimed && previousRemoteOwner != "" && previousRemoteOwner != ownerToken)
	stopWatch := func() {}
	if remoteClaimed {
		stopWatch = s.watchOpenAIWSSessionPreemptOwner(preemptCtx, key, ownerToken, preempt)
	}
	return preemptCtx, func() {
		stopWatch()
		cleanupLocal()
		if remoteClaimed {
			s.releaseOpenAIWSSessionPreemptOwner(context.Background(), key, ownerToken)
		}
		cancel(nil)
	}, true, preemptedPrevious
}

func (s *OpenAIGatewayService) openAIWSSessionPreemptionCache() OpenAIWSSessionPreemptionCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, _ := s.cache.(OpenAIWSSessionPreemptionCache)
	return cache
}

func (s *OpenAIGatewayService) claimOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) (string, bool) {
	cache := s.openAIWSSessionPreemptionCache()
	if cache == nil || owner == "" {
		return "", false
	}
	cacheCtx, cancel := context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
	defer cancel()
	previous, err := cache.ClaimOpenAIResponsesSessionWindow(cacheCtx, key.groupID, openAIWSSessionPreemptCacheHash(key.apiKeyID, key.sessionHash), []byte(owner), openAIWSSessionPreemptOwnerTTL)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(previous)), true
}

func (s *OpenAIGatewayService) releaseOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string) {
	cache := s.openAIWSSessionPreemptionCache()
	if cache == nil || owner == "" {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
	defer cancel()
	_, _ = cache.CompareAndDeleteOpenAIResponsesSessionWindow(cacheCtx, key.groupID, openAIWSSessionPreemptCacheHash(key.apiKeyID, key.sessionHash), []byte(owner))
}

func (s *OpenAIGatewayService) watchOpenAIWSSessionPreemptOwner(ctx context.Context, key openAIWSSessionPreemptKey, owner string, onLost func()) func() {
	cache := s.openAIWSSessionPreemptionCache()
	if cache == nil || owner == "" || onLost == nil {
		return func() {}
	}
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(openAIWSSessionPreemptWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				cacheCtx, cancel := context.WithTimeout(context.Background(), openAIWSStateStoreRedisTimeout)
				owned, err := cache.CompareAndRefreshOpenAIResponsesSessionWindow(cacheCtx, key.groupID, openAIWSSessionPreemptCacheHash(key.apiKeyID, key.sessionHash), []byte(owner), openAIWSSessionPreemptOwnerTTL)
				cancel()
				if err == nil && !owned {
					onLost()
					return
				}
			}
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
	}
}

func isOpenAIWSSessionPreempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if state, _ := ctx.Value(openAIWSSessionPreemptContextKey{}).(*openAIWSSessionPreemptState); state != nil && state.preempted.Load() {
		return true
	}
	return errors.Is(context.Cause(ctx), errOpenAIWSSessionPreempted)
}

func IsOpenAIWSSessionPreemptedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errOpenAIWSSessionPreempted) {
		return true
	}
	var fallbackErr *openAIWSFallbackError
	return errors.As(err, &fallbackErr) && fallbackErr != nil && strings.TrimPrefix(strings.TrimSpace(fallbackErr.Reason), "prewarm_") == "session_preempted"
}
