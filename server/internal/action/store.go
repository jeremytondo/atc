// Package action owns the file-backed Action overlay store.
package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/jeremytondo/atelier-code/internal/session"
)

var (
	ErrNotFound       = errors.New("action not found")
	ErrDuplicate      = errors.New("action already exists")
	ErrBuiltinRemoval = errors.New("built-in action cannot be removed")
	ErrInvalidAction  = errors.New("invalid action")
)

var actionNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var slugNonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a valid action id from a human-facing name: lowercased, with
// runs of anything that is not an ASCII letter or digit collapsed to a single
// dash and leading/trailing dashes trimmed. It returns "" when nothing usable
// remains (e.g. an empty or punctuation-only name), which callers treat as "no
// derivable id". This is the single source of truth for the derivation the API,
// CLI, and web UI all rely on; the web editor mirrors it only to preview the id.
func Slugify(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Trim(slugNonAlnumRE.ReplaceAllString(lower, "-"), "-")
}

// Source identifies where the effective Action definition came from.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceFile    Source = "file"
)

// Origin classifies how clients should present and manage an action.
type Origin string

const (
	OriginBuiltin  Origin = "builtin"
	OriginModified Origin = "modified"
	OriginCustom   Origin = "custom"
)

func classifyOrigin(hasDefault bool, source Source) Origin {
	switch {
	case !hasDefault:
		return OriginCustom
	case source == SourceFile:
		return OriginModified
	default:
		return OriginBuiltin
	}
}

// Discovery is the list shape returned by the store, with origin metadata added
// to the session-owned discovery fields.
type Discovery struct {
	session.ActionDiscovery
	Origin Origin
}

// Store reads and writes the sparse actions.json overlay.
type Store struct {
	path     string
	defaults session.ActionRegistry
	mu       sync.Mutex
}

// NewStore returns a file-backed Action store. defaults are copied so callers
// can keep mutating their original registry safely.
func NewStore(path string, defaults session.ActionRegistry) *Store {
	return &Store{path: path, defaults: normalizeRegistry(defaults)}
}

// Load reads the file overlay if present and merges it over built-in defaults.
func (s *Store) Load(ctx context.Context) (session.ActionRegistry, error) {
	merged, _, err := s.loadWithSources(ctx)
	return merged, err
}

// Discover returns client-safe action metadata with origin information.
func (s *Store) Discover(ctx context.Context) ([]Discovery, error) {
	merged, sources, err := s.loadWithSources(ctx)
	if err != nil {
		return nil, err
	}
	discovered := merged.Discover(ctx)
	actions := make([]Discovery, 0, len(discovered))
	for _, item := range discovered {
		_, hasDefault := s.defaults[item.Name]
		actions = append(actions, Discovery{
			ActionDiscovery: item,
			Origin:          classifyOrigin(hasDefault, sources[item.Name]),
		})
	}
	return actions, nil
}

// Get returns the full effective definition for name and its semantic origin.
func (s *Store) Get(ctx context.Context, name string) (session.Action, Origin, error) {
	merged, sources, err := s.loadWithSources(ctx)
	if err != nil {
		return session.Action{}, "", err
	}
	action, ok := merged[name]
	if !ok {
		return session.Action{}, "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	_, hasDefault := s.defaults[name]
	return action.Clone(), classifyOrigin(hasDefault, sources[name]), nil
}

// Create writes a new file-backed action. It rejects names that already resolve
// through either the file overlay or the built-ins.
func (s *Store) Create(ctx context.Context, name string, action session.Action) error {
	if err := validateName(name); err != nil {
		return err
	}
	action, err := validateWriteAction(name, action)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fileActions, err := s.readFileActions(ctx)
	if err != nil {
		return err
	}
	if _, ok := s.defaults[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	if _, ok := fileActions[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	fileActions[name] = action
	return s.writeFileActions(ctx, fileActions)
}

// Update writes or replaces a file-backed action. Updating a built-in name
// creates a file override.
func (s *Store) Update(ctx context.Context, name string, action session.Action) error {
	if err := validateName(name); err != nil {
		return err
	}
	action, err := validateWriteAction(name, action)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fileActions, err := s.readFileActions(ctx)
	if err != nil {
		return err
	}
	if s.equalsDefault(name, action) {
		delete(fileActions, name)
	} else {
		fileActions[name] = action
	}
	return s.writeFileActions(ctx, fileActions)
}

// Delete removes a file entry. Deleting a file-backed override of a built-in
// reverts to the built-in; deleting a bare built-in is rejected.
func (s *Store) Delete(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fileActions, err := s.readFileActions(ctx)
	if err != nil {
		return err
	}
	if _, ok := fileActions[name]; ok {
		delete(fileActions, name)
		return s.writeFileActions(ctx, fileActions)
	}
	if _, ok := s.defaults[name]; ok {
		return fmt.Errorf("%w: %s", ErrBuiltinRemoval, name)
	}
	return fmt.Errorf("%w: %s", ErrNotFound, name)
}

// SetEnabled toggles whether name can launch a session. It resolves the current
// effective action (file override or built-in), flips its disabled flag, and
// persists it. Enabling an action back to its built-in definition drops the
// overlay entry so the file stays minimal.
func (s *Store) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if err := validateName(name); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fileActions, err := s.readFileActions(ctx)
	if err != nil {
		return err
	}

	effective, ok := fileActions[name]
	def, isBuiltin := s.defaults[name]
	if !ok {
		if !isBuiltin {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		effective = def.Clone()
	}
	effective.Disabled = !enabled
	effective = normalizeAction(effective)
	if _, err := validateWriteAction(name, effective); err != nil {
		return err
	}

	if s.equalsDefault(name, effective) {
		delete(fileActions, name)
	} else {
		fileActions[name] = effective
	}
	return s.writeFileActions(ctx, fileActions)
}

func (s *Store) equalsDefault(name string, action session.Action) bool {
	def, ok := s.defaults[name]
	return ok && reflect.DeepEqual(normalizeAction(def), normalizeAction(action))
}

func (s *Store) loadWithSources(ctx context.Context) (session.ActionRegistry, map[string]Source, error) {
	fileActions, err := s.readFileActions(ctx)
	if err != nil {
		return nil, nil, err
	}
	merged := s.defaults.Clone()
	sources := make(map[string]Source, len(merged)+len(fileActions))
	for name := range merged {
		sources[name] = SourceBuiltin
	}
	for name, action := range fileActions {
		merged[name] = action
		// A file entry that only toggles a built-in's enabled state is not a real
		// customization, so keep reporting it as built-in: the client should not
		// offer delete/revert for a toggle-only overlay, and re-enabling drops it.
		if def, ok := s.defaults[name]; ok && sameExceptDisabled(def, action) {
			sources[name] = SourceBuiltin
		} else {
			sources[name] = SourceFile
		}
	}
	if err := merged.Validate(); err != nil {
		return nil, nil, fmt.Errorf("load actions file %s: %w", s.path, err)
	}
	return merged, sources, nil
}

type fileShape struct {
	Actions map[string]session.Action `json:"actions"`
}

func (s *Store) readFileActions(ctx context.Context) (map[string]session.Action, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return map[string]session.Action{}, nil
	default:
		return nil, fmt.Errorf("read actions file %s: %w", s.path, err)
	}

	var file fileShape
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse actions file %s: %w", s.path, err)
	}
	if file.Actions == nil {
		return map[string]session.Action{}, nil
	}
	actions := make(map[string]session.Action, len(file.Actions))
	for name, action := range file.Actions {
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("parse actions file %s: %v", s.path, err)
		}
		actions[name] = normalizeAction(action)
	}
	return actions, nil
}

func (s *Store) writeFileActions(ctx context.Context, actions map[string]session.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path == "" {
		return errors.New("actions file path is required")
	}
	if actions == nil {
		actions = map[string]session.Action{}
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create actions dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary actions file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fileShape{Actions: actions}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary actions file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary actions file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace actions file %s: %w", s.path, err)
	}
	return nil
}

func validateName(name string) error {
	if !actionNameRE.MatchString(name) {
		return fmt.Errorf("%w: action name %q must match %s", ErrInvalidAction, name, actionNameRE.String())
	}
	return nil
}

func validateWriteAction(name string, action session.Action) (session.Action, error) {
	action = normalizeAction(action)
	if err := (session.ActionRegistry{name: action}).Validate(); err != nil {
		return session.Action{}, fmt.Errorf("%w: %v", ErrInvalidAction, err)
	}
	return action, nil
}

// sameExceptDisabled reports whether two actions are identical apart from their
// enabled state. Both are expected to be normalized so nil and empty
// slices/maps compare equal.
func sameExceptDisabled(a, b session.Action) bool {
	a.Disabled = false
	b.Disabled = false
	return reflect.DeepEqual(normalizeAction(a), normalizeAction(b))
}

func normalizeAction(action session.Action) session.Action {
	action = action.Clone()
	if action.Args == nil {
		action.Args = []string{}
	}
	if action.Params == nil {
		action.Params = map[string]session.ParamSpec{}
	}
	return action
}

func normalizeRegistry(registry session.ActionRegistry) session.ActionRegistry {
	normalized := make(session.ActionRegistry, len(registry))
	for name, action := range registry {
		normalized[name] = normalizeAction(action)
	}
	return normalized
}
