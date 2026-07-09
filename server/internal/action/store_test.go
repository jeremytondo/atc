package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/session"
)

func TestLoadAbsentFileReturnsDefaults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "actions.json"), testDefaults())

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got["claude"].Command != "claude" || got["codex"].Command != "codex" {
		t.Fatalf("actions = %+v, want defaults", got)
	}
}

func TestLoadMergesFileOverDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	writeFile(t, path, `{
  "actions": {
    "codex": {"label":"Codex Pro","command":"/opt/codex","args":["--fast"],"prompt":{}},
    "custom": {"command":"echo","args":["hello"],"params":{"dry-run":{"type":"bool","flag":"--dry-run"}}}
  }
}`)
	store := NewStore(path, testDefaults())

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["claude"].Command != "claude" {
		t.Fatalf("claude = %+v, want default retained", got["claude"])
	}
	if got["codex"].Command != "/opt/codex" || got["codex"].Label != "Codex Pro" {
		t.Fatalf("codex = %+v, want file override", got["codex"])
	}
	if got["custom"].Command != "echo" || got["custom"].Params["dry-run"].Flag != "--dry-run" {
		t.Fatalf("custom = %+v, want file action", got["custom"])
	}
}

func TestLoadAcceptsLegacyBinAndKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	writeFile(t, path, `{"actions":{"lazygit":{"kind":"command","bin":"lazygit"}}}`)
	store := NewStore(path, testDefaults())

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["lazygit"].Command != "lazygit" {
		t.Fatalf("lazygit = %+v, want legacy bin mapped to command", got["lazygit"])
	}
}

func TestLoadInvalidFileErrors(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "bad json", contents: `{not json`},
		{name: "bad name", contents: `{"actions":{"bad/name":{"command":"tool"}}}`},
		{name: "missing command", contents: `{"actions":{"tool":{}}}`},
		{name: "bad legacy kind", contents: `{"actions":{"tool":{"kind":"weird","command":"tool"}}}`},
		{name: "conflicting command and bin", contents: `{"actions":{"tool":{"command":"tool","bin":"/opt/tool"}}}`},
		{name: "bad params", contents: `{"actions":{"tool":{"command":"tool","params":{"model":{"type":"enum"}}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "actions.json")
			writeFile(t, path, tt.contents)
			store := NewStore(path, testDefaults())
			if _, err := store.Load(context.Background()); err == nil {
				t.Fatal("Load err = nil, want error")
			}
		})
	}
}

func TestGetReportsOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	writeFile(t, path, `{"actions":{"codex":{"command":"/opt/codex","prompt":{}}}}`)
	store := NewStore(path, testDefaults())

	_, origin, err := store.Get(context.Background(), "claude")
	if err != nil {
		t.Fatalf("Get claude: %v", err)
	}
	if origin != OriginBuiltin {
		t.Fatalf("claude origin = %q, want builtin", origin)
	}
	codex, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get codex: %v", err)
	}
	if origin != OriginModified || codex.Command != "/opt/codex" {
		t.Fatalf("codex = %+v origin=%q, want modified override", codex, origin)
	}
	_, _, err = store.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	writeFile(t, path, `{"actions":{"custom":{"command":"echo"}}}`)
	store := NewStore(path, testDefaults())

	action := session.Action{Command: "echo"}
	if err := store.Create(context.Background(), "claude", action); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("builtin duplicate err = %v, want ErrDuplicate", err)
	}
	if err := store.Create(context.Background(), "custom", action); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("file duplicate err = %v, want ErrDuplicate", err)
	}
}

func TestUpdateOverridesBuiltinAndCreateCustom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())

	if err := store.Update(context.Background(), "codex", session.Action{
		Command: "/opt/codex",
		Prompt:  &session.PromptSpec{},
	}); err != nil {
		t.Fatalf("Update codex: %v", err)
	}
	if err := store.Update(context.Background(), "custom", session.Action{
		Command: "echo",
		Args:    []string{"hello"},
	}); err != nil {
		t.Fatalf("Update custom: %v", err)
	}

	got, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get codex: %v", err)
	}
	if origin != OriginModified || got.Command != "/opt/codex" {
		t.Fatalf("codex = %+v origin=%q, want modified override", got, origin)
	}
	got, origin, err = store.Get(context.Background(), "custom")
	if err != nil {
		t.Fatalf("Get custom: %v", err)
	}
	if origin != OriginCustom || got.Args[0] != "hello" {
		t.Fatalf("custom = %+v origin=%q, want custom", got, origin)
	}
}

func TestUpdateEqualDefaultDropsOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())
	defaultCodex := session.Action{
		Command: "codex",
		Prompt:  &session.PromptSpec{},
	}

	if err := store.Update(context.Background(), "codex", defaultCodex); err != nil {
		t.Fatalf("Update default codex: %v", err)
	}
	_, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get default codex: %v", err)
	}
	if origin != OriginBuiltin {
		t.Fatalf("codex origin = %q, want builtin", origin)
	}
	assertFileOmitsAction(t, path, "codex")

	if err := store.Update(context.Background(), "codex", session.Action{
		Label:   "Codex Pro",
		Command: "codex",
		Prompt:  &session.PromptSpec{},
	}); err != nil {
		t.Fatalf("Update modified codex: %v", err)
	}
	got, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get modified codex: %v", err)
	}
	if origin != OriginModified || got.Label != "Codex Pro" {
		t.Fatalf("codex = %+v origin=%q, want modified", got, origin)
	}
	assertFileIncludesAction(t, path, "codex")

	if err := store.Update(context.Background(), "codex", defaultCodex); err != nil {
		t.Fatalf("Update back to default: %v", err)
	}
	_, origin, err = store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get reverted codex: %v", err)
	}
	if origin != OriginBuiltin {
		t.Fatalf("reverted codex origin = %q, want builtin", origin)
	}
	assertFileOmitsAction(t, path, "codex")
}

func TestDeleteRevertsOverrideAndRejectsBareBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())
	if err := store.Update(context.Background(), "codex", session.Action{
		Command: "/opt/codex",
		Prompt:  &session.PromptSpec{},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := store.Delete(context.Background(), "codex"); err != nil {
		t.Fatalf("Delete override: %v", err)
	}
	got, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get reverted codex: %v", err)
	}
	if origin != OriginBuiltin || got.Command != "codex" {
		t.Fatalf("reverted codex = %+v origin=%q", got, origin)
	}

	if err := store.Delete(context.Background(), "codex"); !errors.Is(err, ErrBuiltinRemoval) {
		t.Fatalf("Delete builtin err = %v, want ErrBuiltinRemoval", err)
	}
	if err := store.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing err = %v, want ErrNotFound", err)
	}
}

func TestSetEnabledTogglesBuiltinWithoutChangingOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())

	if err := store.SetEnabled(context.Background(), "codex", false); err != nil {
		t.Fatalf("SetEnabled false: %v", err)
	}
	got, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get codex: %v", err)
	}
	// Disabling a built-in materializes an overlay entry, but it is only a toggle,
	// so the reported origin must stay builtin.
	if origin != OriginBuiltin || !got.Disabled || got.Command != "codex" {
		t.Fatalf("disabled codex = %+v origin=%q, want builtin+disabled", got, origin)
	}

	// Re-enabling reverts to the plain built-in and drops the overlay entry.
	if err := store.SetEnabled(context.Background(), "codex", true); err != nil {
		t.Fatalf("SetEnabled true: %v", err)
	}
	got, origin, err = store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get reverted codex: %v", err)
	}
	if origin != OriginBuiltin || got.Disabled {
		t.Fatalf("reverted codex = %+v origin=%q, want enabled builtin", got, origin)
	}
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), `"codex"`) {
		t.Fatalf("overlay still references codex after re-enable: %s", data)
	}
}

func TestSetEnabledKeepsCustomizationOriginModified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())
	if err := store.Update(context.Background(), "codex", session.Action{
		Command: "/opt/codex",
		Prompt:  &session.PromptSpec{},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := store.SetEnabled(context.Background(), "codex", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, origin, err := store.Get(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// A real override that is also disabled stays modified and keeps its command.
	if origin != OriginModified || !got.Disabled || got.Command != "/opt/codex" {
		t.Fatalf("disabled override = %+v origin=%q, want modified+disabled", got, origin)
	}
}

func TestSetEnabledCustomAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	store := NewStore(path, testDefaults())
	if err := store.Update(context.Background(), "custom", session.Action{Command: "echo"}); err != nil {
		t.Fatalf("Update custom: %v", err)
	}
	if err := store.SetEnabled(context.Background(), "custom", false); err != nil {
		t.Fatalf("SetEnabled custom: %v", err)
	}
	got, origin, err := store.Get(context.Background(), "custom")
	if err != nil {
		t.Fatalf("Get custom: %v", err)
	}
	if origin != OriginCustom || !got.Disabled {
		t.Fatalf("disabled custom = %+v origin=%q, want custom+disabled", got, origin)
	}
	if err := store.SetEnabled(context.Background(), "missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetEnabled missing err = %v, want ErrNotFound", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"LazyGit", "lazygit"},
		{"Claude Code", "claude-code"},
		{"Codex", "codex"},
		{"GPT-5 Codex", "gpt-5-codex"},
		{"my_action", "my-action"},
		{"  Spaced  Out!  ", "spaced-out"},
		{"already-a-slug", "already-a-slug"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWriteValidation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "actions.json"), testDefaults())
	tests := []struct {
		name   string
		action session.Action
	}{
		{name: "bad/name", action: session.Action{Command: "tool"}},
		{name: "tool", action: session.Action{}},
		{name: "tool", action: session.Action{Command: "tool", Params: map[string]session.ParamSpec{"model": {Type: "enum"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Update(context.Background(), tt.name, tt.action)
			if !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("Update err = %v, want ErrInvalidAction", err)
			}
		})
	}
}

func TestWriteUsesTemporaryFileRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.json")
	store := NewStore(path, testDefaults())

	if err := store.Update(context.Background(), "custom", session.Action{Command: "echo"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"custom"`) {
		t.Fatalf("actions file = %s, want custom action", data)
	}
	if strings.Contains(string(data), `"bin"`) || strings.Contains(string(data), `"kind"`) {
		t.Fatalf("actions file = %s, want command schema", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func testDefaults() session.ActionRegistry {
	return session.ActionRegistry{
		"claude": {
			Command: "claude",
			Prompt:  &session.PromptSpec{},
		},
		"codex": {
			Command: "codex",
			Prompt:  &session.PromptSpec{},
		},
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func assertFileIncludesAction(t *testing.T, path, name string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if !strings.Contains(string(data), `"`+name+`"`) {
		t.Fatalf("actions file = %s, want %q entry", data, name)
	}
}

func assertFileOmitsAction(t *testing.T, path, name string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if strings.Contains(string(data), `"`+name+`"`) {
		t.Fatalf("actions file = %s, want no %q entry", data, name)
	}
}
