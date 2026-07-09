package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebAssetRoutesServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	webAssetRoutes(fstest.MapFS{
		"index.html": {Data: []byte("<h1>Atelier Code is running</h1>")},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Atelier Code is running") {
		t.Fatalf("body = %q, want Atelier Code page", rec.Body.String())
	}
}

func TestWebAssetRoutesServesStaticAsset(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_app/immutable/app.css", nil)

	webAssetRoutes(fstest.MapFS{
		"index.html":             {Data: []byte("<h1>Atelier Code</h1>")},
		"_app/immutable/app.css": {Data: []byte("body{color:white}")},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "body{color:white}" {
		t.Fatalf("body = %q", got)
	}
}

func TestWebAssetRoutesFallsBackToShellForBrowserRoutes(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/future/browser/route", nil)

	webAssetRoutes(fstest.MapFS{
		"index.html":    {Data: []byte("<h1>Atelier Code index</h1>")},
		"fallback.html": {Data: []byte("<h1>Atelier Code shell</h1>")},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Atelier Code shell") {
		t.Fatalf("body = %q, want fallback shell", rec.Body.String())
	}
}

func TestWebAssetRoutesReturnsNotFoundForMissingStaticAssets(t *testing.T) {
	tests := []string{
		"/_app/immutable/missing.js",
		"/favicon.ico",
		"/assets/missing.css",
		"/missing.html",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)

			webAssetRoutes(fstest.MapFS{
				"index.html":    {Data: []byte("<h1>Atelier Code index</h1>")},
				"fallback.html": {Data: []byte("<h1>Atelier Code shell</h1>")},
			}).ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if strings.Contains(rec.Body.String(), "Atelier Code shell") {
				t.Fatalf("body = %q, want static asset 404 instead of SPA fallback", rec.Body.String())
			}
		})
	}
}

func TestWebAssetRoutesDoesNotServeHiddenSentinels(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_app/.embedkeep", nil)

	webAssetRoutes(fstest.MapFS{
		"index.html":      {Data: []byte("<h1>Atelier Code shell</h1>")},
		"_app/.embedkeep": {Data: []byte("sentinel")},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "sentinel") {
		t.Fatalf("body = %q, want hidden sentinel to remain unserved", rec.Body.String())
	}
}

func TestWebAssetRoutesFailsClearlyWithoutStagedShell(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	webAssetRoutes(fstest.MapFS{
		".embedkeep": {Data: []byte("not the web app")},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "embedded web assets have not been staged") {
		t.Fatalf("body = %q, want staging guidance", rec.Body.String())
	}
}
