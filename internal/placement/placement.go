// Package placement gives the long-lived processes ATC starts a lifetime
// independent of the ATC server (ATC-319). A supervised server is one
// systemd service, and a service's stop kills every process in its
// control group — forking, setsid, and detaching from the terminal do
// not change cgroup membership, so a terminal daemon or shared provider
// runtime started by the server used to die with it. On Linux with
// systemd, each such process is launched inside its own transient scope
// (`systemd-run --user --scope`), established before the process can
// fork anything persistent; the scope is the process's exact ownership
// boundary, and stopping it ends every process the launch ever created.
//
// Only this package knows systemd-run and systemctl. Callers decide what
// to launch and how to name it; the host decides whether a scope is
// possible. Where systemd is absent (macOS, a Linux without it) launches
// are direct and the caller inherits the platform's own persistence
// contract; where systemd is present but its user manager cannot be
// reached, launching fails loudly rather than silently landing in the
// caller's cgroup.
//
// Compatibility: systemd 236 or newer (--collect); older versions are
// refused at preflight. Since 254 systemd-run can expand environment
// variables in scope command arguments, and later versions do so by
// default; --expand-environment=no keeps every argument literal there,
// and older versions never expanded.
package placement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// commandTimeout bounds every manager query; a hung manager must not
// hang its caller.
const commandTimeout = 10 * time.Second

// stopTimeout is how long a scope's processes get between SIGTERM and
// SIGKILL when the scope is stopped — set on every scope at launch so a
// stop is bounded without a manager-wide setting.
const stopTimeout = 5 * time.Second

// minimumVersion is the oldest systemd whose systemd-run has every
// option Wrap uses (--collect arrived in 236).
const minimumVersion = 236

// expandEnvironmentVersion is the first systemd-run that understands
// --expand-environment. Earlier versions never expanded scope arguments.
const expandEnvironmentVersion = 254

// systemDir is what sd_booted() checks: present exactly when systemd is
// the running init.
const systemDir = "/run/systemd/system"

// Host is what this machine offers. The zero value is a host without
// systemd: launches run directly and every scope query answers "no such
// scope".
type Host struct {
	// systemd reports that systemd is the init, so a user manager is
	// expected and every launch must be scoped.
	systemd bool
	// run and ctl are the resolved systemd-run and systemctl paths.
	run, ctl string
	// version is the systemd version; 0 when it could not be read.
	version int
	// unavailable explains why a systemd host cannot scope: a missing
	// tool. Launching on such a host fails with this reason.
	unavailable error
}

// Detect inspects the host once: systemd present as the init, the tools
// on PATH, the version behind them.
func Detect() *Host {
	if runtime.GOOS != "linux" {
		return &Host{}
	}
	if _, err := os.Stat(systemDir); err != nil {
		return &Host{}
	}
	host := &Host{systemd: true}
	var err error
	if host.run, err = exec.LookPath("systemd-run"); err != nil {
		host.unavailable = errors.New("systemd is running but systemd-run is not on PATH")
		return host
	}
	if host.ctl, err = exec.LookPath("systemctl"); err != nil {
		host.unavailable = errors.New("systemd is running but systemctl is not on PATH")
		return host
	}
	host.version = queryVersion(host.run)
	return host
}

// queryVersion parses `systemd-run --version` ("systemd 260 (260.2-2-arch)
// …", and distributions' "245.4" or "256~rc1" forms); 0 when unreadable.
func queryVersion(run string) int {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, run, "--version").Output()
	if err != nil {
		return 0
	}
	return parseVersion(string(out))
}

func parseVersion(output string) int {
	first, _, _ := strings.Cut(output, "\n")
	fields := strings.Fields(first)
	if len(fields) < 2 || fields[0] != "systemd" {
		return 0
	}
	digits := fields[1]
	if i := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = digits[:i]
	}
	version, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return version
}

// Scoped reports whether launches on this host go through scopes: true
// exactly when systemd is the init. A scoped host whose tools are missing
// still reports true — its launches fail in Preflight rather than
// silently running direct.
func (h *Host) Scoped() bool {
	return h != nil && h.systemd
}

// Preflight verifies a scoped launch can be attempted right now: the
// tools exist and the user manager answers. On a direct host it is a
// no-op. The distinction it draws is the one that matters — systemd
// absent is a direct launch, systemd present but unreachable is a
// failure with the manager's own diagnostic.
func (h *Host) Preflight(ctx context.Context) error {
	if !h.Scoped() {
		return nil
	}
	if h.unavailable != nil {
		return h.unavailable
	}
	if h.version != 0 && h.version < minimumVersion {
		return fmt.Errorf("systemd %d is too old to place terminals (%d or newer needed)", h.version, minimumVersion)
	}
	if _, err := h.systemctl(ctx, "show", "--property=Version", "--value"); err != nil {
		return fmt.Errorf("systemd user manager unreachable: %w", err)
	}
	return nil
}

// Wrap returns the argv that launches argv inside the scope named unit:
// systemd-run in scope mode, which places its own process in the scope
// and then execs the command, so the command and everything it forks are
// inside from their first instruction. On a direct host argv is returned
// as is. Arguments reach the command byte-for-byte: no environment
// expansion, whatever the systemd version.
func (h *Host) Wrap(unit, description string, argv []string) []string {
	if !h.Scoped() {
		return argv
	}
	wrapped := []string{h.run, "--user", "--scope", "--quiet", "--collect", "--no-ask-password",
		"--slice=app.slice", "--unit=" + unit, "--description=" + description,
		"--property=TimeoutStopSec=" + strconv.Itoa(int(stopTimeout/time.Second)) + "s"}
	if h.version == 0 || h.version >= expandEnvironmentVersion {
		// An unreadable version gets the flag too: a refused launch is
		// loud, silent expansion of a user's command is not.
		wrapped = append(wrapped, "--expand-environment=no")
	}
	wrapped = append(wrapped, "--")
	return append(wrapped, argv...)
}

// Active reports whether the scope still holds processes. A scope that
// never existed, or that emptied and was collected, is inactive. An
// error means the manager could not be asked — never "inactive".
func (h *Host) Active(ctx context.Context, unit string) (bool, error) {
	if !h.Scoped() {
		return false, nil
	}
	out, err := h.systemctl(ctx, "show", "--property=ActiveState", "--value", unit)
	if err != nil {
		return false, err
	}
	return activeState(out), nil
}

// activeState reads an ActiveState value: anything but a settled
// inactive state still has, or may still have, processes. A failed scope
// is one whose stop ran out of time with processes still there, so it
// stays owned until a stop verifies otherwise.
func activeState(value string) bool {
	switch strings.TrimSpace(value) {
	case "inactive", "":
		return false
	}
	return true
}

// Stop ends every process in the scope — SIGTERM, then SIGKILL after the
// scope's stop timeout — and returns once the manager reports the job
// done. A scope that is not loaded is already stopped.
func (h *Host) Stop(ctx context.Context, unit string) error {
	if !h.Scoped() {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, stopTimeout+commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(stopCtx, h.ctl, "--user", "stop", unit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if strings.Contains(detail, "not loaded") {
			return nil
		}
		return commandError("systemctl --user stop "+unit, err, detail)
	}
	return nil
}

// ListActive returns the names of the loaded scopes matching pattern (a
// systemctl glob such as atc-terminal-*.scope) that still hold
// processes. An error means the listing is unavailable — never empty.
func (h *Host) ListActive(ctx context.Context, pattern string) ([]string, error) {
	if !h.Scoped() {
		return nil, nil
	}
	out, err := h.systemctl(ctx, "list-units", "--type=scope", "--all", "--plain", "--no-legend", "--no-pager", pattern)
	if err != nil {
		return nil, err
	}
	return parseUnits(out), nil
}

// parseUnits reads `list-units --plain --no-legend` rows: UNIT LOAD ACTIVE
// SUB DESCRIPTION, whitespace-separated.
func parseUnits(output string) []string {
	var units []string
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if activeState(fields[2]) {
			units = append(units, fields[0])
		}
	}
	sort.Strings(units)
	return units
}

func (h *Host) systemctl(ctx context.Context, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, h.ctl, append([]string{"--user"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", commandError("systemctl --user "+args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func commandError(what string, err error, detail string) error {
	if detail != "" {
		return fmt.Errorf("%s: %s", what, detail)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Name derives the scope name for one owned process: the kind of thing
// it is, a digest of the namespace it belongs to (the private state
// directory behind it, so two ATC state directories on one machine never
// name the same scope), and its id. Every character is valid in a unit
// name: ids come from the ATC alphabet and the digest is hex.
func Name(kind, namespace, id string) string {
	return prefix(kind, namespace) + id + ".scope"
}

// Pattern is the systemctl glob matching every scope of a kind in a
// namespace; an empty namespace matches the kind across all namespaces.
func Pattern(kind, namespace string) string {
	if namespace == "" {
		return "atc-" + kind + "-*.scope"
	}
	return prefix(kind, namespace) + "*.scope"
}

// ID recovers the id from a scope name minted by Name for the same kind
// and namespace; false for any other unit.
func ID(kind, namespace, unit string) (string, bool) {
	rest, ok := strings.CutPrefix(unit, prefix(kind, namespace))
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ".scope")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func prefix(kind, namespace string) string {
	digest := sha256.Sum256([]byte(namespace))
	return "atc-" + kind + "-" + hex.EncodeToString(digest[:6]) + "-"
}
