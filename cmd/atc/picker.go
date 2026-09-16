package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/remote"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/tui"
	"github.com/jeremytondo/atc/internal/upgrade"
	"github.com/jeremytondo/atc/internal/version"
)

// Bare `atc` opens the picker (ATC-316, ATC-327): the Space-first
// launcher over Local and every saved remote connection, or over one
// remote machine with --remote. The flag is the root command's own — no
// subcommand sees it. Everything here is wiring: the picker lives in
// internal/tui, the remote setup and bootstrap in internal/remote, and
// both are reached through seam variables so the CLI tests can drive the
// launch without a terminal. The connectors below are the picker's view
// of each machine: how to reach it without a person, how to reach it
// with one, and how to let it go.

var (
	runPicker          = tui.Run
	startLocalServer   = service.Start
	verifyRemoteHealth = remote.VerifyHealth
)

func addPickerFlags(root *cobra.Command) {
	root.Flags().String("remote", "", "open the picker against one remote machine (an ssh target)")
	_ = root.RegisterFlagCompletionFunc("remote", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		config, err := sshConfigPath()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return remote.Hosts(config), cobra.ShellCompDirectiveNoFileComp
	})
}

func sshConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

func runRoot(cmd *cobra.Command, _ []string) error {
	target, err := cmd.Flags().GetString("remote")
	if err != nil {
		return err
	}
	if !stdioIsTerminal() {
		_ = cmd.Help()
		return errors.New("the picker needs an interactive terminal (stdin and stdout must be TTYs)")
	}
	if target != "" {
		return runRemotePicker(cmd, target)
	}
	// An explicit ATC_SERVER is honored only when it names this machine —
	// Local attaches through the local zmx namespace.
	if server := os.Getenv("ATC_SERVER"); server != "" && !cli.IsLocalServer(server) {
		return fmt.Errorf("bare atc uses the local server, but ATC_SERVER names %s; use --remote for another machine", server)
	}
	connectionsFile, err := paths.ConnectionsFile()
	if err != nil {
		return err
	}
	saved, err := remote.LoadSaved(connectionsFile)
	if err != nil {
		return err
	}
	sshConfig, err := sshConfigPath()
	if err != nil {
		return err
	}
	opts := tui.Options{
		ClientVersion: version.String(),
		Connections:   []tui.Connection{{Name: remote.LocalName, Local: true, Connector: &localConnector{cmd: cmd}}},
		Aliases:       remote.Hosts(sshConfig),
		Open:          func(alias string) tui.Connector { return newRemoteConnector(alias) },
		Save:          func(remotes []string) error { return remote.StoreSaved(connectionsFile, remotes) },
	}
	for _, alias := range saved {
		opts.Connections = append(opts.Connections, tui.Connection{Name: alias, Connector: newRemoteConnector(alias)})
	}
	return runPicker(cmd.Context(), opts)
}

// runRemotePicker is the single-machine shortcut: the guided setup
// (ATC-325) runs on the plain terminal before the picker opens, and the
// picker shows that one machine. The target is never saved.
func runRemotePicker(cmd *cobra.Command, target string) (err error) {
	connector := newRemoteConnector(target)
	defer func() { err = errors.Join(err, connector.Close()) }()
	session, err := connector.Setup(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	connector.ready = &session
	return runPicker(cmd.Context(), tui.Options{
		ClientVersion: version.String(),
		Connections:   []tui.Connection{{Name: target, Connector: connector}},
	})
}

// localConnector reaches the local server, starting the supervised one
// through the lifecycle when nothing answers. It has no interactive
// side: Setup is Connect again.
type localConnector struct {
	cmd *cobra.Command
}

func (c *localConnector) Connect(ctx context.Context) (tui.Session, error) {
	client, baseURL, err := cli.NewClient()
	if err != nil {
		return tui.Session{}, err
	}
	if !cli.IsLocalServer(baseURL) {
		return tui.Session{}, fmt.Errorf("ATC_SERVER names %s, which is not this machine", baseURL)
	}
	var notice string
	health, err := client.Health(ctx)
	if err != nil {
		var problem *api.Problem
		if errors.As(err, &problem) {
			if problem.Code == api.CodeProtocolMismatch {
				return tui.Session{}, fmt.Errorf("%w; `atc server restart` runs the installed build", err)
			}
			return tui.Session{}, err
		}
		lifecycle, err := lifecycleOptions(c.cmd)
		if err != nil {
			return tui.Session{}, err
		}
		// Only the supervised server can be started; an ATC_SERVER that
		// names some other local port is not it.
		if server := os.Getenv("ATC_SERVER"); server != "" && server != fmt.Sprintf("http://127.0.0.1:%d", lifecycle.Config.Port) {
			return tui.Session{}, fmt.Errorf("no server answers at %s: %w", baseURL, err)
		}
		// The picker owns the terminal while this runs, so the lifecycle
		// must not print to it: the start report is implied by the
		// connection turning ready, and what it says on stderr — the
		// first-run registration notice, or the last logs after a failed
		// start — is carried in the session or the error instead.
		var said strings.Builder
		lifecycle.Stdout, lifecycle.Stderr = io.Discard, &said
		if err := startLocalServer(ctx, lifecycle); err != nil {
			if text := strings.TrimSpace(said.String()); text != "" {
				return tui.Session{}, fmt.Errorf("%w\n%s", err, text)
			}
			return tui.Session{}, err
		}
		notice = oneLine(said.String())
		if health, err = client.Health(ctx); err != nil {
			return tui.Session{}, err
		}
	}
	attacher, err := newSessionAttacher()
	if err != nil {
		return tui.Session{}, err
	}
	return tui.Session{
		Client:        client,
		ServerVersion: health.Version,
		Attach: func(ctx context.Context, terminal api.Terminal) (*exec.Cmd, error) {
			if err := attacher.Preflight(); err != nil {
				return nil, err
			}
			return cli.PrepareAttach(ctx, terminal, attacher)
		},
		Notice: notice,
	}, nil
}

// oneLine folds a multi-line notice into the picker's single message
// line.
func oneLine(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}

func (c *localConnector) Setup(ctx context.Context, _ io.Reader, _, _ io.Writer) (tui.Session, error) {
	return c.Connect(ctx)
}

func (c *localConnector) Close() error { return nil }

// remoteConnector owns one target's private SSH connection for the
// picker's whole run. The connection is opened on first use, so a
// machine without an ssh client fails the remote connections one by one
// and Local still works; Close releases only what was opened.
type remoteConnector struct {
	target string
	mu     sync.Mutex
	ssh    *remote.SSH
	// ready is a session proven before the picker opened (--remote),
	// handed out by the first Connect instead of another probe.
	ready *tui.Session
}

func newRemoteConnector(target string) *remoteConnector {
	return &remoteConnector{target: target}
}

func (c *remoteConnector) setup(stdin io.Reader, stdout, stderr io.Writer) (*remote.Setup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ssh == nil {
		ssh, err := remote.NewSSH(c.target)
		if err != nil {
			return nil, err
		}
		c.ssh = ssh
	}
	return &remote.Setup{
		SSH:      c.ssh,
		Local:    remote.Identity{Version: version.String(), Protocol: api.Protocol},
		Releases: upgrade.GitHub{},
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Verify:   verifyRemoteHealth,
	}, nil
}

func (c *remoteConnector) Connect(ctx context.Context) (tui.Session, error) {
	c.mu.Lock()
	ready := c.ready
	c.ready = nil
	c.mu.Unlock()
	if ready != nil {
		return *ready, nil
	}
	setup, err := c.setup(strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		return tui.Session{}, err
	}
	connection, err := setup.Probe(ctx)
	if err != nil {
		return tui.Session{}, classify(err)
	}
	return c.session(connection), nil
}

func (c *remoteConnector) Setup(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) (tui.Session, error) {
	setup, err := c.setup(stdin, stdout, stderr)
	if err != nil {
		return tui.Session{}, err
	}
	connection, err := setup.Run(ctx)
	if err != nil {
		return tui.Session{}, classify(err)
	}
	return c.session(connection), nil
}

// session is the picker's view of a proven connection: an API client on
// the bootstrap material, and attachments over the shared ssh connection
// through the executable setup settled on.
func (c *remoteConnector) session(connection remote.Connection) tui.Session {
	bootstrap := connection.Bootstrap
	ssh := c.ssh
	return tui.Session{
		Client:        api.NewClient(bootstrap.URL, bootstrap.Token, version.String(), nil),
		ServerVersion: bootstrap.Version,
		Attach: func(ctx context.Context, terminal api.Terminal) (*exec.Cmd, error) {
			return ssh.AttachCommand(ctx, connection.Executable, terminal.ID), nil
		},
		TransportLoss: remote.IsTransportLoss,
	}
}

func (c *remoteConnector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ssh == nil {
		return nil
	}
	return c.ssh.Close()
}

// classify maps the remote package's outcomes onto the picker's: a
// login ssh could not complete alone, or changes that need approval
// (including changes a person declined, which Update offers again). The
// text stays the remote package's own.
func classify(err error) error {
	switch {
	case errors.Is(err, remote.ErrLoginRequired):
		return classified{kind: tui.ErrLoginRequired, err: err}
	case errors.Is(err, remote.ErrSetupRequired), errors.Is(err, remote.ErrDeclined):
		return classified{kind: tui.ErrSetupRequired, err: err}
	}
	return err
}

type classified struct {
	kind, err error
}

func (c classified) Error() string        { return c.err.Error() }
func (c classified) Unwrap() error        { return c.err }
func (c classified) Is(target error) bool { return target == c.kind }
