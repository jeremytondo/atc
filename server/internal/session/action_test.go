package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func paramActions() ActionRegistry {
	return ActionRegistry{
		"claude": {Command: "claude", Args: []string{"--print"}},
		"tool": {
			Command: "tool",
			Params: map[string]ParamSpec{
				"model":  {Type: "enum", Values: []string{"opus", "sonnet"}, Flag: "--model"},
				"resume": {Type: "bool", Flag: "--resume"},
			},
		},
		"spacey": {Command: "/opt/my tools/agent"},
	}
}

func TestActionCommand(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		params  map[string]any
		want    []string
		wantErr error
	}{
		{name: "bare command with base args", action: "claude", want: []string{"claude", "--print"}},
		{name: "enum and bool", action: "tool", params: map[string]any{"model": "opus", "resume": true}, want: []string{"tool", "--model", "opus", "--resume"}},
		{name: "bool false omits flag", action: "tool", params: map[string]any{"resume": false}, want: []string{"tool"}},
		{name: "bool as string", action: "tool", params: map[string]any{"resume": "true"}, want: []string{"tool", "--resume"}},
		{name: "deterministic order", action: "tool", params: map[string]any{"resume": true, "model": "sonnet"}, want: []string{"tool", "--model", "sonnet", "--resume"}},
		{name: "command with space passes through untouched", action: "spacey", want: []string{"/opt/my tools/agent"}},
		{name: "unknown action", action: "ghost", wantErr: ErrUnknownAction},
		{name: "unknown param", action: "tool", params: map[string]any{"nope": "x"}, wantErr: ErrInvalidParam},
		{name: "bad enum value", action: "tool", params: map[string]any{"model": "gpt"}, wantErr: ErrInvalidParam},
		{name: "enum wrong type", action: "tool", params: map[string]any{"model": 7}, wantErr: ErrInvalidParam},
		{name: "bool wrong value", action: "tool", params: map[string]any{"resume": "yes"}, wantErr: ErrInvalidParam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := paramActions().buildCommand(tt.action, tt.params, "")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("command = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestActionCommandMisconfiguredCommand(t *testing.T) {
	registry := ActionRegistry{"empty": {Command: "  "}}
	_, _, err := registry.buildCommand("empty", nil, "")
	if !errors.Is(err, ErrActionMisconfigured) {
		t.Fatalf("err = %v, want ErrActionMisconfigured", err)
	}
}

func TestActionCommandMisconfiguredParams(t *testing.T) {
	tests := []struct {
		name   string
		spec   ParamSpec
		params map[string]any
	}{
		{
			name:   "bool without flag",
			spec:   ParamSpec{Type: "bool"},
			params: map[string]any{"p": true},
		},
		{
			name:   "unsupported type",
			spec:   ParamSpec{Type: "enmu", Flag: "--p"},
			params: map[string]any{"p": "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := ActionRegistry{"a": {Command: "a", Params: map[string]ParamSpec{"p": tt.spec}}}
			_, _, err := registry.buildCommand("a", tt.params, "")
			if !errors.Is(err, ErrActionMisconfigured) {
				t.Fatalf("err = %v, want ErrActionMisconfigured", err)
			}
		})
	}
}

func TestActionCommandBoolFalseWithoutFlagIsFine(t *testing.T) {
	// A no-flag bool only faults when it would emit the flag; false is a no-op.
	registry := ActionRegistry{"a": {Command: "a", Params: map[string]ParamSpec{"p": {Type: "bool"}}}}
	got, _, err := registry.buildCommand("a", map[string]any{"p": false}, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("command = %#v, want %#v", got, []string{"a"})
	}
}

func TestActionRegistryValidate(t *testing.T) {
	tests := []struct {
		name    string
		action  Action
		wantErr bool
	}{
		{name: "valid enum default", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "enum", Values: []string{"a", "b"}, Default: "b"}}}},
		{name: "valid false bool without flag", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "bool", Default: false}}}},
		{name: "valid prompt", action: Action{Command: "codex", Prompt: &PromptSpec{}}},
		{name: "missing command", action: Action{}, wantErr: true},
		{name: "bad enum default", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "enum", Values: []string{"a"}, Default: "b"}}}, wantErr: true},
		{name: "enum without values", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "enum"}}}, wantErr: true},
		{name: "bad bool default", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "bool", Default: "yes"}}}, wantErr: true},
		{name: "true bool without flag", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "bool", Default: true}}}, wantErr: true},
		{name: "unsupported type", action: Action{Command: "codex", Params: map[string]ParamSpec{"p": {Type: "string"}}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := ActionRegistry{"codex": tt.action}
			err := registry.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrActionMisconfigured) {
					t.Fatalf("err = %v, want ErrActionMisconfigured", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestActionDiscoveryIsStableAndSafe(t *testing.T) {
	registry := ActionRegistry{
		"zeta": {Command: "missing-zeta"},
		"alpha": {
			Command: "missing-alpha",
			Prompt:  &PromptSpec{Flag: "--prompt"},
			Params: map[string]ParamSpec{
				"model": {Type: "enum", Values: []string{"a", "b"}, Default: "b", Flag: "--model"},
			},
		},
	}

	got := registry.Discover(context.Background())
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("actions = %+v, want alpha then zeta", got)
	}
	got[0].Params["model"] = ParamSpec{Type: "bool"}
	got[0].Prompt.Flag = "--changed"

	if registry["alpha"].Params["model"].Type != "enum" {
		t.Fatalf("discovery params mutated registry: %+v", registry["alpha"].Params["model"])
	}
	if registry["alpha"].Prompt.Flag != "--prompt" {
		t.Fatalf("discovery prompt mutated registry: %+v", registry["alpha"].Prompt)
	}
}
