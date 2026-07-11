package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const codexUpstreamMinVersion = "0.144.0"

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
