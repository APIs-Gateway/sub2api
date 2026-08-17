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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardResponsesViaRawChatCompletions serves /v1/responses clients through an
// upstream that only supports /v1/chat/completions.
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "Failed to parse request body",
			},
		})
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "model is required",
			},
		})
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	serviceTier := extractOpenAIServiceTierFromBody(body)
	// custom 工具（如 codex 的 exec）降级为 function 工具转发，回程需按名字还原为
	// custom_tool_call 项，先记下名字集合；tool_search 工具同理，回程还原为
	// tool_search_call 项；namespace 子工具（如 MCP 工具）摊平转发，回程按映射还原
	// 为带 namespace 字段的 function_call 项。
	customTools := apicompat.CustomToolNames(responsesReq.Tools)
	functionTools := apicompat.FunctionToolNames(responsesReq.Tools)
	toolSearch := apicompat.HasToolSearchTool(responsesReq.Tools)
	namespaceTools := apicompat.NamespaceToolNames(responsesReq.Tools)

	// 自愈回写：历史里带明文 summary 的 reasoning item 刷新进缓存，覆盖 Redis
	// 被 flush / 跨实例漂移后同 id 的 encrypted-only 副本无法再取明文的情况。
	s.recacheReasoningItemsFromInput(responsesReq.Input)

	chatReq, err := apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &apicompat.ResponsesToChatOptions{
		ReasoningContentByID: s.reasoningContentByID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": err.Error(),
			},
		})
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	// 国产模型默认 effort 补充：需要 mappedModel 判定，推迟到 billingModel 算出之后。
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	if serviceTier == nil {
		serviceTier = extractOpenAIServiceTierFromBody(chatBody)
	}

	logger.L().Debug("openai responses: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	apiKey := account.GetOpenAIApiKey()
	if apiKey == "" {
		return nil, fmt.Errorf("account %d missing api_key", account.ID)
	}
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	targetURL := buildOpenAIChatCompletionsURL(validatedURL)

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(chatBody))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	upstreamReq = upstreamReq.WithContext(WithHTTPUpstreamProfile(upstreamReq.Context(), HTTPUpstreamProfileOpenAI))
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey)
	if clientStream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiCCRawAllowedHeaders[lowerKey] {
			for _, v := range values {
				upstreamReq.Header.Add(key, v)
			}
		}
	}
	if customUA := account.GetOpenAIUserAgent(); customUA != "" {
		upstreamReq.Header.Set("user-agent", customUA)
	}
	account.ApplyHeaderOverrides(upstreamReq.Header)

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	recordUpstream429Attempt(account.ID)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		// Transport-level failure (proxy/DNS/TCP/TLS — no HTTP response). Convert to
		// a failover so the handler switches to a healthy account, and temporarily
		// unschedule the account on durable faults (e.g. rejected proxy credentials).
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(account, resp.StatusCode, upstreamMsg, respBody)
		var tempUnscheduled bool
		shouldFailover, tempUnscheduled = s.openAIPromoteTempUnscheduleFailover(ctx, c, account, resp.StatusCode, respBody, shouldFailover, upstreamModel)
		if shouldFailover {
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  upstreamRequestIDFromHeader(resp.Header),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			shouldDisable := tempUnscheduled
			if !tempUnscheduled {
				shouldDisable = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
			}
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: openAIRetryableOnSameAccount(resp.StatusCode, upstreamMsg, respBody, !shouldDisable && account.IsPoolMode() && (account.IsPoolModeRetryableStatus(resp.StatusCode) || isOpenAITransientProcessingError(resp.StatusCode, upstreamMsg, respBody))),
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	if clientStream {
		return s.streamChatCompletionsAsResponses(c, resp, account, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return s.bufferChatCompletionsAsResponses(c, resp, account, originalModel, customTools, functionTools, toolSearch, namespaceTools, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := upstreamRequestIDFromHeader(resp.Header)
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": gin.H{
					"type":    "api_error",
					"message": "Failed to read upstream response",
				},
			})
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}

	var ccResp apicompat.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &ccResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "api_error",
				"message": "Failed to parse upstream response",
			},
		})
		return nil, fmt.Errorf("parse chat completions response: %w", err)
	}
	responsesResp := apicompat.ChatCompletionsResponseToResponses(&ccResp, originalModel, customTools, functionTools, toolSearch, namespaceTools)

	usage := OpenAIUsage{}
	if parsed, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsed
	}
	// 上游模型不一致拦截：整包尚未写回客户端，直接按 failover 切号。
	if got := extractUpstreamResponseModel(respBody); got != "" {
		if ferr := s.checkUpstreamModelMismatch(c, account, requestID, resp.Header,
			sentModelForCheck(upstreamModel, originalModel), got, false, true, usage); ferr != nil {
			return nil, ferr
		}
	}
	// 模型一致性校验通过后再写 reasoning 缓存：被 failover 丢弃的响应不回注。
	s.cacheReasoningItemsFromOutput(responsesResp.Output)
	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, responsesResp)

	return &OpenAIForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	customTools map[string]bool,
	functionTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := upstreamRequestIDFromHeader(resp.Header)
	headersWritten := false
	writeStreamHeaders := func() {
		if headersWritten {
			return
		}
		headersWritten = true
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
	}

	state := apicompat.NewChatCompletionsToResponsesStreamState(originalModel)
	state.CustomTools = customTools
	state.FunctionTools = functionTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	var usage OpenAIUsage
	var firstTokenMs *int
	clientDisconnected := false
	sawDone := false
	// 上游模型不一致只在首个带 model 的 chunk 上比对一次。
	upstreamModelChecked := false

	writeEvents := func(events []apicompat.ResponsesStreamEvent) {
		if clientDisconnected || len(events) == 0 {
			return
		}
		writeStreamHeaders()
		for _, event := range events {
			sse, err := apicompat.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				return
			}
			observeResponsesStreamSequence(c, event.SequenceNumber)
		}
		c.Writer.Flush()
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			sawDone = true
			break
		}

		if u := extractCCStreamUsage(payload); u != nil {
			usage = *u
		}

		// 上游模型不一致拦截：在 chunk 转成 Responses 事件并写出之前比对；
		// 响应头尚未写出即客户端零输出，此时按 failover 切号。
		if !upstreamModelChecked {
			if got := extractUpstreamResponseModel([]byte(payload)); got != "" {
				upstreamModelChecked = true
				if ferr := s.checkUpstreamModelMismatch(c, account, requestID, resp.Header,
					sentModelForCheck(upstreamModel, originalModel), got, true, !headersWritten, usage); ferr != nil {
					return nil, ferr
				}
			}
		}

		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			logger.L().Warn("openai responses chat fallback: failed to parse chat stream chunk",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			continue
		}
		if firstTokenMs == nil && !isOpenAIChatUsageOnlyStreamChunk(payload) && chatChunkStartsResponsesOutput(&chunk) {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		events := apicompat.ChatCompletionsChunkToResponsesEvents(&chunk, state)
		s.cacheReasoningItemsFromEvents(events)
		writeEvents(events)
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai responses chat fallback: stream read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
		return &OpenAIForwardResult{
			RequestID:       requestID,
			Usage:           usage,
			Model:           originalModel,
			BillingModel:    billingModel,
			UpstreamModel:   upstreamModel,
			ReasoningEffort: reasoningEffort,
			ServiceTier:     serviceTier,
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}, fmt.Errorf("stream usage incomplete: %w", err)
	}

	// A tool call whose argument stream was truncated (e.g. an upstream SSE parse
	// failure or an output-limit interruption) must not be emitted as a completed
	// Responses item: Codex would persist it and replay it on the next turn, where
	// a Chat Completions provider rejects the whole request. Reject before
	// finalizing so no function_call_arguments.done / output_item.done / [DONE] is
	// sent for the poisoned call; already-streamed deltas for other calls stay on
	// the wire (best effort), but this response never completes cleanly.
	if err := state.ValidateToolCallArguments(); err != nil {
		return &OpenAIForwardResult{
			RequestID:       requestID,
			Usage:           usage,
			Model:           originalModel,
			BillingModel:    billingModel,
			UpstreamModel:   upstreamModel,
			ReasoningEffort: reasoningEffort,
			ServiceTier:     serviceTier,
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}, fmt.Errorf("invalid tool call arguments from upstream: %w", err)
	}

	finalEvents := apicompat.FinalizeChatCompletionsResponsesStream(state)
	s.cacheReasoningItemsFromEvents(finalEvents)
	writeEvents(finalEvents)
	if !clientDisconnected {
		writeStreamHeaders()
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}
	if !sawDone {
		logger.L().Debug("openai responses chat fallback: upstream stream ended without done sentinel",
			zap.String("request_id", requestID),
		)
	}

	return &OpenAIForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		Stream:          true,
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
	}, nil
}

func chatChunkStartsResponsesOutput(chunk *apicompat.ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.ReasoningContent != nil || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// responsesReasoningCacheTTL 是 reasoning 缓存（按 reasoning item id）的过期时间。
// Codex 会话可能跨多天恢复历史，取 7 天。
const responsesReasoningCacheTTL = 7 * 24 * time.Hour

// reasoningContentByID 按 reasoning item id 回查缓存的 reasoning 全文，供
// Responses→CC 桥接在客户端不回传明文 summary（encrypted-only reasoning
// item）时回注 reasoning_content。任何失败都 fail-open 返回 ""（维持桥接原
// 行为），因为缓存只是优化而非正确性前提。
func (s *OpenAIGatewayService) reasoningContentByID(itemID string) string {
	if s == nil || s.cache == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := s.cache.GetReasoningContent(ctx, itemID)
	if err != nil {
		return ""
	}
	return content
}

// recacheReasoningItemsFromInput 把请求历史里带明文 summary 的 reasoning item
// 重新写入缓存（best-effort）。Codex 多数时候会原样回传明文 summary，借机
// 刷新 TTL 并自愈 Redis 被 flush / 跨实例漂移造成的缓存缺失。
func (s *OpenAIGatewayService) recacheReasoningItemsFromInput(inputRaw json.RawMessage) {
	if s == nil || s.cache == nil {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return
	}
	for _, raw := range items {
		id, text, ok := apicompat.ExtractResponsesReasoningItem(raw)
		if !ok || id == "" || text == "" {
			continue
		}
		s.setReasoningContent(id, text)
	}
}

// cacheReasoningItemsFromEvents 从 Responses 流事件里提取完成的 reasoning
// item 写入缓存（覆盖一个流中的多个 reasoning item）。
func (s *OpenAIGatewayService) cacheReasoningItemsFromEvents(events []apicompat.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type != "response.output_item.done" || event.Item == nil {
			continue
		}
		s.cacheReasoningItem(event.Item)
	}
}

// cacheReasoningItemsFromOutput 从非流式 Responses 响应的 output 里提取
// reasoning item 写入缓存。
func (s *OpenAIGatewayService) cacheReasoningItemsFromOutput(output []apicompat.ResponsesOutput) {
	for i := range output {
		s.cacheReasoningItem(&output[i])
	}
}

func (s *OpenAIGatewayService) cacheReasoningItem(item *apicompat.ResponsesOutput) {
	if item == nil || item.Type != "reasoning" || item.ID == "" {
		return
	}
	var parts []string
	for _, sum := range item.Summary {
		if t := strings.TrimSpace(sum.Text); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return
	}
	s.setReasoningContent(item.ID, strings.Join(parts, "\n"))
}

// setReasoningContent 写入缓存，使用 detached ctx：客户端断连后仍在 drain
// 上游流（计费需要），此时的 reasoning 也是后续轮次回注所依赖的，不能随
// 请求 ctx 一起取消。失败仅记日志，不影响转发。
func (s *OpenAIGatewayService) setReasoningContent(itemID, content string) {
	if s == nil || s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.cache.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		logger.L().Warn("openai responses chat fallback: cache reasoning content failed",
			zap.Error(err),
			zap.String("item_id", itemID),
		)
	}
}
