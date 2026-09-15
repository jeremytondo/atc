package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSavedConnectionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc", "connections.json")
	got, err := LoadSaved(path)
	if err != nil || got != nil {
		t.Fatalf("missing file = %v, %v; want an empty list", got, err)
	}
	if err := StoreSaved(path, []string{"ws", "devbox"}); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadSaved(path); err != nil || !cmp.Equal(got, []string{"ws", "devbox"}) {
		t.Errorf("after store = %v, %v", got, err)
	}
	if err := StoreSaved(path, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\n  \"remotes\": []\n}\n" {
		t.Errorf("empty store wrote %q, %v", data, err)
	}
	if got, err = LoadSaved(path); err != nil || len(got) != 0 {
		t.Errorf("after clearing = %v, %v", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	if err := os.WriteFile(path, []byte(`{"remotes":["ws","","ws","Local","devbox"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadSaved(path); err != nil || !cmp.Equal(got, []string{"ws", "devbox"}) {
		t.Errorf("hand-edited file = %v, %v; want duplicates, blanks, and Local dropped", got, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSaved(path); err == nil {
		t.Error("a corrupt file loaded")
	}
}
