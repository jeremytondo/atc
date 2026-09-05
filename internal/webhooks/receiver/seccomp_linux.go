package receiver

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The seccomp filter closes what Landlock cannot: Landlock's network
// rules are port-based (allowing a port allows it on every host) and its
// older ABIs scope neither signals nor abstract sockets. The receiver
// needs no new sockets — its public listener and channel connections are
// inherited — no child processes, and no way to touch other processes,
// so the filter denies those syscall families outright with EPERM and
// allows everything else. Unlike Landlock it cannot be installed before
// the exec (it denies execve), so the second stage installs it first thing,
// synchronized onto every thread the runtime has already started.

const (
	bpfLoadWord = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	bpfJumpEq   = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	bpfJumpSet  = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
	bpfReturn   = unix.BPF_RET | unix.BPF_K

	// seccomp_data field offsets: nr, arch, then args[0] (low word first
	// on the little-endian targets ATC builds for).
	offsetNr   = 0
	offsetArch = 4
	offsetArg0 = 16

	retAllow = unix.SECCOMP_RET_ALLOW
	retEPERM = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	retKill  = unix.SECCOMP_RET_KILL_PROCESS
)

// deniedSyscalls are refused unconditionally: creating sockets of any
// family (TCP, UDP, unix path or abstract), creating processes, tracing or
// reading other processes, and signalling by pid or pidfd.
var deniedSyscalls = []uint32{
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
	unix.SYS_FORK, unix.SYS_VFORK, unix.SYS_CLONE3, unix.SYS_EXECVE, unix.SYS_EXECVEAT,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_KILL, unix.SYS_TKILL, unix.SYS_RT_SIGQUEUEINFO, unix.SYS_RT_TGSIGQUEUEINFO,
	unix.SYS_PIDFD_OPEN, unix.SYS_PIDFD_GETFD, unix.SYS_PIDFD_SEND_SIGNAL,
}

func auditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	}
	return 0, fmt.Errorf("no seccomp filter for %s", runtime.GOARCH)
}

// installSeccomp builds the filter and installs it on every thread of the
// process.
func installSeccomp() error {
	arch, err := auditArch()
	if err != nil {
		return err
	}
	pid := uint32(os.Getpid())
	instruction := func(code uint16, jt, jf uint8, k uint32) unix.SockFilter {
		return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
	}
	var program []unix.SockFilter
	// A syscall from a foreign ABI (x32 on amd64) is not one this filter
	// understands, so the process dies rather than guessing.
	program = append(program,
		instruction(bpfLoadWord, 0, 0, offsetArch),
		instruction(bpfJumpEq, 1, 0, arch),
		instruction(bpfReturn, 0, 0, retKill),
		instruction(bpfLoadWord, 0, 0, offsetNr),
	)
	// Each denied syscall jumps to the shared EPERM return at the end;
	// offsets are relative to the following instruction.
	tail := 8 // the clone check (3), tgkill check (3), allow (1), EPERM (1)
	for i, nr := range deniedSyscalls {
		remaining := len(deniedSyscalls) - i - 1
		program = append(program, instruction(bpfJumpEq, uint8(remaining+tail-1), 0, nr))
	}
	// clone: a new thread (CLONE_THREAD in the flags argument) is the Go
	// runtime's business and allowed; a new process is not.
	program = append(program,
		instruction(bpfJumpEq, 0, 2, unix.SYS_CLONE),
		instruction(bpfLoadWord, 0, 0, offsetArg0),
		instruction(bpfJumpSet, 3, 4, unix.CLONE_THREAD),
	)
	// tgkill: the runtime signals its own threads (preemption); only this
	// process's own thread group may be the target.
	program = append(program,
		instruction(bpfJumpEq, 0, 2, unix.SYS_TGKILL),
		instruction(bpfLoadWord, 0, 0, offsetArg0),
		instruction(bpfJumpEq, 0, 1, pid),
	)
	program = append(program,
		instruction(bpfReturn, 0, 0, retAllow),
		instruction(bpfReturn, 0, 0, retEPERM),
	)
	prog := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER, TSYNC): %w", errno)
	}
	return nil
}
