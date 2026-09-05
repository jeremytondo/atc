package webhooks

import "syscall"

// receiverAttr binds the receiver's life to Core's: the kernel kills it
// when its parent dies, however the parent died. Belt to the stdin
// braces, so a forced termination of Core leaves no receiver behind.
func receiverAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
