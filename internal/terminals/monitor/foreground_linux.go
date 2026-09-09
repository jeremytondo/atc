package monitor

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// readProcess reads pid's command line and short name from /proc. ok is
// false when the process does not exist or is a zombie — exited, so no
// longer what the terminal is running.
func readProcess(pid int) (process, bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	if state, _, ok := readStat(dir); !ok || state == "Z" {
		return process{}, false
	}
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

// readStat returns the state and process group from /proc/<pid>/stat,
// whose fields after the parenthesised name are state, ppid, pgrp, …
func readStat(dir string) (state string, pgrp int, ok bool) {
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return "", 0, false
	}
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(fields) < 3 {
		return "", 0, false
	}
	pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, false
	}
	return fields[0], pgrp, true
}

// groupMembers lists the live processes in group pgrp by scanning /proc.
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
		if state, group, ok := readStat("/proc/" + entry.Name()); ok && group == pgrp && state != "Z" {
			members = append(members, pid)
		}
	}
	return members
}
