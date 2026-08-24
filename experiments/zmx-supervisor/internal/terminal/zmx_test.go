package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseListPreservesReachabilityAndIgnoresNoise(t *testing.T) {
	input := strings.Join([]string{
		"→ name=atc-exp-live\tpid=123\tclients=0\tcreated=1",
		"name=atc-exp-down\terr=Timeout\tstatus=unreachable",
		"noise without fields",
		"pid=999\tclients=2",
	}, "\n")
	want := []Session{
		{Name: "atc-exp-down", Reachable: false},
		{Name: "atc-exp-live", Reachable: true, PID: 123},
	}
	got := ParseList(input)
	if len(got) != len(want) {
		t.Fatalf("ParseList() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseList()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestEnvironmentUsesPrivateNamespaceAndScrubsNestedSession(t *testing.T) {
	t.Setenv("ZMX_SESSION", "outer")
	t.Setenv("ZMX_SESSION_PREFIX", "surprise-")
	z := &Zmx{socketDir: "/private/zmx"}
	env := z.env(map[string]string{"ATC_SESSION_ID": "abc"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nZMX_DIR=/private/zmx\n", "\nTERM=xterm-256color\n", "\nATC_SESSION_ID=abc\n"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment missing %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"\nZMX_SESSION=", "\nZMX_SESSION_PREFIX="} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("environment retained %q: %s", unwanted, joined)
		}
	}
}

func TestRealZmxLifecycle(t *testing.T) {
	if os.Getenv("ATC_ZMX_SMOKE") != "1" {
		t.Skip("set ATC_ZMX_SMOKE=1 to exercise the installed zmx")
	}
	socketDir := filepath.Join(t.TempDir(), "z")
	z, err := NewZmx(Config{
		Executable: "zmx", SocketDir: socketDir,
		PollInterval: 100 * time.Millisecond, VerifyPasses: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "atc-exp-real-smoke"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer z.Kill(context.Background(), name)
	if err := z.Create(ctx, CreateOptions{Name: name, CWD: "/tmp", Command: []string{"/bin/sh", "-l"}}); err != nil {
		t.Fatal(err)
	}
	if err := z.Send(ctx, name, []byte("printf 'ATC-ZMX-%s\\n' smoke\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		history, historyErr := z.History(ctx, name)
		if historyErr == nil && strings.Contains(string(history), "ATC-ZMX-smoke") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker not observed; history=%q err=%v", history, historyErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := z.Kill(ctx, name); err != nil {
		t.Fatal(err)
	}
	sessions, err := z.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions after kill = %#v", sessions)
	}
}
