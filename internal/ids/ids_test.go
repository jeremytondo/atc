package ids

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Random UUIDs differ; derived ones are the same for the same key and
// differ across keys, all in the version 4 shape.
func TestUUIDs(t *testing.T) {
	if a, b := UUID(), UUID(); a == b || !uuidPattern.MatchString(a) {
		t.Errorf("UUID() = %s, %s", a, b)
	}
	if a, b := UUIDFrom("k"), UUIDFrom("k"); a != b || !uuidPattern.MatchString(a) {
		t.Errorf("UUIDFrom(k) = %s, %s; want equal", a, b)
	}
	if UUIDFrom("k") == UUIDFrom("l") {
		t.Error("UUIDFrom collapses distinct keys")
	}
}

// Derived ids are stable per key, distinct across keys, and in the long
// format.
func TestDerive(t *testing.T) {
	a, b := Derive("perm-", "t3code/t1/req-1"), Derive("perm-", "t3code/t1/req-1")
	if a != b || !regexp.MustCompile(`^perm-[a-z2-9]{10}$`).MatchString(a) {
		t.Errorf("Derive = %s, %s; want equal, long format", a, b)
	}
	if Derive("perm-", "t3code/t1/req-1") == Derive("perm-", "t3code/t1/req-2") {
		t.Error("Derive collapses distinct keys")
	}
}
