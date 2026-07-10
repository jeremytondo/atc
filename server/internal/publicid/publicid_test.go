package publicid

import "testing"

func TestNewIsWellFormedAndUnique(t *testing.T) {
	const prefix = "ses_"
	seen := make(map[string]bool)
	for range 1000 {
		id, err := New(prefix)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if len(id) != len(prefix)+26 {
			t.Fatalf("id %q len = %d, want %d", id, len(id), len(prefix)+26)
		}
		if id[:len(prefix)] != prefix {
			t.Fatalf("id %q missing %s prefix", id, prefix)
		}
		for _, c := range id[len(prefix):] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'v')) {
				t.Fatalf("id %q has non-base32hex char %q", id, c)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
