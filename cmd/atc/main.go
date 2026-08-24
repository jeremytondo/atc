// Command atc is the ATC command-line interface. It is the single entrypoint
// for the product: client commands and the server lifecycle both live under
// this binary, per the ATC-246 layout.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
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
		return runServer(ctx, stderr, args[1:])
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

func runServer(ctx context.Context, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: atc server run")
	}
	switch args[0] {
	case "run":
		if len(args) != 1 {
			return fmt.Errorf("usage: atc server run")
		}
		return serverRun(ctx, stderr)
	default:
		return fmt.Errorf("unknown server command %q; run `atc help`", args[0])
	}
}

// serverRun runs the server in the foreground until ctx is cancelled. It is
// the permanent foreground primitive a supervisor will exec (ATC-246); the
// API listener is deliberately absent until the ATC-247 transport decision.
func serverRun(ctx context.Context, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("server started", "version", versionString(), "pid", os.Getpid())
	<-ctx.Done()
	logger.Info("server shutting down")
	return nil
}

// versionString resolves the binary's version from embedded build info.
// Released builds carry the module version; source builds fall back to the
// VCS revision. `atc server status` will later compare client and server
// values of this string to detect version skew.
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

  server run   run the ATC server in the foreground (stop with SIGINT or SIGTERM)
  version      print the atc version
  help         print this help
`)
}
