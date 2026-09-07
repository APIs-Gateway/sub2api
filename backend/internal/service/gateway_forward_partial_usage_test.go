package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖 issue #764（对齐上游 b22f73e72523 / PR #5154）：流式转发中途出错
// （缺失 terminal 事件、读错误等）时，Forward/forwardAnthropicAPIKeyPassthroughWithInput
// 必须把已探测到的 usage 部分结果与错误一起返回，供 handler 层照常提交计费，而不是
// 随错误一起被整体丢弃。

func newAnthropicAPIKeyAccountForPartialUsageTest() *Account {
	return &Account{
		ID:          502,
		Name:        "anthropic-apikey-partial-usage",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-anthropic-key",
			"base_url": "https://api.anthropic.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newForwardPartialUsageServiceForTest(upstream *anthropicHTTPUpstreamRecorder) *GatewayService {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
}

func newPartialUsageTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

// --- partialStreamUsageResult 纯函数单测 ---

func TestPartialStreamUsageResult_NilStreamResult_ReturnsNil(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	got := partialStreamUsageResult(resp, nil, "model", "model", time.Now(), errors.New("boom"))
	require.Nil(t, got)
}

func TestPartialStreamUsageResult_NoObservedTokens_ReturnsNil(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	streamResult := &streamingResult{usage: &ClaudeUsage{}}
	got := partialStreamUsageResult(resp, streamResult, "model", "model", time.Now(), errors.New("stream usage incomplete: missing terminal event"))
	require.Nil(t, got, "无已观测 usage 时不应产生幽灵账单记录")
}

func TestPartialStreamUsageResult_FailoverError_ReturnsNil(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	streamResult := &streamingResult{usage: &ClaudeUsage{InputTokens: 10}}
	err := &UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	got := partialStreamUsageResult(resp, streamResult, "model", "model", time.Now(), err)
	require.Nil(t, got, "UpstreamFailoverError 必须保持 result=nil，防止 failover 重试成功后双重计费")
}

func TestPartialStreamUsageResult_FailoverError_WrappedError_ReturnsNil(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	streamResult := &streamingResult{usage: &ClaudeUsage{OutputTokens: 3}}
	wrapped := errors.Join(errors.New("context"), &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable})
	got := partialStreamUsageResult(resp, streamResult, "model", "model", time.Now(), wrapped)
	require.Nil(t, got, "包装过的 UpstreamFailoverError 也必须通过 errors.As 识别")
}

func TestPartialStreamUsageResult_ObservedTokens_ReturnsPartialResult(t *testing.T) {
	resp := &http.Response{Header: http.Header{"X-Request-Id": []string{"rid-1"}}}
	firstTokenMs := 42
	streamResult := &streamingResult{
		usage:            &ClaudeUsage{InputTokens: 9, CacheReadInputTokens: 2},
		firstTokenMs:     &firstTokenMs,
		clientDisconnect: true,
	}
	startTime := time.Now().Add(-time.Second)
	got := partialStreamUsageResult(resp, streamResult, "orig-model", "upstream-model", startTime, errors.New("stream read error: boom"))
	require.NotNil(t, got)
	require.Equal(t, "rid-1", got.RequestID)
	require.Equal(t, "orig-model", got.Model)
	require.Equal(t, "upstream-model", got.UpstreamModel)
	require.True(t, got.Stream)
	require.True(t, got.ClientDisconnect)
	require.NotNil(t, got.FirstTokenMs)
	require.Equal(t, 42, *got.FirstTokenMs)
	require.Equal(t, 9, got.Usage.InputTokens)
	require.Equal(t, 2, got.Usage.CacheReadInputTokens)
	require.True(t, got.Duration > 0)
}

// --- Forward() 端到端：主流式路径（非透传 APIKey 账号）---

func TestGatewayService_Forward_StreamMissingTerminalPreservesPartialUsage(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-5-sonnet-latest",
		Stream: true,
	}

	// newapi 类聚合上游的典型失败形态：message_start/message_delta 携带 usage，
	// 但流在 message_stop 前直接结束（缺失 terminal 事件）。
	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":5}}`,
		"",
		"",
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid-partial"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamSSE)),
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForPartialUsageTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result, "流中断但已探测到 usage 时必须返回部分结果用于计费")
	require.True(t, result.Stream)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.CacheReadInputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, "rid-partial", result.RequestID)
	require.NotNil(t, result.FirstTokenMs)
}

func TestGatewayService_Forward_StreamReadErrorAfterOutputPreservesPartialUsage(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-5-sonnet-latest",
		Stream: true,
	}

	// message_start 已写出（含 usage），随后上游连接异常中断。
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			payload: []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9,\"cache_creation_input_tokens\":4}}}\n\n"),
			err:     io.ErrUnexpectedEOF,
		},
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForPartialUsageTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream read error")
	require.NotNil(t, result, "已写出内容后的读错误必须保留部分 usage")
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
}

func TestGatewayService_Forward_StreamErrorWithoutUsageReturnsNilResult(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-5-sonnet-latest",
		Stream: true,
	}

	// 只有 ping、没有任何 usage 的流中断：不应产生零 usage 的幽灵账单记录。
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("event: ping\ndata: {\"type\": \"ping\"}\n\n")),
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForPartialUsageTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.Nil(t, result, "无已观测 usage 时不应返回部分结果")
}

func TestGatewayService_Forward_FailoverErrorKeepsNilResult(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-5-sonnet-latest",
		Stream: true,
	}

	// 未向客户端写出任何字节前的读错误会被包成 UpstreamFailoverError 走换号重试。
	// 该路径必须保持 result=nil：failover 成功后按成功请求计费，双重结果会造成双重记账。
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &streamReadCloser{
			err: errors.New("connection reset by peer"),
		},
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForPartialUsageTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Nil(t, result, "failover 错误必须保持 result=nil，防止重试成功后双重计费")
}

// --- forwardAnthropicAPIKeyPassthroughWithInput()（经 Forward() 分发）端到端 ---

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardStreamMissingTerminalPreservesPartialUsage(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-7-sonnet-20250219","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-7-sonnet-20250219",
		Stream: true,
	}

	upstreamSSE := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"cache_read_input_tokens":2}}}`,
		"",
		`data: {"type":"message_delta","usage":{"output_tokens":3}}`,
		"",
	}, "\n")
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid-pass-partial"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamSSE)),
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result, "透传流中断但已探测到 usage 时必须返回部分结果用于计费")
	require.True(t, result.Stream)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, "claude-3-7-sonnet-20250219", result.Model)
}

func TestGatewayService_AnthropicAPIKeyPassthrough_ForwardStreamErrorWithoutUsageReturnsNilResult(t *testing.T) {
	c, _ := newPartialUsageTestContext(t)

	body := []byte(`{"model":"claude-3-7-sonnet-20250219","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-3-7-sonnet-20250219",
		Stream: true,
	}

	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicAPIKeyAccountForTest()

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Nil(t, result, "透传路径无已观测 usage 时不应返回部分结果")
}
