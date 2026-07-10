package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// DefaultEnvironmentName is the environment used when a start request omits one.
// It must name a key in the configured registry.
const DefaultEnvironmentName = "host-login-shell"

// Environment-related sentinel errors. Unknown names are caller errors;
// misconfiguration is an operator/config fault.
var (
	ErrUnknownEnvironment       = errors.New("unknown environment")
	ErrEnvironmentMisconfigured = errors.New("environment is misconfigured")
)

// EnvironmentKind identifies how an action argv is wrapped for launch.
type EnvironmentKind string

const EnvironmentKindHostLoginShell EnvironmentKind = "host-login-shell"

// Environment describes how and where an Action runs.
type Environment struct {
	Kind        EnvironmentKind `toml:"kind" json:"kind"`
	Label       string          `toml:"label" json:"label"`
	Description string          `toml:"description" json:"description"`
}

// EnvironmentRegistry maps environment names to their launch wrappers.
type EnvironmentRegistry map[string]Environment

// EnvironmentDiscovery is the safe client-facing shape for one environment.
type EnvironmentDiscovery struct {
	Name        string
	Kind        EnvironmentKind
	Label       string
	Description string
	Default     bool
}

// Command wraps the action's inner argv into the final argv passed to zmx.
func (e Environment) Command(inner []string) ([]string, error) {
	switch e.Kind {
	case EnvironmentKindHostLoginShell:
		return []string{loginShell(), "-l", "-i", "-c", shellJoin(inner)}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported kind %q", ErrEnvironmentMisconfigured, e.Kind)
	}
}

// Discover returns configured environments in stable name order.
func (r EnvironmentRegistry) Discover(context.Context) []EnvironmentDiscovery {
	names := r.names()
	environments := make([]EnvironmentDiscovery, 0, len(names))
	for _, name := range names {
		environment := r[name]
		environments = append(environments, EnvironmentDiscovery{
			Name:        name,
			Kind:        environment.Kind,
			Label:       environment.Label,
			Description: environment.Description,
			Default:     name == DefaultEnvironmentName,
		})
	}
	return environments
}

// Validate checks static environment configuration.
func (r EnvironmentRegistry) Validate() error {
	for _, name := range r.names() {
		environment := r[name]
		switch environment.Kind {
		case EnvironmentKindHostLoginShell:
		case "":
			return fmt.Errorf("%w: environment %q must define kind", ErrEnvironmentMisconfigured, name)
		default:
			return fmt.Errorf("%w: environment %q has unsupported kind %q", ErrEnvironmentMisconfigured, name, environment.Kind)
		}
	}
	return nil
}

func (r EnvironmentRegistry) resolve(name string) (Environment, string, error) {
	if name == "" {
		name = DefaultEnvironmentName
	}
	environment, ok := r[name]
	if !ok {
		return Environment{}, "", fmt.Errorf("%w %q: valid environments are %s", ErrUnknownEnvironment, name, strings.Join(r.names(), ", "))
	}
	return environment, name, nil
}

func (r EnvironmentRegistry) names() []string {
	return slices.Sorted(maps.Keys(r))
}

// loginShell resolves the user's shell, falling back to /bin/sh.
func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func shellJoin(tokens []string) string {
	quoted := make([]string, len(tokens))
	for i, token := range tokens {
		quoted[i] = shellQuote(token)
	}
	return strings.Join(quoted, " ")
}

// shellQuote renders s as a single POSIX-shell token, quoting only when needed.
// Tokens made solely of safe characters are returned as-is for readable
// commands; anything else is single-quoted with embedded quotes escaped.
func shellQuote(s string) string {
	if s != "" && isSafeToken(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isSafeToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			return false
		}
	}
	return true
}
