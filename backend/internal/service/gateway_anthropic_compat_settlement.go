package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Match the native stream idle timeout while collecting usage after a client
// leaves. Cancellation must start draining even if the next upstream read blocks
// before any downstream write can detect the disconnect.
type anthropicCompatDrain struct {
	body             io.Closer
	idle             time.Duration
	mu               sync.Mutex
	timer            *time.Timer
	closed           bool
	stopCancellation func() bool
}

// Disabling the normal connected-stream idle timeout must not make detached
// usage collection keep a concurrency slot forever after the client leaves.
const anthropicCompatDefaultDrainTimeout = 30 * time.Second

func newAnthropicCompatDrain(cfg *config.Config, body io.Closer, c *gin.Context) *anthropicCompatDrain {
	d := &anthropicCompatDrain{body: body, idle: anthropicCompatDefaultDrainTimeout}
	if cfg != nil && cfg.Gateway.StreamDataIntervalTimeout > 0 {
		d.idle = time.Duration(cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	if c != nil && c.Request != nil {
		d.stopCancellation = context.AfterFunc(c.Request.Context(), d.start)
	}
	return d
}

func (d *anthropicCompatDrain) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.idle <= 0 || d.timer != nil {
		return
	}
	d.timer = time.AfterFunc(d.idle, func() { _ = d.body.Close() })
}

func (d *anthropicCompatDrain) touch() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed && d.timer != nil {
		d.timer.Reset(d.idle)
	}
}

func (d *anthropicCompatDrain) stop() {
	if d.stopCancellation != nil {
		d.stopCancellation()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

func anthropicCompatClientGone(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

// Failover must return no billable result. Once usage was metered it is settled
// by the handler instead of replaying, even when no response bytes were sent.
func anthropicCompatIncompleteStream(c *gin.Context, result *ForwardResult, readErr error) (*ForwardResult, error) {
	observed := result.Usage.hasObservedTokens()
	if !c.Writer.Written() {
		c.Writer.Header().Del("Content-Type")
	}
	if readErr != nil && !errors.Is(readErr, bufio.ErrTooLong) && !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, context.DeadlineExceeded) && !observed && !c.Writer.Written() && !result.ClientDisconnect && !anthropicCompatClientGone(c) {
		var streamError *sseStreamErrorEventError
		if errors.As(readErr, &streamError) {
			return nil, streamError
		}
		body, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "upstream_disconnected", "message": "upstream stream disconnected: " + sanitizeStreamError(readErr)},
		})
		// Headers are still pending; an exhausted failover must be able to
		// answer endpoint JSON instead of inheriting the upstream SSE type.
		c.Writer.Header().Del("Content-Type")
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, RetryableOnSameAccount: true}
	}
	err := errors.New("stream usage incomplete: missing terminal event")
	if readErr != nil {
		err = fmt.Errorf("stream usage incomplete: %w", readErr)
	}
	if !observed {
		return nil, err
	}
	return result, err
}

func anthropicCompatBufferedIncomplete(c *gin.Context, writeError func(*gin.Context, int, string, string), result *ForwardResult, readErr error) (*ForwardResult, error) {
	result, err := anthropicCompatIncompleteStream(c, result, readErr)
	var failoverErr *UpstreamFailoverError
	var streamErr *sseStreamErrorEventError
	providerWillFailover := result == nil && !c.Writer.Written() && !anthropicCompatClientGone(c) && errors.As(err, &streamErr)
	if !errors.As(err, &failoverErr) && !providerWillFailover {
		writeError(c, http.StatusBadGateway, "server_error", "Upstream stream ended before the response completed")
	}
	return result, err
}

// Provider SSE errors keep native Anthropic semantic status and cooldown
// policy. Only an unmetered failure before output can enter account failover.
func (s *GatewayService) anthropicCompatProviderError(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, model string, result *ForwardResult, err error) (*ForwardResult, error) {
	var streamErr *sseStreamErrorEventError
	if !errors.As(err, &streamErr) {
		return result, err
	}
	body := []byte(streamErr.RawData)
	eligible := result == nil && !c.Writer.Written() && !anthropicCompatClientGone(c)
	status := http.StatusForbidden
	if eligible && gjson.GetBytes(body, "error.type").String() == "overloaded_error" {
		status = 529
		if s.rateLimitService != nil {
			s.handleFailoverSideEffects(ctx, &http.Response{StatusCode: status, Header: resp.Header.Clone(), Body: io.NopCloser(bytes.NewReader(body))}, account, model)
		}
	}
	detail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		limit := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if limit <= 0 {
			limit = 2048
		}
		detail = truncateString(streamErr.RawData, limit)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: status, UpstreamRequestID: upstreamRequestIDFromHeader(resp.Header),
		Kind: "stream_error", Message: sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body))), Detail: detail,
	})
	if eligible {
		return nil, &UpstreamFailoverError{StatusCode: status, ResponseBody: body, BillingNoCharge: true}
	}
	return result, err
}
