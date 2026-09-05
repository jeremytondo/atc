package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/config"
)

// healthyInfo is the baseline every case mutates: installed, supervised,
// healthy, versions in agreement, loopback-only bind.
func healthyInfo() statusInfo {
	return statusInfo{
		installed:     true,
		unitFile:      "/home/ab/.config/systemd/user/atc.server.service",
		supervisor:    "active",
		responding:    true,
		healthy:       true,
		clientVersion: "v1.2.3",
		serverVersion: "v1.2.3",
		port:          7331,
		bind:          "127.0.0.1",
		hostname:      "workstation",
	}
}

func TestRenderStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		info     func() statusInfo
		want     string
		wantCode int
	}{
		"healthy": {
			info: healthyInfo,
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"version skew flags a restart": {
			info: func() statusInfo {
				s := healthyInfo()
				s.serverVersion = "v1.2.2"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.2 — differs from client v1.2.3; `atc server restart` updates it\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"installed but not responding": {
			info: func() statusInfo {
				s := healthyInfo()
				s.responding = false
				s.healthy = false
				s.serverVersion = ""
				s.supervisor = "failed"
				return s
			},
			want: "atc.server: installed but not responding; try `atc server logs` or `atc server restart`\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (failed)\n" +
				"  client: v1.2.3\n" +
				"  server: unknown (not responding)\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 1,
		},
		"not installed": {
			info: func() statusInfo {
				s := healthyInfo()
				s.installed = false
				s.supervisor = ""
				s.responding = false
				s.healthy = false
				s.serverVersion = ""
				return s
			},
			want: "atc.server: not installed; `atc server start` registers and starts it\n" +
				"  client: v1.2.3\n" +
				"  server: unknown (not responding)\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 2,
		},
		"healthy foreground server without a unit": {
			info: func() statusInfo {
				s := healthyInfo()
				s.installed = false
				s.supervisor = ""
				return s
			},
			want: "atc.server: healthy, but not installed — likely a foreground `atc server run`\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"responding but rejecting the token": {
			info: func() statusInfo {
				s := healthyInfo()
				s.healthy = false
				s.unauthorized = true
				return s
			},
			want: "atc.server: responding but rejected the local token; `atc server restart`, then `atc server token rotate` if it persists\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 1,
		},
		"wildcard bind adds the LAN url and tailnet url": {
			info: func() statusInfo {
				s := healthyInfo()
				s.bind = "0.0.0.0"
				s.tailnet = true
				s.tailnetURL = "https://machine.tail1234.ts.net:7331"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (lan): http://workstation:7331\n" +
				"  api (tailnet): https://machine.tail1234.ts.net:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"specific bind address is the LAN host": {
			info: func() statusInfo {
				s := healthyInfo()
				s.bind = "192.168.1.20"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (lan): http://192.168.1.20:7331\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"launch flag attributes tailscale and shows the ways back": {
			info: func() statusInfo {
				s := healthyInfo()
				s.flags.Tailscale = boolPtr(true)
				s.tailnet = true
				s.tailnetURL = "https://machine.tail1234.ts.net:7331"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): https://machine.tail1234.ts.net:7331\n" +
				"  tailscale: enabled by this launch's flag; `atc server restart --tailscale=false` replaces it, stop then start returns to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"launch flag with tailnet unavailable keeps the diagnostics": {
			info: func() statusInfo {
				s := healthyInfo()
				s.flags.Tailscale = boolPtr(true)
				s.tailnet = true
				s.tailnetProblem = "tailscale is logged out (BackendState NeedsLogin)"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): unavailable (tailscale is logged out (BackendState NeedsLogin))\n" +
				"  tailscale: enabled by this launch's flag; `atc server restart --tailscale=false` replaces it, stop then start returns to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"tailnet route still converging shows expected url without claiming availability": {
			info: func() statusInfo {
				s := healthyInfo()
				s.flags.Tailscale = boolPtr(true)
				s.tailnet = true
				s.tailnetURL = "https://machine.tail1234.ts.net:7331"
				s.tailnetProblem = "tailscale serve has not exposed the route yet"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): pending at https://machine.tail1234.ts.net:7331 (tailscale serve has not exposed the route yet)\n" +
				"  tailscale: enabled by this launch's flag; `atc server restart --tailscale=false` replaces it, stop then start returns to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"unreadable unit reports unknown launch flags instead of guessing": {
			info: func() statusInfo {
				s := healthyInfo()
				s.flagsProblem = "installed unit has no ExecStart line"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  launch flags: unknown (installed unit has no ExecStart line); `atc server stop`, then `atc server start` with the flags you want\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"tailnet exposure unavailable states why": {
			info: func() statusInfo {
				s := healthyInfo()
				s.tailnet = true
				s.tailnetProblem = "tailscale is logged out (BackendState NeedsLogin)"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): unavailable (tailscale is logged out (BackendState NeedsLogin))\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
	} {
		got, code := renderStatus(tc.info())
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s: renderStatus mismatch (-want +got):\n%s", name, diff)
		}
		if code != tc.wantCode {
			t.Errorf("%s: exit code = %d, want %d", name, code, tc.wantCode)
		}
	}
}

func TestRenderWebhooks(t *testing.T) {
	for name, tc := range map[string]struct {
		status api.Webhooks
		err    error
		want   []string
	}{
		"disabled and idle is silent": {status: api.Webhooks{State: api.WebhooksDisabled}},
		"disabled with backlog": {
			status: api.Webhooks{State: api.WebhooksDisabled, Pending: 2},
			want:   []string{"webhooks: disabled (2 pending from earlier intake)"},
		},
		"ready with routes and summaries": {
			status: api.Webhooks{
				State: api.WebhooksReady, URL: "https://machine.tail1234.ts.net", Pending: 1,
				Routes:   []api.WebhookRoute{{IntegrationID: "linear", Path: "/linear"}},
				Rejected: 3, LastRejection: "/linear: 401 bad signature",
			},
			want: []string{
				"webhooks: https://machine.tail1234.ts.net (1 pending)",
				"webhook route (linear): https://machine.tail1234.ts.net/linear",
				"webhooks rejected: 3 since start (last: /linear: 401 bad signature)",
			},
		},
		"ready but blocked": {
			status: api.Webhooks{State: api.WebhooksReady, URL: "https://machine.tail1234.ts.net", Pending: 1000, IntakeBlocked: true},
			want:   []string{"webhooks: https://machine.tail1234.ts.net (1000 pending); intake blocked"},
		},
		"awaiting approval shows the action on one line": {
			status: api.Webhooks{
				State: api.WebhooksStarting, URL: "https://machine.tail1234.ts.net:8443",
				Reason: "tailscale funnel exited: Funnel not available",
				Action: "Funnel not available; \"funnel\" node attribute not set.\n\tSee https://tailscale.com/s/no-funnel.",
			},
			want: []string{
				"webhooks: starting at https://machine.tail1234.ts.net:8443 (tailscale funnel exited: Funnel not available)",
				"webhooks action: Funnel not available; \"funnel\" node attribute not set. See https://tailscale.com/s/no-funnel.",
			},
		},
		"unavailable": {
			status: api.Webhooks{State: api.WebhooksUnavailable, Reason: "webhook ingress requires Linux"},
			want:   []string{"webhooks: unavailable (webhook ingress requires Linux)"},
		},
		"processing failures": {
			status: api.Webhooks{State: api.WebhooksDisabled, ProcessingFailures: 4, LastProcessingFailure: "/linear: provider unreachable"},
			want: []string{
				"webhooks: disabled (0 pending from earlier intake)",
				"webhook processing failures: 4 since start (last: /linear: provider unreachable)",
			},
		},
		"unreachable report": {
			err:  errors.New("connection refused"),
			want: []string{"webhooks: unknown (connection refused)"},
		},
	} {
		if diff := cmp.Diff(tc.want, renderWebhooks(tc.status, tc.err)); diff != "" {
			t.Errorf("%s: renderWebhooks mismatch (-want +got):\n%s", name, diff)
		}
	}
}

// A healthy server's webhook report rides along in the status output.
func TestRenderStatusIncludesWebhookReport(t *testing.T) {
	s := healthyInfo()
	s.flags.Webhooks = boolPtr(true)
	s.webhookStatus = api.Webhooks{State: api.WebhooksReady, URL: "https://machine.tail1234.ts.net"}
	got, _ := renderStatus(s)
	want := "atc.server: running and healthy\n" +
		"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
		"  client: v1.2.3\n" +
		"  server: v1.2.3\n" +
		"  api: http://127.0.0.1:7331\n" +
		"  webhooks: https://machine.tail1234.ts.net (0 pending)\n" +
		"  webhooks: enabled by this launch's flag; `atc server restart --webhooks=false` replaces it, stop then start returns to config.toml\n" +
		"  token: `atc server token` prints the bearer token remote clients use\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("renderStatus mismatch (-want +got):\n%s", diff)
	}
}

func TestInspectTailnetWithTimeoutRendersExpiryAsDiagnostic(t *testing.T) {
	origInspect, origTimeout := inspectTailnetEndpoint, tailnetInspectionTimeout
	t.Cleanup(func() {
		inspectTailnetEndpoint, tailnetInspectionTimeout = origInspect, origTimeout
	})
	tailnetInspectionTimeout = time.Millisecond
	inspectTailnetEndpoint = func(ctx context.Context, _ config.Config, _ string) (string, string) {
		<-ctx.Done()
		return "https://machine.tail1234.ts.net:7331", ctx.Err().Error()
	}

	endpoint, problem, err := inspectTailnetWithTimeout(context.Background(), config.Config{}, "")
	if err != nil {
		t.Fatalf("inspect tailnet: %v", err)
	}
	if diff := cmp.Diff("https://machine.tail1234.ts.net:7331", endpoint); diff != "" {
		t.Errorf("endpoint mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("tailnet endpoint inspection timed out", problem); diff != "" {
		t.Errorf("problem mismatch (-want +got):\n%s", diff)
	}
}
