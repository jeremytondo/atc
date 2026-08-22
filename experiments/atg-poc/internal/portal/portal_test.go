package portal

import (
	"reflect"
	"strings"
	"testing"
)

func TestFilterSessions(t *testing.T) {
	sessions := []string{"agent.auth", "terminal.tests", "Agent.docs"}
	got := filterSessions(sessions, "AGENT")
	want := []string{"agent.auth", "Agent.docs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterSessions() = %#v, want %#v", got, want)
	}
}

func TestClampSelection(t *testing.T) {
	tests := []struct {
		selected int
		count    int
		want     int
	}{
		{selected: 2, count: 3, want: 2},
		{selected: 3, count: 3, want: 2},
		{selected: 1, count: 0, want: 0},
		{selected: -1, count: 3, want: 0},
	}
	for _, test := range tests {
		if got := clampSelection(test.selected, test.count); got != test.want {
			t.Errorf("clampSelection(%d, %d) = %d, want %d", test.selected, test.count, got, test.want)
		}
	}
}

func TestZMXEnvironmentsKeepSwitchContextOnly(t *testing.T) {
	a := &App{
		env:    []string{"PATH=/bin", "ZMX_DIR=/ordinary", "ZMX_SESSION=outer"},
		zmxDir: "/private/portal",
	}

	independent := strings.Join(a.zmxEnv(true), "\n")
	if strings.Contains(independent, "ZMX_SESSION=") {
		t.Fatalf("independent client inherited ZMX_SESSION: %q", independent)
	}
	if !strings.Contains(independent, "ZMX_DIR=/private/portal") {
		t.Fatalf("independent client did not use private namespace: %q", independent)
	}

	switching := strings.Join(a.zmxEnv(false), "\n")
	if !strings.Contains(switching, "ZMX_SESSION=outer") {
		t.Fatalf("switch lost the current zmx session: %q", switching)
	}
}

func TestReadKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"\x1b[A", "up"},
		{"\x1b[B", "down"},
		{"\x0e", "ctrl-n"},
		{"\r", "enter"},
		{"x", "x"},
	}
	for _, test := range tests {
		got, err := readKey(strings.NewReader(test.input))
		if err != nil {
			t.Fatalf("readKey(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("readKey(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
