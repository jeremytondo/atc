package zmx

// Startup installation and activation (ATC-324). Runs once per server
// start, in the background after the API is listening, bounded by
// startupBudget and by the server's own context. It does at most three
// things, in order: reinstall the recorded active runtime by exact
// identity if its executable is missing or damaged; install this build's
// desired runtime if it is not installed; activate the desired runtime if
// it is not the active one and the namespace is provably empty. Every
// failure is recorded for status and left for the next restart — there
// is no retry loop and no repair path outside startup.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// startupBudget bounds the whole startup routine: downloads that take
// longer are failures the next restart retries.
const startupBudget = 5 * time.Minute

// Startup installs and activates as described above. openRecords reports
// whether the terminals domain still lists terminals that have not
// exited — records whose sessions cannot be inspected when no runtime is
// active yet.
func (d *Driver) Startup(ctx context.Context, openRecords func() bool) {
	ctx, cancel := context.WithTimeout(ctx, startupBudget)
	defer cancel()
	if sel, ok := d.runtime.needsRepair(); ok {
		if err := d.repair(ctx, sel); err != nil {
			d.logger.Error("zmx runtime repair failed", "version", sel.Version, "error", err)
			d.runtime.setInstallErr(err)
			return
		}
	}
	active := d.runtime.activeSelection()
	if active == nil {
		if problem := d.runtime.selectionProblem(); problem != nil {
			// Unreadable or unsupported selection: reported by status;
			// nothing to install and nothing to activate.
			d.logger.Warn("zmx runtime unavailable", "error", problem)
			return
		}
	}
	desired := d.runtime.Desired()
	if active != nil && active.Version == desired {
		return
	}
	executable, err := d.install(ctx, desired)
	if err != nil {
		d.logger.Error("zmx installation failed", "version", desired, "error", err)
		d.runtime.setInstallErr(err)
		return
	}
	sel, err := selectionFor(desired, executable)
	if err != nil {
		d.runtime.setInstallErr(err)
		return
	}
	empty := d.emptyUnmanaged(openRecords)
	if active != nil {
		empty = d.emptyManaged
	}
	if err := d.runtime.activate(ctx, sel, empty); err != nil {
		d.logger.Warn("zmx activation deferred", "version", desired, "reason", err)
		d.runtime.setBlocked(err.Error())
	}
}

// repair reinstalls the recorded active runtime and requires the result
// to carry the recorded identity: a release table that would produce a
// different executable for that version is refused, never substituted.
func (d *Driver) repair(ctx context.Context, sel selection) error {
	d.logger.Warn("active zmx executable missing or damaged; reinstalling", "version", sel.Version)
	executable, err := d.install(ctx, sel.Version)
	if err != nil {
		return err
	}
	// The installer writes to a deterministic per-version path; compare
	// absolute forms so a relative install root never looks like a
	// substitution.
	if abs, err := filepath.Abs(executable); err != nil {
		return err
	} else if abs != sel.Executable {
		return fmt.Errorf("reinstalled zmx %s at %s, not the recorded %s", sel.Version, abs, sel.Executable)
	}
	if err := validExecutable(executable, sel.SHA256); err != nil {
		return fmt.Errorf("reinstalled zmx %s does not match the namespace's recorded identity: %w", sel.Version, err)
	}
	d.runtime.load()
	return nil
}

// install runs the installation routine with status reflecting it.
func (d *Driver) install(ctx context.Context, version string) (string, error) {
	d.runtime.setInstalling(version)
	executable, err := d.runtime.Install(ctx, version)
	d.runtime.setInstallErr(nil)
	return executable, err
}

// emptyManaged proves the namespace empty through its active runtime:
// one complete inventory with no session at all, reachable or not, and
// no scope left running. A failed inventory or scope listing proves
// nothing. Never the desired runtime — its compatibility is unproven.
func (d *Driver) emptyManaged(ctx context.Context) (string, error) {
	inventory, err := d.Inventory(ctx)
	if err != nil {
		return "", err
	}
	if len(inventory) > 0 {
		names := make([]string, 0, len(inventory))
		for _, session := range inventory {
			names = append(names, session.Name)
		}
		return fmt.Sprintf("zmx %s is installed; switching from %s waits for the remaining sessions (%s) to end and the server to restart",
			d.runtime.Desired(), d.runtime.activeSelection().Version, strings.Join(names, ", ")), nil
	}
	leftovers, err := d.Leftovers(ctx, inventory)
	if err != nil {
		return "", err
	}
	if len(leftovers) > 0 {
		return fmt.Sprintf("zmx %s is installed; switching waits for the processes left by sessions %s to end and the server to restart",
			d.runtime.Desired(), strings.Join(leftovers, ", ")), nil
	}
	return "", nil
}

// emptyUnmanaged is the first-rollout proof for a namespace with no
// recorded runtime: nothing may be probed, so emptiness is the absence
// of anything at all — no entry in the socket directory, no scope of
// this namespace running, no terminal record that has not exited. Any
// of these means the operator's cleanup is incomplete.
func (d *Driver) emptyUnmanaged(openRecords func() bool) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		const incomplete = "first-rollout cleanup incomplete: "
		entries, err := os.ReadDir(d.socketDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if len(entries) > 0 {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			sort.Strings(names)
			return incomplete + fmt.Sprintf("%s still holds %s; end those sessions with the client that created them, then restart the server", d.socketDir, strings.Join(names, ", ")), nil
		}
		if d.placement.Scoped() {
			units, err := d.placement.ListActive(ctx, placementPattern(d.socketDir))
			if err != nil {
				return "", err
			}
			if len(units) > 0 {
				return incomplete + fmt.Sprintf("processes of earlier terminals are still running (%s); end them, then restart the server", strings.Join(units, ", ")), nil
			}
		}
		if openRecords() {
			return incomplete + "terminals that have not exited are still recorded; delete them, then restart the server", nil
		}
		return "", nil
	}
}
