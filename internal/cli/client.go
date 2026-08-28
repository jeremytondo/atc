// Package cli implements the client-side behavior ATC's user-facing
// clients share: constructing the authenticated API client, resolving the
// project that owns the current directory, and handing a TTY to a running
// session. cmd/atc stays cobra wiring over this package; the TUI (ATC-258)
// will drive the same behavior without going through cobra.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/version"
)

// NewClient builds the shared API client every client command speaks
// through — never server internals. It returns the client and the base URL
// it settled on. The server URL comes from ATC_SERVER (a remote client's
// paste-once setup) or the settled local config port; the token from
// ATC_TOKEN or the local token file. Version skew prints one warning line
// on stderr with the restart remedy.
func NewClient(stderr io.Writer) (*api.Client, string, error) {
	baseURL := os.Getenv("ATC_SERVER")
	if baseURL == "" {
		configPath, err := paths.ConfigFile()
		if err != nil {
			return nil, "", err
		}
		cfg, err := config.Load(configPath, os.LookupEnv)
		if err != nil {
			return nil, "", err
		}
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	}
	token := os.Getenv("ATC_TOKEN")
	if token == "" {
		tokenPath, err := paths.AuthTokenFile()
		if err != nil {
			return nil, "", err
		}
		if token, err = (&authtoken.Store{Path: tokenPath}).Ensure(); err != nil {
			return nil, "", err
		}
	}
	clientVersion := version.String()
	warned := false
	onServerVersion := func(serverVersion string) {
		if serverVersion != clientVersion && !warned {
			warned = true
			_, _ = fmt.Fprintf(stderr, "atc: server is %s, client is %s; run `atc server restart`\n", serverVersion, clientVersion)
		}
	}
	return api.NewClient(baseURL, token, clientVersion, nil, onServerVersion), baseURL, nil
}
