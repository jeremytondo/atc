package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/store"
)

func TestActionsLifecycleJSON(t *testing.T) {
	lookup := testRuntimeLookup(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	handler := api.Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, st, nil)
	serveUnixAPI(t, lookup, http.StripPrefix("/api", handler).ServeHTTP)

	create := runActionsCLI(t, lookup, "create", "--name", "Dev server", "--command", "npm", "--arg", "run", "--arg", "dev", "-o", "json")
	var action api.Action
	if err := json.Unmarshal([]byte(create), &action); err != nil {
		t.Fatalf("decode create: %v (%s)", err, create)
	}
	if action.ID == "" || action.Name != "Dev server" || !action.Enabled || !strings.EqualFold(action.Command, "npm") {
		t.Fatalf("created = %+v", action)
	}

	update := runActionsCLI(t, lookup, "update", action.ID, "--description", "Local app", "--agent=true", "--enabled=false", "-o", "json")
	if err := json.Unmarshal([]byte(update), &action); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if action.Description != "Local app" || !action.IsAgent || action.Enabled {
		t.Fatalf("updated = %+v", action)
	}

	runActionsCLI(t, lookup, "enable", action.ID, "-o", "json")
	shown := runActionsCLI(t, lookup, "show", action.ID, "-o", "json")
	if err := json.Unmarshal([]byte(shown), &action); err != nil || !action.Enabled {
		t.Fatalf("shown = %s err=%v", shown, err)
	}
	list := runActionsCLI(t, lookup, "list", "-o", "json")
	if !strings.Contains(list, action.ID) || !strings.Contains(list, `"args":["run","dev"]`) {
		t.Fatalf("list = %s", list)
	}
	deleted := runActionsCLI(t, lookup, "delete", action.ID, "-o", "json")
	if !strings.Contains(deleted, action.ID) {
		t.Fatalf("delete output = %s", deleted)
	}
}

func TestActionsLifecycleText(t *testing.T) {
	lookup := testRuntimeLookup(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	handler := api.Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, st, nil)
	serveUnixAPI(t, lookup, http.StripPrefix("/api", handler).ServeHTTP)

	created := runActionsCLI(t, lookup, "create", "--name", "Editor", "--command", "nvim", "--description", "Edit files")
	if !strings.Contains(created, "Editor") || !strings.Contains(created, "nvim") || !strings.Contains(created, "Edit files") {
		t.Fatalf("create output = %q", created)
	}
	actions, err := st.ListActions(t.Context())
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	var id string
	for _, action := range actions {
		if action.Name == "Editor" {
			id = action.ID
		}
	}
	if id == "" {
		t.Fatal("Editor action not found")
	}
	if got := runActionsCLI(t, lookup, "disable", id); !strings.Contains(got, "false") {
		t.Fatalf("disable output = %q", got)
	}
	if got := runActionsCLI(t, lookup, "update", id, "--clear-description", "--arg", "README.md"); !strings.Contains(got, "nvim README.md") {
		t.Fatalf("update output = %q", got)
	}
	if got := runActionsCLI(t, lookup, "show", id); !strings.Contains(got, id) {
		t.Fatalf("show output = %q", got)
	}
	if got := runActionsCLI(t, lookup, "list"); !strings.Contains(got, "ID") || !strings.Contains(got, id) {
		t.Fatalf("list output = %q", got)
	}
	if got := runActionsCLI(t, lookup, "delete", id); !strings.Contains(got, id) {
		t.Fatalf("delete output = %q", got)
	}
}

func runActionsCLI(t *testing.T, lookup envLookup, args ...string) string {
	t.Helper()
	cmd := actionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("actions %v: %v", args, err)
	}
	return out.String()
}
