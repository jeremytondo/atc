//go:build linux

package daemon

import (
	"slices"
	"testing"
)

func TestParseSocketInodesForPathPrefersListeningSocket(t *testing.T) {
	content := `Num       RefCount Protocol Flags    Type St Inode Path
00000000: 00000002 00000000 00000000 0001 03 111 /tmp/atc.sock
00000000: 00000002 00000000 00010000 0001 01 222 /tmp/atc.sock
`

	got := parseSocketInodesForPath(content, "/tmp/atc.sock")
	want := []string{"222"}
	if !slices.Equal(got, want) {
		t.Fatalf("inodes = %v, want %v", got, want)
	}
}

func TestParseSocketInodesForPathFallsBackToMatchingInodes(t *testing.T) {
	content := `Num       RefCount Protocol Flags    Type St Inode Path
00000000: 00000002 00000000 00000000 0001 03 111 /tmp/atc.sock
00000000: 00000002 00000000 00000000 0001 03 222 /tmp/atc.sock
`

	got := parseSocketInodesForPath(content, "/tmp/atc.sock")
	want := []string{"111", "222"}
	if !slices.Equal(got, want) {
		t.Fatalf("inodes = %v, want %v", got, want)
	}
}
