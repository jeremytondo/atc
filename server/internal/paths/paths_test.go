package paths

import (
	"path/filepath"
	"testing"
)

func TestControlDirForEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "xdg directory",
			env:  map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "TMPDIR": "/ignored"},
			want: filepath.Join("/run/user/1000", "atc"),
		},
		{
			name: "tmpdir fallback",
			env:  map[string]string{"TMPDIR": "/var/tmp"},
			want: filepath.Join("/var/tmp", "atc-1000"),
		},
		{
			name: "tmp fallback",
			env:  map[string]string{},
			want: filepath.Join("/tmp", "atc-1000"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ControlDirForEnv(func(key string) string {
				return tt.env[key]
			}, 1000)
			if got != tt.want {
				t.Fatalf("control dir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathsForEnv(t *testing.T) {
	paths := PathsForEnv(func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	}, 1000)

	if paths.SocketPath != filepath.Join("/run/user/1000", "atc", SocketName) {
		t.Fatalf("socket path = %q", paths.SocketPath)
	}
	if paths.PIDPath != filepath.Join("/run/user/1000", "atc", PIDName) {
		t.Fatalf("pid path = %q", paths.PIDPath)
	}
}

func TestStateDirForEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "xdg state directory",
			env:  map[string]string{"XDG_STATE_HOME": "/home/u/.local/state", "HOME": "/ignored"},
			want: filepath.Join("/home/u/.local/state", "atc"),
		},
		{
			name: "home fallback",
			env:  map[string]string{"HOME": "/home/u"},
			want: filepath.Join("/home/u", ".local", "state", "atc"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StateDirForEnv(func(key string) string {
				return tt.env[key]
			})
			if got != tt.want {
				t.Fatalf("state dir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDBPathForEnv(t *testing.T) {
	got := DBPathForEnv(func(key string) string {
		if key == "XDG_STATE_HOME" {
			return "/state"
		}
		return ""
	})
	if want := filepath.Join("/state", "atc", DBName); got != want {
		t.Fatalf("db path = %q, want %q", got, want)
	}
}
