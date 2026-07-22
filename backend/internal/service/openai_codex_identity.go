package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

const codexUpstreamMinVersion = "0.144.0"

// ensureCodexIdentityHeaders fills the identity headers required by synthetic
// Codex probes while preserving any caller-provided official identity.
func ensureCodexIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	if strings.TrimSpace(headers.Get("user-agent")) == "" {
		headers.Set("user-agent", codexCLIUserAgent)
	}
	if strings.TrimSpace(headers.Get("originator")) == "" {
		headers.Set("originator", "codex_cli_rs")
	}
	if strings.TrimSpace(headers.Get("version")) == "" {
		headers.Set("version", codexCLIVersion)
	}
	headers.Set("OpenAI-Beta", "responses=experimental")
}

// applyOpenAICodexProbeHeaders adds the minimum Codex fingerprint to a
// generated probe without changing the identity of a real OAuth request.
func applyOpenAICodexProbeHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	ensureCodexIdentityHeaders(headers)
	headers.Set("X-Codex-Window-ID", uuid.NewString())
}

// enforceCodexIdentityHeaders ensures OAuth requests use an official Codex
// identity whose originator matches the final User-Agent client name.
func enforceCodexIdentityHeaders(headers http.Header) {
	if headers == nil || headers.Get("originator") == "" {
		return
	}
	originator, pairedUA, ok := openai.PairCodexClientIdentity(headers.Get("user-agent"))
	if !ok {
		originator, pairedUA = "codex_cli_rs", codexCLIUserAgent
	}
	headers.Set("originator", originator)
	headers.Set("user-agent", pairedUA)
	if version := strings.TrimSpace(headers.Get("version")); version != "" && CompareVersions(version, codexUpstreamMinVersion) < 0 {
		headers.Set("version", codexCLIVersion)
	}
}
