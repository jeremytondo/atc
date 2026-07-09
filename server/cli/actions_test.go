package cli

import (
	"bytes"
	"net/http"
	"testing"
)

func TestActionsListJSONMatchesAPIResponse(t *testing.T) {
	lookup := testRuntimeLookup(t)
	body := `{"actions":[{"name":"codex","label":"Codex","params":{}}]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/actions" {
			t.Fatalf("request = %s %s, want GET /api/actions", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	cmd := actionsCommand(lookup)
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

func TestActionsListText(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"actions":[{"name":"codex","label":"Codex","params":{}},{"name":"lazygit","label":"Lazygit","params":{}}]}`))
	})

	cmd := actionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := "codex\tCodex\nlazygit\tLazygit\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
