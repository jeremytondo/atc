package monitor

import (
	"bytes"
	"syscall"
	"unsafe"
)

// readDirectory uses the proc_info syscall behind libproc's proc_pidinfo,
// keeping CGO_ENABLED=0 builds and avoiding a subprocess on every poll.
// The fixed-width proc_vnodepathinfo ABI holds cwd then root, each a
// 152-byte vnode_info followed by MAXPATHLEN bytes, on both Darwin targets.
// Definitions: https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc_info.h
func readDirectory(pid int) string {
	const (
		procInfoCallPIDInfo  = 2
		procPIDVnodePathInfo = 9
	)
	type vnodePath struct {
		info [152]byte
		path [1024]byte
	}
	var info struct{ cwd, root vnodePath }
	n, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO,
		procInfoCallPIDInfo, uintptr(pid), procPIDVnodePathInfo, 0,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 || n != unsafe.Sizeof(info) {
		return ""
	}
	end := bytes.IndexByte(info.cwd.path[:], 0)
	if end < 0 {
		return ""
	}
	return string(info.cwd.path[:end])
}
