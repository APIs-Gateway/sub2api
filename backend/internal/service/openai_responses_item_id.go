package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// shouldStripOpenAIResponsesInputItemID reports whether an OpenAI Responses
// API input item's client-supplied id must be stripped before the request is
// forwarded upstream through the API-key passthrough path.
//
// Codex CLI (and other Responses API clients) may replay input items using
// internal ids captured from a previous turn (e.g. "item_<uuid>"). OpenAI's
// upstream Responses API validates the id prefix per item type and rejects a
// mismatch with 400 (e.g. "Expected an ID that begins with 'msg'."). Beyond
// avoiding that 400, stripping ids that do not match the expected prefix also
// keeps sub2api/Codex-internal item id structure from leaking to the
// upstream (and, transitively, back to the client via any echoed fields).
//
// Ids longer than the upstream 64-character limit are stripped as well, even
// with the right prefix: OpenAI rejects them with 400 before looking at the
// prefix.
//
// Invalid replayed ids are removed rather than rewritten because a fabricated
// msg/rs/fc id may point at a different upstream object.
//
// This is independent from filterCodexInputWithOptions's OAuth-only handling
// (see openai_codex_transform.go): OAuth accounts and API-key accounts are
// mutually exclusive account types and never hit both code paths for the
// same request.
func shouldStripOpenAIResponsesInputItemID(itemType, id string) bool {
	if id == "" {
		return false
	}
	switch itemType {
	case "message":
		return !isValidOpenAIResponsesInputItemID(id, "msg")
	case "reasoning":
		return !isValidOpenAIResponsesInputItemID(id, "rs")
	}
	if isCodexToolCallInputType(itemType) {
		return !isValidOpenAIResponsesInputItemID(id, "fc")
	}
	return false
}

// openAIResponsesInputItemIDMaxLength is the upstream limit on input item
// ids; a longer id with the right prefix is still rejected with 400
// ("string too long"), so it is stripped like a wrong-prefix id.
const openAIResponsesInputItemIDMaxLength = 64

func isValidOpenAIResponsesInputItemID(id, prefix string) bool {
	return len(id) <= openAIResponsesInputItemIDMaxLength && strings.HasPrefix(id, prefix)
}

// sanitizeOpenAIResponsesInputItemIDs strips invalid input item ids (see
// shouldStripOpenAIResponsesInputItemID) from an OpenAI Responses API request
// body's top-level "input" array. It returns the body unchanged
// (changed=false) when there is nothing to strip, avoiding an unnecessary
// re-encode on the hot path.
//
// Item bodies are sanitized independently and the "input" array is rebuilt in
// a single pass, so the cost stays linear in the number of items / request
// size instead of paying one whole-body sjson rewrite per stripped id.
func sanitizeOpenAIResponsesInputItemIDs(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}

	items := make([][]byte, 0)
	changed := false
	var sanitizeErr error
	index := 0
	input.ForEach(func(_, item gjson.Result) bool {
		currentIndex := index
		index++
		itemBody := []byte(item.Raw)
		if item.IsObject() {
			itemType := item.Get("type")
			id := item.Get("id")
			if itemType.Type == gjson.String && id.Type == gjson.String &&
				shouldStripOpenAIResponsesInputItemID(itemType.String(), id.String()) {
				itemBody, sanitizeErr = sjson.DeleteBytes(itemBody, "id")
				if sanitizeErr != nil {
					sanitizeErr = fmt.Errorf("delete input.%d.id: %w", currentIndex, sanitizeErr)
					return false
				}
				changed = true
			}
		}
		items = append(items, itemBody)
		return true
	})
	if sanitizeErr != nil {
		return nil, false, sanitizeErr
	}
	if !changed {
		return body, false, nil
	}

	rebuiltInput := make([]byte, 0, len(input.Raw))
	rebuiltInput = append(rebuiltInput, '[')
	for i, item := range items {
		if i > 0 {
			rebuiltInput = append(rebuiltInput, ',')
		}
		rebuiltInput = append(rebuiltInput, item...)
	}
	rebuiltInput = append(rebuiltInput, ']')

	sanitized, err := sjson.SetRawBytes(body, "input", rebuiltInput)
	if err != nil {
		return nil, false, fmt.Errorf("replace sanitized input: %w", err)
	}
	return sanitized, true, nil
}
