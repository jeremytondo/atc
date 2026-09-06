//go:build !linux

package webhooks

import "syscall"

// receiverAttr is Linux-only; ingress never starts a receiver elsewhere.
func receiverAttr() *syscall.SysProcAttr {
	return nil
}
