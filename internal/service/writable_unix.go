//go:build unix

package service

import "syscall"

// dirWritable reports whether this process may create entries in dir —
// what staging a new executable beside the old one and renaming over it
// require. The access check answers for the real credentials, ACLs and
// read-only mounts included.
func dirWritable(dir string) bool {
	const writeOK = 0x2
	return syscall.Access(dir, writeOK) == nil
}
