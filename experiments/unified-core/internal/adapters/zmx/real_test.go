package zmx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
)

func TestRealZmx(t *testing.T) {
	if os.Getenv("ATC_UNIFIED_ZMX_SMOKE") != "1" {
		t.Skip("set ATC_UNIFIED_ZMX_SMOKE=1 with ATC_UNIFIED_BINARY to use real zmx")
	}
	binary := os.Getenv("ATC_UNIFIED_BINARY")
	if binary == "" {
		t.Fatal("ATC_UNIFIED_BINARY is required")
	}
	dir := t.TempDir()
	adapter, err := New(Config{
		Executable: "zmx", WrapperExecutable: binary,
		SocketDir: filepath.Join(dir, "zmx"), LogDir: filepath.Join(dir, "logs"),
		PollInterval: 50 * time.Millisecond, VerifyPasses: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	marker := filepath.Join(dir, "exits", "smoke.json")
	err = adapter.Open(ctx, ports.TerminalOpen{
		TerminalID: "term_smoke", SessionName: "atcu-smoke", Agent: domain.AgentClaude, CWD: dir,
		Command: []string{"sh", "-c", "trap 'exit 0' HUP TERM INT; while :; do sleep 60; done"}, ExitPath: marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := adapter.Inventory(ctx)
	if err != nil || len(entries) != 1 || !entries[0].Reachable {
		t.Fatalf("running inventory = %#v, %v", entries, err)
	}
	if err := adapter.Terminate(ctx, "atcu-smoke"); err != nil {
		t.Fatal(err)
	}
	if entries, err := adapter.Inventory(ctx); err != nil || len(entries) != 0 {
		t.Fatalf("post-kill inventory = %#v, %v", entries, err)
	}
}
