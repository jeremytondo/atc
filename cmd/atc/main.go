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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/claude"
	"github.com/jeremytondo/atc/internal/integrations/codex"
	"github.com/jeremytondo/atc/internal/integrations/t3code"
	"github.com/jeremytondo/atc/internal/integrations/zmx"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/server"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/tailscale"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
	"github.com/jeremytondo/atc/internal/upgrade"
	"github.com/jeremytondo/atc/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
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

// run executes the command tree. It is the seam tests drive: the standard
// streams are injected, and errors return here so main owns the exit path.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := newRootCmd()
	root.SetIn(stdin)
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
	root.AddCommand(newThreadCmd(), newTerminalCmd(), newSpaceCmd(), newProjectCmd(), newIntegrationCmd(), newAPICmd(), newVersionCmd(),
		newUpgradeCmd(), newServerCmd(), newChildCmd())
	return root
}

// stdioIsTerminal and stdinIsTTY are this binary's TTY detection, held in
// variables so tests, which never have a TTY, can force them. The cli
// package takes the results as explicit arguments and holds no such state.
var (
	stdioIsTerminal = cli.StdioIsTerminal
	stdinIsTTY      = cli.StdinIsTTY
)

// runWithClient wraps an API-backed command body with the shared client
// construction and error path — every terminal/project/api command starts
// the same way.
func runWithClient(body func(cmd *cobra.Command, args []string, client *api.Client, baseURL string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		client, baseURL, err := cli.NewClient(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		return body(cmd, args, client, baseURL)
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the atc version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}

func newUpgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this binary with the latest release",
		Long: `Download the latest production release, verify its checksum, and atomically
replace this binary. A machine running a dev build is moved to the latest
production release even when that is semver-backwards; with --dev, the
current rolling dev build is installed unconditionally instead.

A server left running the old version is never restarted silently:
interactive runs are asked (terminals persist; in-flight agent turns are
interrupted), headless runs print a reminder unless --restart or
--no-restart pre-answers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			dev, err := flags.GetBool("dev")
			if err != nil {
				return err
			}
			always, err := flags.GetBool("restart")
			if err != nil {
				return err
			}
			never, err := flags.GetBool("no-restart")
			if err != nil {
				return err
			}
			mode := upgrade.RestartAsk
			if always {
				mode = upgrade.RestartAlways
			}
			if never {
				mode = upgrade.RestartNever
			}
			opts := upgrade.Options{
				Dev:         dev,
				Restart:     mode,
				Interactive: stdinIsTTY(),
				Version:     version.String(),
				Stdin:       cmd.InOrStdin(),
				Stdout:      cmd.OutOrStdout(),
				Stderr:      cmd.ErrOrStderr(),
			}
			// A broken config.toml must not block the swap — the new binary
			// may be the fix. Only the post-swap server check needs it.
			if opts.Service, err = lifecycleOptions(cmd); err != nil {
				opts.ConfigErr = err
			}
			return upgrade.Run(cmd.Context(), opts)
		},
	}
	cmd.Flags().Bool("dev", false, "install the current rolling dev build (always reinstalls)")
	cmd.Flags().Bool("restart", false, "restart a server left on the old version, without asking")
	cmd.Flags().Bool("no-restart", false, "never restart the server")
	cmd.MarkFlagsMutuallyExclusive("restart", "no-restart")
	return cmd
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
		Version: version.String(),
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
		Version: version.String(),
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

// tailscaleFlagHelp documents the tri-state lifecycle flag on start and
// restart: persistence in the unit, omission-preserves, and =false as a
// return to config.toml rather than a force-off.
const tailscaleFlagHelp = `

--tailscale renders tailnet exposure into the registered unit, so the setting
survives restarts, reboots, and upgrades. Omitting the flag preserves the
unit's current setting; --tailscale=false removes it so config.toml decides
again (with tailscale = true configured there, exposure stays on).`

// addTailscaleFlag registers the lifecycle --tailscale flag; the tri-state
// semantics live in tailscaleLifecycleOptions.
func addTailscaleFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().Bool("tailscale", false, "persist tailnet exposure in the service unit (omitted: keep the installed setting; false: follow config.toml)")
	return cmd
}

// tailscaleLifecycleOptions is lifecycleOptions plus the tri-state
// --tailscale flag start and restart carry (ATC-283): a flag the user
// never set stays nil so the installed unit's override is preserved.
func tailscaleLifecycleOptions(cmd *cobra.Command) (service.Options, error) {
	opts, err := lifecycleOptions(cmd)
	if err != nil {
		return service.Options{}, err
	}
	if flags := cmd.Flags(); flags.Changed("tailscale") {
		enabled, err := flags.GetBool("tailscale")
		if err != nil {
			return service.Options{}, err
		}
		opts.Tailscale = &enabled
	}
	return opts, nil
}

func newServerStartCmd() *cobra.Command {
	return addTailscaleFlag(lifecycleCmd("start",
		"Register and start the supervised server",
		`Register the server with the user supervisor (launchd on macOS, systemd user
units on Linux) and start it. Registration happens on every start: the unit is
re-rendered from the current binary, so upgrades only need `+"`atc server restart`"+`.
A healthy server running the current unit is left untouched; a changed unit
bounces it. The first start prints what was registered and how to undo it
(atc server uninstall).`+tailscaleFlagHelp,
		tailscaleLifecycleOptions, service.Start))
}

func newServerStopCmd() *cobra.Command {
	return lifecycleCmd("stop",
		"Stop the supervised server without uninstalling it",
		`Stop the supervised server process. The unit stays installed, so the server
returns at next boot on Linux or next login on macOS; use `+"`atc server uninstall`"+` to remove it entirely.`,
		recoveryOptions, service.Stop)
}

func newServerRestartCmd() *cobra.Command {
	return addTailscaleFlag(lifecycleCmd("restart",
		"Restart the supervised server",
		`Re-render the unit and restart the server process. This is the remedy for
upgrades, config edits, and client/server version skew.`+tailscaleFlagHelp,
		tailscaleLifecycleOptions, service.Restart))
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

// hookHost is the address hook deliveries dial. Hooks always run on the
// server's own machine, so an unspecified bind means plain loopback; a
// specific bind (including ::1, which would make 127.0.0.1 unreachable)
// is dialed as bound, bracketed for IPv6 literals.
func hookHost(bind string) string {
	ip := net.ParseIP(bind)
	if ip == nil || ip.IsUnspecified() {
		return "127.0.0.1"
	}
	if ip.To4() == nil {
		return "[" + bind + "]"
	}
	return bind
}

// serverRun runs the server in the foreground until the command context is
// cancelled. A SIGINT that lands during boot (migrations, the startup
// reconcile) is the same clean shutdown as one that lands while serving.
func serverRun(cmd *cobra.Command, args []string) error {
	err := serverRunUntilCancelled(cmd, args)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func serverRunUntilCancelled(cmd *cobra.Command, _ []string) error {
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
	tokens := authtoken.Store{Path: tokenPath}
	if _, err := tokens.Ensure(); err != nil {
		return err
	}

	versionValue := version.String()
	logger.Info("server starting", "version", versionValue, "pid", os.Getpid())

	// Storage opens (and migrates) before any domain state exists; a
	// database that will not open or migrate fails the boot closed.
	databasePath, err := paths.DatabaseFile()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	socketDir, err := paths.TerminalSocketDir()
	if err != nil {
		return err
	}
	markerDir, err := paths.ExitMarkerDir()
	if err != nil {
		return err
	}
	selfExecutable, err := os.Executable()
	if err != nil {
		return err
	}
	// New validates the socket-path budget — a state dir too deep for
	// unix sockets is a boot error with the remedy in the message. A
	// missing zmx binary deliberately is not: statuses degrade to
	// unreachable and delete keeps working.
	driver, err := zmx.New(zmx.Options{
		SocketDir:         socketDir,
		MarkerDir:         markerDir,
		WrapperExecutable: selfExecutable,
		Logger:            logger,
	})
	if err != nil {
		return err
	}
	hub := events.NewHub(events.DefaultBacklog)
	projectService := projects.NewService(projects.Options{
		Repository: database.Projects(),
		Hub:        hub,
	})
	// The Default space is rooted at the server user's home (ATC-296).
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	terminalService := terminals.NewService(terminals.Options{
		Repository: database.Terminals(),
		Driver:     driver,
		Spaces:     database.Spaces(),
		HomeDir:    homeDir,
		MarkerDir:  markerDir,
		Hub:        hub,
		Logger:     logger,
	})
	if err := terminalService.Load(ctx); err != nil {
		return err
	}
	// One blocking reconcile before the listener exists: no request ever
	// observes pre-reconcile state (legacy's rule).
	terminalService.Reconcile(ctx)

	// Threads load after terminals so the boot-time status coercion and
	// the sweep read a settled terminal view.
	threadService := threads.NewService(threads.Options{
		Repository: database.Threads(),
		Terminals:  terminalService,
		Projects:   database.Projects(),
		Hub:        hub,
		Logger:     logger,
	})
	if err := threadService.Load(ctx); err != nil {
		return err
	}

	// The listener binds before the handler exists: the hook plumbing
	// bakes the actual bound port into every launch's settings file.
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Claude hook plumbing (ATC-255): per-launch settings under the state
	// dir, POSTing to the loopback ingest route. Registrations reload so
	// TUIs launched by an earlier server process keep validating.
	hookDir, err := paths.HookDir()
	if err != nil {
		return err
	}
	claudeHooks, err := claude.NewHooks(claude.HooksOptions{
		Dir:       hookDir,
		BaseURL:   fmt.Sprintf("http://%s:%d", hookHost(cfg.Bind), port),
		Threads:   threadService,
		Terminals: terminalService,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	if err := claudeHooks.LoadRegistrations(); err != nil {
		return err
	}

	// Codex thread evidence (ATC-284): one read-only connection to the
	// user's shared app-server, kept for the life of the server. Nothing
	// is started at boot — a Codex launch that finds no server answering
	// starts one.
	codexHome, err := codex.CodexHome()
	if err != nil {
		return err
	}
	codexObserver := codex.NewObserver(codex.ObserverOptions{
		CodexHome:     codexHome,
		ClientVersion: versionValue,
		Threads:       threadService,
		Terminals:     terminalService,
		Logger:        logger,
	})

	// T3 Code (ATC-285): a read-only mirror of the local T3 environment's
	// threads, self-discovering and self-pairing; its session lives beside
	// the auth token. Links on its threads derive from its live state;
	// the threads domain classifies them into projects by origin.
	t3Home, err := t3code.Home()
	if err != nil {
		return err
	}
	t3SessionPath, err := paths.T3CodeSessionFile()
	if err != nil {
		return err
	}
	t3Observer := t3code.New(t3code.Options{
		Home:        t3Home,
		SessionPath: t3SessionPath,
		Threads:     threadService,
		Hub:         hub,
		Logger:      logger,
	})
	threadService.SetLinker(t3code.ID, t3Observer.Links)

	// One registration line per built-in Integration; a duplicate id fails
	// the boot. The typed seams each implements are wired above — the
	// catalog only describes them.
	catalog, err := integrations.NewService(integrations.Options{
		Integrations: []integrations.Integration{
			claude.Integration(claudeHooks), codex.Integration(codexObserver), t3code.Integration(t3Observer), zmx.Integration(),
		},
	})
	if err != nil {
		return err
	}

	// The application coordinator runs the cross-domain workflows: every
	// terminal create (shell, command, App, thread resume), the deletion of
	// terminals and spaces (the terminals domain's delete, then the
	// Integrations' per-terminal cleanups, then the threads view), and the
	// project mutations threads classify against.
	coordinator := application.New(application.Options{
		Terminals:    terminalService,
		Threads:      threadService,
		Projects:     projectService,
		Integrations: catalog,
		Cleanups:     []func(string){claudeHooks.Deregister, codexObserver.Forget},
		Logger:       logger,
	})

	handler := server.NewHandler(server.Options{
		Verify:       tokens.Verify,
		Version:      versionValue,
		Logger:       logger,
		Terminals:    terminalService,
		Projects:     projectService,
		Integrations: catalog,
		Threads:      threadService,
		Events:       hub,
		InternalRoutes: map[string]http.Handler{
			"POST " + claude.HooksPath: claudeHooks.Handler(),
		},
		Coordinator: coordinator,
	})
	// The reconcile loop is waited on before the deferred database close,
	// so shutdown never races an in-flight pass against it. The wait is
	// cheap: every step of the loop is context-aware, and the loop's
	// context is cancelled by the time Serve returns — either by the
	// shutdown signal or, for a server error, explicitly here.
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	var background sync.WaitGroup
	background.Go(func() { terminalService.Run(loopCtx) })
	background.Go(func() { threadService.Run(loopCtx) })
	background.Go(func() { codexObserver.Run(loopCtx) })
	background.Go(func() { t3Observer.Run(loopCtx) })

	// The exposure supervisor fronts the actual bound port (they are one
	// port by contract) and is waited on so shutdown reaps the serve
	// child before the process exits.
	var exposure sync.WaitGroup
	if cfg.Tailscale {
		supervisor := tailscale.NewSupervisor(tailscaleExecutable, listener.Addr().(*net.TCPAddr).Port, logger)
		exposure.Go(func() { supervisor.Run(ctx) })
	}

	serveErr := server.Serve(ctx, listener, handler, logger)
	stopLoop()
	background.Wait()
	exposure.Wait()
	return serveErr
}
