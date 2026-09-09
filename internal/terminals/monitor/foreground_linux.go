package monitor

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// readProcess reads pid's command line and short name from /proc. ok is
// false when the process does not exist. A zombie has an empty command
// line, which callers treat as exited.
func readProcess(pid int) (process, bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	comm, err := os.ReadFile(dir + "/comm")
	if err != nil {
		return process{}, false
	}
	p := process{short: strings.TrimSpace(string(comm))}
	if cmdline, err := os.ReadFile(dir + "/cmdline"); err == nil {
		p.argv = splitCmdline(cmdline)
	}
	return p, true
}

func splitCmdline(cmdline []byte) []string {
	cmdline = bytes.TrimSuffix(cmdline, []byte{0})
	if len(cmdline) == 0 {
		return nil
	}
	parts := bytes.Split(cmdline, []byte{0})
	argv := make([]string, len(parts))
	for i, part := range parts {
		argv[i] = string(part)
	}
	return argv
}

// groupMembers lists the live processes in group pgrp by scanning
// /proc/*/stat, whose fields after the parenthesised name are state,
// ppid, pgrp, …
func groupMembers(pgrp int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var members []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(fields) < 3 || fields[0] == "Z" {
			continue
		}
		if group, err := strconv.Atoi(fields[2]); err == nil && group == pgrp {
			members = append(members, pid)
		}
	}
	return members
}
