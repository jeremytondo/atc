package exitmarker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	exited := started.Add(time.Second)
	code := 3
	marker := Marker{TerminalID: "term-x7k2f", PID: 42, StartedAt: started, ExitedAt: &exited, Code: &code}
	if err := Write(Path(dir, "term-x7k2f"), marker); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, "term-x7k2f")
	if err != nil {
		t.Fatal(err)
	}
	marker.Version = Version
	if diff := cmp.Diff(&marker, got); diff != "" {
		t.Errorf("marker (-want +got):\n%s", diff)
	}
	if !got.Exited() {
		t.Error("Exited() = false for a marker with ExitedAt set")
	}
}

func TestReadAbsentIsNilNotError(t *testing.T) {
	got, err := Read(t.TempDir(), "term-x7k2f")
	if err != nil || got != nil {
		t.Errorf("Read(absent) = %v, %v; want nil, nil", got, err)
	}
}

func TestStartMarkerIsNotEvidence(t *testing.T) {
	dir := t.TempDir()
	if err := Write(Path(dir, "term-x7k2f"), Marker{TerminalID: "term-x7k2f", PID: 42, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, "term-x7k2f")
	if err != nil {
		t.Fatal(err)
	}
	if got.Exited() {
		t.Error("start-time marker must not count as exit evidence")
	}
}

func TestRejectsForeignAndMalformedMarkers(t *testing.T) {
	dir := t.TempDir()
	// A marker naming a different terminal is never adopted.
	if err := Write(Path(dir, "term-aaaaa"), Marker{TerminalID: "term-other", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, "term-aaaaa"); err == nil {
		t.Error("marker for another terminal must error")
	}
	// Garbage bytes are an error, not evidence.
	if err := os.WriteFile(filepath.Join(dir, "term-bbbbb.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, "term-bbbbb"); err == nil {
		t.Error("malformed marker must error")
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Write(Path(dir, "term-x7k2f"), Marker{TerminalID: "term-x7k2f", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "term-x7k2f"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "term-x7k2f"); err != nil {
		t.Errorf("second Remove = %v, want nil", err)
	}
}
