package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/adapters/acp"
	"github.com/elevenideas/atc/experiments/unified-core/internal/adapters/zmx"
	"github.com/elevenideas/atc/experiments/unified-core/internal/child"
	"github.com/elevenideas/atc/experiments/unified-core/internal/codexproxy"
	"github.com/elevenideas/atc/experiments/unified-core/internal/core"
	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/httpapi"
	"github.com/elevenideas/atc/experiments/unified-core/internal/play"
	"github.com/elevenideas/atc/experiments/unified-core/internal/provider"
	"github.com/elevenideas/atc/experiments/unified-core/internal/providerprofile"
	"github.com/elevenideas/atc/experiments/unified-core/internal/status"
	"github.com/elevenideas/atc/experiments/unified-core/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atc-unified serve|play|api|repl")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "play":
		return playTUI(args[1:])
	case "api":
		return callAPI(args[1:], os.Stdin, os.Stdout)
	case "repl":
		return repl(args[1:])
	case "__child":
		return runChild(args[1:])
	case "__codex_tui":
		return runCodexTUI(args[1:])
	case "__prepare_claude_profile":
		return prepareClaudeProfile(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func prepareClaudeProfile(args []string) error {
	flags := flag.NewFlagSet("__prepare_claude_profile", flag.ContinueOnError)
	configDir := flags.String("config-dir", "", "isolated Claude configuration directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configDir == "" || flags.NArg() != 0 {
		return errors.New("usage: atc-unified __prepare_claude_profile --config-dir DIR")
	}
	return providerprofile.CompleteClaudeOnboarding(*configDir)
}

func playTUI(args []string) error {
	flags := flag.NewFlagSet("play", flag.ContinueOnError)
	base := flags.String("base", "http://127.0.0.1:7332", "server base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: atc-unified play [--base URL]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return play.Run(ctx, play.Config{BaseURL: *base})
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:7332", "HTTP listen address")
	stateDir := flags.String("state", ".state", "private prototype state directory")
	zmxExecutable := flags.String("zmx", "zmx", "zmx executable")
	codexRemote := flags.String("codex-remote", "", "shared codex app-server endpoint")
	claudeModel := flags.String("claude-model", provider.ClaudeCheapModel, "Claude model for ACP and TUI sessions")
	codexModel := flags.String("codex-model", provider.CodexCheapModel, "Codex model for ACP and TUI sessions")
	effort := flags.String("effort", provider.CheapEffort, "provider reasoning effort")
	debug := flags.Bool("debug", false, "enable raw diagnostic timeline endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	models := map[domain.Agent]string{domain.AgentClaude: *claudeModel, domain.AgentCodex: *codexModel}
	efforts := map[domain.Agent]string{domain.AgentClaude: *effort, domain.AgentCodex: *effort}
	for _, agent := range []domain.Agent{domain.AgentClaude, domain.AgentCodex} {
		if err := provider.ValidateSelection(agent, models[agent], efforts[agent]); err != nil {
			return err
		}
	}
	absState, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	repository, err := store.NewFile(absState)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	terminal, err := zmx.New(zmx.Config{
		Executable: *zmxExecutable, WrapperExecutable: executable,
		SocketDir: filepath.Join(absState, "zmx"), LogDir: filepath.Join(absState, "logs"),
		HookBaseURL: "http://" + *listen, CodexRemote: *codexRemote, Models: models, Efforts: efforts,
	})
	if err != nil {
		return err
	}
	service, err := core.New(core.Config{
		Repository: repository, Chat: acp.New(acp.Config{Models: models, Efforts: efforts, Stderr: os.Stderr}),
		Terminal: terminal, Status: status.New(nil), StateDir: absState,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.RecoverChatSessions(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "chat recovery:", err)
	}
	_, _ = service.ReconcileTerminals(ctx)
	server := &http.Server{Addr: *listen, Handler: httpapi.New(service, *debug), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Close(shutdown)
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "ATC unified-core listening on http://%s\n", *listen)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func callAPI(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("api", flag.ContinueOnError)
	base := flags.String("base", "http://127.0.0.1:7332", "server base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if len(remaining) < 2 {
		return errors.New("usage: atc-unified api [--base URL] METHOD PATH [JSON]")
	}
	var body io.Reader
	if len(remaining) > 2 {
		body = strings.NewReader(strings.Join(remaining[2:], " "))
	} else if remaining[0] != http.MethodGet {
		body = input
	}
	request, err := http.NewRequest(remaining[0], strings.TrimRight(*base, "/")+remaining[1], body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	fmt.Fprintln(output, response.Status)
	_, copyErr := io.Copy(output, response.Body)
	if response.StatusCode >= 400 && copyErr == nil {
		return fmt.Errorf("request failed with %s", response.Status)
	}
	return copyErr
}

func repl(args []string) error {
	flags := flag.NewFlagSet("repl", flag.ContinueOnError)
	base := flags.String("base", "http://127.0.0.1:7332", "server base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Enter METHOD PATH [JSON], or :quit")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stdout, "> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == ":quit" || line == ":exit" {
			return nil
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			fmt.Fprintln(os.Stderr, "usage: METHOD PATH [JSON]")
			continue
		}
		callArgs := []string{"--base", *base, parts[0], parts[1]}
		if len(parts) == 3 {
			callArgs = append(callArgs, parts[2])
		}
		if err := callAPI(callArgs, bytes.NewReader(nil), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

func runChild(args []string) error {
	flags := flag.NewFlagSet("__child", flag.ContinueOnError)
	marker := flags.String("marker", "", "exit marker path")
	terminal := flags.String("terminal", "", "terminal identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	return child.Run(context.Background(), *marker, *terminal, command)
}

func runCodexTUI(args []string) error {
	flags := flag.NewFlagSet("__codex_tui", flag.ContinueOnError)
	remote := flags.String("remote", "", "shared app-server endpoint")
	statusURL := flags.String("status-url", "", "core status ingestion endpoint")
	cwd := flags.String("cwd", "", "working directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	return codexproxy.Run(context.Background(), codexproxy.Config{
		Remote: *remote, StatusURL: *statusURL, CWD: *cwd, Command: command,
	})
}
