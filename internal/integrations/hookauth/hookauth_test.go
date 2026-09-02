package hookauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPrepareReplacesEarlierLaunch(t *testing.T) {
	registry, err := NewRegistry[struct{}](t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := registry.Prepare("term-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("header mode = %o, want 0600", info.Mode().Perm())
	}
	first := readSecret(t, path)
	deliver := func(secret string) (terminalID string, code int) {
		code = registry.Deliver(secret, func(id string, _ *struct{}) int {
			terminalID = id
			return http.StatusNoContent
		})
		return terminalID, code
	}
	if id, code := deliver(first); code != http.StatusNoContent || id != "term-aaaaa" {
		t.Fatalf("deliver(first) = %q, %d", id, code)
	}
	if _, code := deliver("unknown"); code != http.StatusNotFound {
		t.Errorf("unknown secret: got %d, want 404", code)
	}

	// A relaunch replaces the registration: the old secret stops
	// validating and the new one delivers.
	if _, err := registry.Prepare("term-aaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, code := deliver(first); code != http.StatusNotFound {
		t.Errorf("replaced secret still validates")
	}
	if id, code := deliver(readSecret(t, path)); code != http.StatusNoContent || id != "term-aaaaa" {
		t.Errorf("fresh secret: got %q, %d", id, code)
	}
}

// Per-launch state persists across deliveries and dies with replacement.
func TestDeliverCarriesState(t *testing.T) {
	registry, err := NewRegistry[int](t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := registry.Prepare("term-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	secret := readSecret(t, path)
	bump := func() (state int) {
		registry.Deliver(secret, func(_ string, s *int) int {
			*s++
			state = *s
			return http.StatusNoContent
		})
		return state
	}
	if bump(); bump() != 2 {
		t.Error("state did not persist across deliveries")
	}
	if _, err := registry.Prepare("term-aaaaa"); err != nil {
		t.Fatal(err)
	}
	secret = readSecret(t, path)
	if bump() != 1 {
		t.Error("replacement did not reset state")
	}
}

// Deregistration is the delete-time cleanup: the secret stops validating
// and the header file goes, without waiting for the next boot's Load.
func TestDeregister(t *testing.T) {
	registry, err := NewRegistry[struct{}](t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := registry.Prepare("term-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	secret := readSecret(t, path)

	registry.Deregister("term-aaaaa")
	if code := registry.Deliver(secret, func(string, *struct{}) int { return http.StatusNoContent }); code != http.StatusNotFound {
		t.Errorf("deregistered secret: got %d, want 404", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("deregistered header file survived")
	}
	registry.Deregister("term-unknown") // a no-op, never a panic
}

func TestLoadCleansAndKeeps(t *testing.T) {
	dir := t.TempDir()
	registry, err := NewRegistry[struct{}](dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := registry.Prepare("term-live")
	if err != nil {
		t.Fatal(err)
	}
	secret := readSecret(t, live)
	if _, err := registry.Prepare("term-gone"); err != nil {
		t.Fatal(err)
	}
	// Malformed content is discarded like a stale launch.
	if err := os.WriteFile(registry.HeaderPath("term-junk"), []byte("not a header line"), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewRegistry[struct{}](dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	err = reloaded.Load(func(terminalID string) bool { return terminalID == "term-live" },
		func(terminalID string) { removed = append(removed, terminalID) })
	if err != nil {
		t.Fatal(err)
	}
	if code := reloaded.Deliver(secret, func(string, *struct{}) int { return http.StatusNoContent }); code != http.StatusNoContent {
		t.Error("live secret did not reload")
	}
	for _, stale := range []string{"term-gone", "term-junk"} {
		if _, err := os.Stat(reloaded.HeaderPath(stale)); !os.IsNotExist(err) {
			t.Errorf("stale header %s survived", stale)
		}
	}
	if len(removed) != 2 {
		t.Errorf("remove callbacks = %v", removed)
	}
}

func TestHandlerShell(t *testing.T) {
	var gotSecret string
	var gotBody []byte
	handler := Handler(func(_ context.Context, secret string, body []byte) int {
		gotSecret, gotBody = secret, body
		return http.StatusNoContent
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d, want 405", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
	req.Header.Set(SecretHeader, "s3cret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || gotSecret != "s3cret" || string(gotBody) != `{"a":1}` {
		t.Errorf("POST: code %d, secret %q, body %q", rec.Code, gotSecret, gotBody)
	}

	// An oversized body is truncated at the bound, not buffered without
	// limit; the deliverer sees at most maxPayloadBytes.
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", maxPayloadBytes+1024)))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if len(gotBody) != maxPayloadBytes {
		t.Errorf("oversized body reached deliverer at %d bytes", len(gotBody))
	}
}

func readSecret(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := strings.CutPrefix(string(content), SecretHeader+": ")
	if !ok {
		t.Fatalf("header file %q lacks the header prefix", content)
	}
	return secret
}
