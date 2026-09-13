package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (s *OpenAIGatewayService) forwardGrokResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel string,
	reqStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	if account.Type != AccountTypeOAuth {
		return nil, fmt.Errorf("grok account type %s is not supported by subscription forwarding", account.Type)
	}

	upstreamModel := account.GetMappedModel(originalModel)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = "grok-4.3"
	}
	if isGrokImageGenerationModel(upstreamModel) {
		return nil, fmt.Errorf("model %s is an image model and is not available on the Responses endpoint; use /v1/images/generations instead", upstreamModel)
	}
	patchedBody, err := patchGrokResponsesBody(body, upstreamModel)
	if err != nil {
		return nil, err
	}
	// OpenAI /responses/compact is not a native xAI endpoint. Convert it into a
	// normal Grok Responses turn that asks for a structured summary, then map the
	// reply back to an OpenAI compaction item on the way out.
	if isOpenAIResponsesCompactPath(c) {
		patchedBody, err = buildGrokCompactRequestBody(patchedBody)
		if err != nil {
			return nil, err
		}
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	upstreamReq, err := buildGrokResponsesRequest(upstreamCtx, c, account, patchedBody, token)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	upstreamStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
		upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
		}
		kind := "http_error"
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			kind = "failover"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               kind,
			Message:            upstreamMsg,
		})
		s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		// Grok content-policy 403s are request-scoped (bad prompt/media), not an
		// account problem: handleGrokAccountUpstreamError already skipped the
		// cooldown above. Falling through to the generic handleErrorResponse
		// would still call handleOpenAIAccountUpstreamError -> handleAuthError
		// -> SetError, permanently marking a perfectly healthy OAuth account as
		// errored and then failing the request over to the next pool account
		// (which would hit the same rejection). Answer the caller directly and
		// leave the account/pool untouched.
		if isGrokContentPolicyRejection(resp.StatusCode, respBody) {
			return nil, s.writeGrokContentPolicyRejection(c, resp.StatusCode, upstreamMsg)
		}
		return s.handleErrorResponse(ctx, resp, c, account, patchedBody, upstreamModel)
	}
	defer func() { _ = resp.Body.Close() }()

	s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))

	upstreamRequestID := firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	var usage *OpenAIUsage
	var firstTokenMs *int
	responseID := ""
	if reqStream {
		maxLineSize := defaultMaxLineSize
		if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = s.cfg.Gateway.MaxLineSize
		}
		resp.Body = newGrokResponsesBillingPingFilterBody(resp.Body, account, maxLineSize)
		// 上游模型不一致拦截：把流交给通用 Responses 处理器之前，只预读 preamble
		// （response.created / in_progress）拿 response.model 比对；此时客户端零输出，
		// 命中直接 failover；预读的字节原样回放给后续处理器。
		if ferr := s.checkGrokStreamUpstreamModelMismatch(c, account, resp, upstreamRequestID, upstreamModel, originalModel); ferr != nil {
			return nil, ferr
		}
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, upstreamModel)
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
		responseID = strings.TrimSpace(streamResult.responseID)
	} else {
		// 上游模型不一致拦截：整包读完先比对，再交给通用非流式处理器（其内部会再读一次 body）。
		body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
		if err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if got := grokUpstreamResponseModelFromBody(resp.Header, body); got != "" {
			usageValue, _ := extractOpenAIUsageFromJSONBytes(body)
			if ferr := s.checkUpstreamModelMismatch(c, account, upstreamRequestID,
				sentModelForCheck(upstreamModel, originalModel), got, false, true, usageValue); ferr != nil {
				return nil, ferr
			}
		}
		nonStreamResult, err := s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, upstreamModel)
		if err != nil {
			return nil, err
		}
		usage = nonStreamResult.usage
		responseID = strings.TrimSpace(nonStreamResult.responseID)
	}

	if usage == nil {
		usage = &OpenAIUsage{}
	}
	reasoningEffort := extractOpenAIReasoningEffortFromBody(patchedBody, originalModel)
	return &OpenAIForwardResult{
		RequestID:       firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
		ResponseID:      responseID,
		Usage:           *usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		Stream:          reqStream,
		OpenAIWSMode:    false,
		ResponseHeaders: resp.Header.Clone(),
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
	}, nil
}

// grokUpstreamModelPeekMaxBytes 限制流式预读的字节数：preamble 事件很小，超出即放弃预读，
// 交给通用处理器（其自身也有同样的比对钩子）。
const grokUpstreamModelPeekMaxBytes = 64 * 1024

// grokUpstreamResponseModelFromBody 从非流式整包里取 B：JSON 整包取 response.model / 顶层 model；
// 上游即便 stream=false 也可能回 SSE（handleSSEToJSON 场景），此时取首个带 model 的 data 事件。
func grokUpstreamResponseModelFromBody(header http.Header, body []byte) string {
	if isEventStreamResponse(header) || bodyHasSSEFraming(body) {
		model, _ := peekResponsesSSEPreambleModel(bufio.NewReader(bytes.NewReader(body)), len(body)+1)
		return model
	}
	return extractUpstreamResponseModel(body)
}

// peekResponsesSSEPreambleModel 逐行预读 Responses SSE，直到首个带 model 的 data 事件、
// 首个非 preamble 事件、[DONE]、超出 maxBytes 或 EOF 为止。返回找到的 model（可能为空）
// 与已消费的原始字节，调用方负责回放。
func peekResponsesSSEPreambleModel(r *bufio.Reader, maxBytes int) (string, []byte) {
	consumed := make([]byte, 0, 1024)
	for {
		line, err := r.ReadBytes('\n')
		consumed = append(consumed, line...)
		if payload, ok := extractOpenAISSEDataLine(strings.TrimRight(string(line), "\r\n")); ok {
			payload = strings.TrimSpace(payload)
			if payload == "[DONE]" {
				return "", consumed
			}
			if payload != "" {
				if got := extractUpstreamResponseModel([]byte(payload)); got != "" {
					return got, consumed
				}
				if !openAIStreamEventIsPreamble(gjson.Get(payload, "type").String()) {
					return "", consumed
				}
			}
		}
		if err != nil || len(consumed) >= maxBytes {
			return "", consumed
		}
	}
}

type grokPeekedBody struct {
	io.Reader
	closer io.Closer
}

func (b *grokPeekedBody) Close() error { return b.closer.Close() }

// checkGrokStreamUpstreamModelMismatch 预读流式 preamble 做上游模型比对；无论结果如何，
// 预读的字节都通过 resp.Body 回放，后续处理器看到的字节流与未预读时完全一致。
func (s *OpenAIGatewayService) checkGrokStreamUpstreamModelMismatch(
	c *gin.Context, account *Account, resp *http.Response,
	upstreamRequestID, upstreamModel, originalModel string,
) *UpstreamFailoverError {
	original := resp.Body
	reader := bufio.NewReader(original)
	got, consumed := peekResponsesSSEPreambleModel(reader, grokUpstreamModelPeekMaxBytes)
	resp.Body = &grokPeekedBody{Reader: io.MultiReader(bytes.NewReader(consumed), reader), closer: original}
	if got == "" {
		return nil
	}
	return s.checkUpstreamModelMismatch(c, account, upstreamRequestID,
		sentModelForCheck(upstreamModel, originalModel), got, true, true, OpenAIUsage{})
}

func patchGrokResponsesBody(body []byte, upstreamModel string) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid json request body")
	}
	out, err := sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return nil, err
	}
	out, err = convertOpenAICompactInputsForGrok(out)
	if err != nil {
		return nil, err
	}
	out, err = sanitizeGrokReasoningNullContent(out)
	if err != nil {
		return nil, err
	}
	for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier"} {
		if gjson.GetBytes(out, unsupportedField).Exists() {
			out, err = sjson.DeleteBytes(out, unsupportedField)
			if err != nil {
				return nil, err
			}
		}
	}
	out, err = stripRedundantGrokViewImageTool(out)
	if err != nil {
		return nil, err
	}
	if !gjson.GetBytes(out, "tools").Exists() && gjson.GetBytes(out, "tool_choice").Exists() {
		out, err = sjson.DeleteBytes(out, "tool_choice")
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// An inline input_image is already visible to Grok. Keeping Codex's local
// view_image tool in the same turn can make Grok announce a tool call without
// actually calling it, so remove only that redundant automatic choice.
func stripRedundantGrokViewImageTool(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	items := input.Array()
	if len(items) == 0 {
		return body, nil
	}
	current := items[len(items)-1]
	if strings.TrimSpace(current.Get("role").String()) != "user" ||
		!openAIJSONValueMayContainImageInput(current) {
		return body, nil
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.IsObject() && strings.TrimSpace(toolChoice.Get("type").String()) == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if choiceName == "view_image" {
			return body, nil
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	filtered := make([]json.RawMessage, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "function" &&
			strings.TrimSpace(tool.Get("name").String()) == "view_image" {
			changed = true
			continue
		}
		filtered = append(filtered, json.RawMessage(tool.Raw))
	}
	if !changed {
		return body, nil
	}
	if len(filtered) == 0 && strings.TrimSpace(toolChoice.String()) == "required" {
		return body, nil
	}

	if len(filtered) == 0 {
		out, err := sjson.DeleteBytes(body, "tools")
		if err != nil {
			return nil, err
		}
		return sjson.DeleteBytes(out, "parallel_tool_calls")
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}

// sanitizeGrokReasoningNullContent 删除 reasoning 项中的 "content": null。
// xAI 的 untagged enum 反序列化器拒收该字段，返回 422。
func sanitizeGrokReasoningNullContent(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		contentResult := item.Get("content")
		if contentResult.Exists() && contentResult.Type == gjson.Null {
			var err error
			body, err = sjson.DeleteBytes(body, fmt.Sprintf("input.%d.content", i))
			if err != nil {
				return nil, err
			}
		}
	}
	return body, nil
}

// isGrokImageGenerationModel identifies Grok image models that must go
// through /v1/images/generations instead of the Responses endpoint.
func isGrokImageGenerationModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "grok-imagine" ||
		model == "grok-imagine-edit" ||
		strings.HasPrefix(model, "grok-imagine-image")
}

func buildGrokResponsesRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string) (*http.Request, error) {
	targetURL := xai.BuildResponsesURL(account.GetGrokBaseURL())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileGrok))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "sub2api-grok/1.0")
	if c != nil {
		if v := c.GetHeader("OpenAI-Beta"); strings.TrimSpace(v) != "" {
			req.Header.Set("OpenAI-Beta", v)
		}
	}
	return req, nil
}

func (s *OpenAIGatewayService) updateGrokUsageSnapshot(ctx context.Context, accountID int64, snapshot *xai.QuotaSnapshot) {
	if s == nil || s.accountRepo == nil || accountID <= 0 || snapshot == nil {
		return
	}
	if s.codexSnapshotThrottle != nil && !s.codexSnapshotThrottle.Allow(accountID, time.Now()) {
		return
	}
	_ = s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		grokQuotaSnapshotExtraKey: snapshot,
	})
}

func (s *OpenAIGatewayService) handleGrokAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte) {
	if s == nil || account == nil {
		return
	}
	if isGrokContentPolicyRejection(statusCode, responseBody) {
		return
	}
	if statusCode == http.StatusForbidden && s.applyGrokForbiddenPolicy(ctx, account, responseBody) {
		return
	}
	if account.IsPoolMode() {
		slog.Info("grok_pool_mode_error_state_skipped", "account_id", account.ID, "status_code", statusCode)
		return
	}
	switch statusCode {
	case http.StatusUnauthorized:
		if s.grokTokenProvider == nil {
			s.tempUnscheduleGrok(ctx, account, 10*time.Minute, "grok oauth token unauthorized")
			break
		}
		if err := s.grokTokenProvider.RefreshAfterUnauthorized(ctx, account); err != nil {
			s.tempUnscheduleGrok(ctx, account, 10*time.Minute, "grok oauth token refresh failed")
			break
		}
		s.ClearAccountSchedulingBlock(account.ID)
		if s.accountRepo != nil {
			_ = s.accountRepo.ClearTempUnschedulable(ctx, account.ID)
		}
	case http.StatusPaymentRequired:
		s.tempUnscheduleGrok(ctx, account, 30*time.Minute, "grok payment required")
	case http.StatusForbidden:
		s.tempUnscheduleGrok(ctx, account, 30*time.Minute, "grok entitlement or subscription tier denied")
	case http.StatusTooManyRequests:
		cooldown := 2 * time.Minute
		if snapshot := xai.ParseQuotaHeaders(headers, statusCode); snapshot != nil && snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
			cooldown = time.Duration(*snapshot.RetryAfterSeconds) * time.Second
		}
		s.tempUnscheduleGrok(ctx, account, cooldown, "grok rate limited")
	default:
		if statusCode >= 500 {
			s.tempUnscheduleGrok(ctx, account, 2*time.Minute, "grok upstream temporary error")
		}
	}
	_ = responseBody
}

// writeGrokContentPolicyRejection answers a Grok content-policy 403 directly
// with a generic, safe rejection instead of routing through the shared OpenAI
// error handler. The upstream body/message is intentionally not echoed back:
// it must not leak account-pool or subscription-to-API implementation details.
func (s *OpenAIGatewayService) writeGrokContentPolicyRejection(c *gin.Context, statusCode int, upstreamMsg string) error {
	MarkResponseCommitted(c)
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    "invalid_request_error",
			"message": "Your request was rejected by the upstream content safety system. Please modify your input and try again.",
		},
	})
	if upstreamMsg == "" {
		return fmt.Errorf("grok content policy rejection: %d", statusCode)
	}
	return fmt.Errorf("grok content policy rejection: %d message=%s", statusCode, upstreamMsg)
}

func (s *OpenAIGatewayService) tempUnscheduleGrok(ctx context.Context, account *Account, cooldown time.Duration, reason string) {
	if s == nil || account == nil {
		return
	}
	until := time.Now().Add(cooldown)
	s.BlockAccountScheduling(account, until, reason)
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason)
	}
}
