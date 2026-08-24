package child

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunRecordsAuthoritativeExitEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exits", "terminal.json")
	err := Run(context.Background(), path, "terminal", []string{"sh", "-c", "exit 7"})
	if err == nil {
		t.Fatal("child exit unexpectedly succeeded")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var marker Marker
	if err := json.Unmarshal(contents, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Version != 1 || marker.TerminalID != "terminal" || marker.ExitedAt == nil || marker.ExitCode == nil || *marker.ExitCode != 7 {
		t.Fatalf("marker = %#v", marker)
	}
}
