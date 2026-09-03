package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/integrations/zmx"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/terminals/wrapper"
)

func newTerminalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "terminal",
		Short: "Create and manage persistent terminal sessions",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc terminal <create|get|list|update|delete|attach>")
		},
	}
	cmd.AddCommand(newTerminalCreateCmd(), newTerminalGetCmd(), newTerminalListCmd(),
		newTerminalUpdateCmd(), newTerminalDeleteCmd(), newTerminalAttachCmd())
	return cmd
}

// newSessionAttacher wires the concrete driver's attach mechanics for
// this local client — the client-side counterpart of main.go wiring
// terminals.Driver for the server.
func newSessionAttacher() (cli.SessionAttacher, error) {
	socketDir, err := paths.TerminalSocketDir()
	if err != nil {
		return nil, err
	}
	return zmx.NewAttacher(socketDir), nil
}

func newTerminalCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a terminal, start its session, and attach",
		Long: `Create a persistent terminal session.

Session mode (choose one):
  (none)          start a plain interactive shell
  --command CMD   run CMD through your login shell
  --app ID        launch an app listed by ` + "`atc integration list`" + `
  --thread ID     resume a conversation in its original app

App conversations become threads at their first prompt. Thread mode reuses a
terminal already showing the conversation. Unavailable apps are rejected before
creating a terminal.

Use --space and --directory to control placement. With neither flag, a local
server uses your current directory; a remote server uses the space's directory.

The command attaches by default (detach with ctrl-\). Use --detach to leave the
session running in the background. If attaching is unavailable, the created
terminal is printed instead. Sessions survive disconnects and server restarts;
reattach with ` + "`atc terminal attach <id>`" + `.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, baseURL string) error {
			params, err := placementParams(cmd, baseURL)
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			if params.Command, err = flags.GetString("command"); err != nil {
				return err
			}
			if params.AppID, err = flags.GetString("app"); err != nil {
				return err
			}
			if params.ThreadID, err = flags.GetString("thread"); err != nil {
				return err
			}
			return runAndMaybeAttach(cmd, baseURL, func(ctx context.Context) (api.Terminal, error) {
				return client.CreateTerminal(ctx, params)
			})
		}),
	}
	addPlacementFlags(cmd)
	cmd.Flags().String("command", "", "command to run through your shell; omit for a plain shell")
	cmd.Flags().String("app", "", "app to launch, as integration/app (e.g. codex/tui)")
	cmd.Flags().String("thread", "", "thread to resume through the app that started it")
	cmd.MarkFlagsMutuallyExclusive("command", "app", "thread")
	return cmd
}

// addPlacementFlags declares the flags every launching command shares:
// where the terminal goes, what it is called, and --detach.
func addPlacementFlags(cmd *cobra.Command) {
	cmd.Flags().String("name", "", "display name (defaults to the directory's basename)")
	cmd.Flags().String("space", "", "space the terminal belongs to (defaults to the Default space)")
	cmd.Flags().String("directory", "", "working directory (local default: current directory; remote: space directory)")
	cmd.Flags().Bool("detach", false, "print the terminal instead of attaching to it")
}

// placementParams reads the placement flags. With neither --directory
// nor --space, a local server gets this process's working directory as
// the explicit directory: the natural "open a terminal here", meaningful
// only when the server shares this machine. A relative --directory is
// resolved against this process; existence is the server's to check.
func placementParams(cmd *cobra.Command, baseURL string) (api.TerminalCreateParams, error) {
	flags := cmd.Flags()
	var params api.TerminalCreateParams
	var err error
	if params.Name, err = flags.GetString("name"); err != nil {
		return params, err
	}
	if params.SpaceID, err = flags.GetString("space"); err != nil {
		return params, err
	}
	if params.Directory, err = flags.GetString("directory"); err != nil {
		return params, err
	}
	switch {
	case params.Directory != "":
		if params.Directory, err = filepath.Abs(params.Directory); err != nil {
			return params, err
		}
	case params.SpaceID == "" && cli.IsLocalServer(baseURL):
		if params.Directory, err = os.Getwd(); err != nil {
			return params, err
		}
	}
	return params, nil
}

// runAndMaybeAttach runs act — the API call yielding a terminal — then
// attaches unless --detach (ATC-282). Two kinds of inability differ on
// purpose: local tooling missing (no zmx) fails before act so nothing is
// created, while an environment that can never attach (no TTY, a server
// on another machine) still runs act and prints the terminal, so
// automation composes with the same surface.
func runAndMaybeAttach(cmd *cobra.Command, baseURL string, act func(ctx context.Context) (api.Terminal, error)) error {
	detach, err := cmd.Flags().GetBool("detach")
	if err != nil {
		return err
	}
	var attacher cli.SessionAttacher
	var skipped error
	if !detach {
		if skipped = cli.AttachPreflight(baseURL, stdioIsTerminal()); skipped == nil {
			if attacher, err = newSessionAttacher(); err != nil {
				return err
			}
			if err := attacher.Preflight(); err != nil {
				return err
			}
		}
	}
	terminal, err := act(cmd.Context())
	if err != nil {
		return err
	}
	printTerminal(cmd.OutOrStdout(), terminal)
	if detach {
		return nil
	}
	if skipped != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "atc: not attaching: %v\n", skipped)
		return nil
	}
	if err := cli.AttachSession(terminal, attacher); err != nil {
		return fmt.Errorf("%w; retry with: atc terminal attach %s", err, terminal.ID)
	}
	return nil
}

func newTerminalGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one terminal",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			terminal, err := client.Terminal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printTerminal(cmd.OutOrStdout(), terminal)
			return nil
		}),
	}
}

func newTerminalListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List terminals with their statuses",
		Long: `List terminals in every space, including exited and missing terminals.
Use --space to scope the list. Records remain until deleted.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			space, err := cmd.Flags().GetString("space")
			if err != nil {
				return err
			}
			terminals, err := client.Terminals(cmd.Context(), space)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(terminals) == 0 {
				_, err := fmt.Fprintln(out, "no terminals")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tNAME\tAPP\tSPACE\tDIRECTORY")
			for _, terminal := range terminals {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", terminal.ID, statusLabel(terminal), terminal.Name, terminal.AppID, terminal.SpaceID, terminal.Directory)
			}
			return w.Flush()
		}),
	}
	cmd.Flags().String("space", "", "only terminals belonging to this space")
	return cmd
}

func newTerminalUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Rename a terminal or move it to another space",
		Long: `Rename a terminal, or move it to another space with --space. A move changes
nothing else: the session keeps running in its directory, with its app and
its thread.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			var params api.TerminalUpdateParams
			if flags.Changed("name") {
				name, err := flags.GetString("name")
				if err != nil {
					return err
				}
				params.Name = api.Some(name)
			}
			if flags.Changed("space") {
				space, err := flags.GetString("space")
				if err != nil {
					return err
				}
				params.SpaceID = api.Some(space)
			}
			terminal, err := client.UpdateTerminal(cmd.Context(), args[0], params)
			if err != nil {
				return err
			}
			printTerminal(cmd.OutOrStdout(), terminal)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "new display name")
	cmd.Flags().String("space", "", "space to move the terminal to")
	cmd.MarkFlagsOneRequired("name", "space")
	return cmd
}

func newTerminalDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Stop a terminal's session and remove it",
		Long: `Stop the session and delete the terminal. The record is still removed if
the session cannot be stopped.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			if err := client.DeleteTerminal(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return err
		}),
	}
}

func newTerminalAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach this terminal's TTY to a running session",
		Long: `Hand this terminal over to the session (detach with ctrl-\). Local-only:
the session's socket lives on the server's machine. The terminal must be
running — attach never resurrects or creates sessions.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, baseURL string) error {
			if err := cli.AttachPreflight(baseURL, stdioIsTerminal()); err != nil {
				return err
			}
			// The API check keeps zmx's attach-auto-creates behavior from
			// resurrecting anything: only a verified-running session is
			// attached.
			terminal, err := client.Terminal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			attacher, err := newSessionAttacher()
			if err != nil {
				return err
			}
			return cli.AttachSession(terminal, attacher)
		}),
	}
}

func statusLabel(terminal api.Terminal) string {
	if terminal.Status == api.TerminalExited && terminal.ExitCode != nil {
		return fmt.Sprintf("exited (%d)", *terminal.ExitCode)
	}
	return string(terminal.Status)
}

func printTerminal(out io.Writer, terminal api.Terminal) {
	w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "id\t%s\n", terminal.ID)
	_, _ = fmt.Fprintf(w, "name\t%s\n", terminal.Name)
	_, _ = fmt.Fprintf(w, "status\t%s\n", statusLabel(terminal))
	_, _ = fmt.Fprintf(w, "space\t%s\n", terminal.SpaceID)
	_, _ = fmt.Fprintf(w, "directory\t%s\n", terminal.Directory)
	if terminal.Command != "" {
		_, _ = fmt.Fprintf(w, "command\t%s\n", terminal.Command)
	}
	if terminal.AppID != "" {
		_, _ = fmt.Fprintf(w, "app\t%s\n", terminal.AppID)
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", terminal.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	_ = w.Flush()
}

func newAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Make an authenticated raw request to the ATC API",
		Long: `The raw gateway over the contract: anything the clients can do, automation
can do. Adds the bearer token and version headers, streams the response to
stdout (including the /v1/events SSE feed), and exits non-zero on an HTTP
error status.

Examples:
  atc api /v1/terminals
  atc api -X POST -d '{"command":"hx"}' /v1/terminals
  atc api /v1/events`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			method, err := flags.GetString("method")
			if err != nil {
				return err
			}
			data, err := flags.GetString("data")
			if err != nil {
				return err
			}
			var body io.Reader
			if data == "-" {
				body = cmd.InOrStdin()
			} else if data != "" {
				body = strings.NewReader(data)
			}
			if method == "" {
				method = "GET"
				if body != nil {
					method = "POST"
				}
			}
			resp, err := client.Raw(cmd.Context(), strings.ToUpper(method), args[0], body)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if _, err := io.Copy(cmd.OutOrStdout(), resp.Body); err != nil && cmd.Context().Err() == nil {
				return err
			}
			if resp.StatusCode >= 400 {
				// The response body (a problem document) is already on
				// stdout; the status line explains the non-zero exit.
				return fmt.Errorf("HTTP %s", resp.Status)
			}
			return nil
		}),
	}
	cmd.Flags().StringP("method", "X", "", "HTTP method (default GET, or POST with --data)")
	cmd.Flags().StringP("data", "d", "", "request body; \"-\" reads stdin")
	return cmd
}

// newChildCmd is the hidden wrapper subcommand: every ATC terminal
// session's root task, recording exit evidence (ATC-251). It is not a
// second supervisor and is never run by hand.
func newChildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "__child",
		Hidden: true,
		Short:  "Internal: terminal session root task",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			opts := wrapper.Options{}
			var err error
			if opts.MarkerPath, err = flags.GetString("marker"); err != nil {
				return err
			}
			if opts.TerminalID, err = flags.GetString("id"); err != nil {
				return err
			}
			if opts.Directory, err = flags.GetString("dir"); err != nil {
				return err
			}
			if opts.Command, err = flags.GetString("command"); err != nil {
				return err
			}
			if code := wrapper.Run(opts); code != 0 {
				return &service.ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().String("marker", "", "exit marker file path")
	cmd.Flags().String("id", "", "terminal id")
	cmd.Flags().String("dir", "", "working directory")
	cmd.Flags().String("command", "", "command to run through the shell")
	_ = cmd.MarkFlagRequired("marker")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}
