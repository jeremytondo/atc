package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/remote"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/tui"
	"github.com/jeremytondo/atc/internal/version"
)

// Bare `atc` opens the picker (ATC-316): the Space-first launcher over
// the local server, or over a remote machine with --remote. The flag is
// the root command's own — no subcommand sees it. Everything here is
// wiring: the picker lives in internal/tui, the remote bootstrap in
// internal/remote, and both are reached through seam variables so the
// CLI tests can drive the launch without a terminal.

var (
	runPicker        = tui.Run
	startLocalServer = service.Start
)

func addPickerFlags(root *cobra.Command) {
	root.Flags().String("remote", "", "open the picker against a remote machine (an ssh target)")
	_ = root.RegisterFlagCompletionFunc("remote", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return remote.Hosts(filepath.Join(home, ".ssh", "config")), cobra.ShellCompDirectiveNoFileComp
	})
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
	opts := tui.Options{ClientVersion: version.String()}
	if target != "" {
		err = connectRemote(cmd, target, &opts)
	} else {
		err = connectLocal(cmd, &opts)
	}
	if err != nil {
		return err
	}
	return runPicker(cmd.Context(), opts)
}

// connectLocal points the picker at the local server, starting the
// supervised one through the lifecycle when nothing answers. An explicit
// ATC_SERVER is honored only when it names this machine — the picker
// attaches through the local zmx namespace.
func connectLocal(cmd *cobra.Command, opts *tui.Options) error {
	ctx := cmd.Context()
	// The skew warning would land on the picker's screen; its status
	// line reports the same fact.
	client, baseURL, err := cli.NewClient(io.Discard)
	if err != nil {
		return err
	}
	if !cli.IsLocalServer(baseURL) {
		return fmt.Errorf("bare atc uses the local server, but ATC_SERVER names %s; use --remote for another machine", baseURL)
	}
	health, err := client.Health(ctx)
	if err != nil {
		var problem *api.Problem
		if errors.As(err, &problem) {
			return err
		}
		if os.Getenv("ATC_SERVER") != "" {
			return fmt.Errorf("no server answers at %s: %w", baseURL, err)
		}
		lifecycle, err := lifecycleOptions(cmd)
		if err != nil {
			return err
		}
		if err := startLocalServer(ctx, lifecycle); err != nil {
			return err
		}
		if health, err = client.Health(ctx); err != nil {
			return err
		}
	}
	attacher, err := newSessionAttacher()
	if err != nil {
		return err
	}
	opts.Client = client
	opts.ServerVersion = health.Version
	opts.Attach = func(terminal api.Terminal) (*exec.Cmd, error) {
		if err := attacher.Preflight(); err != nil {
			return nil, err
		}
		return cli.PrepareAttach(terminal, attacher)
	}
	return nil
}

// connectRemote bootstraps over ssh and points the picker at the remote
// server's tailnet URL with the token it returned, held in memory only.
func connectRemote(cmd *cobra.Command, target string, opts *tui.Options) error {
	ssh, err := remote.NewSSH()
	if err != nil {
		return err
	}
	bootstrap, err := ssh.Bootstrap(cmd.Context(), target, cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	opts.Client = api.NewClient(bootstrap.URL, bootstrap.Token, version.String(), nil, nil)
	opts.Target = target
	opts.ServerVersion = bootstrap.Version
	opts.Attach = func(terminal api.Terminal) (*exec.Cmd, error) {
		return ssh.AttachCommand(target, terminal.ID), nil
	}
	opts.TransportLoss = remote.IsTransportLoss
	return nil
}
