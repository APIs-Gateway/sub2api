package apicompat

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Message provenance is in memory only. Input/history parts and parsed provider
// messages keep their existing serialization; only content reconstructed here
// receives the owned output_text compatibility arrays.
func (p *ResponsesContentPart) UnmarshalJSON(data []byte) error {
	type alias ResponsesContentPart
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = ResponsesContentPart(decoded)
	p.rawJSON = append(json.RawMessage(nil), data...)
	return nil
}

func (p ResponsesContentPart) MarshalJSON() ([]byte, error) {
	type alias ResponsesContentPart
	if !p.ownedOutputText || p.Type != "output_text" {
		return json.Marshal(alias(p))
	}
	return json.Marshal(outputTextPartWire(&p))
}

func marshalOwnedResponsesMessage(o ResponsesOutput) ([]byte, error) {
	// Added-message vendor metadata has no authoritative done/terminal object
	// in this fallback. Keep it while replacing only the reconstructed fields.
	m := make(map[string]json.RawMessage)
	if len(o.rawJSON) > 0 {
		if err := json.Unmarshal(o.rawJSON, &m); err != nil {
			return nil, err
		}
	}
	fields := map[string]any{
		"type": "message", "id": o.ID, "role": o.Role, "status": o.Status,
		"content": messageContentWire(o.Content),
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		m[key] = encoded
	}
	return json.Marshal(m)
}

func (e *ResponsesStreamEvent) UnmarshalJSON(data []byte) error {
	type alias ResponsesStreamEvent
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var indexes map[string]json.RawMessage
	if err := json.Unmarshal(data, &indexes); err != nil {
		return err
	}
	*e = ResponsesStreamEvent(decoded)
	_, e.hasOutputIndex = indexes["output_index"]
	_, e.hasContentIndex = indexes["content_index"]
	e.decodedFromJSON = true
	return nil
}

type bufferedMessagePart struct {
	index   int
	indexed bool
	part    ResponsesContentPart
}

type bufferedMessage struct {
	outputIndex int
	indexed     bool
	observedID  string
	fallbackID  string
	committedID string
	doneStatus  string
	rawJSON     json.RawMessage
	parts       []bufferedMessagePart
}

func (m *bufferedMessage) id() string {
	if m.committedID != "" {
		return m.committedID
	}
	if m.observedID != "" {
		return m.observedID
	}
	if m.fallbackID == "" {
		m.fallbackID = generateItemID()
	}
	return m.fallbackID
}

func (a *BufferedResponseAccumulator) messageForEvent(e *ResponsesStreamEvent) *bufferedMessage {
	id := e.ItemID
	if e.Item != nil && e.Item.Type == "message" && e.Item.ID != "" {
		id = e.Item.ID
	}
	indexed := e.hasOutputIndex || (!e.decodedFromJSON && e.OutputIndex >= 0)
	key := "legacy"
	if indexed && e.OutputIndex >= 0 {
		key = "index:" + strconv.Itoa(e.OutputIndex)
	} else if id != "" {
		key = "id:" + id
	} else if len(a.messages) == 1 {
		return &a.messages[0]
	}
	if _, exists := a.messageIndexes[key]; !exists && len(a.messages) == 1 && !a.messages[0].indexed && a.messages[0].observedID == "" {
		// An unindexed local fallback can acquire a later observed identity
		// while it is the only message. Keep the record, including any queued ID.
		a.messageIndexes[key] = 0
		a.messages[0].indexed = indexed && e.OutputIndex >= 0
		a.messages[0].outputIndex = e.OutputIndex
	}
	if i, ok := a.messageIndexes[key]; ok {
		m := &a.messages[i]
		// An observed identity wins over a cached, unexposed local fallback.
		// A conflicting later identity never renames a reserved/exposed item.
		if id != "" && m.observedID == "" && m.committedID == "" {
			m.observedID = id
		}
		return m
	}
	if a.messageIndexes == nil {
		a.messageIndexes = make(map[string]int)
	}
	a.messageIndexes[key] = len(a.messages)
	a.messages = append(a.messages, bufferedMessage{
		outputIndex: e.OutputIndex, indexed: indexed && e.OutputIndex >= 0,
		observedID: id,
	})
	return &a.messages[len(a.messages)-1]
}

func (m *bufferedMessage) part(index int, kind string, indexed bool) *ResponsesContentPart {
	for i := range m.parts {
		if m.parts[i].index == index && m.parts[i].part.Type == kind {
			m.parts[i].indexed = m.parts[i].indexed || indexed
			return &m.parts[i].part
		}
	}
	m.parts = append(m.parts, bufferedMessagePart{index: index, indexed: indexed, part: ResponsesContentPart{Type: kind}})
	return &m.parts[len(m.parts)-1].part
}

// Valid explicit content indexes describe part order, independently of arrival.
// Keep legacy unindexed slots in their existing positions; only indexed slots
// exchange entries. Equal indexes keep their arrival order.
func (m *bufferedMessage) orderedParts() []bufferedMessagePart {
	ordered := append([]bufferedMessagePart(nil), m.parts...)
	var indexed []bufferedMessagePart
	for _, part := range ordered {
		if part.indexed && part.index >= 0 {
			indexed = append(indexed, part)
		}
	}
	sort.SliceStable(indexed, func(i, j int) bool { return indexed[i].index < indexed[j].index })
	next := 0
	for i := range ordered {
		if ordered[i].indexed && ordered[i].index >= 0 {
			ordered[i] = indexed[next]
			next++
		}
	}
	return ordered
}

func (a *BufferedResponseAccumulator) observeMessageEvent(e *ResponsesStreamEvent) {
	switch e.Type {
	case "response.output_item.added", "response.output_item.done":
		if e.Item == nil || e.Item.Type != "message" {
			return
		}
		m := a.messageForEvent(e)
		if len(m.rawJSON) == 0 {
			m.rawJSON = append(json.RawMessage(nil), e.Item.rawJSON...)
		}
		if e.Type == "response.output_item.done" && validMessageStatus(e.Item.Status) {
			m.doneStatus = e.Item.Status
		}
		for i, part := range e.Item.Content {
			p := m.part(i, part.Type, true)
			// Added content is a seed, not another delta. Done is authoritative.
			if e.Type == "response.output_item.done" || (p.Text == "" && p.Refusal == "") {
				retainStreamedMessagePart(p, part)
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		if e.Delta == "" {
			return
		}
		m := a.messageForEvent(e)
		if e.Type == "response.output_text.delta" {
			m.part(e.ContentIndex, "output_text", e.hasContentIndex || !e.decodedFromJSON).Text += e.Delta
		} else {
			m.part(e.ContentIndex, "refusal", e.hasContentIndex || !e.decodedFromJSON).Refusal += e.Delta
		}
	case "response.content_part.added", "response.content_part.done":
		if e.Part == nil {
			return
		}
		p := a.messageForEvent(e).part(e.ContentIndex, e.Part.Type, e.hasContentIndex || !e.decodedFromJSON)
		if e.Type == "response.content_part.done" || (p.Text == "" && p.Refusal == "") {
			retainStreamedMessagePart(p, *e.Part)
		}
	case "response.output_text.done", "response.refusal.done":
		m := a.messageForEvent(e)
		if e.Type == "response.output_text.done" {
			if e.Text != "" {
				m.part(e.ContentIndex, "output_text", e.hasContentIndex || !e.decodedFromJSON).Text = e.Text
			}
		} else {
			if e.Refusal != "" {
				m.part(e.ContentIndex, "refusal", e.hasContentIndex || !e.decodedFromJSON).Refusal = e.Refusal
			}
		}
	}
}

func validMessageStatus(status string) bool {
	return status == "in_progress" || status == "completed" || status == "incomplete"
}

func messageStatusForTerminal(status string) string {
	switch status {
	case "completed", "done", "response.completed", "response.done":
		return "completed"
	case "incomplete", "cancelled", "canceled", "response.incomplete", "response.cancelled", "response.canceled":
		return "incomplete"
	default:
		return "in_progress"
	}
}

func (m *bufferedMessage) output(status string) (ResponsesOutput, bool) {
	usable := false
	for _, entry := range m.parts {
		p := entry.part
		if (p.Type == "output_text" && p.Text != "") || (p.Type == "refusal" && p.Refusal != "") {
			usable = true
		}
	}
	if !usable {
		return ResponsesOutput{}, false
	}
	content := make([]ResponsesContentPart, 0, len(m.parts))
	for _, entry := range m.orderedParts() {
		p := entry.part
		p.ownedOutputText = p.Type == "output_text"
		content = append(content, p)
	}
	if len(content) == 0 {
		return ResponsesOutput{}, false
	}
	itemStatus := m.doneStatus
	if itemStatus == "" {
		itemStatus = messageStatusForTerminal(status)
	}
	return ResponsesOutput{
		ownedMessage: true, rawJSON: m.rawJSON,
		Type: "message", ID: m.id(), Role: "assistant", Status: itemStatus, Content: content,
	}, true
}

// CommitWireEvent reserves identities once a payload has been queued for the
// client. A staged raw frame cannot be renamed later without rewriting that
// frame. This is deliberately earlier than flush, and keeps first-output staging
// consistent without treating a pure BuildOutput call as wire exposure.
func (a *BufferedResponseAccumulator) CommitWireEvent(e *ResponsesStreamEvent) {
	if e == nil {
		return
	}
	switch e.Type {
	case "response.output_item.added", "response.output_item.done", "response.output_text.delta", "response.refusal.delta", "response.content_part.added", "response.content_part.done":
		if e.Item != nil && e.Item.Type != "message" {
			return
		}
		if e.ItemID == "" && (e.Item == nil || e.Item.ID == "") {
			return
		}
		m := a.messageForEvent(e)
		if m.committedID == "" {
			m.committedID = m.id()
		}
	default:
		if e.Response != nil {
			for i := range a.messages {
				m := &a.messages[i]
				for _, item := range e.Response.Output {
					if item.Type == "message" && item.ID == m.id() {
						m.committedID = item.ID
					}
				}
			}
		}
	}
}

func (a *BufferedResponseAccumulator) supplementMessages(resp *ResponsesResponse) {
	var missing []ResponsesOutput
	terminalMessageCount := 0
	for _, item := range resp.Output {
		if item.Type == "message" {
			terminalMessageCount++
		}
	}
	for i := range a.messages {
		m := &a.messages[i]
		status := resp.Status
		if unfinishedMessageContext(a.terminalStatus) {
			status = a.terminalStatus
		}
		generated, hasContent := m.output(status)
		if !hasContent {
			continue
		}
		match := -1
		for j := range resp.Output {
			if resp.Output[j].Type == "message" && resp.Output[j].ID != "" && resp.Output[j].ID == generated.ID {
				match = j
				break
			}
		}
		if match < 0 && m.indexed && m.outputIndex < len(resp.Output) {
			item := resp.Output[m.outputIndex]
			if item.Type == "message" && (item.ID == "" || m.observedID == "") {
				match = m.outputIndex
			}
		}
		// Legacy first-empty recovery is only safe for one unambiguous message.
		if match < 0 && len(a.messages) == 1 && terminalMessageCount == 1 && m.observedID == "" {
			for j := range resp.Output {
				if resp.Output[j].Type == "message" {
					match = j
					break
				}
			}
		}
		if match >= 0 {
			item := &resp.Output[match]
			// Match only the original terminal part indexes. A newly inserted
			// part must not become authoritative evidence for another entry.
			terminalContent := append([]ResponsesContentPart(nil), item.Content...)
			var missingParts []ResponsesContentPart
			for _, entry := range m.orderedParts() {
				part := entry.part
				if part.Type != "output_text" && part.Type != "refusal" {
					if entry.index >= len(terminalContent) {
						missingParts = append(missingParts, part)
					}
					continue
				}
				if part.Text == "" && part.Refusal == "" {
					continue
				}
				part.ownedOutputText = part.Type == "output_text"
				partIndex := entry.index
				filled := false
				if partIndex >= 0 && partIndex < len(terminalContent) && terminalContent[partIndex].Type == part.Type {
					current := &item.Content[partIndex]
					if (part.Type == "output_text" && strings.TrimSpace(current.Text) == "") || (part.Type == "refusal" && current.Refusal == "") {
						if part.Type == "output_text" {
							current.Text = part.Text
							current.ownedOutputText = true
						} else {
							current.Refusal = part.Refusal
						}
						ensureSupplementedMessageFields(item, generated)
					}
					filled = true
				}
				if !filled {
					// Preserve terminal content of this type when its index was not
					// reported. Do not append a duplicate of authoritative text.
					for _, current := range terminalContent {
						if !entry.indexed && current.Type == part.Type && (strings.TrimSpace(current.Text) != "" || current.Refusal != "") {
							filled = true
						}
					}
					if !filled {
						missingParts = append(missingParts, part)
					}
				}
			}
			if len(missingParts) > 0 {
				item.Content = append(item.Content, missingParts...)
				ensureSupplementedMessageFields(item, generated)
			}
			continue
		}
		missing = append(missing, generated)
	}
	// Match every existing item against the original terminal indexes before
	// inserting missing messages. Insertion must not shift a later match.
	if len(missing) > 0 {
		insertAt := len(resp.Output)
		for j := range resp.Output {
			if resp.Output[j].Type == "function_call" {
				insertAt = j
				break
			}
		}
		tail := append([]ResponsesOutput(nil), resp.Output[insertAt:]...)
		resp.Output = append(resp.Output[:insertAt], missing...)
		resp.Output = append(resp.Output, tail...)
	}
}

func (a *BufferedResponseAccumulator) hasMessageContent() bool {
	for _, m := range a.messages {
		for _, p := range m.parts {
			if (p.part.Type == "output_text" && p.part.Text != "") || (p.part.Type == "refusal" && p.part.Refusal != "") {
				return true
			}
		}
	}
	return false
}

func ensureSupplementedMessageFields(item *ResponsesOutput, generated ResponsesOutput) {
	item.ownedMessage = true
	if item.ID == "" {
		item.ID = generated.ID
	}
	if item.Status == "" {
		item.Status = generated.Status
	}
	if item.Role == "" {
		item.Role = "assistant"
	}
}

// Empty or omitted done text is not evidence that an already streamed reply
// disappeared. Preserve it while accepting supplied done metadata.
func retainStreamedMessagePart(current *ResponsesContentPart, incoming ResponsesContentPart) {
	if incoming.Text == "" {
		incoming.Text = current.Text
	}
	if incoming.Refusal == "" {
		incoming.Refusal = current.Refusal
	}
	if len(incoming.Annotations) == 0 {
		incoming.Annotations = current.Annotations
	}
	if len(incoming.Logprobs) == 0 {
		incoming.Logprobs = current.Logprobs
	}
	incoming.rawJSON = mergeMessagePartMetadata(current.rawJSON, incoming.rawJSON)
	*current = incoming
}

// Merge only the sidecars of a part that this accumulator is reconstructing.
// Incoming keys, including explicit nulls, win; missing done metadata does not
// discard earlier provider keys. RawMessage keeps vendor numbers exact.
func mergeMessagePartMetadata(previous, incoming json.RawMessage) json.RawMessage {
	if len(previous) == 0 {
		return incoming
	}
	if len(incoming) == 0 {
		return previous
	}
	var merged, supplied map[string]json.RawMessage
	if json.Unmarshal(previous, &merged) != nil || json.Unmarshal(incoming, &supplied) != nil {
		return incoming
	}
	if merged == nil {
		merged = make(map[string]json.RawMessage)
	}
	for key, value := range supplied {
		merged[key] = value
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return incoming
	}
	return encoded
}

func unfinishedMessageContext(status string) bool {
	return messageStatusForTerminal(status) == "incomplete"
}

// A contradictory completed status cannot make an explicitly unfinished
// terminal event complete. This affects only newly reconstructed messages.
func messageTerminalContext(eventType, responseStatus string) string {
	if unfinishedMessageContext(eventType) {
		return eventType
	}
	if responseStatus != "" {
		return responseStatus
	}
	return eventType
}
