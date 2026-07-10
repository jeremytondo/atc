package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEnvironmentCommandHostLoginShell(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	env := Environment{Kind: EnvironmentKindHostLoginShell}

	got, err := env.Command([]string{"codex", "--prompt", "review this", "it's quoted"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{"/usr/bin/zsh", "-l", "-i", "-c", "codex --prompt 'review this' 'it'\\''s quoted'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestEnvironmentRegistryResolveDefaults(t *testing.T) {
	registry := EnvironmentRegistry{
		"host-login-shell": {Kind: EnvironmentKindHostLoginShell},
	}
	got, name, err := registry.resolve("")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if name != "host-login-shell" || got.Kind != EnvironmentKindHostLoginShell {
		t.Fatalf("resolve default = %q %+v", name, got)
	}

	if _, _, err := registry.resolve("missing"); !errors.Is(err, ErrUnknownEnvironment) {
		t.Fatalf("resolve missing err = %v, want ErrUnknownEnvironment", err)
	}
}

func TestEnvironmentRegistryDiscoverAndValidate(t *testing.T) {
	registry := EnvironmentRegistry{
		"host-login-shell": {
			Kind:        EnvironmentKindHostLoginShell,
			Label:       "Host login shell",
			Description: "Default",
		},
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := registry.Discover(context.Background())
	if len(got) != 1 || got[0].Name != "host-login-shell" || got[0].Kind != EnvironmentKindHostLoginShell || !got[0].Default {
		t.Fatalf("discovery = %+v", got)
	}

	if err := (EnvironmentRegistry{"bad": {Kind: "container"}}).Validate(); !errors.Is(err, ErrEnvironmentMisconfigured) {
		t.Fatalf("unsupported kind err = %v, want ErrEnvironmentMisconfigured", err)
	}
	if err := (EnvironmentRegistry{"bad": {}}).Validate(); !errors.Is(err, ErrEnvironmentMisconfigured) {
		t.Fatalf("missing kind err = %v, want ErrEnvironmentMisconfigured", err)
	}
}
