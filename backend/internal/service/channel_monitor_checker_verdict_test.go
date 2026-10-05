//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 渠道状态页的判定口径：
//   - 硬失败（error / failed）只留给连接失败、HTTP 5xx、2xx 但文本为空；
//   - 请求已发出却等不到响应头 / 读不完响应体（客户端超时）、4xx（含 429）、「很慢」都只算 degraded。

// verdictClientTimeout 让超时类用例在几百毫秒内跑完；非超时用例用足够宽松的值。
const (
	verdictShortTimeout = 250 * time.Millisecond
	verdictLongTimeout  = 10 * time.Second
)

// swapMonitorClientTimeout 把 monitorHTTPClient 换成不带 SSRF 校验、超时可控的普通 client。
func swapMonitorClientTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	orig := monitorHTTPClient
	monitorHTTPClient = &http.Client{Timeout: timeout}
	t.Cleanup(func() { monitorHTTPClient = orig })
}

// newVerdictServer 起一个 httptest server。handler 拿到的 release 在测试结束时关闭，
// 用来让「故意卡住」的 handler 退出，避免 srv.Close 等不到它。
func newVerdictServer(t *testing.T, h func(w http.ResponseWriter, release <-chan struct{})) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		h(w, release)
	}))
	t.Cleanup(srv.Close) // 后注册先执行：先放行 handler，再关 server
	t.Cleanup(func() { close(release) })
	return srv.URL
}

const (
	verdictSSEText  = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n"
	verdictSSEEmpty = "data: {\"type\":\"response.completed\"}\n\n"
)

// verdictOpts 与线上监控一致：OpenAI Responses + replace + SSE。
func verdictOpts() *CheckOptions {
	return &CheckOptions{
		APIMode:          MonitorAPIModeResponses,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		ResponseFormat:   MonitorResponseFormatSSE,
		BodyOverride: map[string]any{
			"model":        "gpt-x",
			"instructions": "Reply with exactly: ok",
			"input":        "ok",
			"stream":       true,
		},
	}
}

// respond 立即回一个固定状态码和 body 的 handler。
func respond(code int, body string) func(http.ResponseWriter, <-chan struct{}) {
	return func(w http.ResponseWriter, _ <-chan struct{}) { writeStatus(w, code, body) }
}

func writeStatus(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(code)
	_, _ = fmt.Fprint(w, body)
}

// stallAfterHeaders 先把响应头发出去并 flush，然后卡住不再写 body。
func stallAfterHeaders(code int) func(http.ResponseWriter, <-chan struct{}) {
	return func(w http.ResponseWriter, release <-chan struct{}) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(code)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}
}

func TestRunCheckForModel_VerdictTable(t *testing.T) {
	cases := []struct {
		name        string
		timeout     time.Duration
		handler     func(w http.ResponseWriter, release <-chan struct{})
		wantStatus  string
		wantMessage []string // 每一项都必须出现在 message 里
	}{
		{
			name:       "2xx 且有文本 → operational",
			timeout:    verdictLongTimeout,
			handler:    func(w http.ResponseWriter, _ <-chan struct{}) { writeStatus(w, http.StatusOK, verdictSSEText) },
			wantStatus: MonitorStatusOperational,
		},
		{
			name:        "2xx 但文本为空 → failed",
			timeout:     verdictLongTimeout,
			handler:     func(w http.ResponseWriter, _ <-chan struct{}) { writeStatus(w, http.StatusOK, verdictSSEEmpty) },
			wantStatus:  MonitorStatusFailed,
			wantMessage: []string{"empty text"},
		},
		{
			name:        "HTTP 500 → error",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusInternalServerError, "boom"),
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"upstream HTTP 500", "boom"},
		},
		{
			name:        "HTTP 502 → error",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusBadGateway, "bad gateway"),
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"upstream HTTP 502"},
		},
		{
			name:        "HTTP 503 → error",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusServiceUnavailable, "down"),
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"upstream HTTP 503"},
		},
		{
			name:        "HTTP 504 → error",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusGatewayTimeout, "gw timeout"),
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"upstream HTTP 504"},
		},
		{
			name:        "HTTP 429 限流 → degraded，不是硬失败",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusTooManyRequests, "slow down"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 429", "slow down"},
		},
		{
			name:        "HTTP 401 凭据问题 → degraded",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusUnauthorized, "bad key"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 401"},
		},
		{
			name:        "HTTP 403 → degraded",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusForbidden, "forbidden"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 403"},
		},
		{
			name:        "HTTP 400 → degraded",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusBadRequest, "bad request"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 400"},
		},
		{
			name:        "HTTP 404 → degraded",
			timeout:     verdictLongTimeout,
			handler:     respond(http.StatusNotFound, "no model"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 404"},
		},
		{
			name:        "HTTP 499 → degraded",
			timeout:     verdictLongTimeout,
			handler:     respond(499, "client closed"),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"upstream HTTP 499"},
		},
		{
			name:        "等响应头超时 → degraded，message 注明超时",
			timeout:     verdictShortTimeout,
			handler:     func(_ http.ResponseWriter, release <-chan struct{}) { <-release },
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"超时", "响应头"},
		},
		{
			name:        "2xx 已发响应头、读 body 超时 → degraded，message 注明超时",
			timeout:     verdictShortTimeout,
			handler:     stallAfterHeaders(http.StatusOK),
			wantStatus:  MonitorStatusDegraded,
			wantMessage: []string{"超时", "响应内容"},
		},
		{
			name:        "5xx 响应头之后读 body 超时 → 仍按 5xx 记 error",
			timeout:     verdictShortTimeout,
			handler:     stallAfterHeaders(http.StatusBadGateway),
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"upstream HTTP 502"},
		},
		{
			name:    "响应读到一半连接被掐断 → error",
			timeout: verdictLongTimeout,
			handler: func(w http.ResponseWriter, _ <-chan struct{}) {
				w.Header().Set("Content-Length", "1000")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, "data: {")
			},
			wantStatus:  MonitorStatusError,
			wantMessage: []string{"read body"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapMonitorClientTimeout(t, tc.timeout)
			endpoint := newVerdictServer(t, tc.handler)

			res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-test", "gpt-x", verdictOpts())

			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (message=%q)", res.Status, tc.wantStatus, res.Message)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(res.Message, want) {
					t.Errorf("message %q should contain %q", res.Message, want)
				}
			}
			if res.LatencyMs == nil {
				t.Errorf("latency should always be recorded")
			}
		})
	}
}

func TestRunCheckForModel_ConnectionRefusedIsError(t *testing.T) {
	swapMonitorClientTimeout(t, verdictLongTimeout)
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close() // 端口已关闭：连接被拒

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-test", "gpt-x", verdictOpts())

	if res.Status != MonitorStatusError {
		t.Fatalf("connection refused should be error, got %q (message=%q)", res.Status, res.Message)
	}
}

// timeoutNetError 模拟 dial / TLS 阶段的超时错误（net.Error.Timeout() == true）。
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestRunCheckForModel_DialTimeoutIsConnectionFailureNotSlow(t *testing.T) {
	// 建连阶段超时，请求根本没发出去：这是连接失败，必须记 error，不能被当成「慢」。
	orig := monitorHTTPClient
	monitorHTTPClient = &http.Client{
		Timeout: verdictLongTimeout,
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutNetError{}}
			},
		},
	}
	t.Cleanup(func() { monitorHTTPClient = orig })

	res := runCheckForModel(context.Background(), MonitorProviderOpenAI, "http://example.invalid", "sk-test", "gpt-x", verdictOpts())

	if res.Status != MonitorStatusError {
		t.Fatalf("dial timeout should be error, got %q (message=%q)", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "超时：") {
		t.Errorf("dial timeout must not be reported as a slow-response timeout: %q", res.Message)
	}
}

func TestRunCheckForModel_ChallengeMismatchVerdict(t *testing.T) {
	// 非 replace 模式：有内容但答案不对 → degraded；没有内容 → failed。
	cases := []struct {
		name       string
		body       string
		wantStatus string
	}{
		{"答案不对但有内容", `{"choices":[{"message":{"content":"definitely not the number"}}]}`, MonitorStatusDegraded},
		{"没有内容", `{"choices":[{"message":{"content":""}}]}`, MonitorStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapMonitorClientTimeout(t, verdictLongTimeout)
			endpoint := newVerdictServer(t, func(w http.ResponseWriter, _ <-chan struct{}) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			})

			res := runCheckForModel(context.Background(), MonitorProviderOpenAI, endpoint, "sk-test", "gpt-x", nil)

			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (message=%q)", res.Status, tc.wantStatus, res.Message)
			}
			if !strings.Contains(res.Message, "challenge mismatch") {
				t.Errorf("message should mention challenge mismatch, got %q", res.Message)
			}
		})
	}
}

func TestFinalizeOperationalOrDegraded_ThresholdIs30Seconds(t *testing.T) {
	cases := []struct {
		name    string
		latency time.Duration
		want    string
	}{
		{"1 秒", time.Second, MonitorStatusOperational},
		{"15 秒（旧降级线）不再算慢", 15 * time.Second, MonitorStatusOperational},
		{"29.999 秒", 29*time.Second + 999*time.Millisecond, MonitorStatusOperational},
		{"正好 30 秒", 30 * time.Second, MonitorStatusDegraded},
		{"60 秒", time.Minute, MonitorStatusDegraded},
		{"89 秒", 89 * time.Second, MonitorStatusDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &CheckResult{}
			ms := int(tc.latency / time.Millisecond)
			got := finalizeOperationalOrDegraded(res, tc.latency, ms)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.want == MonitorStatusDegraded && !strings.Contains(got.Message, "slow response") {
				t.Errorf("degraded message should say slow response, got %q", got.Message)
			}
		})
	}
}

func TestMonitorTimeouts_AreNinetySeconds(t *testing.T) {
	// 生产用的共享 client 必须按 90 秒配置：总超时与等响应头超时一致。
	if monitorHTTPClient.Timeout != 90*time.Second {
		t.Errorf("request timeout = %v, want 90s", monitorHTTPClient.Timeout)
	}
	tr, ok := monitorHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", monitorHTTPClient.Transport)
	}
	if tr.ResponseHeaderTimeout != 90*time.Second {
		t.Errorf("response header timeout = %v, want 90s", tr.ResponseHeaderTimeout)
	}
	// HEAD ping 只给管理员看，不能被放宽到 90 秒。
	if monitorPingHTTPClient.Timeout != monitorPingTimeout {
		t.Errorf("ping timeout = %v, want %v", monitorPingHTTPClient.Timeout, monitorPingTimeout)
	}
}

func TestIsClientTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"普通错误", errors.New("connection reset by peer"), false},
		{"ctx 取消", context.Canceled, false},
		{"ctx 截止", context.DeadlineExceeded, true},
		{"包装过的 ctx 截止", fmt.Errorf("do request: %w", context.DeadlineExceeded), true},
		{"net.Error 超时", &net.OpError{Op: "read", Err: timeoutNetError{}}, true},
		{"url.Error 包装的超时", &url.Error{Op: "Post", URL: "https://x", Err: timeoutNetError{}}, true},
		{"EOF", io.ErrUnexpectedEOF, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClientTimeout(tc.err); got != tc.want {
				t.Fatalf("isClientTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWrapMonitorTimeout(t *testing.T) {
	timeout := fmt.Errorf("do request: %w", context.DeadlineExceeded)
	plain := errors.New("connection reset by peer")

	cases := []struct {
		name        string
		err         error
		requestSent bool
		wantWrapped bool
	}{
		{"请求已发出 + 超时 → 慢", timeout, true, true},
		{"请求没发完 + 超时（建连 / TLS）→ 连接失败", timeout, false, false},
		{"请求已发出 + 非超时错误 → 连接失败", plain, true, false},
		{"没错误", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapMonitorTimeout(tc.err, tc.requestSent, monitorTimeoutPhaseHeaders)
			var te *monitorTimeoutError
			if isWrapped := errors.As(got, &te); isWrapped != tc.wantWrapped {
				t.Fatalf("wrapped = %v, want %v (got %v)", isWrapped, tc.wantWrapped, got)
			}
			if tc.err == nil && got != nil {
				t.Fatalf("nil error must stay nil, got %v", got)
			}
			if tc.wantWrapped && !errors.Is(got, context.DeadlineExceeded) {
				t.Errorf("wrapped error must keep the original cause")
			}
		})
	}
}

func TestApplyTransportError(t *testing.T) {
	headersTimeout := &monitorTimeoutError{phase: monitorTimeoutPhaseHeaders, err: context.DeadlineExceeded}
	bodyTimeout := &monitorTimeoutError{phase: monitorTimeoutPhaseBody, err: context.DeadlineExceeded}

	cases := []struct {
		name        string
		err         error
		statusCode  int
		wantStatus  string
		wantMessage string
	}{
		{"等响应头超时", headersTimeout, 0, MonitorStatusDegraded, "超时"},
		{"读 body 超时（2xx）", bodyTimeout, http.StatusOK, MonitorStatusDegraded, "超时"},
		{"读 body 超时（429）", bodyTimeout, http.StatusTooManyRequests, MonitorStatusDegraded, "超时"},
		{"读 body 超时（503）按 5xx 记", bodyTimeout, http.StatusServiceUnavailable, MonitorStatusError, "upstream HTTP 503"},
		{"普通连接错误", errors.New("dial tcp: connection refused"), 0, MonitorStatusError, "connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := applyTransportError(&CheckResult{}, tc.err, tc.statusCode)
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", res.Status, tc.wantStatus)
			}
			if !strings.Contains(res.Message, tc.wantMessage) {
				t.Errorf("message %q should contain %q", res.Message, tc.wantMessage)
			}
		})
	}
}

func TestTimeoutMessage_NamesThePhaseAndTheLimit(t *testing.T) {
	headers := timeoutMessage(monitorTimeoutPhaseHeaders)
	body := timeoutMessage(monitorTimeoutPhaseBody)
	for _, msg := range []string{headers, body} {
		if !strings.Contains(msg, "超时") || !strings.Contains(msg, "90 秒") {
			t.Errorf("timeout message should say 超时 and 90 秒, got %q", msg)
		}
	}
	if headers == body {
		t.Errorf("header-phase and body-phase messages should differ")
	}
}
