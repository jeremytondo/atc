package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/diagnostics"
)

func TestRoutesHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got diagnostics.Health
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status body = %q, want ok", got.Status)
	}
}

func TestRoutesVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)

	Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got diagnostics.Version
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Name != "atc" || got.Version != "dev" || got.Commit != "unknown" {
		t.Fatalf("version body = %#v", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	oversized := `{"name":"` + strings.Repeat("x", maxJSONBodyBytes) + `"}`
	cases := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
	}{
		{name: "valid single value", body: `{"name":"ok"}`, wantOK: true},
		{name: "valid with trailing whitespace", body: `{"name":"ok"}` + "\n  ", wantOK: true},
		{name: "invalid JSON", body: `{"name":`, wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "trailing second value", body: `{"name":"ok"}{"name":"again"}`, wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "trailing garbage", body: `{"name":"ok"} extra`, wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "body over size limit", body: oversized, wantOK: false, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			var dst payload
			ok := decodeJSON(rec, req, &dst)
			if ok != tc.wantOK {
				t.Fatalf("decodeJSON ok = %v, want %v (body %q)", ok, tc.wantOK, rec.Body.String())
			}
			if !tc.wantOK && rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantOK && dst.Name != "ok" {
				t.Fatalf("decoded name = %q, want ok", dst.Name)
			}
		})
	}
}

func TestRoutesUnknownRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)

	Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("missing JSON error field in %#v", body)
	}
}
