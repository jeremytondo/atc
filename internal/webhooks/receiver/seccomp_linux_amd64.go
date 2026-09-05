package receiver

import "golang.org/x/sys/unix"

// archDeniedSyscalls are the legacy syscalls amd64 still has beside the
// modern ones the shared list denies: fork and vfork for process
// creation, and the path-based mode, owner, and timestamp changes.
var archDeniedSyscalls = []uint32{
	unix.SYS_FORK, unix.SYS_VFORK,
	unix.SYS_CHMOD, unix.SYS_CHOWN, unix.SYS_LCHOWN,
	unix.SYS_UTIME, unix.SYS_UTIMES, unix.SYS_FUTIMESAT,
}
