// Package harness owns the provider-neutral state, persistence, logging, and
// REPL behavior used to evaluate ACP as an ATC-facing boundary.
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type LogRecord struct {
	Timestamp string          `json:"timestamp"`
	Layer     string          `json:"layer"`
	Direction string          `json:"direction,omitempty"`
	Kind      string          `json:"kind"`
	SessionID string          `json:"sessionId,omitempty"`
	Status    string          `json:"status,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type JSONLLogger struct {
	mu   sync.Mutex
	file *os.File
}

func OpenJSONLLogger(path string) (*JSONLLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	return &JSONLLogger{file: file}, nil
}

func (l *JSONLLogger) Write(record LogRecord) error {
	record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func (l *JSONLLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

func rawData(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"marshalError":"` + err.Error() + `"}`)
	}
	return encoded
}
