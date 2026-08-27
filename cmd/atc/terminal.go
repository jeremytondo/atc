package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/version"
	"github.com/jeremytondo/atc/internal/wrapper"
	"github.com/jeremytondo/atc/internal/zmx"
)

// newAPIClient builds the shared API client every CLI command speaks
// through — never server internals. The server URL comes from ATC_SERVER
// (a remote client's paste-once setup) or the settled local config port;
// the token from ATC_TOKEN or the local token file. Version skew prints
// one stderr warning line with the restart remedy.
func newAPIClient(cmd *cobra.Command) (*api.Client, string, error) {
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
	stderr := cmd.ErrOrStderr()
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

// runWithClient wraps an API-backed command body with the shared client
// construction and error path — every terminal/api command starts the same
// way.
func runWithClient(body func(cmd *cobra.Command, args []string, client *api.Client, baseURL string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		client, baseURL, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		return body(cmd, args, client, baseURL)
	}
}

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

func newTerminalCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a terminal and start its session",
		Long: `Create a terminal and start its session immediately. The session survives
disconnects and ATC server restarts; attach with ` + "`atc terminal attach <id>`" + `.
With --app the command runs through your login shell (profile and rc files
loaded); without it you get a plain interactive shell.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			flags := cmd.Flags()
			params := api.TerminalCreateParams{}
			var err error
			if params.Name, err = flags.GetString("name"); err != nil {
				return err
			}
			if params.Directory, err = flags.GetString("dir"); err != nil {
				return err
			}
			if params.App, err = flags.GetString("app"); err != nil {
				return err
			}
			terminal, err := client.CreateTerminal(cmd.Context(), params)
			if err != nil {
				return err
			}
			printTerminal(cmd.OutOrStdout(), terminal)
			return nil
		}),
	}
	cmd.Flags().String("name", "", "display name (defaults from --app, else \"Shell\")")
	cmd.Flags().String("dir", "", "working directory (defaults to the server user's home)")
	cmd.Flags().String("app", "", "command to run through your shell; omit for a plain shell")
	return cmd
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
	return &cobra.Command{
		Use:   "list",
		Short: "List terminals with their statuses",
		Long: `List every terminal. Exited and missing terminals stay listed — an exit you
never saw is information, not garbage — until you delete them.`,
		Args: cobra.NoArgs,
		RunE: runWithClient(func(cmd *cobra.Command, _ []string, client *api.Client, _ string) error {
			terminals, err := client.Terminals(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(terminals) == 0 {
				_, err := fmt.Fprintln(out, "no terminals")
				return err
			}
			w := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tSTATUS\tNAME\tDIRECTORY")
			for _, terminal := range terminals {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", terminal.ID, statusLabel(terminal), terminal.Name, terminal.Directory)
			}
			return w.Flush()
		}),
	}
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
			if !isLoopback(baseURL) {
				return fmt.Errorf("atc terminal attach is local-only (the session socket lives on the server's machine); this client targets %s", baseURL)
			}
			// The API check keeps zmx's attach-auto-creates behavior from
			// resurrecting anything: only a verified-running session is
			// attached.
			terminal, err := client.Terminal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if terminal.Status != api.TerminalRunning {
				return fmt.Errorf("terminal %s is %s, not running", terminal.ID, terminal.Status)
			}
			socketDir, err := paths.TerminalSocketDir()
			if err != nil {
				return err
			}
			// A loopback URL does not prove the server shares this
			// process's namespace (an SSH-forwarded remote server, a
			// different XDG_STATE_HOME). A session the API calls running
			// must have its socket here; anything else would hand the TTY
			// to zmx's auto-create.
			if _, err := os.Stat(filepath.Join(socketDir, terminal.ID)); err != nil {
				return fmt.Errorf("terminal %s has no session socket under %s — the server appears to be remote (an SSH-forwarded port?) or running with a different state directory", terminal.ID, socketDir)
			}
			executable, argv, env, err := zmx.AttachCommand(socketDir, terminal.ID)
			if err != nil {
				return err
			}
			// Exec replaces this process: the user's real TTY belongs to
			// zmx until detach.
			return syscall.Exec(executable, argv, env)
		}),
	}
}

func isLoopback(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	_, _ = fmt.Fprintf(w, "directory\t%s\n", terminal.Directory)
	if terminal.App != "" {
		_, _ = fmt.Fprintf(w, "app\t%s\n", terminal.App)
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
  atc api -X POST -d '{"app":"hx"}' /v1/terminals
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
			if opts.App, err = flags.GetString("app"); err != nil {
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
	cmd.Flags().String("app", "", "command to run through the shell")
	_ = cmd.MarkFlagRequired("marker")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}
