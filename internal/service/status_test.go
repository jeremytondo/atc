package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
				s.tailscale = true
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
		"service override attributes tailscale and shows the clearing command": {
			info: func() statusInfo {
				s := healthyInfo()
				s.tailscaleOverride = true
				s.tailnetURL = "https://machine.tail1234.ts.net:7331"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): https://machine.tail1234.ts.net:7331\n" +
				"  tailscale: enabled by the service flag; `atc server restart --tailscale=false` returns control to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"service override with tailnet unavailable keeps the diagnostics": {
			info: func() statusInfo {
				s := healthyInfo()
				s.tailscaleOverride = true
				s.tailnetProblem = "tailscale is logged out (BackendState NeedsLogin)"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  api (tailnet): unavailable (tailscale is logged out (BackendState NeedsLogin))\n" +
				"  tailscale: enabled by the service flag; `atc server restart --tailscale=false` returns control to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"tailnet route still converging shows expected url without claiming availability": {
			info: func() statusInfo {
				s := healthyInfo()
				s.tailscaleOverride = true
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
				"  tailscale: enabled by the service flag; `atc server restart --tailscale=false` returns control to config.toml\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"unreadable unit reports an unknown override instead of guessing": {
			info: func() statusInfo {
				s := healthyInfo()
				s.overrideProblem = "installed unit has no ExecStart line"
				return s
			},
			want: "atc.server: running and healthy\n" +
				"  unit: /home/ab/.config/systemd/user/atc.server.service (active)\n" +
				"  client: v1.2.3\n" +
				"  server: v1.2.3\n" +
				"  api: http://127.0.0.1:7331\n" +
				"  tailscale: unknown service override (installed unit has no ExecStart line); rerun `atc server start` with an explicit --tailscale or --tailscale=false\n" +
				"  token: `atc server token` prints the bearer token remote clients use\n",
			wantCode: 0,
		},
		"tailnet exposure unavailable states why": {
			info: func() statusInfo {
				s := healthyInfo()
				s.tailscale = true
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
