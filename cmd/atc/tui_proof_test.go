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
