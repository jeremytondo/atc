package placement

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestParseVersion(t *testing.T) {
	for output, want := range map[string]int{
		"systemd 260 (260.2-2-arch)\n+PAM +AUDIT\n": 260,
		"systemd 245 (245.4-4ubuntu3)\n":            245,
		"systemd 250.4\n":                           250,
		"systemd 256~rc1 (256~rc1-1)\n":             256,
		"":                                          0,
		"something else\n":                          0,
		"systemd abc\n":                             0,
	} {
		if got := parseVersion(output); got != want {
			t.Errorf("parseVersion(%q) = %d, want %d", output, got, want)
		}
	}
}

// The wrapped argv establishes the scope before the command runs and
// keeps every argument literal: the expansion flag exactly on the
// versions that know it (an unknown version included — a refused launch
// beats a silently expanded one), and never on the versions that never
// expanded.
func TestWrap(t *testing.T) {
	argv := []string{"/usr/bin/zmx", "attach", "term-abcde", "/usr/bin/atc", "__child", "--command", "echo $$ ${HOME}"}
	base := []string{"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect", "--no-ask-password",
		"--slice=app.slice", "--unit=atc-terminal-x.scope", "--description=ATC terminal term-abcde",
		"--property=TimeoutStopSec=5s"}
	for name, tc := range map[string]struct {
		version int
		flag    bool
	}{
		"254 knows the flag":    {254, true},
		"260 expands unless":    {260, true},
		"253 never expanded":    {253, false},
		"unknown gets the flag": {0, true},
	} {
		host := &Host{systemd: true, run: "/usr/bin/systemd-run", ctl: "/usr/bin/systemctl", version: tc.version}
		want := append([]string(nil), base...)
		if tc.flag {
			want = append(want, "--expand-environment=no")
		}
		want = append(append(want, "--"), argv...)
		got := host.Wrap("atc-terminal-x.scope", "ATC terminal term-abcde", argv)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", name, diff)
		}
	}
}

// A host without systemd launches directly and answers every scope
// question as "nothing there"; a nil host is the same host.
func TestDirectHost(t *testing.T) {
	ctx := context.Background()
	argv := []string{"/usr/bin/zmx", "attach", "term-abcde"}
	for name, host := range map[string]*Host{"zero": {}, "nil": nil} {
		if host.Scoped() {
			t.Errorf("%s host reports scoped", name)
		}
		if err := host.Preflight(ctx); err != nil {
			t.Errorf("%s Preflight = %v", name, err)
		}
		if diff := cmp.Diff(argv, host.Wrap("u.scope", "d", argv)); diff != "" {
			t.Errorf("%s Wrap changed argv (-want +got):\n%s", name, diff)
		}
		if active, err := host.Active(ctx, "u.scope"); active || err != nil {
			t.Errorf("%s Active = %v, %v", name, active, err)
		}
		if err := host.Stop(ctx, "u.scope"); err != nil {
			t.Errorf("%s Stop = %v", name, err)
		}
		if units, err := host.ListActive(ctx, "atc-*"); units != nil || err != nil {
			t.Errorf("%s ListActive = %v, %v", name, units, err)
		}
	}
}

// A systemd host missing its tools, or too old for them, is still scoped
// — its launches must fail rather than run direct — and Preflight names
// the reason.
func TestUnavailableHostFailsPreflight(t *testing.T) {
	for name, host := range map[string]*Host{
		"missing tool": {systemd: true, unavailable: os.ErrNotExist},
		"systemd 229":  {systemd: true, run: "/usr/bin/systemd-run", ctl: "/usr/bin/systemctl", version: 229},
	} {
		if !host.Scoped() {
			t.Errorf("%s: host with systemd reports direct", name)
		}
		if err := host.Preflight(context.Background()); err == nil {
			t.Errorf("%s: Preflight succeeded", name)
		}
	}
}

func TestNames(t *testing.T) {
	name := Name("terminal", "/home/ab/.local/state/atc/terminals", "term-abcde")
	if !strings.HasPrefix(name, "atc-terminal-") || !strings.HasSuffix(name, "-term-abcde.scope") {
		t.Fatalf("Name = %q", name)
	}
	if len(name) != len("atc-terminal-")+12+len("-term-abcde.scope") {
		t.Errorf("Name = %q, want a 12-hex-digit namespace digest", name)
	}
	other := Name("terminal", "/tmp/other/terminals", "term-abcde")
	if other == name {
		t.Error("two namespaces named the same scope")
	}
	if Name("terminal", "/home/ab/.local/state/atc/terminals", "term-abcde") != name {
		t.Error("Name is not deterministic")
	}
	pattern := Pattern("terminal", "/home/ab/.local/state/atc/terminals")
	if !strings.HasPrefix(name, strings.TrimSuffix(pattern, "*.scope")) {
		t.Errorf("Pattern %q does not cover %q", pattern, name)
	}
	if got := Pattern("terminal", ""); got != "atc-terminal-*.scope" {
		t.Errorf("Pattern(kind, \"\") = %q", got)
	}
	for unit, want := range map[string]string{
		name:                               "term-abcde",
		other:                              "",
		"atc-codex-abc.scope":              "",
		strings.TrimSuffix(name, ".scope"): "",
	} {
		id, ok := ID("terminal", "/home/ab/.local/state/atc/terminals", unit)
		if id != want || ok != (want != "") {
			t.Errorf("ID(%q) = %q, %v; want %q", unit, id, ok, want)
		}
	}
}

func TestParseUnits(t *testing.T) {
	output := "atc-terminal-aa-term-b.scope loaded active running [systemd-run] /usr/bin/zmx attach term-b\n" +
		"atc-terminal-aa-term-a.scope loaded active abandoned ATC terminal term-a\n" +
		"atc-terminal-aa-term-c.scope loaded inactive dead ATC terminal term-c\n" +
		"atc-terminal-aa-term-d.scope loaded failed failed ATC terminal term-d\n" +
		"\n"
	want := []string{"atc-terminal-aa-term-a.scope", "atc-terminal-aa-term-b.scope", "atc-terminal-aa-term-d.scope"}
	if diff := cmp.Diff(want, parseUnits(output)); diff != "" {
		t.Errorf("parseUnits (-want +got):\n%s", diff)
	}
	if units := parseUnits(""); units != nil {
		t.Errorf("parseUnits(\"\") = %v, want nil", units)
	}
}

func TestActiveState(t *testing.T) {
	for value, want := range map[string]bool{
		"active\n": true, "activating": true, "deactivating": true, "reloading": true,
		"failed": true, "inactive\n": false, "": false,
	} {
		if got := activeState(value); got != want {
			t.Errorf("activeState(%q) = %v, want %v", value, got, want)
		}
	}
}

// requireHost is the real-systemd gate shared by the supervisor
// regression tests: the dedicated CI job sets ATC_SUPERVISOR_TESTS=require
// so a missing prerequisite fails instead of silently skipping.
func requireHost(t *testing.T) *Host {
	t.Helper()
	host := Detect()
	err := host.Preflight(context.Background())
	if host.Scoped() && err == nil {
		return host
	}
	reason := "no systemd on this host"
	if err != nil {
		reason = err.Error()
	}
	if os.Getenv("ATC_SUPERVISOR_TESTS") == "require" {
		t.Fatalf("real supervisor tests required but unavailable: %s", reason)
	}
	t.Skipf("real supervisor tests unavailable: %s", reason)
	return nil
}

// Against the real user manager: a wrapped command runs inside the named
// scope, the scope reads active while it runs and is listed, Stop ends
// it, and the emptied scope is collected. Arguments with dollar signs
// arrive literally.
func TestRealScopeLifecycle(t *testing.T) {
	host := requireHost(t)
	ctx := context.Background()
	namespace := t.TempDir()
	id := "probe-" + time.Now().UTC().Format("150405.000000")
	unit := Name("test", namespace, id)
	t.Cleanup(func() { _ = host.Stop(context.Background(), unit) })

	marker := t.TempDir() + "/cgroup"
	argv := host.Wrap(unit, "ATC placement test", []string{"/bin/sh", "-c",
		`printf '%s\n' "$1" > "$2"; cat /proc/self/cgroup >> "$2"; sleep 300`, "sh", "$$ ${HOME} $(x)", marker})
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var content string
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && strings.Count(string(data), "\n") >= 2 {
			content = string(data)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scoped command never wrote its marker (last %q, err %v)", data, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	literal, cgroup, _ := strings.Cut(content, "\n")
	if literal != "$$ ${HOME} $(x)" {
		t.Errorf("argument arrived as %q, want it literal", literal)
	}
	if !strings.Contains(cgroup, "/"+unit) {
		t.Errorf("command runs in cgroup %q, want the scope %s", strings.TrimSpace(cgroup), unit)
	}
	if active, err := host.Active(ctx, unit); err != nil || !active {
		t.Fatalf("Active = %v, %v; want true", active, err)
	}
	units, err := host.ListActive(ctx, Pattern("test", namespace))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{unit}, units); diff != "" {
		t.Errorf("ListActive (-want +got):\n%s", diff)
	}
	if err := host.Stop(ctx, unit); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if active, err := host.Active(ctx, unit); err != nil || active {
		t.Fatalf("Active after stop = %v, %v; want false", active, err)
	}
	// Stopping again is success: the goal state holds.
	if err := host.Stop(ctx, unit); err != nil {
		t.Errorf("Stop(stopped) = %v", err)
	}
	if units, err := host.ListActive(ctx, Pattern("test", namespace)); err != nil || len(units) != 0 {
		t.Errorf("ListActive after stop = %v, %v; want none", units, err)
	}
}
