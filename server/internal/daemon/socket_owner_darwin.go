//go:build darwin

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
)

func socketOwnerPID(socketPath string) int {
	output, err := exec.Command("lsof", "-nP", "-t", socketPath).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(output), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
