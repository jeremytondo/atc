package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/buildinfo"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/daemon"
	"github.com/jeremytondo/atc/internal/logging"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/server"
	"github.com/spf13/cobra"
)

type envLookup func(string) string

// Execute runs the atc command line interface.
func Execute() error {
	cmd := rootCommand()
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

func rootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "atc",
		Short:         "Run and administer the atc local service",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().String("config", "", "Config file path (default $XDG_CONFIG_HOME/atc/atc.toml, or ATC_CONFIG)")

	cmd.AddCommand(serveCommand(os.Getenv, os.Stderr))
	cmd.AddCommand(startCommand(os.Getenv))
	cmd.AddCommand(stopCommand(os.Getenv))
	cmd.AddCommand(statusCommand(os.Getenv))
	cmd.AddCommand(healthCommand(os.Getenv))
	cmd.AddCommand(versionCommand())
	cmd.AddCommand(actionsCommand(os.Getenv))
	cmd.AddCommand(environmentsCommand(os.Getenv))
	cmd.AddCommand(projectsCommand(os.Getenv))
	cmd.AddCommand(sessionsCommand(os.Getenv))

	return cmd
}

// resolveConfig loads the layered configuration for a command, reading the
// shared --config flag and, when present, the command's --http-addr flag.
func resolveConfig(cmd *cobra.Command, lookup envLookup) (config.Config, error) {
	opts := config.Options{Env: lookup}
	if cmd.Flags().Lookup("config") != nil {
		opts.ConfigPath, _ = cmd.Flags().GetString("config")
		opts.ConfigChanged = cmd.Flags().Changed("config")
	}
	if cmd.Flags().Lookup("http-addr") != nil {
		opts.HTTPAddr, _ = cmd.Flags().GetString("http-addr")
		opts.HTTPAddrChanged = cmd.Flags().Changed("http-addr")
	}
	return config.Load(opts)
}

// resolvePaths derives the service control paths, honoring an explicit
// control_dir from config and otherwise falling back to the environment.
func resolvePaths(cfg config.Config, lookup envLookup) paths.Paths {
	if cfg.Paths.ControlDir != "" {
		return paths.PathsForDir(cfg.Paths.ControlDir)
	}
	return paths.PathsForEnv(paths.EnvLookup(lookup), os.Getuid())
}

func serveCommand(lookup envLookup, logOutput io.Writer) *cobra.Command {
	var httpAddr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the atc service in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(cmd, lookup)
			if err != nil {
				return err
			}
			logger := logging.New(cfg.Log, logOutput)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if err := server.Serve(ctx, server.Config{
				HTTPAddr:         cfg.Server.HTTPAddr,
				HTTPAddrExplicit: cfg.Server.HTTPAddr != server.DefaultHTTPAddr,
				SocketPath:       resolvePaths(cfg, lookup).SocketPath,
				DBPath:           cfg.Store.DBPath,
				ZmxBin:           cfg.Zmx.Bin,
				ActionsPath:      cfg.ActionsPath,
				Environments:     cfg.Environments,
				AuthToken:        cfg.Auth.Token,
				Logger:           logger,
			}); err != nil {
				return fmt.Errorf("run service: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "TCP listen address (default 127.0.0.1:7331, or ATC_HTTP_ADDR)")

	return cmd
}

func startCommand(lookup envLookup) *cobra.Command {
	var httpAddr string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the atc service in the background",
		Long: "Start the atc service in the background.\n\n" +
			"PID file: <service-control-dir>/atc.pid.\n" +
			"Socket path: <service-control-dir>/atc.sock.\n" +
			serviceControlDirHelp(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(cmd, lookup)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Second)
			defer cancel()

			configPath := ""
			if cmd.Flags().Changed("config") {
				configPath, _ = cmd.Flags().GetString("config")
			}

			result, err := daemon.Start(ctx, daemon.Config{
				HTTPAddr:         cfg.Server.HTTPAddr,
				HTTPAddrExplicit: cfg.Server.HTTPAddr != server.DefaultHTTPAddr,
				ConfigPath:       configPath,
				Paths:            resolvePaths(cfg, lookup),
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "started pid %d\n", result.PID)
			fmt.Fprintf(cmd.OutOrStdout(), "http %s\n", result.HTTPAddr)
			fmt.Fprintf(cmd.OutOrStdout(), "socket %s\n", result.SocketPath)
			fmt.Fprintf(cmd.OutOrStdout(), "pid-file %s\n", result.PIDPath)
			fmt.Fprintf(cmd.OutOrStdout(), "log %s\n", result.LogPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "TCP listen address (default 127.0.0.1:7331, or ATC_HTTP_ADDR)")

	return cmd
}

func stopCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background atc service",
		Long: "Stop the background atc service.\n\n" +
			"PID file: <service-control-dir>/atc.pid.\n" +
			"Socket path: <service-control-dir>/atc.sock.\n" +
			serviceControlDirHelp(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(cmd, lookup)
			if err != nil {
				return err
			}
			servicePaths := resolvePaths(cfg, lookup)
			ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Second)
			defer cancel()

			pid, err := daemon.Stop(ctx, servicePaths)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stopped pid %d\n", pid)
			return nil
		},
	}
	return cmd
}

func statusCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report background atc service process status",
		Long: "Report background atc service process status from the PID file and Unix socket.\n\n" +
			"PID file: <service-control-dir>/atc.pid.\n" +
			"Socket path: <service-control-dir>/atc.sock.\n" +
			serviceControlDirHelp(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(cmd, lookup)
			if err != nil {
				return err
			}
			status, err := daemon.InspectService(resolvePaths(cfg, lookup))
			if err != nil {
				return err
			}
			switch {
			case status.Running:
				fmt.Fprintf(cmd.OutOrStdout(), "running pid %d\n", status.PID)
			case status.SocketPID > 0 && status.Stale:
				fmt.Fprintf(cmd.OutOrStdout(), "running pid %d (untracked socket; stale pid %d)\n", status.SocketPID, status.PID)
			case status.SocketPID > 0:
				fmt.Fprintf(cmd.OutOrStdout(), "running pid %d (untracked socket)\n", status.SocketPID)
			case status.SocketReachable && status.Stale:
				fmt.Fprintf(cmd.OutOrStdout(), "running (untracked socket; stale pid %d)\n", status.PID)
			case status.SocketReachable:
				fmt.Fprintln(cmd.OutOrStdout(), "running (untracked socket)")
			case status.Stale:
				fmt.Fprintf(cmd.OutOrStdout(), "not running stale pid %d\n", status.PID)
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "not running")
			}
			return nil
		},
	}
	return cmd
}

func healthCommand(lookup envLookup) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check the atc service API through the local Unix socket",
		Long: "Check the atc service API through the local Unix socket.\n\n" +
			"Socket path: <service-control-dir>/atc.sock. The service control directory is " +
			"$XDG_RUNTIME_DIR/atc when XDG_RUNTIME_DIR is set, otherwise " +
			"$TMPDIR/atc-$UID when TMPDIR is set, otherwise /tmp/atc-$UID.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig(cmd, lookup)
			if err != nil {
				return err
			}
			socketPath := resolvePaths(cfg, lookup).SocketPath
			body, err := newAPIClient(socketPath).get(cmd.Context(), "health")
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}
			var health struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(body, &health); err != nil {
				return fmt.Errorf("health check failed: decode response: %w", err)
			}
			if health.Status == "" {
				return fmt.Errorf("health check failed: response missing status")
			}
			fmt.Fprintln(cmd.OutOrStdout(), health.Status)
			return nil
		},
	}
	return cmd
}

func versionCommand() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the atc build version",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := buildinfo.Current()
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", info.Name, info.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "commit %s\n", info.Commit)
			fmt.Fprintf(cmd.OutOrStdout(), "date %s\n", info.Date)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print version metadata as JSON")
	return cmd
}

func serviceControlDirHelp() string {
	return "The service control directory is $XDG_RUNTIME_DIR/atc when XDG_RUNTIME_DIR is set, " +
		"otherwise $TMPDIR/atc-$UID when TMPDIR is set, otherwise /tmp/atc-$UID."
}
