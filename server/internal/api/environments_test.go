package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/diagnostics"
)

func TestListEnvironmentsReturnsDiscoveryMetadata(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})

	rec := do(t, h, http.MethodGet, "/environments", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got struct {
		Environments []struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Default bool   `json:"default"`
		} `json:"environments"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Environments) != 1 || got.Environments[0].Name != "host-login-shell" || got.Environments[0].Kind != "host-login-shell" || !got.Environments[0].Default {
		t.Fatalf("environments = %+v", got.Environments)
	}
}

func TestListEnvironmentsRequiresService(t *testing.T) {
	rec := do(t, Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil), http.MethodGet, "/environments", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
}
