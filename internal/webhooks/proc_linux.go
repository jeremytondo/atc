package webhooks

import "syscall"

// receiverAttr binds the receiver's life to Core's: the kernel kills it
// when the thread that forked it dies, however it died (the forking
// goroutine keeps that thread locked for the receiver's lifetime). Belt to
// the stdin braces, so a forced termination of Core leaves no receiver
// behind.
func receiverAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
