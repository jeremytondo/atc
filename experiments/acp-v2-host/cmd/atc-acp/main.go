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
	"strings"
	"time"

	"github.com/elevenideas/atc/experiments/acp-v2-host/internal/acp"
	"github.com/elevenideas/atc/experiments/acp-v2-host/internal/harness"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type options struct {
	provider  string
	agent     string
	agentArgs stringList
	cwd       string
	statePath string
	logPath   string
	decision  string
	new       bool
	replay    bool
	raw       bool
	probe     bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	parsed, err := parseOptions(args)
	if err != nil {
		return err
	}
	command, err := agentCommand(parsed)
	if err != nil {
		return err
	}
	logger, err := harness.OpenJSONLLogger(parsed.logPath)
	if err != nil {
		return err
	}
	defer logger.Close()

	host := harness.New(harness.Config{
		Provider:       parsed.provider,
		Command:        command,
		CWD:            parsed.cwd,
		StatePath:      parsed.statePath,
		ReplayOnResume: parsed.replay,
		ForceNew:       parsed.new,
		ProbeOnly:      parsed.probe,
		RawVisible:     parsed.raw,
		Decision:       parsed.decision,
		Logger:         logger,
		Stderr:         stderr,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = host.Start(ctx)
	cancel()
	if err != nil {
		stopCtx, stopCancel := harness.ShutdownContext()
		_ = host.Stop(stopCtx)
		stopCancel()
		return err
	}
	if parsed.probe {
		metadata, _ := json.MarshalIndent(host.Metadata(), "", "  ")
		fmt.Fprintf(stdout, "ACP v2 supported\n%s\n", metadata)
		stopCtx, stopCancel := harness.ShutdownContext()
		defer stopCancel()
		return host.Stop(stopCtx)
	}

	printMetadata(stdout, host)
	printHelp(stdout)
	go printEvents(stdout, host.Events())

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for {
		fmt.Fprint(stdout, "> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return stop(host)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ":") {
			if err := host.Prompt(context.Background(), line); err != nil {
				fmt.Fprintln(stderr, "error:", err)
			}
			continue
		}
		shouldExit, err := runCommand(line, stdout, host)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
		}
		if shouldExit {
			return stop(host)
		}
	}
}

func parseOptions(args []string) (options, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return options{}, err
	}
	parsed := options{provider: "codex", cwd: cwd, decision: "ask", replay: true}
	flags := flag.NewFlagSet("atc-acp", flag.ContinueOnError)
	flags.StringVar(&parsed.provider, "provider", parsed.provider, "codex, claude, or custom")
	flags.StringVar(&parsed.agent, "agent", "", "ACP agent executable (overrides provider default)")
	flags.Var(&parsed.agentArgs, "agent-arg", "agent argument; may be repeated")
	flags.StringVar(&parsed.cwd, "cwd", parsed.cwd, "absolute session working directory")
	flags.StringVar(&parsed.statePath, "state", "", "persisted session metadata path")
	flags.StringVar(&parsed.logPath, "log", "", "JSONL traffic and normalized event log")
	flags.StringVar(&parsed.decision, "permissions", parsed.decision, "ask, allow, or deny")
	flags.BoolVar(&parsed.new, "new", false, "create a new session instead of resuming saved metadata")
	flags.BoolVar(&parsed.replay, "replay", parsed.replay, "request full history replay on resume")
	flags.BoolVar(&parsed.raw, "raw", false, "show raw ACP traffic (always written to JSONL)")
	flags.BoolVar(&parsed.probe, "probe", false, "only test ACP v2 initialization")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	absCWD, err := filepath.Abs(parsed.cwd)
	if err != nil {
		return options{}, err
	}
	parsed.cwd = absCWD
	if parsed.statePath == "" {
		parsed.statePath = filepath.Join(parsed.cwd, ".atc-acp", parsed.provider+".json")
	}
	if parsed.logPath == "" {
		parsed.logPath = filepath.Join(parsed.cwd, ".atc-acp", parsed.provider+".jsonl")
	}
	if parsed.decision != "ask" && parsed.decision != "allow" && parsed.decision != "deny" {
		return options{}, fmt.Errorf("invalid --permissions %q", parsed.decision)
	}
	return parsed, nil
}

func agentCommand(parsed options) (acp.Command, error) {
	path := parsed.agent
	args := append([]string(nil), parsed.agentArgs...)
	if path == "" {
		path = "npx"
		switch parsed.provider {
		case "codex":
			args = []string{"-y", "@agentclientprotocol/codex-acp"}
		case "claude":
			args = []string{"-y", "@agentclientprotocol/claude-agent-acp"}
		case "custom":
			return acp.Command{}, errors.New("--provider custom requires --agent")
		default:
			return acp.Command{}, fmt.Errorf("unknown provider %q", parsed.provider)
		}
	}
	return acp.Command{Path: path, Args: args, Dir: parsed.cwd}, nil
}

func runCommand(line string, stdout io.Writer, host *harness.Harness) (bool, error) {
	parts := strings.Fields(line)
	switch parts[0] {
	case ":help":
		printHelp(stdout)
	case ":status":
		printJSON(stdout, host.Snapshot())
	case ":meta":
		printMetadata(stdout, host)
	case ":permissions":
		printJSON(stdout, host.PendingPermissions())
	case ":approve", ":deny":
		permissionID := ""
		if len(parts) > 1 {
			permissionID = parts[1]
		}
		decision := strings.TrimPrefix(parts[0], ":")
		return false, host.Decide(permissionID, decision)
	case ":cancel":
		return false, host.Cancel()
	case ":raw":
		if len(parts) != 2 || (parts[1] != "on" && parts[1] != "off") {
			return false, errors.New("usage: :raw on|off")
		}
		host.SetRawVisible(parts[1] == "on")
	case ":exit", ":quit":
		return true, nil
	case ":crash":
		fmt.Fprintln(stdout, "terminating host without cleanup; persisted metadata and JSONL remain")
		os.Exit(86)
	default:
		return false, fmt.Errorf("unknown command %s", parts[0])
	}
	return false, nil
}

func printEvents(stdout io.Writer, events <-chan harness.Event) {
	for event := range events {
		switch event.Kind {
		case "raw":
			fmt.Fprintf(stdout, "\n[raw %s] %s\n", event.Text, event.Raw)
		case "assistant":
			fmt.Fprint(stdout, event.Text)
		case "status":
			fmt.Fprintf(stdout, "\n[status] %s\n", event.Text)
		default:
			fmt.Fprintf(stdout, "\n[%s] %s\n", event.Kind, event.Text)
		}
	}
}

func printMetadata(stdout io.Writer, host *harness.Harness) {
	printJSON(stdout, host.Metadata())
}

func printJSON(stdout io.Writer, value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(stdout, err)
		return
	}
	fmt.Fprintln(stdout, string(encoded))
}

func printHelp(stdout io.Writer) {
	fmt.Fprintln(stdout, `Commands:
  <text>              send a prompt when idle
  :status             inspect normalized ATC-style state
  :meta               inspect provider/session metadata
  :permissions        list pending permission requests
  :approve [id]       choose the first allow option
  :deny [id]          choose the first reject option
  :cancel             cancel active work and pending permissions
  :raw on|off         toggle raw ACP traffic display
  :exit               close the session and agent cleanly
  :crash              terminate the Go host without cleanup`)
}

func stop(host *harness.Harness) error {
	ctx, cancel := harness.ShutdownContext()
	defer cancel()
	return host.Stop(ctx)
}
