package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// syncWriter is a strings.Builder safe for the concurrent writes a racy
// warning path would attempt, so the race detector watches the warning
// state itself rather than the test buffer.
type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// The version-skew warning fires once even when concurrent requests
// deliver the server version simultaneously — the callback runs on each
// request's goroutine, and the client supports concurrent use.
func TestNewClientVersionSkewWarnsOnceUnderConcurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(api.ServerVersionHeader, "v9.9.9-skewed")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"v9.9.9-skewed"}`))
	}))
	defer srv.Close()
	t.Setenv("ATC_SERVER", srv.URL)
	t.Setenv("ATC_TOKEN", "atc_cli-test-token")

	var stderr syncWriter
	client, baseURL, err := NewClient(&stderr)
	if err != nil {
		t.Fatal(err)
	}
	if baseURL != srv.URL {
		t.Fatalf("baseURL = %q, want ATC_SERVER %q", baseURL, srv.URL)
	}

	var requests sync.WaitGroup
	for range 8 {
		requests.Go(func() {
			if _, err := client.Health(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	requests.Wait()

	if got := strings.Count(stderr.String(), "run `atc server restart`"); got != 1 {
		t.Errorf("skew warning printed %d times, want exactly once:\n%s", got, stderr.String())
	}
}
