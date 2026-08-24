// Package providerprofile prepares experiment-owned provider profiles. Claude
// Code currently authenticates a fresh CLAUDE_CONFIG_DIR without marking its
// interactive onboarding complete, so a valid headless login otherwise opens
// a second login flow when the first TUI starts.
package providerprofile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func CompleteClaudeOnboarding(configDir string) error {
	path := filepath.Join(configDir, ".claude.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Claude profile: %w", err)
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(contents, &profile); err != nil {
		return fmt.Errorf("decode Claude profile: %w", err)
	}
	if profile == nil {
		return fmt.Errorf("decode Claude profile: expected a JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(profile["hasCompletedOnboarding"]), []byte("true")) {
		return nil
	}
	profile["hasCompletedOnboarding"] = json.RawMessage("true")
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Claude profile: %w", err)
	}
	encoded = append(encoded, '\n')
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect Claude profile: %w", err)
	}
	temporary, err := os.CreateTemp(configDir, ".claude-profile-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Claude profile: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary Claude profile: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary Claude profile: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary Claude profile: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Claude profile: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Claude profile: %w", err)
	}
	return nil
}
