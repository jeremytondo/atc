package main

import (
	"regexp"
	"strings"
	"testing"
)

// The integration reads: list shows every compiled-in Integration with
// its apps and evidence-based availability; get shows one with its apps,
// agents, and (for a connection-backed one) its connection.
func TestIntegrationListAndGetCLI(t *testing.T) {
	startTestServer(t)

	stdout, _, err := runCLI(t, "integration", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{
		"ID\tNAME\tAVAILABLE\tAPPS\tAGENTS",
		"claude\tClaude Code\tyes\tclaude/tui\tclaude",
		"codex\tCodex\tno\tcodex/tui\tcodex",
		"t3code\tT3 Code\tno\tt3code/web, t3code/desktop\tclaudeAgent, codex, cursor, grok, opencode",
		"zmx\tzmx\tyes\t\t",
	} {
		if !containsRow(stdout, want) {
			t.Errorf("list missing row %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCLI(t, "integration", "get", "t3code")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, pattern := range []string{
		`(?m)^id\s+t3code$`,
		`(?m)^available\s+no$`,
		`(?m)^connection\s+unavailable \(`,
		`(?m)^capabilities\s+thread_observation$`,
		`(?m)^agent\s+claudeAgent \(Claude Code\)$`,
		`(?m)^app\s+t3code/web \(T3 Code \(web\)\): handoff$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(stdout) {
			t.Errorf("get output does not match %s:\n%s", pattern, stdout)
		}
	}
	stdout, _, err = runCLI(t, "integration", "get", "codex")
	if err != nil || !regexp.MustCompile(`(?m)^app\s+codex/tui \(Codex\): terminal_start, terminal_resume; available no$`).MatchString(stdout) ||
		!regexp.MustCompile(`(?m)^install\s+npm install -g @openai/codex$`).MatchString(stdout) {
		t.Errorf("get codex = %q, %v", stdout, err)
	}

	if _, _, err := runCLI(t, "integration", "get", "nonexistent"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
	if _, _, err := runCLI(t, "agent", "list"); err == nil {
		t.Error("the agent command family still exists")
	}
}

// containsRow reports whether a tab-separated row appears in tabwriter
// output, whose columns are padded with spaces.
func containsRow(output, row string) bool {
	cells := strings.Split(row, "\t")
	for _, line := range strings.Split(output, "\n") {
		fields := regexp.MustCompile(`\s{2,}`).Split(strings.TrimRight(line, " "), -1)
		if len(fields) < len(cells) {
			// Trailing empty cells are trimmed by tabwriter.
			for len(fields) < len(cells) {
				fields = append(fields, "")
			}
		}
		match := true
		for i, cell := range cells {
			if fields[i] != cell {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
