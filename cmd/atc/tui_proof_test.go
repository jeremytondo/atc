package main

import (
	"strings"
	"testing"
)

func TestTUIProofRequiresInteractiveTerminal(t *testing.T) {
	_, _, err := runCLI(t, "__tui-proof")
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("__tui-proof without TTY = %v", err)
	}
}

func TestBootstrapHostMatchesReachableListener(t *testing.T) {
	for name, tc := range map[string]struct{ bind, want string }{
		"ipv4 loopback": {"127.0.0.1", "127.0.0.1"},
		"ipv6 loopback": {"::1", "::1"},
		"wildcard v4":   {"0.0.0.0", "127.0.0.1"},
		"wildcard v6":   {"::", "127.0.0.1"},
		"hostname":      {"localhost", "localhost"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := bootstrapHost(tc.bind); got != tc.want {
				t.Errorf("bootstrapHost(%q) = %q, want %q", tc.bind, got, tc.want)
			}
		})
	}
}

func TestProofAndRemoteProtocolStayHidden(t *testing.T) {
	stdout, _, err := runCLI(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"__tui-proof", "__remote"} {
		if strings.Contains(stdout, hidden) {
			t.Errorf("root help exposes %s:\n%s", hidden, stdout)
		}
	}
}
