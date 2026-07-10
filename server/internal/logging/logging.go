// Package logging builds Atelier Code's structured logger from already-resolved
// configuration. It owns the mapping from config's log level and format strings
// to a concrete slog handler, keeping that concern out of the config package.
package logging

import (
	"io"
	"log/slog"

	"github.com/jeremytondo/atelier-code/internal/config"
)

// New constructs a slog.Logger honoring the configured level and format. The
// level and format strings are assumed valid (config.Load validates them);
// unrecognized values fall back to info level and a text handler.
func New(cfg config.LogConfig, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level(cfg.Level)}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

func level(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
