package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/remote"
)

// The bootstrap over the lifecycle seams and a private state directory:
// a stopped server is started through Start, a healthy one is left alone,
// the token is the store's own, and the URL is the live tailnet endpoint.
func TestBootstrapStartsStoppedServerAndReportsEndpoint(t *testing.T) {
	s := &seamStub{tailnetURL: "https://host.tailnet.ts.net:1"}
	installSeams(t, s)
	opts, _ := seamEnv(t)
	opts.Config.Tailscale = true
	tailnetPollInterval = time.Millisecond
	// Unhealthy until the supervisor has been asked to start it.
	probeOnce = func(context.Context, Options, string) probeOutcome {
		for _, command := range s.commands {
			if strings.Join(command, " ") == "systemctl --user start "+UnitName {
				return probeOutcome{responding: true, healthy: true, serverVersion: "v9.9.9"}
			}
		}
		return probeOutcome{}
	}

	got, err := Bootstrap(context.Background(), opts)
	if err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}
	token, err := ensureToken()
	if err != nil {
		t.Fatal(err)
	}
	want := remote.Bootstrap{URL: "https://host.tailnet.ts.net:1", Token: token, Version: "v9.9.9"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Bootstrap (-want +got):\n%s", diff)
	}
	wantCommands := [][]string{
		{"loginctl", "enable-linger"},
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", UnitName},
		{"systemctl", "--user", "start", UnitName},
	}
	if diff := cmp.Diff(wantCommands, s.commands); diff != "" {
		t.Errorf("supervisor commands (-want +got):\n%s", diff)
	}
	if out := opts.Stdout.(*strings.Builder).String(); strings.Contains(out, token) {
		t.Error("lifecycle output leaked the token")
	}

	// Healthy already: nothing is started, the version is the server's.
	s.commands = nil
	probeOnce = func(context.Context, Options, string) probeOutcome {
		return probeOutcome{responding: true, healthy: true, serverVersion: "v1.0.0"}
	}
	if got, err = Bootstrap(context.Background(), opts); err != nil || got.Version != "v1.0.0" || len(s.commands) != 0 {
		t.Errorf("healthy bootstrap = %+v, %v; commands %v", got, err, s.commands)
	}
}

func TestBootstrapRefusesWithoutTailnetAndBoundsTheWait(t *testing.T) {
	s := &seamStub{healthy: true, tailnetProblem: "tailscale serve has not exposed the route yet"}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	tailnetPollInterval = time.Millisecond
	origTimeout := tailnetServingTimeout
	tailnetServingTimeout = 20 * time.Millisecond
	t.Cleanup(func() { tailnetServingTimeout = origTimeout })

	_, err := Bootstrap(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "`tailscale = true`") || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("config without tailnet: err = %v, want the config remedy", err)
	}
	if len(s.commands) != 0 {
		t.Errorf("refusal touched the supervisor: %v", s.commands)
	}

	// Configuration enables it but the running launch was started with
	// --tailscale=false: the remedy is the restart flag.
	opts.Config.Tailscale = true
	s.active = true
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(false)})
	if _, err := Bootstrap(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--tailscale") {
		t.Errorf("launch without tailnet: err = %v, want the restart remedy", err)
	}

	// Enabled but never serving: the wait is bounded and names the reason.
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
	_, err = Bootstrap(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "did not reach serving") || !strings.Contains(err.Error(), s.tailnetProblem) {
		t.Errorf("never serving: err = %v", err)
	}
}
