package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type diskState struct {
	Version  int      `json:"version"`
	Sessions []Record `json:"sessions"`
}

type Store struct {
	dir       string
	statePath string
	exitDir   string
}

func NewStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	store := &Store{
		dir:       abs,
		statePath: filepath.Join(abs, "sessions.json"),
		exitDir:   filepath.Join(abs, "exits"),
	}
	if err := os.MkdirAll(store.exitDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("protect state directory: %w", err)
	}
	return store, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) ExitPath(id string) string { return filepath.Join(s.exitDir, id+".json") }

func (s *Store) Load() ([]Record, error) {
	contents, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	var state diskState
	if err := json.Unmarshal(contents, &state); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported state version %d", state.Version)
	}
	return state.Sessions, nil
}

func (s *Store) Save(records []Record) error {
	copyRecords := append([]Record(nil), records...)
	sort.Slice(copyRecords, func(i, j int) bool { return copyRecords[i].Name < copyRecords[j].Name })
	contents, err := json.MarshalIndent(diskState{Version: 1, Sessions: copyRecords}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	return replaceFile(s.statePath, contents)
}

func (s *Store) LoadExit(id string) (*ExitMarker, error) {
	contents, err := os.ReadFile(s.ExitPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read exit marker: %w", err)
	}
	var marker ExitMarker
	if err := json.Unmarshal(contents, &marker); err != nil {
		return nil, fmt.Errorf("decode exit marker: %w", err)
	}
	if marker.Version != 1 || marker.SessionID != id {
		return nil, fmt.Errorf("invalid exit marker for %s", id)
	}
	return &marker, nil
}

func (s *Store) RemoveExit(id string) error {
	if err := os.Remove(s.ExitPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove exit marker: %w", err)
	}
	return nil
}

func WriteExitMarker(path string, marker ExitMarker) error {
	marker.Version = 1
	contents, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encode exit marker: %w", err)
	}
	return replaceFile(path, contents)
}

func replaceFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
