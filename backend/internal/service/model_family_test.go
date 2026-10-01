//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelFamily_Hits(t *testing.T) {
	cases := map[string]string{
		"gpt-6-sol":                     "gpt-6-sol",
		"GPT-6-Sol":                     "gpt-6-sol",
		"gpt-6-sol-high":                "gpt-6-sol",
		"gpt-6-sol-openai-compact":      "gpt-6-sol",
		"gpt-6-luna":                    "gpt-6-luna",
		"gpt-6.1-sol":                   "gpt-6.1-sol",
		"gpt-6.1-sol-xhigh":             "gpt-6.1-sol",
		"gpt-6":                         "gpt-6-astra",
		"gpt-6-astra":                   "gpt-6-astra",
		"openai/gpt-6-astra":            "gpt-6-astra",
		"gpt-5.6":                       "gpt-5.6-sol",
		"gpt-5.6-sol":                   "gpt-5.6-sol",
		"gpt-5.6-terra-high":            "gpt-5.6-terra",
		"gpt-5.6-luna":                  "gpt-5.6-luna",
		"gpt-5.5":                       "gpt-5.5",
		"gpt-5.5-pro":                   "gpt-5.5-pro",
		"gpt-5.5-2026-03-01":            "gpt-5.5",
		"gpt-5.4-mini":                  "gpt-5.4-mini",
		"gpt-5.4":                       "gpt-5.4",
		"gpt-5.3-codex-spark":           "gpt-5.3-codex-spark",
		"gpt-5.3-codex":                 "gpt-5.3-codex",
		"gpt-image-2":                   "gpt-image-2",
		"claude-opus-4-8":               "claude-opus-4-8",
		"claude-opus-4.8":               "claude-opus-4-8",
		"claude-opus-4-8-20260101":      "claude-opus-4-8",
		"claude-opus-4-8[1m]":           "claude-opus-4-8",
		"claude-opus-4-8-thinking":      "claude-opus-4-8",
		"claude-opus-4-20250514":        "claude-opus-4",
		"claude-opus-5":                 "claude-opus-5",
		"claude-opus-5-5":               "claude-opus-5-5",
		"claude-opus-5.5":               "claude-opus-5-5",
		"claude-sonnet-4-5":             "claude-sonnet-4-5",
		"claude-3-5-sonnet-20241022":    "claude-sonnet-3-5",
		"claude-3-haiku-20240307":       "claude-haiku-3",
		"anthropic.claude-sonnet-4-5":   "claude-sonnet-4-5",
		"claude-fable-5":                "claude-fable-5",
		"claude-fable-5-20260801":       "claude-fable-5",
		"gemini-2.5-pro":                "gemini-2.5-pro",
		"gemini-2.5-flash-lite":         "gemini-2.5-flash-lite",
		"models/gemini-3-flash-preview": "gemini-3-flash",
	}
	for model, want := range cases {
		require.Equal(t, want, ModelFamily(model), model)
	}
}

func TestModelFamily_MissesDoNotEnterBreaker(t *testing.T) {
	for _, model := range []string{
		"",
		"   ",
		"gpt-5.x-随便写",
		"gpt-5.9-whatever",
		"gpt-5",
		"gpt-5.5-nonsense",
		"gpt-6-solitude",
		"gpt-6.1-sol-bogus",
		"codex",
		"my-codex-model",
		"gpt-4o",
		"claude-opus",
		"claude-opus-4-8-bogus",
		"claude-nonsense-4",
		"claude-fable-5-xyz",
		"gemini-2.5-ultra",
		"deepseek-v4",
		"totally-unknown",
	} {
		require.Empty(t, ModelFamily(model), "%q must not map to a family", model)
	}
}
