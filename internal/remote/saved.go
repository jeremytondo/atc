package remote

// Saved connections (ATC-327): the SSH aliases a plain `atc` launch opens
// beside Local, kept per client in one small JSON file. Only the names are
// saved — transport and authentication settings stay in the user's SSH
// configuration, and API credentials stay in picker memory. A saved alias
// is a choice, not a proof: the picker attempts it on every launch.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// LocalName is the built-in connection's name, which no alias may take.
const LocalName = "Local"

type savedConnections struct {
	Remotes []string `json:"remotes"`
}

// LoadSaved reads the saved remote connection names in their saved order.
// A missing file is an empty list.
func LoadSaved(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved savedConnections
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("read saved connections %s: %w", path, err)
	}
	// A hand-edited file may repeat a name, hold an empty one, or claim
	// the built-in connection's; each connection is one alias, so none
	// is honoured.
	var remotes []string
	for _, name := range saved.Remotes {
		if name != "" && name != LocalName && !slices.Contains(remotes, name) {
			remotes = append(remotes, name)
		}
	}
	return remotes, nil
}

// StoreSaved writes the remote connection names, replacing the file
// whole so a crash mid-write cannot leave a partial list.
func StoreSaved(path string, remotes []string) error {
	if remotes == nil {
		remotes = []string{}
	}
	data, err := json.MarshalIndent(savedConnections{Remotes: remotes}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".connections-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
