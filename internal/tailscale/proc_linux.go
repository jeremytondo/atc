package tailscale

import "syscall"

// childAttr binds the exposure child's life to this process: the kernel
// delivers SIGKILL to the child when its parent dies, however the parent
// died. Foreground serve and funnel configuration lives exactly as long as
// the CLI process holding it, so this is what keeps a forced termination
// of ATC from leaving a public route behind.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
