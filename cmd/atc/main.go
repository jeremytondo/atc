// Command atc is the ATC command-line interface. It is the single entrypoint
// for the product: client commands and the server lifecycle both live under
// this binary, per the ATC-246 layout.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "atc:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version":
		return runVersion(stdout, args[1:])
	case "server":
		return runServer(ctx, stdout, stderr, args[1:])
	default:
		return fmt.Errorf("unknown command %q; run `atc help`", args[0])
	}
}

func runVersion(stdout io.Writer, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: atc version")
	}
	fmt.Fprintln(stdout, versionString())
	return nil
}

func runServer(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: atc server <run|token>")
	}
	switch args[0] {
	case "run":
		return serverRun(ctx, stderr, args[1:])
	case "token":
		return serverToken(stdout, args[1:])
	default:
		return fmt.Errorf("unknown server command %q; run `atc help`", args[0])
	}
}

// serverToken prints the bearer token for the paste-once remote setup,
// creating it first when absent — so a fresh install prints the same
// credential the server enforces. `rotate` reissues it; the old token
// stops working immediately, without a server restart.
func serverToken(stdout io.Writer, args []string) error {
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		return err
	}
	store := authtoken.Store{Path: tokenPath}
	switch {
	case len(args) == 0:
		token, err := store.Ensure()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, token)
		return nil
	case len(args) == 1 && args[0] == "rotate":
		token, err := store.Rotate()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, token)
		return nil
	default:
		return fmt.Errorf("usage: atc server token [rotate]")
	}
}

// serverRun runs the server in the foreground until ctx is cancelled. It is
// the permanent foreground primitive a supervisor will exec (ATC-246).
func serverRun(ctx context.Context, stderr io.Writer, args []string) error {
	flags := flag.NewFlagSet("atc server run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flagPort := flags.Int("port", 0, "listen port (overrides ATC_PORT and config.toml)")
	flagBind := flags.String("bind", "", "bind address (overrides ATC_BIND and config.toml)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: atc server run [--port N] [--bind ADDR]")
	}

	configPath, err := paths.ConfigFile()
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return err
	}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.Port = *flagPort
		case "bind":
			cfg.Bind = *flagBind
		}
	})

	// JSON lines at the state-dir log (adopted legacy convention; no
	// rotation, deliberately), mirrored to stderr for the foreground case.
	logPath, err := paths.LogFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(logFile, stderr), nil))

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
	handler := server.NewHandler(server.Options{
		Version: version,
		Verify:  store.Verify,
		Logger:  logger,
	})
	return server.ListenAndServe(ctx, net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)), handler, logger)
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

func printUsage(w io.Writer) {
	fmt.Fprint(w, `atc is the ATC terminal client and server.

Usage:

  atc <command> [arguments]

Commands:

  server run [--port N] [--bind ADDR]
               run the ATC server in the foreground (stop with SIGINT or SIGTERM)
  server token [rotate]
               print the API bearer token, creating it if absent; rotate reissues it
  version      print the atc version
  help         print this help

Configuration precedence: flags > ATC_PORT/ATC_BIND > ~/.config/atc/config.toml > defaults.
Remote access: front the loopback listener with
  tailscale serve --https=7331 localhost:7331
`)
}
