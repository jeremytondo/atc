//go:build !linux

package tailscale

import "syscall"

// childAttr is Linux-only parent-death binding; other platforms rely on
// the interrupt-then-kill teardown alone.
func childAttr() *syscall.SysProcAttr {
	return nil
}
