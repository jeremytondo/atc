package receiver

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The ABI floor exists because older Landlock cannot deny truncation;
// the ruleset built at that floor must handle it.
func TestRequiredABIMediatesTruncation(t *testing.T) {
	if fsAccess(minABI)&unix.LANDLOCK_ACCESS_FS_TRUNCATE == 0 {
		t.Fatalf("fsAccess(%d) does not handle LANDLOCK_ACCESS_FS_TRUNCATE; truncate would reach any file the server user can write", minABI)
	}
}
