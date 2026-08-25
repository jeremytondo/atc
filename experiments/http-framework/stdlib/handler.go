// Package stdlib is the net/http-only candidate for the ATC-259 chassis
// spike: Go 1.22+ ServeMux patterns, hand-rolled middleware, no
// dependencies. There is no OpenAPI story here beyond writing the document
// by hand.
package stdlib

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

const (
	clientVersionHeader = "Atc-Client-Version"
	serverVersionHeader = "Atc-Server-Version"
)

type health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// NewHandler builds the chassis surface: authed /v1/health with version
// headers both ways. Middleware order (outermost first): version headers,
// then auth, then routing — headers appear on every response including
// 401s, and unauthenticated callers cannot probe which routes exist.
func NewHandler(token, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, health{Status: "ok", Version: version})
	})
	return withVersionHeaders(version, withAuth(token, mux))
}

func withVersionHeaders(version string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(serverVersionHeader, version)
		// The client's version is available for skew logging; the spike
		// only needs it read, not acted on.
		_ = r.Header.Get(clientVersionHeader)
		next.ServeHTTP(w, r)
	})
}

func withAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="atc"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
