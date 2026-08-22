package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/agentstatus"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/codexbridge"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/supervisor"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/terminal"
)

type options struct {
	zmx        string
	stateDir   string
	cwd        string
	staleAfter time.Duration
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__child" {
		os.Exit(runChild(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "__agent_status_hook" {
		os.Exit(runAgentStatusHook(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "__codex_status_bridge" {
		os.Exit(runCodexStatusBridge(os.Args[2:]))
	}
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runAgentStatusHook(args []string) int {
	flags := flag.NewFlagSet("__agent_status_hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var stateDir, sessionID, provider string
	flags.StringVar(&stateDir, "state-dir", "", "state directory")
	flags.StringVar(&sessionID, "id", "", "session id")
	flags.StringVar(&provider, "provider", "", "provider")
	if err := flags.Parse(args); err != nil || stateDir == "" || sessionID == "" || provider == "" {
		fmt.Fprintln(os.Stderr, "invalid agent status hook arguments")
		return 2
	}
	if err := agentstatus.RecordSignal(stateDir, sessionID, provider, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "record agent status hook:", err)
		return 1
	}
	return 0
}

func runCodexStatusBridge(args []string) int {
	flags := flag.NewFlagSet("__codex_status_bridge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var stateDir, sessionID string
	flags.StringVar(&stateDir, "state-dir", "", "state directory")
	flags.StringVar(&sessionID, "id", "", "session id")
	if err := flags.Parse(args); err != nil || stateDir == "" || sessionID == "" || len(flags.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "invalid codex status bridge arguments")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve codex status bridge working directory:", err)
		return 1
	}
	code, err := codexbridge.Run(codexbridge.Config{
		StateDir:  stateDir,
		SessionID: sessionID,
		Command:   flags.Args(),
		CWD:       cwd,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex status bridge:", err)
	}
	return code
}

func runChild(args []string) int {
	flags := flag.NewFlagSet("__child", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var markerPath, sessionID string
	flags.StringVar(&markerPath, "marker", "", "exit marker path")
	flags.StringVar(&sessionID, "id", "", "session id")
	if err := flags.Parse(args); err != nil || markerPath == "" || sessionID == "" {
		fmt.Fprintln(os.Stderr, "invalid child wrapper arguments")
		return 127
	}
	return supervisor.RunChild(markerPath, sessionID, flags.Args())
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer) error {
	parsed, command, err := parseOptions(args)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	store, err := supervisor.NewStore(parsed.stateDir)
	if err != nil {
		return err
	}
	adapter, err := terminal.NewZmx(terminal.Config{
		Executable: parsed.zmx,
		SocketDir:  filepath.Join(parsed.stateDir, "zmx"),
	})
	if err != nil {
		return err
	}
	host, err := supervisor.New(supervisor.Config{
		Terminal: adapter, Store: store, Executable: executable, StaleAfter: parsed.staleAfter,
	})
	if err != nil {
		return err
	}
	ctx := context.Background()
	if len(command) != 0 {
		_, err := execute(ctx, command, parsed.cwd, stdin, stdout, host)
		return err
	}

	fmt.Fprintf(stdout, "ATC zmx supervisor experiment\nstate: %s\nzmx:   %s\n", store.Dir(), filepath.Join(store.Dir(), "zmx"))
	printHelp(stdout)
	if snapshots, reconcileErr := host.Reconcile(ctx); reconcileErr != nil {
		fmt.Fprintln(stderr, "recovery warning:", reconcileErr)
		printJSON(stdout, snapshots)
	} else if len(snapshots) > 0 {
		fmt.Fprintln(stdout, "Recovered sessions:")
		printJSON(stdout, snapshots)
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for {
		fmt.Fprint(stdout, "zmx> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		quit, commandErr := execute(ctx, parts, parsed.cwd, stdin, stdout, host)
		if commandErr != nil {
			fmt.Fprintln(stderr, "error:", commandErr)
		}
		if quit {
			return nil
		}
	}
}

func parseOptions(args []string) (options, []string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return options{}, nil, err
	}
	parsed := options{zmx: "zmx", cwd: cwd, staleAfter: 30 * time.Second}
	flags := flag.NewFlagSet("atc-zmx", flag.ContinueOnError)
	flags.StringVar(&parsed.zmx, "zmx", parsed.zmx, "zmx executable")
	flags.StringVar(&parsed.stateDir, "state-dir", "", "private experiment state and zmx directory")
	flags.StringVar(&parsed.cwd, "cwd", parsed.cwd, "default session working directory")
	flags.DurationVar(&parsed.staleAfter, "stale-after", parsed.staleAfter, "absence grace before a missing session becomes stale")
	if err := flags.Parse(args); err != nil {
		return options{}, nil, err
	}
	parsed.cwd, err = filepath.Abs(parsed.cwd)
	if err != nil {
		return options{}, nil, err
	}
	if parsed.stateDir == "" {
		parsed.stateDir = filepath.Join(parsed.cwd, ".atc-zmx")
	}
	parsed.stateDir, err = filepath.Abs(parsed.stateDir)
	if err != nil {
		return options{}, nil, err
	}
	return parsed, flags.Args(), nil
}

func execute(ctx context.Context, parts []string, cwd string, stdin *os.File, stdout io.Writer, host *supervisor.Supervisor) (bool, error) {
	if len(parts) == 0 {
		return false, nil
	}
	switch parts[0] {
	case "help":
		printHelp(stdout)
	case "list", "recover":
		snapshots, err := host.Reconcile(ctx)
		printJSON(stdout, snapshots)
		return false, err
	case "status":
		if len(parts) != 2 {
			return false, errors.New("usage: status NAME")
		}
		snapshots, err := host.Reconcile(ctx)
		if err != nil {
			printJSON(stdout, snapshots)
			return false, err
		}
		for _, snapshot := range snapshots {
			if !snapshot.Orphan && snapshot.Name == parts[1] {
				printJSON(stdout, snapshot)
				return false, nil
			}
		}
		return false, fmt.Errorf("unknown session %q", parts[1])
	case "transitions":
		if len(parts) != 2 {
			return false, errors.New("usage: transitions NAME")
		}
		transitions, err := host.AgentTransitions(parts[1])
		if err == nil {
			printJSON(stdout, transitions)
		}
		return false, err
	case "create":
		if len(parts) < 3 {
			return false, errors.New("usage: create NAME shell|process|codex|claude [COMMAND ARGS...]")
		}
		request := supervisor.CreateRequest{Name: parts[1], Kind: parts[2], CWD: cwd}
		if len(parts) > 3 {
			request.Command = parts[3:]
		}
		snapshot, err := host.Create(ctx, request)
		if err == nil {
			printJSON(stdout, snapshot)
		}
		return false, err
	case "send":
		if len(parts) < 3 {
			return false, errors.New("usage: send NAME TEXT (appends carriage return)")
		}
		return false, host.Send(ctx, parts[1], []byte(strings.Join(parts[2:], " ")+"\r"))
	case "send-raw":
		if len(parts) < 3 {
			return false, errors.New(`usage: send-raw NAME "GO-ESCAPED-BYTES"`)
		}
		value := strings.Join(parts[2:], " ")
		decoded, err := strconv.Unquote(`"` + value + `"`)
		if err != nil {
			return false, err
		}
		return false, host.Send(ctx, parts[1], []byte(decoded))
	case "history":
		if len(parts) < 2 || len(parts) > 3 {
			return false, errors.New("usage: history NAME [LINES]")
		}
		lines := 80
		if len(parts) == 3 {
			parsed, err := strconv.Atoi(parts[2])
			if err != nil || parsed < 1 {
				return false, errors.New("history line count must be positive")
			}
			lines = parsed
		}
		history, err := host.History(ctx, parts[1])
		if err != nil {
			return false, err
		}
		fmt.Fprint(stdout, tailLines(string(history), lines))
	case "attach":
		if len(parts) != 2 {
			return false, errors.New("usage: attach NAME")
		}
		outputFile, ok := stdout.(*os.File)
		if !ok {
			return false, errors.New("attach requires a real terminal output")
		}
		fmt.Fprintln(stdout, "attaching; press Ctrl-\\ to detach without stopping the session")
		return false, host.Attach(ctx, parts[1], stdin, outputFile, os.Stderr)
	case "stop":
		if len(parts) != 2 {
			return false, errors.New("usage: stop NAME")
		}
		return false, host.Stop(ctx, parts[1])
	case "cleanup":
		if len(parts) != 1 {
			return false, errors.New("usage: cleanup")
		}
		result, err := host.Cleanup(ctx)
		if err == nil {
			printJSON(stdout, result)
		}
		return false, err
	case "crash":
		fmt.Fprintln(stdout, "terminating only the Go host; zmx sessions and state remain")
		os.Exit(86)
	case "quit", "exit":
		return true, nil
	default:
		return false, fmt.Errorf("unknown command %q", parts[0])
	}
	return false, nil
}

func tailLines(value string, count int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > count+1 {
		lines = lines[len(lines)-count-1:]
	}
	return strings.Join(lines, "\n")
}

func printJSON(output io.Writer, value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(output, err)
		return
	}
	fmt.Fprintln(output, string(encoded))
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `Commands:
  create NAME shell                    create a login shell
  create NAME process COMMAND [ARGS]   create an arbitrary workload
  create NAME codex [ARGS]             create a real Codex TUI
  create NAME claude [ARGS]            create a real Claude TUI
  list | recover                       reconcile persisted and zmx state
  status NAME                          inspect one normalized session
  transitions NAME                     inspect normalized agent-status evidence
  send NAME TEXT                       send text followed by carriage return
  send-raw NAME ESCAPED                send bytes such as \x03 without a return
  history NAME [LINES]                 receive recent terminal output
  attach NAME                          interact directly; Ctrl-\ detaches
  stop NAME                            deliberately terminate the session
  cleanup                              kill reachable orphans; forget exited/stale records
  crash                                exit only this Go host with status 86
  quit                                 leave every zmx session running`)
}
