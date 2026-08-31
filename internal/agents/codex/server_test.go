package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startServer runs the codex on PATH as a detached app-server listening
// on the well-known socket of the given home, output to the control
// directory's log file.
func TestStartServerSpawnsDetachedAppServer(t *testing.T) {
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ATC_TEST_OUT\"\nprintf '%s\\n' \"$CODEX_HOME\" >> \"$ATC_TEST_OUT\"\n" +
		"pwd >> \"$ATC_TEST_OUT\"\necho started >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ATC_TEST_OUT", out)
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, ".codex")

	if err := startServer(codexHome); err != nil {
		t.Fatal(err)
	}
	var content []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if content, _ = os.ReadFile(out); strings.Count(string(content), "\n") >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// pwd reports the physical directory; on macOS the temp root is a
	// symlink into /private.
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	want := "app-server\n--listen\nunix://\n" + codexHome + "\n" + physicalHome + "\n"
	if string(content) != want {
		t.Errorf("spawned codex saw:\n%s\nwant:\n%s", content, want)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if log, err := os.ReadFile(serverLogPath(codexHome)); err == nil && strings.Contains(string(log), "started") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("server output never reached the control directory's log file")
}

func TestStartServerRequiresCodexOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := startServer(t.TempDir()); err == nil {
		t.Fatal("startServer succeeded with no codex on PATH")
	}
}
