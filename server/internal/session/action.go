package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Action-related sentinel errors. These map to HTTP 400 (caller error) except
// ErrActionMisconfigured, which is an operator/config fault and maps to 500.
var (
	// ErrUnknownAction is returned when start names an action absent from the
	// registry.
	ErrUnknownAction = errors.New("unknown action")
	// ErrInvalidParam is returned when a start parameter is unknown for the
	// action or has the wrong type or value.
	ErrInvalidParam = errors.New("invalid action parameter")
	// ErrActionMisconfigured is returned when a registered action cannot produce a
	// command.
	ErrActionMisconfigured = errors.New("action is misconfigured")
	// ErrActionDisabled is returned when start names an action that has been
	// disabled. Disabled actions stay visible in discovery but cannot launch.
	ErrActionDisabled = errors.New("action is disabled")
)

// PromptSpec describes how an initial prompt is passed to an action. An empty
// Flag makes the prompt a positional argument.
type PromptSpec struct {
	Flag string `toml:"flag" json:"flag,omitempty"`
}

// ParamSpec is the closed definition of one parameter an action accepts. Only
// "enum" and "bool" parameters exist: free-form string parameters are
// deliberately unsupported so request data is never interpolated raw into the
// launched command.
type ParamSpec struct {
	// Type is "enum" or "bool".
	Type string `toml:"type" json:"type"`
	// Values is the allowed set for an enum parameter.
	Values []string `toml:"values" json:"values"`
	// Default is the value applied when callers omit the parameter.
	Default any `toml:"default" json:"default"`
	// Flag is the command-line flag emitted for the parameter. For an enum it
	// precedes the chosen value; for a bool it is emitted alone when true. An
	// empty flag on an enum emits the value positionally.
	Flag string `toml:"flag" json:"flag"`
	// Label is optional display metadata for clients.
	Label string `toml:"label" json:"label"`
	// Description is optional display metadata for clients.
	Description string `toml:"description" json:"description"`
}

// Action is a launchable command template: an executable command, fixed base
// arguments, optional display metadata, prompt placement, and a closed set of
// typed parameters.
// Everything here is operator-defined config.
type Action struct {
	Label       string               `toml:"label" json:"label,omitempty"`
	Description string               `toml:"description" json:"description,omitempty"`
	Command     string               `toml:"command" json:"command"`
	Args        []string             `toml:"args" json:"args,omitempty"`
	Prompt      *PromptSpec          `toml:"prompt" json:"prompt,omitempty"`
	Params      map[string]ParamSpec `toml:"params" json:"params,omitempty"`
	// Disabled hides the action from launch. It stays in discovery so operators
	// can re-enable it, but a start request naming it is rejected. Absent in the
	// overlay file means enabled, so built-ins and existing configs stay live.
	Disabled bool `toml:"disabled" json:"disabled,omitempty"`
}

// UnmarshalJSON accepts the current action schema and the pre-release
// `bin`/`kind` spelling. `kind` used to distinguish agent from command actions;
// prompt support is now expressed by the presence of `prompt`.
func (a *Action) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind        string               `json:"kind"`
		Label       string               `json:"label"`
		Description string               `json:"description"`
		Command     string               `json:"command"`
		Bin         string               `json:"bin"`
		Args        []string             `json:"args"`
		Prompt      *PromptSpec          `json:"prompt"`
		Params      map[string]ParamSpec `json:"params"`
		Disabled    bool                 `json:"disabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Kind {
	case "", "command", "agent":
	default:
		return fmt.Errorf("unsupported legacy kind %q", raw.Kind)
	}
	if raw.Command != "" && raw.Bin != "" && raw.Command != raw.Bin {
		return fmt.Errorf("command %q conflicts with legacy bin %q", raw.Command, raw.Bin)
	}
	command := raw.Command
	if command == "" {
		command = raw.Bin
	}
	*a = Action{
		Label:       raw.Label,
		Description: raw.Description,
		Command:     command,
		Args:        raw.Args,
		Prompt:      raw.Prompt,
		Params:      raw.Params,
		Disabled:    raw.Disabled,
	}
	return nil
}

// ActionRegistry maps action names to their definitions.
type ActionRegistry map[string]Action

// DefaultActions is the built-in action registry. Built-ins are always present
// underneath file-managed actions and can be overridden, but not removed.
func DefaultActions() ActionRegistry {
	return ActionRegistry{
		"claude": {
			Label:       "Claude",
			Description: "Claude Code CLI",
			Command:     "claude",
			Prompt:      &PromptSpec{},
		},
		"codex": {
			Label:       "Codex",
			Description: "OpenAI Codex CLI",
			Command:     "codex",
			Prompt:      &PromptSpec{},
		},
	}
}

// Load lets an in-memory registry satisfy ActionLoader in tests that do not
// need a file-backed store.
func (r ActionRegistry) Load(ctx context.Context) (ActionRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Clone(), nil
}

// Clone returns a deep enough copy of the registry for callers to mutate safely.
func (r ActionRegistry) Clone() ActionRegistry {
	clone := make(ActionRegistry, len(r))
	for name, action := range r {
		clone[name] = action.Clone()
	}
	return clone
}

// Clone returns a copy of the action, including slices and nested maps.
func (a Action) Clone() Action {
	if a.Args != nil {
		a.Args = append([]string{}, a.Args...)
	}
	a.Prompt = clonePromptSpec(a.Prompt)
	a.Params = cloneParamSpecs(a.Params)
	return a
}

// ActionDiscovery is the safe client-facing shape for one configured action.
type ActionDiscovery struct {
	Name        string
	Label       string
	Description string
	Prompt      *PromptSpec
	Params      map[string]ParamSpec
	Disabled    bool
}

// Discover returns configured actions in stable name order without exposing
// operator launch internals such as Command and Args.
func (r ActionRegistry) Discover(ctx context.Context) []ActionDiscovery {
	names := r.names()
	actions := make([]ActionDiscovery, 0, len(names))
	for _, name := range names {
		action := r[name]
		actions = append(actions, ActionDiscovery{
			Name:        name,
			Label:       action.Label,
			Description: action.Description,
			Prompt:      clonePromptSpec(action.Prompt),
			Params:      cloneParamSpecs(action.Params),
			Disabled:    action.Disabled,
		})
	}
	return actions
}

// Validate checks static action parameter configuration that would otherwise
// surface only when a start request tries to apply defaults.
func (r ActionRegistry) Validate() error {
	for _, actionName := range r.names() {
		action := r[actionName]
		if strings.TrimSpace(action.Command) == "" {
			return fmt.Errorf("%w: action %q must define command", ErrActionMisconfigured, actionName)
		}
		keys := make([]string, 0, len(action.Params))
		for key := range action.Params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := validateParamSpec(actionName, key, action.Params[key]); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildCommand validates an action start request and returns the inner argv
// plus the accepted params that should be stored with the session.
func (r ActionRegistry) buildCommand(name string, params map[string]any, prompt string) ([]string, map[string]any, error) {
	action, ok := r[name]
	if !ok {
		return nil, nil, fmt.Errorf("%w %q: valid actions are %s", ErrUnknownAction, name, strings.Join(r.names(), ", "))
	}
	if action.Disabled {
		return nil, nil, fmt.Errorf("%w: action %q is disabled", ErrActionDisabled, name)
	}
	if strings.TrimSpace(action.Command) == "" {
		return nil, nil, fmt.Errorf("%w: action %q has no command", ErrActionMisconfigured, name)
	}

	tokens := append([]string{action.Command}, action.Args...)
	extra, accepted, err := action.resolveParams(params)
	if err != nil {
		return nil, nil, err
	}
	tokens = append(tokens, extra...)

	if prompt != "" {
		if action.Prompt == nil {
			return nil, nil, fmt.Errorf("%w: action %q does not accept an initial prompt", ErrInvalidParam, name)
		}
		if action.Prompt.Flag != "" {
			tokens = append(tokens, action.Prompt.Flag)
		}
		tokens = append(tokens, prompt)
	}

	return tokens, accepted, nil
}

// names returns the registered action names in stable order for error messages.
func (r ActionRegistry) names() []string {
	return slices.Sorted(maps.Keys(r))
}

// resolveParams validates params against the action's spec and returns the extra
// command tokens they contribute, in deterministic order. Defaults are applied
// as accepted params and emitted just like caller-provided values.
func (a Action) resolveParams(params map[string]any) ([]string, map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	for key := range params {
		if _, ok := a.Params[key]; !ok {
			return nil, nil, fmt.Errorf("%w: %q is not accepted by this action", ErrInvalidParam, key)
		}
	}

	keys := make([]string, 0, len(a.Params))
	for key := range a.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	accepted := make(map[string]any)
	var tokens []string
	for _, key := range keys {
		spec := a.Params[key]
		raw, provided := params[key]
		if !provided {
			if spec.Default == nil {
				continue
			}
			raw = spec.Default
		}

		switch spec.Type {
		case "enum":
			value, err := enumValue(key, spec, raw, !provided)
			if err != nil {
				return nil, nil, err
			}
			accepted[key] = value
			if spec.Flag != "" {
				tokens = append(tokens, spec.Flag)
			}
			tokens = append(tokens, value)
		case "bool":
			on, err := boolValue(key, raw, !provided)
			if err != nil {
				return nil, nil, err
			}
			accepted[key] = on
			if !on {
				continue
			}
			if spec.Flag == "" {
				return nil, nil, fmt.Errorf("%w: bool parameter %q has no flag", ErrActionMisconfigured, key)
			}
			tokens = append(tokens, spec.Flag)
		default:
			return nil, nil, fmt.Errorf("%w: parameter %q has unsupported type %q", ErrActionMisconfigured, key, spec.Type)
		}
	}
	return tokens, accepted, nil
}

// enumValue checks that raw is one of the spec's allowed string values.
func enumValue(key string, spec ParamSpec, raw any, fromDefault bool) (string, error) {
	s, ok := raw.(string)
	if !ok {
		if fromDefault {
			return "", fmt.Errorf("%w: default for enum parameter %q must be a string", ErrActionMisconfigured, key)
		}
		return "", fmt.Errorf("%w: parameter %q must be a string", ErrInvalidParam, key)
	}
	if slices.Contains(spec.Values, s) {
		return s, nil
	}
	if fromDefault {
		return "", fmt.Errorf("%w: default %q is not valid for enum parameter %q", ErrActionMisconfigured, s, key)
	}
	return "", fmt.Errorf("%w: %q is not a valid value for %q (allowed: %s)", ErrInvalidParam, s, key, strings.Join(spec.Values, ", "))
}

// boolValue accepts a JSON bool or the strings "true"/"false"; the current CLI
// sends every parameter as a string, while JSON API clients can send a native
// bool. Defaults with the wrong type are operator misconfiguration.
func boolValue(key string, raw any, fromDefault bool) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		switch v {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	if fromDefault {
		return false, fmt.Errorf("%w: default for bool parameter %q must be true or false", ErrActionMisconfigured, key)
	}
	return false, fmt.Errorf("%w: parameter %q must be true or false", ErrInvalidParam, key)
}

func validateParamSpec(actionName, key string, spec ParamSpec) error {
	switch spec.Type {
	case "enum":
		if len(spec.Values) == 0 {
			return fmt.Errorf("%w: enum parameter %q for action %q must define values", ErrActionMisconfigured, key, actionName)
		}
		if spec.Default != nil {
			if _, err := enumValue(key, spec, spec.Default, true); err != nil {
				return fmt.Errorf("%w for action %q", err, actionName)
			}
		}
	case "bool":
		if spec.Default != nil {
			on, err := boolValue(key, spec.Default, true)
			if err != nil {
				return fmt.Errorf("%w for action %q", err, actionName)
			}
			if on && spec.Flag == "" {
				return fmt.Errorf("%w: bool parameter %q for action %q defaults true but has no flag", ErrActionMisconfigured, key, actionName)
			}
		}
	default:
		return fmt.Errorf("%w: parameter %q for action %q has unsupported type %q", ErrActionMisconfigured, key, actionName, spec.Type)
	}
	return nil
}

func clonePromptSpec(spec *PromptSpec) *PromptSpec {
	if spec == nil {
		return nil
	}
	clone := *spec
	return &clone
}

func cloneParamSpecs(params map[string]ParamSpec) map[string]ParamSpec {
	if len(params) == 0 {
		return map[string]ParamSpec{}
	}
	clone := make(map[string]ParamSpec, len(params))
	for name, spec := range params {
		spec.Values = append([]string(nil), spec.Values...)
		clone[name] = spec
	}
	return clone
}
