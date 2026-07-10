package cli

import (
	"bytes"
	"net/http"
	"testing"
)

func TestEnvironmentsListJSONMatchesAPIResponse(t *testing.T) {
	lookup := testRuntimeLookup(t)
	body := `{"environments":[{"name":"host-login-shell","kind":"host-login-shell","label":"Host login shell","default":true}]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/environments" {
			t.Fatalf("request = %s %s, want GET /api/environments", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	cmd := environmentsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != body+"\n" {
		t.Fatalf("output = %q, want raw JSON response", got)
	}
}

func TestEnvironmentsListText(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"environments":[{"name":"host-login-shell","kind":"host-login-shell","label":"Host login shell","default":true}]}`))
	})

	cmd := environmentsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := "host-login-shell\thost-login-shell\tdefault\tHost login shell\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
