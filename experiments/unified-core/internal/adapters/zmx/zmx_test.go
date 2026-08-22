package zmx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/provider"
)

func TestProviderCommandsPinCheapModels(t *testing.T) {
	adapter := &Adapter{
		wrapperExecutable: "/tmp/atc-unified", hookBaseURL: "http://127.0.0.1:1",
		codexRemote: "ws://127.0.0.1:2",
		models: map[domain.Agent]string{
			domain.AgentClaude: provider.ClaudeCheapModel,
			domain.AgentCodex:  provider.CodexCheapModel,
		},
		efforts: map[domain.Agent]string{
			domain.AgentClaude: provider.CheapEffort,
			domain.AgentCodex:  provider.CheapEffort,
		},
	}
	for _, agent := range []domain.Agent{domain.AgentClaude, domain.AgentCodex} {
		command, err := adapter.providerCommand(ports.TerminalOpen{
			TerminalID: "term_test", SessionName: "atcu-test", Agent: agent, CWD: "/tmp",
		})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, adapter.models[agent]) || !strings.Contains(joined, provider.CheapEffort) {
			t.Fatalf("%s command does not pin model and effort: %q", agent, joined)
		}
	}
}

func TestParseInventoryFiltersPrivateNamespaceAndPreservesUnreachable(t *testing.T) {
	entries := ParseInventory("name=atcu-live\tpid=42\n" +
		"name=user-session\tpid=99\n" +
		"→ name=atc-unified-down\tpid=43\terr=connection refused\n")
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Name != "atc-unified-down" || entries[0].Reachable {
		t.Fatalf("unreachable entry = %#v", entries[0])
	}
	if entries[1].Name != "atcu-live" || !entries[1].Reachable || entries[1].DaemonPID != 42 {
		t.Fatalf("live entry = %#v", entries[1])
	}
}

func TestAttachRejectsAutoCreatedReplacementAndVerifiedKill(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "inventory")
	scriptPath := filepath.Join(dir, "fake-zmx")
	script := `#!/bin/sh
set -eu
state="` + statePath + `"
case "$1" in
  list)
    if [ -f "$state" ]; then
      cat "$state"
    fi
    ;;
  attach)
    printf 'name=%s\tpid=202\n' "$2" > "$state"
    ;;
  kill)
    : > "$state"
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("name=atcu-terminal\tpid=101\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{
		Executable: scriptPath, WrapperExecutable: scriptPath,
		SocketDir: filepath.Join(dir, "sockets"), LogDir: filepath.Join(dir, "logs"),
		PollInterval: time.Millisecond, VerifyPasses: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Attach(context.Background(), "atcu-terminal", strings.NewReader(""), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "refused auto-created replacement") {
		t.Fatalf("attach error = %v", err)
	}
	contents, readErr := os.ReadFile(statePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(contents) != 0 {
		t.Fatalf("phantom replacement survived: %q", contents)
	}

	if err := os.WriteFile(statePath, []byte("name=atcu-terminal\tpid=303\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Terminate(context.Background(), "atcu-terminal"); err != nil {
		t.Fatal(err)
	}
	entries, err := adapter.Inventory(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("post-kill inventory = %#v, %v", entries, err)
	}
	if err := adapter.Terminate(context.Background(), "user-session"); err == nil {
		t.Fatal("terminated outside private namespace")
	}
}
