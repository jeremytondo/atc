package tailscale

import "syscall"

// childAttr binds the exposure child's life to this process: the kernel
// delivers SIGKILL to the child when the thread that forked it dies,
// however it died — so the forking goroutine keeps its thread locked for
// the child's lifetime. Foreground serve and funnel configuration lives
// exactly as long as the CLI process holding it, so this is what keeps a
// forced termination of ATC from leaving a public route behind.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
