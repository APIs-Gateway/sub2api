package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func defaultOpenAIAdvancedSchedulerWeightUpstreamCost(cfg *config.Config) float64 {
	if cfg == nil {
		return 0
	}
	return cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost
}

func parseOpenAIOAuthSchedulingRateMultiplier(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return defaultOpenAIOAuthSchedulingRateMultiplier
	}
	return value
}

func parseNonNegativeFiniteSchedulerWeight(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, infraerrors.BadRequest(
			"INVALID_OPENAI_ADVANCED_SCHEDULER_WEIGHT",
			"upstream cost weight must be finite and non-negative",
		)
	}
	return value, nil
}

func resolveOpenAIAdvancedSchedulerWeight(raw string, fallback float64) float64 {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := parseNonNegativeFiniteSchedulerWeight(raw)
	if err != nil {
		return fallback
	}
	return value
}

func formatOpenAIAdvancedSchedulerFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
