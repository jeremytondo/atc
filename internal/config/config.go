// Package config loads server settings under the contract decided in
// ATC-259: precedence flags > ATC_<KEY> env > config.toml > defaults;
// snake_case keys; unknown keys in the file refuse startup so a typo'd key
// can never be silently ignored. Flags apply at the command seam in
// cmd/atc; everything below the flag level resolves here. The TOML format
// never leaks past this package.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config is the settled server configuration. v1 is deliberately minimal.
type Config struct {
	// Port the server listens on and clients connect to. 0 means an
	// OS-assigned ephemeral port (useful in tests, not for real use: the
	// stable default is what remote clients paste into their setup).
	Port int `toml:"port"`
	// Bind address. Default loopback only; the practical non-loopback
	// value is 0.0.0.0, but binding a single non-loopback address drops
	// the loopback listener and breaks local clients. Documented, not
	// policed — any address is accepted (legacy-observed semantics).
	Bind string `toml:"bind"`
	// Tailscale supervises a loopback-fronting `tailscale serve` exposure
	// for the server's lifetime.
	Tailscale bool `toml:"tailscale"`
	// TailscaleExecutable names the tailscale CLI; a custom value is exact
	// user intent and disables the macOS app-bundle fallback.
	TailscaleExecutable string `toml:"tailscale_executable"`
	// Webhooks runs the restricted webhook receiver and exposes it to the
	// internet through Tailscale Funnel for the server's lifetime
	// (ATC-306). Independent of Tailscale, which is private Serve
	// exposure of the API.
	Webhooks bool `toml:"webhooks"`
	// WebhooksPort is the public HTTPS port Funnel serves the receiver on.
	// Funnel supports only 443, 8443, and 10000; the server never
	// switches ports on its own to work around a conflict.
	WebhooksPort int `toml:"webhooks_port"`
	// DocumentsPort is the port the document origin listens on (ATC-318):
	// published artifacts for browsers, on Bind locally and on the tailnet
	// whenever Tailscale is. A distinct port is a distinct browser origin
	// from the API, which is the isolation the reader relies on, so it may
	// never equal Port. 0 means an OS-assigned port, as for Port.
	DocumentsPort int `toml:"documents_port"`
}

// funnelPorts are the public ports Tailscale Funnel can serve.
var funnelPorts = []int{443, 8443, 10000}

// Default is the configuration with no file, environment, or flags present.
// Port 7331 is the stable contract port (ATC-245).
func Default() Config {
	return Config{Port: 7331, Bind: "127.0.0.1", TailscaleExecutable: "tailscale", WebhooksPort: 443, DocumentsPort: 7332}
}

// Load resolves the file and environment levels: defaults, overlaid with
// path's contents when the file exists, overlaid with the ATC_<KEY>
// environment.
// lookupEnv is injected so tests control the environment (os.LookupEnv in
// production).
func Load(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, err
	}
	// No file is a normal install state: data is nil and only the
	// defaults and environment apply.
	return load(path, data, lookupEnv)
}

// load resolves the file and environment levels over the file's bytes;
// path names it in diagnostics. Nil data means no file.
func load(path string, data []byte, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Default()
	if data != nil {
		dec := toml.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			var strict *toml.StrictMissingError
			if errors.As(err, &strict) {
				return Config{}, fmt.Errorf(
					"%s contains keys the server does not understand (fix or remove them):\n%s",
					path, strict.String())
			}
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	if value, ok := lookupEnv("ATC_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_PORT=%q is not a number", value)
		}
		cfg.Port = port
	}
	if value, ok := lookupEnv("ATC_BIND"); ok {
		cfg.Bind = value
	}
	if value, ok := lookupEnv("ATC_TAILSCALE"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_TAILSCALE=%q is not a boolean", value)
		}
		cfg.Tailscale = enabled
	}
	if value, ok := lookupEnv("ATC_TAILSCALE_EXECUTABLE"); ok {
		cfg.TailscaleExecutable = value
	}
	if value, ok := lookupEnv("ATC_WEBHOOKS"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_WEBHOOKS=%q is not a boolean", value)
		}
		cfg.Webhooks = enabled
	}
	if value, ok := lookupEnv("ATC_WEBHOOKS_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_WEBHOOKS_PORT=%q is not a number", value)
		}
		cfg.WebhooksPort = port
	}
	if value, ok := lookupEnv("ATC_DOCUMENTS_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_DOCUMENTS_PORT=%q is not a number", value)
		}
		cfg.DocumentsPort = port
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks value constraints. Load runs it, but flags apply above
// this package (at the command seam), so the caller must run it again after
// the final precedence layer — otherwise a flag could smuggle in a value
// every lower layer would have refused.
func (c Config) Validate() error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port %d is outside 0-65535", c.Port)
	}
	if c.Bind == "" {
		return errors.New("bind must not be empty")
	}
	if c.TailscaleExecutable == "" {
		return errors.New("tailscale_executable must not be empty")
	}
	if !slices.Contains(funnelPorts, c.WebhooksPort) {
		return fmt.Errorf("webhooks_port %d is not a port Tailscale Funnel can serve (443, 8443, or 10000)", c.WebhooksPort)
	}
	if c.DocumentsPort < 0 || c.DocumentsPort > 65535 {
		return fmt.Errorf("documents_port %d is outside 0-65535", c.DocumentsPort)
	}
	if c.DocumentsPort != 0 && c.DocumentsPort == c.Port {
		return fmt.Errorf("documents_port and port are both %d: the document origin must be a distinct origin from the API", c.Port)
	}
	return nil
}

// ValidateExposure checks the exposures a launch will actually run,
// once flags have settled them: private Serve fronts the API at
// https://<node>:<port> and Funnel serves the receiver at
// https://<node>:<webhooks_port>, and Serve fronts the document origin at
// https://<node>:<documents_port> alongside the API, so two on one port
// would be one Tailscale endpoint with two owners. The server never switches ports to
// work around it.
func (c Config) ValidateExposure(tailnet, webhooks bool) error {
	if tailnet && webhooks && c.WebhooksPort == c.Port {
		return fmt.Errorf("webhooks_port and port are both %d: the public webhook endpoint and the private tailnet API cannot share a Tailscale port", c.Port)
	}
	if tailnet && webhooks && c.DocumentsPort != 0 && c.WebhooksPort == c.DocumentsPort {
		return fmt.Errorf("webhooks_port and documents_port are both %d: the public webhook endpoint and the private document origin cannot share a Tailscale port", c.DocumentsPort)
	}
	return nil
}

// EnableTailscale sets `tailscale = true` in the configuration file at
// path, creating the file when absent (ATC-325 guided setup). The edit is
// textual — one key line replaced or appended — so every other line,
// comments included, survives exactly, as do the file's mode and, when
// path is a symlink (a dotfiles checkout), the link itself: the file it
// points at is what changes. The result must load, or nothing is
// written: a file the server would refuse is never produced from one it
// accepted.
func EnableTailscale(path string) error {
	mode := fs.FileMode(0o600)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// A dangling link stays a link: the file it names is created.
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	content := setKey(string(data), "tailscale", "true")
	if _, err := load(path, []byte(content), func(string) (string, bool) { return "", false }); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(content)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// setKey rewrites a top-level `key = ...` line to value, or appends one.
// The file has no tables, so a line starting with the key is the key.
func setKey(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		name, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(name) == key {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + key + " = " + value + "\n"
}
