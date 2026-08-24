package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/elevenideas/atc/experiments/acp-host/internal/acp"
)

type Metadata struct {
	Version         int                `json:"version"`
	Provider        string             `json:"provider"`
	Command         string             `json:"command"`
	Args            []string           `json:"args"`
	CWD             string             `json:"cwd"`
	SessionID       string             `json:"sessionId"`
	ProtocolVersion int                `json:"protocolVersion"`
	AgentInfo       acp.Implementation `json:"agentInfo"`
	Capabilities    json.RawMessage    `json:"capabilities"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

func LoadMetadata(path string) (Metadata, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Metadata{}, nil
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("read state: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode state: %w", err)
	}
	if metadata.Version != 1 {
		return Metadata{}, fmt.Errorf("unsupported state version %d", metadata.Version)
	}
	return metadata, nil
}

func SaveMetadata(path string, metadata Metadata) error {
	metadata.Version = 1
	metadata.UpdatedAt = time.Now().UTC()
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary state: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
