package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListenerBoundaryAddsListenerContext(t *testing.T) {
	for _, kind := range []ListenerKind{ListenerTCP, ListenerUnix} {
		t.Run(string(kind), func(t *testing.T) {
			var got ListenerContext
			var ok bool

			handler := withListenerBoundary(kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, ok = ListenerFromContext(r.Context())
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			if !ok {
				t.Fatal("listener context missing")
			}
			if got.Kind != kind {
				t.Fatalf("listener kind = %q, want %q", got.Kind, kind)
			}
		})
	}
}
