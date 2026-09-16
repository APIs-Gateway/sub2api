package service

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const responsesStreamSequenceNextKey = "responses_stream_sequence_next"

// ObserveResponsesStreamSequence records an already-forwarded Responses event
// so a later gateway-generated terminal event can continue its ordering.
func ObserveResponsesStreamSequence(c *gin.Context, payload []byte) {
	if c == nil || !InboundIsResponses(c) {
		return
	}
	sequence := gjson.GetBytes(payload, "sequence_number")
	if !sequence.Exists() || sequence.Type != gjson.Number || sequence.Int() < 0 {
		return
	}
	next := int(sequence.Int()) + 1
	if current, ok := c.Get(responsesStreamSequenceNextKey); ok {
		if value, ok := current.(int); ok && value > next {
			return
		}
	}
	c.Set(responsesStreamSequenceNextKey, next)
}

// NextResponsesStreamSequence allocates the next sequence number for a
// gateway-generated Responses event. The first event is numbered zero.
func NextResponsesStreamSequence(c *gin.Context) int {
	if c == nil {
		return 0
	}
	next := 0
	if value, ok := c.Get(responsesStreamSequenceNextKey); ok {
		if parsed, ok := value.(int); ok && parsed >= 0 {
			next = parsed
		}
	}
	c.Set(responsesStreamSequenceNextKey, next+1)
	return next
}

// openAIResponsesSequenceTracker is request-local state for WebSocket HTTP
// bridge turns, where a gin context can span more than one response stream.
type openAIResponsesSequenceTracker struct {
	next int
}

func (t *openAIResponsesSequenceTracker) Observe(payload []byte) {
	if t == nil {
		return
	}
	sequence := gjson.GetBytes(payload, "sequence_number")
	if !sequence.Exists() || sequence.Type != gjson.Number || sequence.Int() < 0 {
		return
	}
	if next := int(sequence.Int()) + 1; next > t.next {
		t.next = next
	}
}

func (t *openAIResponsesSequenceTracker) Next() int {
	if t == nil {
		return 0
	}
	next := t.next
	t.next++
	return next
}
