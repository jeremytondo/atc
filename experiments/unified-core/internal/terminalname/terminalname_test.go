package terminalname

import (
	"strings"
	"testing"
)

func TestNameFitsConstrainedZmxSocketDirectory(t *testing.T) {
	name := FromID("term_123456789012345678901234")
	if len(name) > 31 {
		t.Fatalf("name %q is %d bytes", name, len(name))
	}
	if !strings.HasPrefix(name, Prefix) || name != FromID("term_123456789012345678901234") {
		t.Fatalf("name = %q", name)
	}
	if name == FromID("term_abcdefghijklmnopqrstuvwx") {
		t.Fatal("different terminal IDs collided")
	}
}

func TestManagedRecognizesCurrentAndPersistedNamespaces(t *testing.T) {
	if !IsManaged("atcu-1234") || !IsManaged("atc-unified-term_old") || IsManaged("user-session") {
		t.Fatal("managed namespace classification is incorrect")
	}
}
