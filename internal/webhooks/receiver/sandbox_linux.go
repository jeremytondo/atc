package receiver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// minABI is the Landlock ABI the receiver requires: 1 (Linux 5.13) gives
// the filesystem restriction and confines ptrace. Network, signal, and
// socket confinement come from the seccomp filter on every kernel, and
// Landlock's own network (ABI 4) and scoping (ABI 6) rules are layered on
// where available.
const minABI = 1

// fsAccess is every filesystem access right the given ABI knows, so the
// ruleset handles (and therefore denies by default) all of them.
func fsAccess(abi int) uint64 {
	access := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		access |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return access
}

// enforce restricts the calling thread with Landlock: the filesystem
// closed except for reading and executing this binary and the system
// loader directories (a cgo-linked build needs its libc), every TCP bind
// and connect denied where the kernel supports that, abstract sockets and
// signals scoped where it supports that. no_new_privs first, as Landlock
// (and the second stage's seccomp filter) require. permanent marks
// kernels that cannot support this.
func enforce(executable string) (abi int, permanent bool, err error) {
	abi, err = landlockABI()
	if err != nil {
		return 0, true, err
	}
	if abi < minABI {
		return 0, true, fmt.Errorf("landlock ABI %d is below the required %d", abi, minABI)
	}
	attr := unix.LandlockRulesetAttr{Access_fs: fsAccess(abi)}
	// Older kernels know shorter structs and refuse a longer one.
	size := unsafe.Offsetof(attr.Access_net)
	if abi >= 4 {
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		size = unsafe.Offsetof(attr.Scoped)
	}
	if abi >= 6 {
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
		size = unsafe.Sizeof(attr)
	}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return 0, false, fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()

	allowPath := func(path string, access uint64) error {
		fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
		if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
			return fmt.Errorf("landlock_add_rule(%s): %w", path, errno)
		}
		return nil
	}
	if err := allowPath(executable, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_EXECUTE); err != nil {
		return 0, false, fmt.Errorf("cannot allow executing %s: %w", executable, err)
	}
	for _, dir := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64"} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if err := allowPath(dir, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR|unix.LANDLOCK_ACCESS_FS_EXECUTE); err != nil {
			return 0, false, err
		}
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return 0, false, fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return 0, false, fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return abi, false, nil
}

// confine is the second stage's own restriction: the seccomp filter that
// denies socket creation, process creation, tracing, and signalling,
// applied to every thread.
func confine() error {
	return installSeccomp()
}

// landlockABI asks the kernel which Landlock ABI it supports.
func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	switch {
	case errno == unix.ENOSYS:
		return 0, errors.New("landlock is not available: this kernel was built without it or is older than Linux 5.13")
	case errno == unix.EOPNOTSUPP:
		return 0, errors.New("landlock is disabled on this kernel (not in the lsm= boot parameter)")
	case errno != 0:
		return 0, fmt.Errorf("landlock_create_ruleset(version): %w", errno)
	}
	return int(abi), nil
}

// selfTest proves the restriction from inside the second stage: every
// access the receiver must not have is attempted and must be refused by
// policy (EACCES/EPERM), and the inherited channel must be usable. Any
// other outcome is a failure — an unrestricted receiver never serves.
func selfTest(opts Options) (checks map[string]string, failures []string) {
	checks = map[string]string{}
	denied := func(name string, err error, cleanup func()) {
		switch {
		case err == nil:
			if cleanup != nil {
				cleanup()
			}
			checks[name] = "allowed"
			failures = append(failures, name+" was allowed")
		case errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM):
			checks[name] = "denied"
		default:
			checks[name] = "failed: " + err.Error()
			failures = append(failures, name+" was not refused by policy: "+err.Error())
		}
	}
	if opts.ProbePath != "" {
		f, err := os.Open(opts.ProbePath)
		denied("read_credential", err, func() { _ = f.Close() })
	}
	_, err := os.ReadDir("/")
	denied("list_root", err, nil)
	probe := "/tmp/atc-webhook-receiver-" + strconv.Itoa(os.Getpid())
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	denied("create_file", err, func() { _ = f.Close(); _ = os.Remove(probe) })
	if opts.DenyPort > 0 {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.DenyPort)), 2*time.Second)
		denied("connect_tcp", err, func() { _ = conn.Close() })
		// Landlock lets any sandbox bind an ephemeral port (0); the
		// restriction is on explicit ports, so the probe names one. An
		// unrestricted receiver would see the port in use, not a refusal.
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.DenyPort)))
		denied("bind_tcp", err, func() { _ = listener.Close() })
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	denied("udp_socket", err, func() { _ = packet.Close() })
	unixConn, err := net.DialTimeout("unix", "/run/atc-webhook-receiver-probe.sock", time.Second)
	denied("unix_socket", err, func() { _ = unixConn.Close() })
	// Landlock confines ptrace to the sandbox and seccomp denies it; the
	// parent (Core) is outside. A successful attach stops the tracee, so
	// detach at once.
	parent := os.Getppid()
	err = unix.PtraceAttach(parent)
	denied("trace_parent", err, func() { _ = unix.PtraceDetach(parent) })
	// Signal 0 delivers nothing but exercises the permission check.
	denied("signal_parent", syscall.Kill(parent, 0), nil)
	_, err = syscall.ForkExec("/proc/self/exe", []string{"atc", "version"}, &syscall.ProcAttr{})
	denied("spawn_process", err, nil)

	usable := 0
	for i := range opts.ChannelConns {
		if _, err := unix.Getsockname(channelFD(i)); err == nil {
			usable++
		}
	}
	if usable != opts.ChannelConns {
		failures = append(failures, fmt.Sprintf("channel: %d of %d inherited connections usable", usable, opts.ChannelConns))
	}
	checks["channel"] = strconv.Itoa(usable) + " connections"
	checks["environment"] = strconv.Itoa(len(os.Environ())) + " variables"
	return checks, failures
}
