package monitor

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// zombie is SZOMB from <sys/proc.h>: exited, not yet reaped.
const zombie int8 = 5

// readProcess reads pid's short name from kern.proc.pid and its command
// line from kern.procargs2 — the same sysctls ps and libproc use. ok is
// false when the process does not exist. A zombie has no command line,
// which callers treat as exited.
func readProcess(pid int) (process, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return process{}, false
	}
	p := process{short: unix.ByteSliceToString(info.Proc.P_comm[:])}
	if info.Proc.P_stat == zombie {
		return p, true
	}
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil {
		p.argv = parseProcArgs(raw)
	}
	return p, true
}

// parseProcArgs decodes kern.procargs2: a native-endian int32 argc, the
// executable path, NUL padding, then argc NUL-terminated arguments.
func parseProcArgs(raw []byte) []string {
	if len(raw) < 4 {
		return nil
	}
	argc := int(binary.NativeEndian.Uint32(raw))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 || argc <= 0 {
		return nil
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	argv := make([]string, 0, argc)
	for len(argv) < argc && len(rest) > 0 {
		end = bytes.IndexByte(rest, 0)
		if end < 0 {
			argv = append(argv, string(rest))
			break
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return argv
}

// groupMembers lists the live processes in group pgrp through
// kern.proc.pgrp.
func groupMembers(pgrp int) []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgrp)
	if err != nil {
		return nil
	}
	members := make([]int, 0, len(procs))
	for _, proc := range procs {
		if proc.Proc.P_stat != zombie {
			members = append(members, int(proc.Proc.P_pid))
		}
	}
	return members
}
