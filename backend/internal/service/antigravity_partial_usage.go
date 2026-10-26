package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Keep native cumulative counters separately: prompt includes cache reads,
// while candidate and thought output counters have independent presence. A
// sparse/zero later frame cannot erase provider usage already observed. A newer
// positive counter replaces its prior value; snapshots are never summed/maxed.
type antigravityUsageCollector struct {
	prompt, candidates, cached, thoughts, image int
	usage                                       ClaudeUsage
}

func (u *antigravityUsageCollector) observe(data []byte) *ClaudeUsage {
	metadata := gjson.GetBytes(data, "usageMetadata")
	update := func(name string, target *int) {
		if field := metadata.Get(name); field.Exists() && field.Int() > 0 {
			*target = int(field.Int())
		}
	}
	update("promptTokenCount", &u.prompt)
	update("candidatesTokenCount", &u.candidates)
	update("cachedContentTokenCount", &u.cached)
	update("thoughtsTokenCount", &u.thoughts)
	metadata.Get("candidatesTokensDetails").ForEach(func(_, detail gjson.Result) bool {
		if detail.Get("modality").String() == "IMAGE" && detail.Get("tokenCount").Int() > 0 {
			u.image = int(detail.Get("tokenCount").Int())
			return false
		}
		return true
	})
	u.usage.InputTokens = max(0, u.prompt-u.cached)
	u.usage.OutputTokens = u.candidates + u.thoughts
	u.usage.CacheReadInputTokens = u.cached
	u.usage.ImageOutputTokens = u.image
	return &u.usage
}

func (s *AntigravityGatewayService) antigravityObserveUsage(collector *antigravityUsageCollector, line string) *ClaudeUsage {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return &collector.usage
	}
	inner, err := s.unwrapV1InternalResponse([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
	if err != nil {
		return &collector.usage
	}
	return collector.observe(inner)
}

// A metered failed attempt must be settled once instead of entering replay.
// Transport safety and absence of client output never prove zero provider cost.
func antigravityInterruptedUsage(usage *ClaudeUsage, firstTokenMs *int, disconnected bool, err error) (*antigravityStreamResult, error) {
	return antigravityInterruptedImageUsage(usage, 0, firstTokenMs, disconnected, err)
}

func antigravityInterruptedImageUsage(usage *ClaudeUsage, imageCount int, firstTokenMs *int, disconnected bool, err error) (*antigravityStreamResult, error) {
	if !usage.hasObservedTokens() && imageCount == 0 {
		return nil, err
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		err = fmt.Errorf("metered upstream completion interrupted: status %d", failover.StatusCode)
	}
	return &antigravityStreamResult{usage: usage, imageCount: imageCount, firstTokenMs: firstTokenMs, clientDisconnect: disconnected}, err
}

func (s *AntigravityGatewayService) antigravityLineHasProviderError(c *gin.Context, line string, stream bool) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	inner, err := s.unwrapV1InternalResponse([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
	if err != nil {
		return false
	}
	_, status, ok := antigravitySafeGeminiError(inner)
	if ok {
		s.recordAntigravityGeminiClientError(c, status, inner, stream)
	}
	return ok
}

func (s *AntigravityGatewayService) antigravityLineHasTerminal(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "[DONE]" {
		return true
	}
	inner, err := s.unwrapV1InternalResponse([]byte(payload))
	if err != nil {
		return false
	}
	var parsed map[string]any
	if json.Unmarshal(inner, &parsed) != nil {
		return false
	}
	return extractGeminiFinishReason(parsed) != ""
}

func (r *antigravityStreamResult) hasBillableUsage() bool {
	return r != nil && (r.usage.hasObservedTokens() || r.imageCount > 0)
}

func (s *AntigravityGatewayService) antigravityLineHasImage(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	inner, err := s.unwrapV1InternalResponse([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
	if err != nil {
		return false
	}
	var parsed map[string]any
	if json.Unmarshal(inner, &parsed) != nil {
		return false
	}
	for _, part := range extractGeminiParts(parsed) {
		inline, ok := part["inlineData"].(map[string]any)
		if !ok {
			continue
		}
		data, _ := inline["data"].(string)
		mime, _ := inline["mimeType"].(string)
		if strings.TrimSpace(data) != "" && strings.HasPrefix(strings.ToLower(mime), "image/") {
			return true
		}
	}
	return false
}
