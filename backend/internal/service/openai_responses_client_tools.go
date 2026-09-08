package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Codex 客户端（0.147+）会声明 custom / tool_search / namespace 三类工具，
// 官方 Responses 上游认得，但 type=apikey 的二级中转商只认标准 function 工具，
// 未降级的声明会被拒绝或静默丢弃，表现为客户端侧工具调用整体失效。
// 这里在透传出站前把它们降级为 function 工具，并在回程还原成客户端认识的形态。
const openAIResponsesClientToolMappingContextKey = "openai_responses_client_tool_mapping"

func hasResponsesClientToolMapping(mapping apicompat.ResponsesClientToolMapping) bool {
	return len(mapping.CustomTools) > 0 || mapping.ToolSearch || len(mapping.NamespaceTools) > 0
}

func setOpenAIResponsesClientToolMapping(c *gin.Context, mapping apicompat.ResponsesClientToolMapping) {
	if c == nil {
		return
	}
	if !hasResponsesClientToolMapping(mapping) {
		clearOpenAIResponsesClientToolMapping(c)
		return
	}
	c.Set(openAIResponsesClientToolMappingContextKey, mapping)
}

// clearOpenAIResponsesClientToolMapping 清掉上一次转发尝试留下的映射。
// failover 会在同一个 gin.Context 上重试其它账号，换号后必须重新协商。
func clearOpenAIResponsesClientToolMapping(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get(openAIResponsesClientToolMappingContextKey); !exists {
		return
	}
	c.Set(openAIResponsesClientToolMappingContextKey, apicompat.ResponsesClientToolMapping{})
}

func openAIResponsesClientToolMapping(c *gin.Context) (apicompat.ResponsesClientToolMapping, bool) {
	if c == nil {
		return apicompat.ResponsesClientToolMapping{}, false
	}
	value, ok := c.Get(openAIResponsesClientToolMappingContextKey)
	if !ok {
		return apicompat.ResponsesClientToolMapping{}, false
	}
	mapping, typed := value.(apicompat.ResponsesClientToolMapping)
	return mapping, typed && hasResponsesClientToolMapping(mapping)
}

// needsOpenAIResponsesClientToolAdaptation 是一次廉价预检：透传是热路径，
// 绝大多数请求不含这三类工具，不该为它们付出一次全量 Unmarshal + 重新编码。
func needsOpenAIResponsesClientToolAdaptation(body []byte) bool {
	needsAdaptation := false
	var visit func(gjson.Result) bool
	visit = func(value gjson.Result) bool {
		if value.IsObject() {
			switch strings.TrimSpace(value.Get("type").String()) {
			case "custom", "custom_tool_call", "custom_tool_call_output",
				"tool_search", "tool_search_call", "tool_search_output":
				needsAdaptation = true
				return false
			}
		}
		if value.IsObject() || value.IsArray() {
			value.ForEach(func(_, child gjson.Result) bool {
				return visit(child)
			})
		}
		return !needsAdaptation
	}
	visit(gjson.ParseBytes(body))
	return needsAdaptation
}

func adaptOpenAIResponsesClientTools(body []byte) ([]byte, apicompat.ResponsesClientToolMapping, error) {
	if !needsOpenAIResponsesClientToolAdaptation(body) {
		return body, apicompat.ResponsesClientToolMapping{}, nil
	}

	requestBody, err := decodeOpenAIResponsesClientToolsRequestBody(body)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, err
	}

	mapping, changed, err := apicompat.AdaptResponsesClientTools(requestBody)
	if err != nil || !changed {
		return body, mapping, err
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, fmt.Errorf("encode OpenAI Responses client tools: %w", err)
	}
	return rebuilt, mapping, nil
}

// adaptOpenAIResponsesClientToolsWithInheritedMapping behaves like
// adaptOpenAIResponsesClientTools when body declares its own "tools" field.
// OpenAI WS HTTP bridge follow-up turns, whose client relies on the upstream
// to remember what it declared on an earlier turn of the same session, omit
// "tools" entirely instead of repeating the declaration. Treating that
// omission as "no client tools" would forward this turn's unlowered
// custom/tool_search/namespace input history and tool_choice to a
// function-only upstream unmodified. When body omits "tools" and a previous
// turn recorded a mapping plus what it lowered "tools" to, this reinstates
// both instead. It also returns the "tools" declaration actually sent
// upstream this turn (freshly lowered or inherited) so the caller can
// remember it for a possible next turn.
func adaptOpenAIResponsesClientToolsWithInheritedMapping(
	body []byte,
	previousMapping apicompat.ResponsesClientToolMapping,
	previousLoweredTools []any,
) ([]byte, apicompat.ResponsesClientToolMapping, []any, error) {
	canInherit := hasResponsesClientToolMapping(previousMapping) && len(previousLoweredTools) > 0
	if !needsOpenAIResponsesClientToolAdaptation(body) && !canInherit {
		return body, apicompat.ResponsesClientToolMapping{}, nil, nil
	}

	requestBody, err := decodeOpenAIResponsesClientToolsRequestBody(body)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, nil, err
	}

	mapping, changed, err := apicompat.AdaptResponsesClientToolsWithInheritedMapping(requestBody, previousMapping, previousLoweredTools)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, nil, err
	}
	loweredTools, _ := requestBody["tools"].([]any)
	if !changed {
		return body, mapping, loweredTools, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, nil, fmt.Errorf("encode OpenAI Responses client tools: %w", err)
	}
	return rebuilt, mapping, loweredTools, nil
}

func decodeOpenAIResponsesClientToolsRequestBody(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return nil, fmt.Errorf("decode OpenAI Responses client tools: %w", err)
	}
	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("decode OpenAI Responses client tools trailing data: %w", err)
	}
	return requestBody, nil
}

// restoreOpenAIResponsesClientToolPayload 还原非流式响应里的降级工具调用。
func restoreOpenAIResponsesClientToolPayload(c *gin.Context, payload []byte) ([]byte, error) {
	mapping, ok := openAIResponsesClientToolMapping(c)
	if !ok || !json.Valid(payload) {
		return payload, nil
	}
	restored, changed, err := apicompat.RestoreResponsesClientToolPayload(payload, mapping)
	if err != nil {
		return payload, fmt.Errorf("restore OpenAI Responses client tools: %w", err)
	}
	if changed {
		return restored, nil
	}
	return payload, nil
}

type responsesClientToolStreamBody struct {
	*io.PipeReader
	source io.Closer
}

func (b *responsesClientToolStreamBody) Close() error {
	readerErr := b.PipeReader.Close()
	sourceErr := b.source.Close()
	if readerErr != nil {
		return readerErr
	}
	return sourceErr
}

func newResponsesClientToolStreamBody(
	source io.ReadCloser,
	mapping apicompat.ResponsesClientToolMapping,
	maxLineSize int,
) io.ReadCloser {
	reader, writer := io.Pipe()
	body := &responsesClientToolStreamBody{PipeReader: reader, source: source}
	go transformResponsesClientToolStream(source, writer, mapping, maxLineSize)
	return body
}

func transformResponsesClientToolStream(
	source io.ReadCloser,
	destination *io.PipeWriter,
	mapping apicompat.ResponsesClientToolMapping,
	maxLineSize int,
) {
	defer func() { _ = source.Close() }()
	if maxLineSize <= 0 {
		maxLineSize = defaultMaxLineSize
	}

	scanner := bufio.NewScanner(source)
	scanBuf := getSSEScannerBuf64K()
	defer putSSEScannerBuf64K(scanBuf)
	scanner.Buffer(scanBuf[:0], maxLineSize)
	documents := newOpenAISSEJSONDocumentScanner(scanner)
	restorer := apicompat.NewResponsesClientToolStreamRestorer(mapping)
	buffered := bufio.NewWriterSize(destination, 4*1024)
	pendingFields := make([]string, 0, 2)
	frameHadEventField := false
	frameEmitted := false

	writeLine := func(line string) error {
		if _, err := buffered.WriteString(line); err != nil {
			return err
		}
		return buffered.WriteByte('\n')
	}
	// 还原可能把一个事件拆成多个，event: 行需要按各自 payload 的 type 重写，
	// 否则下游看到的事件名会和 data 对不上。
	writePendingFields := func(payload []byte, includeNonEvent bool) error {
		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		for _, field := range pendingFields {
			if _, isEvent := extractOpenAISSEEventLine(field); isEvent {
				if eventType != "" {
					if err := writeLine("event: " + eventType); err != nil {
						return err
					}
				} else if err := writeLine(field); err != nil {
					return err
				}
				continue
			}
			if includeNonEvent {
				if err := writeLine(field); err != nil {
					return err
				}
			}
		}
		return nil
	}
	writePayloads := func(payloads [][]byte) error {
		for index, payload := range payloads {
			if index == 0 {
				if err := writePendingFields(payload, true); err != nil {
					return err
				}
			} else if frameHadEventField {
				eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
				if eventType != "" {
					if err := writeLine("event: " + eventType); err != nil {
						return err
					}
				}
			}
			if err := writeLine("data: " + string(payload)); err != nil {
				return err
			}
			if err := writeLine(""); err != nil {
				return err
			}
		}
		return buffered.Flush()
	}

	for documents.Scan() {
		line := documents.Text()
		data, isData := extractOpenAISSEDataLine(line)
		if isData {
			payload := []byte(data)
			payloads := [][]byte{payload}
			if json.Valid(payload) {
				var err error
				payloads, _, err = restorer.RestoreEvent(payload)
				if err != nil {
					_ = buffered.Flush()
					_ = destination.CloseWithError(fmt.Errorf("restore Responses client tool event: %w", err))
					return
				}
			}
			if err := writePayloads(payloads); err != nil {
				_ = destination.CloseWithError(err)
				return
			}
			pendingFields = pendingFields[:0]
			frameHadEventField = false
			frameEmitted = true
			continue
		}

		if line == "" {
			if !frameEmitted {
				for _, field := range pendingFields {
					if err := writeLine(field); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
				}
				if len(pendingFields) > 0 {
					if err := writeLine(""); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
					if err := buffered.Flush(); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
				}
			}
			pendingFields = pendingFields[:0]
			frameHadEventField = false
			frameEmitted = false
			continue
		}

		if _, isEvent := extractOpenAISSEEventLine(line); isEvent {
			frameHadEventField = true
		}
		pendingFields = append(pendingFields, line)
	}

	for _, field := range pendingFields {
		if err := writeLine(field); err != nil {
			_ = destination.CloseWithError(err)
			return
		}
	}
	if err := buffered.Flush(); err != nil {
		_ = destination.CloseWithError(err)
		return
	}
	if err := documents.Err(); err != nil {
		_ = destination.CloseWithError(err)
		return
	}
	_ = destination.Close()
}
