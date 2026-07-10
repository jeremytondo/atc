package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/config"
)

func TestNewRespectsLevel(t *testing.T) {
	logger := New(config.LogConfig{Level: "warn", Format: "text"}, &bytes.Buffer{})
	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("info should be disabled at warn level")
	}
	if !logger.Enabled(ctx, slog.LevelError) {
		t.Error("error should be enabled at warn level")
	}
}

func TestNewFormats(t *testing.T) {
	var jsonBuf bytes.Buffer
	New(config.LogConfig{Level: "info", Format: "json"}, &jsonBuf).Info("hello")
	if !json.Valid(bytes.TrimSpace(jsonBuf.Bytes())) {
		t.Errorf("json format produced non-JSON output: %q", jsonBuf.String())
	}

	var textBuf bytes.Buffer
	New(config.LogConfig{Level: "info", Format: "text"}, &textBuf).Info("hello")
	if !strings.Contains(textBuf.String(), "msg=hello") {
		t.Errorf("text format missing expected output: %q", textBuf.String())
	}
}
