package zmx

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// capture records the arguments of a single run invocation.
type capture struct {
	bin        string
	dir        string
	env        []string
	stdin      []byte
	args       []string
	attachName string
	attachRows uint16
	attachCols uint16
}

// stubWrapper returns a Wrapper whose run records calls into got and returns
// the given stdout.
func stubWrapper(t *testing.T, bin string, stdout []byte) (*Wrapper, *capture) {
	t.Helper()
	got := &capture{}
	w := &Wrapper{
		bin: bin,
		run: func(_ context.Context, b string, opts runOptions, args ...string) ([]byte, error) {
			got.bin = b
			got.dir = opts.dir
			got.env = append([]string(nil), opts.env...)
			got.stdin = opts.stdin
			got.args = args
			return stdout, nil
		},
		attach: func(_ context.Context, b, name string, rows, cols uint16) (PTY, error) {
			got.bin = b
			got.attachName = name
			got.attachRows = rows
			got.attachCols = cols
			return nil, nil
		},
	}
	return w, got
}

func TestNewDefaultsBinToZmx(t *testing.T) {
	if w := New(""); w.bin != DefaultBin {
		t.Fatalf("bin = %q, want %q", w.bin, DefaultBin)
	}
	if w := New("/opt/zmx"); w.bin != "/opt/zmx" {
		t.Fatalf("bin = %q, want /opt/zmx", w.bin)
	}
}

func TestStartBuildsRunCommand(t *testing.T) {
	w, got := stubWrapper(t, "zmx", nil)

	if err := w.Start(context.Background(), "atc:item:DEV-22", "/work/dir", []string{"/usr/bin/zsh", "-l", "-i", "-c", "claude --foo"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	wantArgs := []string{"run", "atc:item:DEV-22", "-d", "/usr/bin/zsh", "-l", "-i", "-c", "claude --foo"}
	if !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("args = %v, want %v", got.args, wantArgs)
	}
	if got.dir != "/work/dir" {
		t.Fatalf("dir = %q, want /work/dir", got.dir)
	}
	if got.stdin != nil {
		t.Fatalf("stdin = %q, want nil", got.stdin)
	}
	assertEnvValue(t, got.env, "TERM", defaultSessionTermType)
}

func TestSendPipesPayloadToStdin(t *testing.T) {
	w, got := stubWrapper(t, "zmx", nil)

	if err := w.Send(context.Background(), "atc:free:abc", []byte("hello")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	wantArgs := []string{"send", "atc:free:abc"}
	if !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("args = %v, want %v", got.args, wantArgs)
	}
	if string(got.stdin) != "hello" {
		t.Fatalf("stdin = %q, want hello", got.stdin)
	}
	if got.env != nil {
		t.Fatalf("env = %v, want nil", got.env)
	}
}

func TestAttachPassesInitialSize(t *testing.T) {
	w, got := stubWrapper(t, "/opt/zmx", nil)

	if _, err := w.Attach(context.Background(), "atc-aabbcc", 43, 132); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if got.bin != "/opt/zmx" || got.attachName != "atc-aabbcc" {
		t.Fatalf("attach target = %q %q, want /opt/zmx atc-aabbcc", got.bin, got.attachName)
	}
	if got.attachRows != 43 || got.attachCols != 132 {
		t.Fatalf("attach size = %dx%d, want 43x132", got.attachRows, got.attachCols)
	}
}

func TestTerminateBuildsKillCommand(t *testing.T) {
	w, got := stubWrapper(t, "zmx", nil)

	if err := w.Terminate(context.Background(), "atc-aabbcc"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	wantArgs := []string{"kill", "atc-aabbcc"}
	if !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("args = %v, want %v", got.args, wantArgs)
	}
	if got.env != nil {
		t.Fatalf("env = %v, want nil", got.env)
	}
}

func TestSessionRunEnvSetsStableTerminalType(t *testing.T) {
	got := setEnv([]string{"PATH=/usr/bin", "TERM=dumb", "SHELL=/usr/bin/zsh"}, "TERM", defaultSessionTermType)

	assertEnvValue(t, got, "PATH", "/usr/bin")
	assertEnvValue(t, got, "SHELL", "/usr/bin/zsh")
	assertEnvValue(t, got, "TERM", defaultSessionTermType)
	assertEnvCount(t, got, "TERM", 1)
}

func TestCommandsDoNotInheritParentZmxSession(t *testing.T) {
	t.Setenv(zmxSessionEnv, "parent-session")
	bin, err := filepath.Abs(filepath.Join("testdata", "zmx-env-guard.sh"))
	if err != nil {
		t.Fatalf("resolve fake zmx path: %v", err)
	}
	w := New(bin)

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "run",
			run: func(ctx context.Context) error {
				return w.Start(ctx, "atc-test", t.TempDir(), []string{"/bin/sh"})
			},
		},
		{
			name: "send",
			run: func(ctx context.Context) error {
				return w.Send(ctx, "atc-test", []byte("hello"))
			},
		},
		{
			name: "list",
			run: func(ctx context.Context) error {
				_, err := w.List(ctx)
				return err
			},
		},
		{
			name: "terminate",
			run: func(ctx context.Context) error {
				return w.Terminate(ctx, "atc-test")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(context.Background()); err != nil {
				t.Fatalf("%s inherited parent %s: %v", tt.name, zmxSessionEnv, err)
			}
		})
	}

	t.Run("attach", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		session, err := w.Attach(ctx, "atc-test", 24, 80)
		if err != nil {
			t.Fatalf("Attach: %v", err)
		}
		defer session.Close()

		buf := make([]byte, 256)
		n, err := session.Read(buf)
		if err != nil {
			t.Fatalf("read attach output: %v", err)
		}
		if got := string(buf[:n]); !strings.Contains(got, "attached without parent ZMX_SESSION") {
			t.Fatalf("attach output = %q", got)
		}
	})
}

func TestNameForIDIsDeterministicAndOpaque(t *testing.T) {
	first := NameForID("ses_alpha")
	second := NameForID("ses_alpha")
	other := NameForID("ses_beta")

	if first != second {
		t.Fatalf("NameForID not deterministic: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("different ids produced same name %q", first)
	}
	if !strings.HasPrefix(first, "atc-") || len(first) != len("atc-")+32 {
		t.Fatalf("name = %q, want atc- plus 32 hex chars", first)
	}
	if strings.Contains(first, "ses_alpha") {
		t.Fatalf("name leaked id: %q", first)
	}
	for _, c := range first[len("atc-"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("name %q has non-hex char %q", first, c)
		}
	}
}

func TestListParsesSessions(t *testing.T) {
	out := "→ name=atc:item:DEV-22\tpid=123\tclients=1\tcreated=1781982140\tstart_dir=/home/u/p\tcmd=/usr/bin/zsh -l -i -c claude\n" +
		"  name=atc:free:abc\tpid=456\tcreated=1781982999\tstart_dir=/tmp\tcmd=htop\n"
	w, _ := stubWrapper(t, "zmx", []byte(out))

	sessions, err := w.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(sessions), sessions)
	}

	first := sessions[0]
	if first.Name != "atc:item:DEV-22" || first.PID != 123 || first.Created != 1781982140 {
		t.Fatalf("first = %+v", first)
	}
	if first.StartDir != "/home/u/p" || first.Cmd != "/usr/bin/zsh -l -i -c claude" {
		t.Fatalf("first cmd/dir = %+v", first)
	}
	if sessions[1].Name != "atc:free:abc" || sessions[1].Cmd != "htop" {
		t.Fatalf("second = %+v", sessions[1])
	}
}

func TestListToleratesMalformedLines(t *testing.T) {
	out := "garbage line without fields\n" +
		"\n" +
		"→ name=atc:item:DEV-1\tpid=7\n" +
		"pid=99\tclients=2\n" // no name: skipped
	w, _ := stubWrapper(t, "zmx", []byte(out))

	sessions, err := w.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "atc:item:DEV-1" {
		t.Fatalf("sessions = %+v, want only DEV-1", sessions)
	}
}

func TestExecRunReportsMissingBinary(t *testing.T) {
	_, err := execRun(context.Background(), "definitely-not-a-real-binary-xyz", runOptions{}, "list")
	if err == nil {
		t.Fatal("execRun returned nil error, want missing-binary error")
	}
	for _, want := range []string{"definitely-not-a-real-binary-xyz", "ATC_ZMX_BIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

func assertEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if got := strings.TrimPrefix(entry, prefix); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("env missing %s in %v", key, env)
}

func assertEnvCount(t *testing.T, env []string, key string, want int) {
	t.Helper()
	prefix := key + "="
	got := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			got++
		}
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d in %v", key, got, want, env)
	}
}
