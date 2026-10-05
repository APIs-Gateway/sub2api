package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// Gemini sends cumulative snapshots. Keep the latest metered snapshot rather
// than summing it or allowing an empty terminal/error frame to erase it.
func antigravityRetainMeteredUsage(previous, next *ClaudeUsage) *ClaudeUsage {
	if next.hasObservedTokens() {
		return next
	}
	if previous != nil {
		return previous
	}
	return &ClaudeUsage{}
}

func (s *AntigravityGatewayService) antigravityObserveUsage(previous *ClaudeUsage, line string) *ClaudeUsage {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return previous
	}
	inner, err := s.unwrapV1InternalResponse([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
	if err != nil {
		return previous
	}
	return antigravityRetainMeteredUsage(previous, extractGeminiUsage(inner))
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
