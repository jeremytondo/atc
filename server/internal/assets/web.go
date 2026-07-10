package assets

import (
	"embed"
	"fmt"
	"io/fs"
)

// embeddedWeb contains the staged frontend build output under web/.
// The all: prefix is required because SvelteKit emits JS/CSS under _app/.
//
//go:embed all:web
var embeddedWeb embed.FS

// WebFS returns the embedded frontend assets rooted at internal/assets/web.
// HTTP routing and browser fallback behavior live in internal/server.
func WebFS() (fs.FS, error) {
	webFS, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		return nil, fmt.Errorf("embedded web assets: %w", err)
	}
	return webFS, nil
}
