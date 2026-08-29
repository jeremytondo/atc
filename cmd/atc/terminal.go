package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/terminals/wrapper"
	"github.com/jeremytondo/atc/internal/terminals/zmx"
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

// newSessionAttacher wires the concrete adapter's attach mechanics for
// this local client — the client-side counterpart of main.go wiring
// terminals.Adapter for the server.
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
		Short: "Create a terminal and start its session",
		Long: `Create a terminal and start its session immediately. The terminal lands in
the project owning the current directory (found by walking up, the way git
finds a repository) and starts in that project's directory; pass --project
to pick one explicitly. The session survives disconnects and ATC server
restarts; attach with ` + "`atc terminal attach <id>`" + `,
or pass ` + "`--attach`" + ` to attach immediately.
With --command it runs through your login shell (profile and rc files
loaded); without it you get a plain interactive shell.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, baseURL string) error {
			command, err := cmd.Flags().GetString("command")
			if err != nil {
				return err
			}
			return createAndMaybeAttach(cmd, client, baseURL, func(ctx context.Context, projectID, name string) (api.Terminal, error) {
				return client.CreateTerminal(ctx, api.TerminalCreateParams{ProjectID: projectID, Name: name, Command: command})
			})
		}),
	}
	cmd.Flags().String("name", "", "display name (defaults from --command, else \"Shell\")")
	cmd.Flags().String("project", "", "project the terminal belongs to (defaults to the project owning the current directory)")
	cmd.Flags().String("command", "", "command to run through your shell; omit for a plain shell")
	cmd.Flags().Bool("attach", false, "attach to the terminal after creation")
	return cmd
}

// createAndMaybeAttach is the workflow shared by terminal create and
// agent launch: attach preflights run before anything is created, the
// project defaults from the current directory, and a failed attach after
// a successful create reports the terminal and the retry remedy. create
// performs the API call with the resolved project and name flags.
func createAndMaybeAttach(cmd *cobra.Command, client *api.Client, baseURL string, create func(ctx context.Context, projectID, name string) (api.Terminal, error)) error {
	flags := cmd.Flags()
	attach, err := flags.GetBool("attach")
	if err != nil {
		return err
	}
	var attacher cli.SessionAttacher
	if attach {
		if err := cli.AttachPreflight(baseURL, stdioIsTerminal()); err != nil {
			return err
		}
		if attacher, err = newSessionAttacher(); err != nil {
			return err
		}
		if err := attacher.Preflight(); err != nil {
			return err
		}
	}
	name, err := flags.GetString("name")
	if err != nil {
		return err
	}
	projectID, err := flags.GetString("project")
	if err != nil {
		return err
	}
	if projectID == "" {
		if projectID, err = cli.ResolveProjectID(cmd.Context(), client, cmd.InOrStdin(), cmd.ErrOrStderr(), stdinIsTTY()); err != nil {
			return err
		}
	}
	terminal, err := create(cmd.Context(), projectID, name)
	if err != nil {
		return err
	}
	printTerminal(cmd.OutOrStdout(), terminal)
	if attach {
		if err := cli.AttachSession(terminal, attacher); err != nil {
			return fmt.Errorf("%w; the terminal was created; retry with: atc terminal attach %s", err, terminal.ID)
		}
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
		Long: `List every terminal (pass --project to scope to one project). Exited and
missing terminals stay listed — an exit you never saw is information, not
garbage — until you delete them.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			project, err := cmd.Flags().GetString("project")
			if err != nil {
				return err
			}
			terminals, err := client.Terminals(cmd.Context(), project)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(terminals) == 0 {
				_, err := fmt.Fprintln(out, "no terminals")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tNAME\tPROJECT\tDIRECTORY")
			for _, terminal := range terminals {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", terminal.ID, statusLabel(terminal), terminal.Name, terminal.ProjectID, terminal.Directory)
			}
			return w.Flush()
		}),
	}
	cmd.Flags().String("project", "", "only terminals belonging to this project")
	return cmd
}

func newTerminalUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a terminal (name is the only mutable field)",
		Args:  cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return err
			}
			terminal, err := client.UpdateTerminal(cmd.Context(), args[0], api.TerminalUpdateParams{Name: name})
			if err != nil {
				return err
			}
			printTerminal(cmd.OutOrStdout(), terminal)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "new display name")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newTerminalDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Stop a terminal's session and remove it",
		Long: `Stop the session (best-effort — the record is removed even when zmx is
unhealthy) and delete the terminal.`,
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
	_, _ = fmt.Fprintf(w, "project\t%s\n", terminal.ProjectID)
	_, _ = fmt.Fprintf(w, "directory\t%s\n", terminal.Directory)
	if terminal.Command != "" {
		_, _ = fmt.Fprintf(w, "command\t%s\n", terminal.Command)
	}
	if terminal.Agent != "" {
		_, _ = fmt.Fprintf(w, "agent\t%s\n", terminal.Agent)
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
  atc api -X POST -d '{"projectId":"proj-x7k2f","command":"hx"}' /v1/terminals
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
