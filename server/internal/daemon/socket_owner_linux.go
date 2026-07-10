//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const unixSocketAcceptFlag = "00010000"

func socketOwnerPID(socketPath string) int {
	inodes := socketInodesForPath(socketPath)
	if len(inodes) == 0 {
		return 0
	}
	targets := make(map[string]struct{}, len(inodes))
	for _, inode := range inodes {
		targets["socket:["+inode+"]"] = struct{}{}
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if processHasSocketFD(pid, targets) {
			return pid
		}
	}
	return 0
}

func socketInodesForPath(socketPath string) []string {
	content, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return nil
	}
	return parseSocketInodesForPath(string(content), socketPath)
}

func parseSocketInodesForPath(content, socketPath string) []string {
	var listening []string
	var other []string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[len(fields)-1] != socketPath {
			continue
		}
		if fields[3] == unixSocketAcceptFlag {
			listening = append(listening, fields[6])
			continue
		}
		other = append(other, fields[6])
	}
	if len(listening) > 0 {
		return listening
	}
	return other
}

func processHasSocketFD(pid int, targets map[string]struct{}) bool {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		if _, ok := targets[link]; ok {
			return true
		}
	}
	return false
}
