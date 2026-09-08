package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHostsFollowsIncludesAndSkipsPatterns(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config", `# comment
Include conf.d/*.conf
Host workstation
  HostName ws.example
host=laptop other
Host *.example !bastion
Host tablet?
Host workstation
`)
	if err := os.Mkdir(filepath.Join(dir, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("conf.d", "work.conf"), "Host build\nInclude "+filepath.Join(dir, "config")+"\n")
	write(filepath.Join("conf.d", "ignored.txt"), "Host nope\n")

	want := []string{"build", "workstation", "laptop", "other"}
	if diff := cmp.Diff(want, Hosts(filepath.Join(dir, "config"))); diff != "" {
		t.Errorf("Hosts (-want +got):\n%s", diff)
	}
	if got := Hosts(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("missing config = %v, want nil", got)
	}
}
