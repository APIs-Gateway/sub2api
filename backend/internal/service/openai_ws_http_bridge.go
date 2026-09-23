package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIWSClientReadLimitBytesDefault     int64 = 64 * 1024 * 1024
	openAIWSHTTPBridgeThresholdBytesDefault int64 = 15 * 1024 * 1024
	openAIWSHTTPBridgeErrorBodyLimitBytes         = 64 * 1024
)

// ResolveOpenAIWSClientFirstMessageTimeout returns the effective client ingress deadline.
func ResolveOpenAIWSClientFirstMessageTimeout(cfg *config.Config) time.Duration {
	seconds := config.DefaultOpenAIWSClientFirstMessageTimeoutSeconds
	if cfg != nil && cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds > 0 {
		seconds = cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func ResolveOpenAIWSClientReadLimitBytes(cfg *config.Config) int64 {
	if cfg == nil || cfg.Gateway.OpenAIWS.ClientReadLimitBytes <= 0 {
		return openAIWSClientReadLimitBytesDefault
	}
	return cfg.Gateway.OpenAIWS.ClientReadLimitBytes
}

func (s *OpenAIGatewayService) openAIWSHTTPBridgeEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled
}

func (s *OpenAIGatewayService) openAIWSHTTPBridgeThresholdBytes() int64 {
	if s == nil || s.cfg == nil || s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes <= 0 {
		return openAIWSHTTPBridgeThresholdBytesDefault
	}
	return s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes
}

func (s *OpenAIGatewayService) shouldBridgeOpenAIWSHTTP(payloadBytes int, previousResponseID string) bool {
	if !s.openAIWSHTTPBridgeEnabled() {
		return false
	}
	if strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	threshold := s.openAIWSHTTPBridgeThresholdBytes()
	return threshold > 0 && int64(payloadBytes) >= threshold
}

func prepareOpenAIWSHTTPBridgeBody(payload []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("response.create payload must be a JSON object")
	}
	delete(body, "type")
	delete(body, "generate")
	delete(body, "previous_response_id")
	body["stream"] = true
	return json.Marshal(body)
}

type openAIWSToolCallReplayCollector struct {
	items []json.RawMessage
	seen  map[string]struct{}
}

func (c *openAIWSToolCallReplayCollector) AddEvent(eventType string, message []byte) {
	switch strings.TrimSpace(eventType) {
	case "response.output_item.done":
		c.addItem(gjson.GetBytes(message, "item"))
	case "response.completed", "response.done":
		output := gjson.GetBytes(message, "response.output")
		if !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			c.addItem(item)
		}
	}
}

func (c *openAIWSToolCallReplayCollector) Items() []json.RawMessage {
	return cloneOpenAIWSRawMessages(c.items)
}

func (c *openAIWSToolCallReplayCollector) addItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return
	}
	if !isCodexToolCallContextItemType(item.Get("type").String()) {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.items = append(c.items, json.RawMessage(raw))
}

// openAIWSHTTPBridgeToolState carries the client-tool lowering mapping and
// the resulting "tools" declaration actually sent upstream across the turns
// of a single WS HTTP bridge session, so that a follow-up turn which omits
// "tools" (because the client trusts the upstream to remember what it
// declared earlier in the same session) can still be lowered and restored
// correctly instead of being treated as if it declared no client tools.
//
// This is deliberately plain per-connection state, not a registry keyed by
// session/connection ID: proxyOpenAIWSHTTPBridgeTurn is called from a turn
// loop that lives entirely inside the single request handler goroutine
// owning one WS connection (see the http bridge loop in
// openai_ws_forwarder.go), so a zero value is always the correct "nothing
// negotiated yet" state for a new connection, and the state is discarded
// automatically together with that goroutine's stack -- no separate
// lifecycle management or explicit cleanup is required.
type openAIWSHTTPBridgeToolState struct {
	ClientMapping apicompat.ResponsesClientToolMapping
	LoweredTools  []any
}

func buildOpenAIWSHTTPBridgeErrorEvent(statusCode int, message string, sequenceNumber int) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		message = "upstream request failed"
	}
	event := map[string]any{
		"type":            "error",
		"sequence_number": sequenceNumber,
		"status":          statusCode,
		"error": map[string]any{
			"type":    "upstream_error",
			"message": message,
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		return []byte(fmt.Sprintf(`{"type":"error","sequence_number":%d,"error":{"type":"upstream_error","message":"upstream request failed"}}`, sequenceNumber))
	}
	return body
}

func (s *OpenAIGatewayService) proxyOpenAIWSHTTPBridgeTurn(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token string,
	payload []byte,
	payloadBytes int,
	originalModel string,
	imageBillingModel string,
	imageSizeTier string,
	imageInputSize string,
	turn int,
	previousToolState openAIWSHTTPBridgeToolState,
	writeClientMessage func([]byte) error,
) (*OpenAIForwardResult, error) {
	if s == nil {
		return nil, errors.New("service is nil")
	}
	if s.httpUpstream == nil {
		return nil, errors.New("openai http upstream is nil")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if writeClientMessage == nil {
		return nil, errors.New("client websocket writer is nil")
	}

	body, err := prepareOpenAIWSHTTPBridgeBody(payload)
	if err != nil {
		return nil, fmt.Errorf("prepare http bridge body: %w", err)
	}

	// Codex 0.147+ 在 WS HTTP bridge 场景下同样会声明 custom / tool_search /
	// namespace 三类客户端工具；type=apikey 的二级中转商上游只认标准 function
	// 工具，未降级会导致工具调用整体失效。出站前降级，回程流式还原。
	var clientToolMapping apicompat.ResponsesClientToolMapping
	var loweredClientTools []any
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey {
		var adaptErr error
		body, clientToolMapping, loweredClientTools, adaptErr = adaptOpenAIResponsesClientToolsWithInheritedMapping(
			body, previousToolState.ClientMapping, previousToolState.LoweredTools,
		)
		if adaptErr != nil {
			return nil, fmt.Errorf("adapt openai ws http bridge client tools: %w", adaptErr)
		}
	}
	// The bridge forwards Lite turns with the Lite header over HTTP, so the
	// payload must satisfy the same Lite contract as the HTTP and WS paths
	// (OAuth tool carrier, parallel_tool_calls=false for every OpenAI account).
	if account.Platform != PlatformGrok && isOpenAIResponsesLiteWebSocketPayload(payload) {
		liteBody, liteChanged, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(body, account)
		if liteErr != nil {
			return nil, fmt.Errorf("normalize responses Lite payload: %w", liteErr)
		}
		if liteChanged {
			body = liteBody
		}
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := s.buildUpstreamRequestOpenAIPassthrough(upstreamCtx, c, account, body, token)
	releaseUpstreamCtx()
	if err != nil {
		return nil, err
	}
	if isOpenAIResponsesLiteWebSocketPayload(payload) {
		upstreamReq.Header.Set(responsesLiteHeader, "true")
	}
	if err := applyMappedGPT55LiteCompatibility(upstreamReq, account, body); err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if c != nil {
		c.Set("openai_passthrough", true)
		c.Set("openai_ws_http_bridge", true)
	}

	turnStart := time.Now()
	sequence := openAIResponsesSequenceTracker{}
	recordUpstream429Attempt(account.ID)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		if turn == 1 {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		}
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "Upstream request failed", sequence.Next()))
		return nil, fmt.Errorf("upstream http bridge request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, openAIWSHTTPBridgeErrorBodyLimitBytes))
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		if upstreamMsg == "" {
			upstreamMsg = http.StatusText(resp.StatusCode)
		}
		shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(account, resp.StatusCode, upstreamMsg, respBody)
		accountErrorHandled := false
		if resp.StatusCode == http.StatusTooManyRequests && account.Platform == PlatformOpenAI {
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, originalModel)
			accountErrorHandled = true
		}
		if turn == 1 && shouldFailover {
			if accountErrorHandled {
				return nil, &UpstreamFailoverError{
					StatusCode:      resp.StatusCode,
					ResponseBody:    append([]byte(nil), respBody...),
					ResponseHeaders: cloneHeader(resp.Header),
				}
			}
			return nil, s.handleFailoverErrorResponsePassthrough(ctx, resp, c, account, body, respBody)
		}
		if !accountErrorHandled && shouldFailover {
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, originalModel)
		}
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(resp.StatusCode, upstreamMsg, sequence.Next()))
		return nil, fmt.Errorf("upstream http bridge error: status=%d message=%s", resp.StatusCode, upstreamMsg)
	}

	responseID := ""
	usage := OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	reqStream := openAIWSPayloadBoolFromRaw(body, "stream", true)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	replayCollector := &openAIWSToolCallReplayCollector{}
	firstEventType := ""
	lastEventType := ""
	sawDone := false
	wroteDownstream := false
	upstreamModelChecked := false
	clientDisconnected := false
	// 首轮 OpenAI 在首个语义输出前暂存元数据帧（response.created / in_progress /
	// 空 reasoning item 等）：随后若是容量降载等可 failover 的失败，客户端尚未收到
	// 任何本次尝试的事件，handler 可安全换号/同账号重放第 1 轮。
	pendingClientMessages := make([][]byte, 0, 4)
	pendingClientMessageBytes := int64(0)
	capacityFailoverSuppressedLogged := false
	upstreamRequestID := upstreamRequestIDFromHeader(resp.Header)
	mappedModel := ""
	if originalModel != "" {
		mappedModel = normalizeOpenAIModelForUpstream(account, account.GetMappedModel(originalModel))
	}

	resultWithUsage := func() *OpenAIForwardResult {
		imageCount := imageCounter.Count()
		result := &OpenAIForwardResult{
			RequestID:       responseID,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   mappedModel,
			ServiceTier:     extractOpenAIServiceTierFromBody(body),
			ReasoningEffort: ApplyThinkingEnabledFallback(extractOpenAIReasoningEffortFromBody(body, mappedModel, originalModel), body, mappedModel),
			Stream:          reqStream,
			OpenAIWSMode:    true,
			ResponseHeaders: cloneHeader(resp.Header),
			Duration:        time.Since(turnStart),
			FirstTokenMs:    firstTokenMs,
		}
		if replayInput := replayCollector.Items(); len(replayInput) > 0 {
			result.wsReplayInput = replayInput
			result.wsReplayInputExists = true
		}
		if hasResponsesClientToolMapping(clientToolMapping) {
			result.wsClientToolState = openAIWSHTTPBridgeToolState{
				ClientMapping: clientToolMapping,
				LoweredTools:  loweredClientTools,
			}
		}
		if imageCount > 0 {
			result.ImageCount = imageCount
			result.ImageSize = imageSizeTier
			result.ImageInputSize = imageInputSize
			result.ImageOutputSizes = imageCounter.Sizes()
			result.BillingModel = imageBillingModel
		}
		return result
	}

	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	if hasResponsesClientToolMapping(clientToolMapping) {
		resp.Body = newResponsesClientToolStreamBody(resp.Body, clientToolMapping, maxLineSize)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer putSSEScannerBuf64K(scanBuf)

	for scanner.Scan() {
		line := scanner.Text()
		data, ok := extractOpenAISSEDataLine(line)
		if !ok {
			continue
		}
		trimmedData := strings.TrimSpace(data)
		if trimmedData == "" {
			continue
		}
		if trimmedData == "[DONE]" {
			sawDone = true
			continue
		}

		upstreamMessage := []byte(trimmedData)
		if normalized, changed := normalizeCompletedImageGenerationStatus(upstreamMessage); changed {
			upstreamMessage = normalized
		}
		eventType, eventResponseID, _ := parseOpenAIWSEventEnvelope(upstreamMessage)
		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}
		if eventType != "" {
			eventCount++
			if firstEventType == "" {
				firstEventType = eventType
			}
			lastEventType = eventType
		}
		if isOpenAIWSTokenEvent(eventType) {
			tokenEventCount++
			if firstTokenMs == nil {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
		}
		if openAIWSEventShouldParseUsage(eventType) {
			parseOpenAIWSResponseUsageFromCompletedEvent(upstreamMessage, &usage)
		}
		imageCounter.AddSSEData(upstreamMessage)

		// 上游模型不一致拦截：首个带 model 的 SSE 事件、模型改写前比对；
		// 与本函数其它 failover 一致，只有首轮且未向客户端写出时才中断换号
		// （handler 换号后会用 wsFirstMessage 重放第 1 轮）；turn>=2 只打标不拦截。
		if !upstreamModelChecked {
			if got := extractUpstreamResponseModel(upstreamMessage); got != "" {
				upstreamModelChecked = true
				if ferr := s.checkUpstreamModelMismatch(c, account, responseID, nil, sentModelForCheck(mappedModel, originalModel), got, reqStream, turn == 1 && !wroteDownstream, usage); ferr != nil {
					return nil, ferr
				}
			}
		}

		// 客户端可见 model 对齐：无条件把 model / response.model 改成客户端原始请求模型
		//（turn>=2 只打标不拦截时尤其重要：上游真实值只进审计 mark）。
		upstreamMessage = alignClientVisibleModel(upstreamMessage, originalModel)
		if s.toolCorrector != nil && openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
			if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
				upstreamMessage = corrected
			}
		}
		replayCollector.AddEvent(eventType, upstreamMessage)

		var upstreamEventErr error
		requestScopedCapacity := (eventType == "error" || eventType == "response.failed") &&
			account.Platform == PlatformOpenAI && isOpenAIUpstreamCapacityShedEvent(upstreamMessage)
		if eventType == "error" {
			errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(upstreamMessage)
			errMessage := strings.TrimSpace(errMsgRaw)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			statusCode := openAIWSErrorHTTPStatusFromRaw(errCodeRaw, errTypeRaw)
			shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(account, statusCode, errMessage, upstreamMessage)
			accountErrorHandled := false
			if statusCode == http.StatusTooManyRequests && account.Platform == PlatformOpenAI {
				s.persistOpenAIWSRateLimitSignal(ctx, account, resp.Header, upstreamMessage, errCodeRaw, errTypeRaw, errMsgRaw)
				accountErrorHandled = true
			}
			if shouldFailover && !accountErrorHandled {
				s.handleOpenAIAccountUpstreamError(ctx, account, statusCode, resp.Header, upstreamMessage, originalModel)
			}
			// A disconnected client needs this attempt drained for usage, not replayed,
			// even when only non-semantic heartbeats were delivered.
			if turn == 1 && !clientDisconnected && !wroteDownstream && shouldFailover {
				if account.Platform == PlatformOpenAI && !accountErrorHandled {
					// 与上游一致：OpenAI 流内 error 帧按流式失败构造 failover（容量降载带
					// RequestScopedTransient + 同账号重试）。fork 独有的 429 持久化分支
					// （accountErrorHandled）保留原构造，不重复写 429 切号闸门。
					return nil, s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, upstreamMessage, errMessage, resp.Header)
				}
				failoverErr := &UpstreamFailoverError{
					StatusCode:      statusCode,
					ResponseBody:    append([]byte(nil), upstreamMessage...),
					ResponseHeaders: cloneHeader(resp.Header),
				}
				if requestScopedCapacity {
					// 容量降载是请求级信号：同账号有界重试，且不得据此临时封禁账号。
					failoverErr.RetryableOnSameAccount = true
					failoverErr.RequestScopedTransient = true
				}
				return nil, failoverErr
			}
			upstreamEventErr = errors.New(errMessage)
		}
		// response.failed 在首轮尚未输出语义内容时（元数据帧已被暂存）与 HTTP 流式路径
		// 同一边界：可重试类失败走 failover，而不是把终止事件原样交给客户端。
		if eventType == "response.failed" && turn == 1 && account.Platform == PlatformOpenAI &&
			!clientDisconnected && !wroteDownstream {
			failedMessage := extractOpenAISSEErrorMessage(upstreamMessage)
			if hit, _, _ := detectOpenAICyberPolicy(upstreamMessage); !hit &&
				openAIStreamFailedEventShouldFailover(upstreamMessage, failedMessage) {
				return nil, s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, upstreamMessage, failedMessage, resp.Header)
			}
		}
		if requestScopedCapacity && wroteDownstream && !capacityFailoverSuppressedLogged {
			logOpenAICapacityFailoverSuppressed(ctx, account, "ws_http_bridge", upstreamRequestID, eventType)
			capacityFailoverSuppressedLogged = true
		}

		// 客户端写出副本改写容量降载码：Codex 对 error/response.failed 中的
		// server_is_overloaded / slow_down 判致命并终止会话，改写后走客户端内置
		// 重试。账号状态与终止事件判定（本函数内 shouldFailoverOpenAIUpstreamResponse
		// 等逻辑）仍使用未改写的 upstreamMessage。
		clientMessage := upstreamMessage
		if eventType == "error" || eventType == "response.failed" {
			if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(clientMessage); changed {
				clientMessage = rewritten
			}
		}
		if !clientDisconnected {
			// 传输心跳不暂存：它只维持客户端连接，不算语义输出。
			stageBeforeSemanticOutput := turn == 1 && account.Platform == PlatformOpenAI &&
				!wroteDownstream && eventType != "keepalive"
			commitStagedMessages := !stageBeforeSemanticOutput ||
				eventType == "error" ||
				isOpenAIWSTerminalEvent(eventType) ||
				openAIStreamDataStartsClientOutput(string(clientMessage), eventType)
			if stageBeforeSemanticOutput && !commitStagedMessages {
				if pendingClientMessageBytes+int64(len(clientMessage)) > openAIFirstOutputStageMaxBytes {
					return nil, s.newOpenAIStreamFailoverError(
						c, account, true, upstreamRequestID, nil,
						"OpenAI WS HTTP bridge first-output staging limit exceeded",
						resp.Header,
					)
				}
				pendingClientMessages = append(pendingClientMessages, append([]byte(nil), clientMessage...))
				pendingClientMessageBytes += int64(len(clientMessage))
			} else {
				batch := [][]byte{clientMessage}
				if eventType != "keepalive" && len(pendingClientMessages) > 0 {
					batch = append(pendingClientMessages, clientMessage)
					pendingClientMessages = nil
					pendingClientMessageBytes = 0
				}
				for _, message := range batch {
					if err := writeClientMessage(message); err != nil {
						if isOpenAIWSClientDisconnectError(err) {
							clientDisconnected = true
							closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
							logOpenAIWSModeInfo(
								"ingress_ws_http_bridge_client_disconnected_drain account_id=%d turn=%d close_status=%s close_reason=%s",
								account.ID,
								turn,
								closeStatus,
								truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
							)
							break
						}
						return nil, wrapOpenAIWSIngressTurnError(
							"write_client",
							fmt.Errorf("write client websocket event: %w", err),
							wroteDownstream,
						)
					}
					sequence.Observe(message)
					// Transport heartbeats keep the client connection alive but are not
					// semantic output, so they must not block a pre-output failover.
					if eventType != "keepalive" {
						wroteDownstream = true
					}
				}
			}
		}

		if upstreamEventErr != nil {
			return resultWithUsage(), upstreamEventErr
		}
		if isOpenAIWSTerminalEvent(eventType) {
			terminalEventCount++
			firstTokenMsValue := -1
			if firstTokenMs != nil {
				firstTokenMsValue = *firstTokenMs
			}
			logOpenAIWSModeInfo(
				"ingress_ws_http_bridge_turn_completed account_id=%d turn=%d response_id=%s payload_bytes=%d duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
				payloadBytes,
				time.Since(turnStart).Milliseconds(),
				eventCount,
				tokenEventCount,
				terminalEventCount,
				truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
				firstTokenMsValue,
				clientDisconnected,
			)
			return resultWithUsage(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		streamErr := fmt.Errorf("read upstream http bridge stream: %w", err)
		if turn == 1 && !clientDisconnected && !wroteDownstream {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, streamErr, true)
		}
		return resultWithUsage(), streamErr
	}
	terminalErr := errors.New("upstream http bridge stream ended before terminal event")
	if sawDone {
		terminalErr = errors.New("upstream http bridge stream sent [DONE] before terminal event")
	}
	if turn == 1 && !clientDisconnected && !wroteDownstream {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, terminalErr, true)
	}
	return resultWithUsage(), terminalErr
}
