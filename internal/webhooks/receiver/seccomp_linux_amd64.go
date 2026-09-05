package receiver

import "golang.org/x/sys/unix"

// archDeniedSyscalls are the legacy process-creation syscalls amd64 still
// has; arm64 has only clone.
var archDeniedSyscalls = []uint32{unix.SYS_FORK, unix.SYS_VFORK}
