package openai

import (
	"strings"
	"testing"
)

func TestPairCodexClientIdentity(t *testing.T) {
	tests := []struct {
		name           string
		userAgent      string
		wantOriginator string
		wantUserAgent  string
		wantOK         bool
	}{
		{name: "CLI", userAgent: "codex_cli_rs/0.144.1", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/0.144.1", wantOK: true},
		{name: "TUI", userAgent: "codex-tui/0.144.1 (Mac OS X) (codex-tui; 0.144.1)", wantOriginator: "codex-tui", wantUserAgent: "codex-tui/0.144.1 (Mac OS X) (codex-tui; 0.144.1)", wantOK: true},
		{name: "Codex desktop", userAgent: "Codex Desktop/1.2.3", wantOriginator: "Codex Desktop", wantUserAgent: "Codex Desktop/1.2.3", wantOK: true},
		{name: "trailer restores overridden identity", userAgent: "cccc/0.142.0 (Linux) (codex-tui; 0.142.0)", wantOriginator: "codex-tui", wantUserAgent: "codex-tui/0.142.0 (Linux) (codex-tui; 0.142.0)", wantOK: true},
		{name: "canonicalizes exact originator", userAgent: "CODEX_CLI_RS/1.0.0", wantOriginator: "codex_cli_rs", wantUserAgent: "codex_cli_rs/1.0.0", wantOK: true},
		{name: "third party rejected", userAgent: "curl/8.0", wantOK: false},
		{name: "missing slash rejected", userAgent: "codex_cli_rs", wantOK: false},
		{name: "unsafe trailer rejected", userAgent: "cccc/1.0 (Codex Desktop/2; 1.0)", wantOK: false},
		{name: "control byte rejected", userAgent: "Codex \x01evil/1.0", wantOK: false},
		{name: "oversized family rejected", userAgent: "Codex " + strings.Repeat("a", 80) + "/1.0", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originator, userAgent, ok := PairCodexClientIdentity(tt.userAgent)
			if ok != tt.wantOK || originator != tt.wantOriginator || userAgent != tt.wantUserAgent {
				t.Fatalf("PairCodexClientIdentity(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.userAgent, originator, userAgent, ok, tt.wantOriginator, tt.wantUserAgent, tt.wantOK)
			}
		})
	}
}
