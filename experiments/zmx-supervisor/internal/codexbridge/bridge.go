// Package codexbridge runs a Codex TUI and a private app-server in the same
// zmx-owned process tree. A passive WebSocket connection records the exact
// structured events emitted for the remote TUI; it never writes turns or
// answers provider requests.
package codexbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/agentstatus"
)

type Config struct {
	StateDir  string
	SessionID string
	Command   []string
	CWD       string
}

func Run(config Config) (int, error) {
	if len(config.Command) == 0 {
		return 127, errors.New("codex status bridge requires a command")
	}
	if config.CWD == "" {
		return 127, errors.New("codex status bridge requires a working directory")
	}
	sessionDir, err := agentstatus.SessionDirectory(config.StateDir, config.SessionID)
	if err != nil {
		return 2, err
	}
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return 1, fmt.Errorf("create codex status directory: %w", err)
	}
	socketPath := filepath.Join(sessionDir, "codex-app-server.sock")
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 1, fmt.Errorf("remove stale codex app-server socket: %w", err)
	}
	logFile, err := os.OpenFile(
		filepath.Join(sessionDir, "codex-app-server.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return 1, fmt.Errorf("open codex app-server log: %w", err)
	}
	defer logFile.Close()

	remote := "unix://" + socketPath
	server := exec.Command(config.Command[0], "app-server", "--listen", remote)
	server.Dir = config.CWD
	server.Env = os.Environ()
	server.Stdout = logFile
	server.Stderr = logFile
	if err := server.Start(); err != nil {
		return 127, fmt.Errorf("start codex app-server: %w", err)
	}
	serverDone := waitCommand(server)

	connectContext, cancelConnect := context.WithTimeout(context.Background(), 15*time.Second)
	connection, err := connect(connectContext, socketPath, serverDone)
	cancelConnect()
	if err != nil {
		terminate(server, serverDone)
		return 1, err
	}
	connection.SetReadLimit(16 << 20)
	defer connection.CloseNow()
	if err := initialize(connection); err != nil {
		terminate(server, serverDone)
		return 1, fmt.Errorf("initialize codex app-server observer: %w", err)
	}

	observerDone := make(chan error, 1)
	go func() {
		observerDone <- observe(connection, config.StateDir, config.SessionID)
	}()

	tui := exec.Command(config.Command[0], tuiArguments(config.CWD, remote, config.Command[1:])...)
	tui.Dir = config.CWD
	tui.Env = os.Environ()
	tui.Stdin = os.Stdin
	tui.Stdout = os.Stdout
	tui.Stderr = os.Stderr
	if err := tui.Start(); err != nil {
		connection.CloseNow()
		terminate(server, serverDone)
		return 127, fmt.Errorf("start codex remote TUI: %w", err)
	}
	tuiDone := waitCommand(tui)
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	for {
		select {
		case waitErr := <-tuiDone:
			connection.CloseNow()
			terminate(server, serverDone)
			return exitCode(waitErr), nil
		case waitErr := <-serverDone:
			terminate(tui, tuiDone)
			return 1, unexpectedExit("codex app-server exited while the TUI was running", waitErr)
		case observerErr := <-observerDone:
			terminate(tui, tuiDone)
			terminate(server, serverDone)
			return 1, fmt.Errorf("codex app-server observer stopped: %w", observerErr)
		case received := <-signals:
			_ = tui.Process.Signal(received)
		}
	}
}

func tuiArguments(cwd, remote string, arguments []string) []string {
	result := []string{"--cd", cwd, "--remote", remote}
	return append(result, arguments...)
}

func connect(ctx context.Context, socketPath string, serverDone <-chan error) (*websocket.Conn, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		connection, err := dial(ctx, socketPath)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		select {
		case waitErr := <-serverDone:
			return nil, unexpectedExit("codex app-server exited before accepting connections", waitErr)
		case <-ctx.Done():
			return nil, fmt.Errorf("codex app-server did not accept connections: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func dial(ctx context.Context, socketPath string) (*websocket.Conn, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport}
	connection, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	return connection, nil
}

func initialize(connection *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := map[string]any{
		"id":     1,
		"method": "initialize",
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name":    "atc-zmx-status",
				"title":   "ATC zmx status experiment",
				"version": "0.1.0",
			},
		},
	}
	if err := wsjson.Write(ctx, connection, request); err != nil {
		return err
	}
	for {
		var response struct {
			ID    json.RawMessage `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if err := wsjson.Read(ctx, connection, &response); err != nil {
			return err
		}
		if string(response.ID) != "1" {
			continue
		}
		if len(response.Error) != 0 && string(response.Error) != "null" {
			return fmt.Errorf("initialize rejected: %s", response.Error)
		}
		return wsjson.Write(ctx, connection, map[string]any{"method": "initialized", "params": map[string]any{}})
	}
}

func observe(connection *websocket.Conn, stateDir, sessionID string) error {
	for {
		var payload json.RawMessage
		if err := wsjson.Read(context.Background(), connection, &payload); err != nil {
			return err
		}
		var envelope struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(payload, &envelope) != nil || envelope.Method == "" {
			continue
		}
		if !recordsStatus(envelope.Method) {
			continue
		}
		if err := agentstatus.RecordSignal(stateDir, sessionID, "codex", bytes.NewReader(payload)); err != nil {
			return err
		}
	}
}

func recordsStatus(method string) bool {
	return method == "thread/started" || method == "thread/status/changed"
}

func waitCommand(command *exec.Cmd) <-chan error {
	result := make(chan error, 1)
	go func() { result <- command.Wait() }()
	return result
}

func terminate(command *exec.Cmd, done <-chan error) {
	if command.Process == nil || command.ProcessState != nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = command.Process.Kill()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}

func unexpectedExit(message string, err error) error {
	if err == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, err)
}
