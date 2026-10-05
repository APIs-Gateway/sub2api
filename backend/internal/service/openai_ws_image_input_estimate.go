package service

import (
	"sync"

	"github.com/tidwall/gjson"
)

// Responses WS continues input through previous_response_id, not through the
// mere existence of a socket. Retain only bounded numeric admission metadata,
// never image URLs, file contents or the provider's encrypted output.
type openAIWSImageInputEstimates struct {
	mu         sync.Mutex
	byResponse map[string]int
	order      []string
	pending    map[int]int
}

const openAIWSImageInputEstimateLimit = 128

func openAIWSInputMayContainImage(input gjson.Result) bool {
	return openAIWSInputMayContainImageAtDepth(input, 0)
}

func openAIWSInputMayContainImageAtDepth(input gjson.Result, depth int) bool {
	if depth >= 4 {
		return input.IsObject() || input.IsArray()
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if openAIWSInputMayContainImageAtDepth(item, depth+1) {
				return true
			}
		}
		return false
	}
	if !input.IsObject() {
		return false
	}
	switch input.Get("type").String() {
	case "input_image", "image_url", "image", "input_file", "item_reference", "compaction":
		// Stored files/items and compacted opaque state can contain images.
		return true
	}
	return openAIWSInputMayContainImageAtDepth(input.Get("content"), depth+1)
}

func (s *openAIWSImageInputEstimates) prepare(turn int, body []byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	budget := (len(body) + 3) / 4
	if budget < 1 {
		budget = 1
	}
	previous := gjson.GetBytes(body, "previous_response_id").String()
	inherited := 0
	potential := openAIWSInputMayContainImage(gjson.GetBytes(body, "input"))
	if previous != "" {
		var known bool
		inherited, known = s.byResponse[previous]
		// An uncached ID may hydrate persisted input upstream. A cache miss
		// must not turn a model with paid image tokens into known-free text.
		potential = potential || !known || inherited > 0
	}
	conversation := gjson.GetBytes(body, "conversation")
	if conversation.Exists() && conversation.Type != gjson.Null && conversation.Raw != `""` {
		potential = true
	}
	estimate := 0
	if potential {
		estimate = inherited + budget
	}
	if s.pending == nil {
		s.pending = make(map[int]int)
	}
	_, alreadyPending := s.pending[turn]
	if alreadyPending || len(s.pending) < openAIWSImageInputEstimateLimit {
		s.pending[turn] = estimate
	}
	return estimate
}

func (s *openAIWSImageInputEstimates) complete(turn int, responseID string, actualImageInput int) {
	if responseID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byResponse == nil {
		s.byResponse = make(map[string]int)
	}
	estimate, known := s.pending[turn]
	delete(s.pending, turn)
	if !known {
		// Missing turn metadata cannot prove a stored response has no image.
		estimate = 1
	}
	if actualImageInput > estimate {
		estimate = actualImageInput
	}
	if s.byResponse[responseID] > estimate {
		estimate = s.byResponse[responseID]
	}
	if _, exists := s.byResponse[responseID]; !exists {
		if len(s.order) == openAIWSImageInputEstimateLimit {
			delete(s.byResponse, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, responseID)
	}
	s.byResponse[responseID] = estimate
}

func beforeOpenAIPassthroughUpstreamTurn(hooks *OpenAIWSIngressHooks, estimates *openAIWSImageInputEstimates, turn int, body []byte, model string) error {
	imageInput := estimates.prepare(turn, body)
	if hooks == nil {
		return nil
	}
	if hooks.BeforePassthroughUpstreamTurn != nil {
		return hooks.BeforePassthroughUpstreamTurn(turn, body, model, imageInput)
	}
	if hooks.BeforeUpstreamTurn != nil {
		return hooks.BeforeUpstreamTurn(turn, body, model)
	}
	return nil
}
