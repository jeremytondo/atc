package authtoken

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Path: filepath.Join(t.TempDir(), "auth-token")}
}

func TestEnsureCreatesValidToken(t *testing.T) {
	store := newStore(t)
	token, err := store.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^atc_[A-Za-z0-9_-]{43}$`).MatchString(token) {
		t.Errorf("token %q does not match the contract format", token)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != token+"\n" {
		t.Errorf("file = %q, want token plus newline", data)
	}
}

func TestEnsureIsStableAndReassertsMode(t *testing.T) {
	store := newStore(t)
	first, err := store.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	// A copy or restore may have widened the mode.
	if err := os.Chmod(store.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := store.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("Ensure() minted a new token over an existing one")
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600 re-asserted", perm)
	}
}

func TestEnsureRefusesMalformedFile(t *testing.T) {
	store := newStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, []byte("hand-typed-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ensure(); err == nil || !strings.Contains(err.Error(), "rotate") {
		t.Errorf("Ensure() = %v, want malformed-file error naming the remedy", err)
	}
	if store.Verify("Bearer hand-typed-secret") {
		t.Error("Verify accepted contents Ensure refused")
	}
}

func TestRotateInvalidatesOldTokenImmediately(t *testing.T) {
	store := newStore(t)
	old, err := store.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("Rotate() returned the old token")
	}
	if store.Verify("Bearer " + old) {
		t.Error("old token still verifies after rotation")
	}
	if !store.Verify("Bearer " + fresh) {
		t.Error("fresh token does not verify")
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after rotate = %o, want 600", perm)
	}
}

func TestVerify(t *testing.T) {
	store := newStore(t)
	token, err := store.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		authorization string
		want          bool
	}{
		"exact":            {"Bearer " + token, true},
		"case-insensitive": {"bearer " + token, true},
		"extra whitespace": {"Bearer   " + token, true},
		"missing header":   {"", false},
		"no scheme":        {token, false},
		"wrong token":      {"Bearer atc_" + strings.Repeat("x", 43), false},
		"truncated":        {"Bearer " + token[:len(token)-1], false},
	} {
		if got := store.Verify(tc.authorization); got != tc.want {
			t.Errorf("%s: Verify(%q) = %v, want %v", name, tc.authorization, got, tc.want)
		}
	}
}

func TestVerifyFailsClosedWithoutFile(t *testing.T) {
	store := newStore(t)
	if store.Verify("Bearer atc_" + strings.Repeat("A", 43)) {
		t.Error("Verify accepted a token with no file on disk")
	}
}
