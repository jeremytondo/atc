// Command atc is the ATC command-line interface. It is the single entrypoint
// for the product: client commands and the server lifecycle both live under
// this binary, per the ATC-246 layout. The command tree is Cobra-based; bare
// `atc` stays a stub that prints help until the TUI epic (ATC-258).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/server"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/tailscale"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		// An ExitError's report is already on stdout (e.g. `server status`
		// exit codes); it only picks the process exit code.
		var exit *service.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "atc:", err)
		os.Exit(1)
	}
}

// run executes the command tree. It is the seam tests drive: output streams
// are injected, and errors return here so main owns the exit path.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	if args == nil {
		// SetArgs(nil) would make Cobra fall back to os.Args.
		args = []string{}
	}
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	// Help lists commands in the order they are added, so the advanced
	// foreground `run` can sit last under `atc server`.
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:   "atc",
		Short: "The ATC terminal client and server",
		Long: `atc is the ATC terminal client and server.

Configuration precedence: flags > ATC_<KEY> environment > ~/.config/atc/config.toml > defaults.
Keys: port, bind, tailscale, tailscale_executable. Set tailscale = true to
expose on the tailnet without the flag.`,
		// Errors surface once, prefixed "atc:" in main.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(newVersionCmd(), newServerCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the atc version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), versionString())
			return err
		},
	}
}

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run and administer the ATC server",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("usage: atc server <start|stop|restart|status|logs|token|uninstall|run>")
		},
	}
	// Display order is deliberate (EnableCommandSorting is off): the
	// supervised lifecycle first, the advanced foreground primitive last.
	cmd.AddCommand(newServerStartCmd(), newServerStopCmd(), newServerRestartCmd(),
		newServerStatusCmd(), newServerLogsCmd(), newServerTokenCmd(),
		newServerUninstallCmd(), newServerRunCmd())
	return cmd
}

// lifecycleOptions settles the configuration the supervised daemon itself
// will read: config file and defaults only. The unit stamps no ATC_*
// environment (supervisors start services with a minimal environment), so
// honoring the invoking shell's ATC_PORT here would probe a port the daemon
// never serves.
func lifecycleOptions(cmd *cobra.Command) (service.Options, error) {
	configPath, err := paths.ConfigFile()
	if err != nil {
		return service.Options{}, err
	}
	cfg, err := config.Load(configPath, func(string) (string, bool) { return "", false })
	if err != nil {
		return service.Options{}, err
	}
	return service.Options{
		Config:  cfg,
		Version: versionString(),
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	}, nil
}

// recoveryOptions builds Options without settling configuration. Stop,
// logs, and uninstall never consume it, and they must keep working when
// config.toml is broken — the same condition that can keep the daemon from
// booting must not block diagnostics, stopping, or the promised total undo.
func recoveryOptions(cmd *cobra.Command) (service.Options, error) {
	return service.Options{
		Version: versionString(),
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	}, nil
}

func lifecycleCmd(use, short, long string, options func(*cobra.Command) (service.Options, error), action func(context.Context, service.Options) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options(cmd)
			if err != nil {
				return err
			}
			return action(cmd.Context(), opts)
		},
	}
}

func newServerStartCmd() *cobra.Command {
	return lifecycleCmd("start",
		"Register and start the supervised server",
		`Register the server with the user supervisor (launchd on macOS, systemd user
units on Linux) and start it. Registration happens on every start: the unit is
re-rendered from the current binary, so upgrades only need `+"`atc server restart`"+`.
A healthy server running the current unit is left untouched; a changed unit
bounces it. The first start prints what was registered and how to undo it
(atc server uninstall).`,
		lifecycleOptions, service.Start)
}

func newServerStopCmd() *cobra.Command {
	return lifecycleCmd("stop",
		"Stop the supervised server until next login",
		`Stop the supervised server process. The unit stays installed, so the server
returns at next login; use `+"`atc server uninstall`"+` to remove it entirely.`,
		recoveryOptions, service.Stop)
}

func newServerRestartCmd() *cobra.Command {
	return lifecycleCmd("restart",
		"Restart the supervised server",
		`Re-render the unit and restart the server process. This is the remedy for
upgrades, config edits, and client/server version skew.`,
		lifecycleOptions, service.Restart)
}

func newServerStatusCmd() *cobra.Command {
	return lifecycleCmd("status",
		"Report server health, versions, and API URLs",
		`Probe the server's health endpoint (the source of truth for liveness), report
unit state, client and server versions, and ready-to-paste API URLs.
Exit codes: 0 healthy, 1 installed but not responding, 2 not installed.`,
		lifecycleOptions, service.Status)
}

func newServerLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show the supervised server's logs",
		Long: `Show the supervised server's captured output: the systemd journal on Linux,
the launchd-captured log file on macOS.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := recoveryOptions(cmd)
			if err != nil {
				return err
			}
			follow, err := cmd.Flags().GetBool("follow")
			if err != nil {
				return err
			}
			lines, err := cmd.Flags().GetInt("lines")
			if err != nil {
				return err
			}
			return service.Logs(cmd.Context(), opts, follow, lines)
		},
	}
	cmd.Flags().BoolP("follow", "f", false, "keep printing new log lines as they arrive")
	cmd.Flags().IntP("lines", "n", 100, "number of recent lines to show")
	return cmd
}

func newServerUninstallCmd() *cobra.Command {
	return lifecycleCmd("uninstall",
		"Stop the server and remove its registration",
		`Stop the supervised server and remove its unit. Data is never deleted; the
config, token, and (on macOS) log files that remain are listed.`,
		recoveryOptions, service.Uninstall)
}

func newServerRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the ATC server in the foreground (advanced)",
		Long: `Run the ATC server in the foreground until SIGINT or SIGTERM, logging to
stderr. This is the primitive the supervised unit execs; most users want
` + "`atc server start`" + `.`,
		Args: cobra.NoArgs,
		RunE: serverRun,
	}
	cmd.Flags().Int("port", 0, "listen port (overrides ATC_PORT and config.toml)")
	cmd.Flags().String("bind", "", "bind address (overrides ATC_BIND and config.toml)")
	cmd.Flags().Bool("tailscale", false, "expose the server on the tailnet via a supervised `tailscale serve` (overrides ATC_TAILSCALE and config.toml)")
	return cmd
}

// newServerTokenCmd prints the bearer token for the paste-once remote setup,
// creating it first when absent — so a fresh install prints the same
// credential the server enforces. `rotate` reissues it; the old token stops
// working immediately, without a server restart.
func newServerTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the API bearer token, creating it if absent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printToken(cmd.OutOrStdout(), (*authtoken.Store).Ensure)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Reissue the API bearer token, revoking the old one immediately",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printToken(cmd.OutOrStdout(), (*authtoken.Store).Rotate)
		},
	})
	return cmd
}

func printToken(stdout io.Writer, issue func(*authtoken.Store) (string, error)) error {
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		return err
	}
	token, err := issue(&authtoken.Store{Path: tokenPath})
	if err != nil {
		return err
	}
	// A failed or truncated write matters here: the printed token is the
	// credential the user pastes into a remote client.
	_, err = fmt.Fprintln(stdout, token)
	return err
}

// serverRun runs the server in the foreground until the command context is
// cancelled.
func serverRun(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()

	configPath, err := paths.ConfigFile()
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return err
	}
	// Flags outrank the loaded config, but only when the user set them.
	flags := cmd.Flags()
	if flags.Changed("port") {
		if cfg.Port, err = flags.GetInt("port"); err != nil {
			return err
		}
	}
	if flags.Changed("bind") {
		if cfg.Bind, err = flags.GetString("bind"); err != nil {
			return err
		}
	}
	if flags.Changed("tailscale") {
		if cfg.Tailscale, err = flags.GetBool("tailscale"); err != nil {
			return err
		}
	}
	// Flags are the final precedence layer, and Load's validation ran
	// before they applied — revalidate so a flag cannot smuggle in a value
	// the lower layers would have refused (e.g. --bind= binding every
	// interface).
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Exposure misconfiguration is a boot error; everything after this
	// point self-heals instead of blocking the loopback server.
	var tailscaleExecutable string
	if cfg.Tailscale {
		var err error
		if tailscaleExecutable, err = tailscale.ResolveExecutable(cfg.TailscaleExecutable); err != nil {
			return err
		}
	}

	// Supervisor-owned logging (ATC-260): logfmt on stderr only. The
	// journal captures it on Linux; the LaunchAgent redirects it to the
	// state-dir log file on macOS; foreground runs see it directly.
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	// With auth required everywhere, a server that cannot verify tokens
	// serves nothing but 401s — refuse to start instead (deliberate
	// deviation from legacy, whose loopback path never consulted the file).
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		return err
	}
	store := authtoken.Store{Path: tokenPath}
	if _, err := store.Ensure(); err != nil {
		return err
	}

	version := versionString()
	logger.Info("server starting", "version", version, "pid", os.Getpid())
	handler := server.NewHandler(store.Verify, version, logger)

	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return err
	}

	// The exposure supervisor fronts the actual bound port (they are one
	// port by contract) and is waited on so shutdown reaps the serve
	// child before the process exits.
	var exposure sync.WaitGroup
	if cfg.Tailscale {
		supervisor := tailscale.NewSupervisor(tailscaleExecutable, listener.Addr().(*net.TCPAddr).Port, logger)
		exposure.Go(func() { supervisor.Run(ctx) })
	}

	serveErr := server.Serve(ctx, listener, handler, logger)
	exposure.Wait()
	return serveErr
}

// versionString resolves the binary's version from embedded build info.
// Released builds carry the module version; source builds fall back to the
// VCS revision. The value is a build identity suitable for client/server
// skew comparison, with one limit: builds without VCS metadata all report
// "devel" and cannot be told apart.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	version := info.Main.Version
	if version != "" && version != "(devel)" {
		return version
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "devel"
	}
	if dirty {
		return "devel-" + revision + "-dirty"
	}
	return "devel-" + revision
}
