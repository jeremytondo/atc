// Package huma is the Huma v2 candidate for the ATC-259 chassis spike:
// operations registered against Go types, OpenAPI 3.1 generated from those
// types and served by the framework, mounted on the standard library
// ServeMux via the humago adapter.
//
// Auth and version headers deliberately stay plain net/http middleware
// wrapping the mux (identical to the stdlib candidate) rather than Huma
// operation middleware: Huma middleware only runs for registered
// operations, so it cannot enforce "401 before route discovery" on
// unknown paths, and the chassis wants one auth story for every byte
// served, docs endpoints included.
package huma

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

const (
	clientVersionHeader = "Atc-Client-Version"
	serverVersionHeader = "Atc-Server-Version"
)

// HealthOutput doubles as the response contract and its documentation;
// this struct is what the generated OpenAPI schema is derived from.
type HealthOutput struct {
	Body struct {
		Status  string `json:"status" enum:"ok" doc:"Liveness state of the server."`
		Version string `json:"version" doc:"Version of the running server binary."`
	}
}

// NewHandler builds the same chassis surface as the stdlib candidate,
// plus /openapi.json and /docs for free. Middleware order matches
// stdlib: version headers, then auth, then routing.
func NewHandler(token, version string) http.Handler {
	mux := http.NewServeMux()

	config := huma.DefaultConfig("ATC API", version)
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer"},
	}
	// Declared globally so the generated document tells adapter authors
	// every operation needs the token; enforcement happens in middleware.
	config.Security = []map[string][]string{{"bearerAuth": {}}}
	api := humago.New(mux, config)

	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/v1/health",
		Summary:     "Server liveness",
		Description: "Source of truth for whether the server is up.",
	}, func(ctx context.Context, _ *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "ok"
		out.Body.Version = version
		return out, nil
	})

	return withVersionHeaders(version, withAuth(token, mux))
}

func withVersionHeaders(version string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(serverVersionHeader, version)
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid or missing token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
