package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/acp-host/internal/acp"
)

func TestFakeAgentProcess(t *testing.T) {
	if os.Getenv("ATC_ACP_FAKE_AGENT") != "1" {
		return
	}
	if err := runFakeAgent(os.Stdin, os.Stdout, os.Getenv("ATC_ACP_FAKE_VERSION")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestLifecyclePermissionCancellationAndFreshProcessResume(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.json")
	logPath := filepath.Join(directory, "events.jsonl")

	host, logger := startFakeHost(t, directory, statePath, logPath, false)
	if got := host.Snapshot(); got.Status != StatusIdle || got.SessionID != "fake-session" {
		t.Fatalf("unexpected initial state: %+v", got)
	}

	if err := host.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusIdle && snapshot.LastStopReason == "end_turn"
	})

	if err := host.Prompt(context.Background(), "permission"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusWaitingForPermission && snapshot.Pending == 1
	})
	if err := host.Decide("", "allow"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusIdle && snapshot.Pending == 0
	})

	if err := host.Prompt(context.Background(), "permission"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusWaitingForPermission && snapshot.Pending == 1
	})
	if err := host.Decide("", "deny"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusIdle && snapshot.Pending == 0
	})

	if err := host.Prompt(context.Background(), "permission"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusWaitingForPermission && snapshot.Pending == 1
	})
	if err := host.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusIdle && snapshot.Pending == 0 && snapshot.LastStopReason == "cancelled"
	})

	if err := host.Prompt(context.Background(), "cancel"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool { return snapshot.Status == StatusWorking })
	if err := host.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, host, func(snapshot Snapshot) bool {
		return snapshot.Status == StatusIdle && snapshot.LastStopReason == "cancelled"
	})
	stopHost(t, host)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	resumedLogPath := filepath.Join(directory, "resumed.jsonl")
	resumed, resumedLogger := startFakeHost(t, directory, statePath, resumedLogPath, false)
	if got := resumed.Metadata().SessionID; got != "fake-session" {
		t.Fatalf("resumed session ID = %q", got)
	}
	waitEvent(t, resumed, func(event Event) bool {
		return event.Kind == "assistant" && event.Text == "replayed history"
	})
	stopHost(t, resumed)
	if err := resumedLogger.Close(); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"layer":"raw"`) || !strings.Contains(string(contents), `"layer":"normalized"`) {
		t.Fatalf("log does not contain raw and normalized records:\n%s", contents)
	}
	if !strings.Contains(string(contents), `"method":"session/prompt"`) || !strings.Contains(string(contents), `"stopReason":"cancelled"`) {
		t.Fatalf("log does not prove pending v1 prompt completion and cancellation:\n%s", contents)
	}
	resumedContents, err := os.ReadFile(resumedLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resumedContents), `"method":"session/load"`) {
		t.Fatalf("replay did not use session/load:\n%s", resumedContents)
	}
}

func TestResumeWithoutReplayUsesSessionResume(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.json")
	host, logger := startFakeHost(t, directory, statePath, filepath.Join(directory, "created.jsonl"), true)
	stopHost(t, host)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(directory, "resumed.jsonl")
	logger, err := OpenJSONLLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed := New(Config{
		Provider:       "fake",
		Command:        fakeCommand(directory, "1"),
		CWD:            directory,
		StatePath:      statePath,
		ReplayOnResume: false,
		Decision:       "ask",
		Logger:         logger,
		Stderr:         io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := resumed.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	stopHost(t, resumed)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"method":"session/resume"`) || strings.Contains(string(contents), `"method":"session/load"`) {
		t.Fatalf("non-replay restore used the wrong method:\n%s", contents)
	}
}

func TestRejectsProtocolV2(t *testing.T) {
	directory := t.TempDir()
	logger, err := OpenJSONLLogger(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	host := New(Config{
		Provider:  "fake-v2",
		Command:   fakeCommand(directory, "2"),
		CWD:       directory,
		StatePath: filepath.Join(directory, "state.json"),
		ProbeOnly: true,
		Decision:  "ask",
		Logger:    logger,
		Stderr:    io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = host.Start(ctx)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "requires ACP v1") {
		t.Fatalf("expected explicit v2 rejection, got %v", err)
	}
	stopHost(t, host)
}

func TestSelectPermissionOption(t *testing.T) {
	options := []acp.PermissionOption{
		{OptionID: "deny", Kind: "reject_once"},
		{OptionID: "allow", Kind: "allow_once"},
	}
	for _, test := range []struct {
		decision string
		want     string
	}{
		{decision: "allow", want: "allow"},
		{decision: "deny", want: "deny"},
	} {
		option, err := selectPermissionOption(options, test.decision)
		if err != nil {
			t.Fatal(err)
		}
		if option.OptionID != test.want {
			t.Fatalf("%s selected %q, want %q", test.decision, option.OptionID, test.want)
		}
	}
}

func startFakeHost(t *testing.T, directory, statePath, logPath string, forceNew bool) (*Harness, *JSONLLogger) {
	t.Helper()
	logger, err := OpenJSONLLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	host := New(Config{
		Provider:       "fake",
		Command:        fakeCommand(directory, "1"),
		CWD:            directory,
		StatePath:      statePath,
		ReplayOnResume: true,
		ForceNew:       forceNew,
		Decision:       "ask",
		Logger:         logger,
		Stderr:         io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := host.Start(ctx); err != nil {
		logger.Close()
		t.Fatal(err)
	}
	return host, logger
}

func fakeCommand(directory, version string) acp.Command {
	return acp.Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestFakeAgentProcess"},
		Dir:  directory,
		Env: []string{
			"ATC_ACP_FAKE_AGENT=1",
			"ATC_ACP_FAKE_VERSION=" + version,
		},
	}
}

func stopHost(t *testing.T, host *Harness) {
	t.Helper()
	ctx, cancel := ShutdownContext()
	defer cancel()
	if err := host.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, host *Harness, condition func(Snapshot) bool) {
	t.Helper()
	if condition(host.Snapshot()) {
		return
	}
	waitEvent(t, host, func(Event) bool { return condition(host.Snapshot()) })
}

func waitEvent(t *testing.T, host *Harness, condition func(Event) bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-host.Events():
			if condition(event) {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event; state: %+v", host.Snapshot())
		}
	}
}

type fakeWireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

func runFakeAgent(input io.Reader, output io.Writer, version string) error {
	protocolVersion := 2
	if version == "1" {
		protocolVersion = 1
	}
	scanner := bufio.NewScanner(input)
	writer := bufio.NewWriter(output)
	defer writer.Flush()
	permissionPending := false
	var promptID json.RawMessage
	for scanner.Scan() {
		var message fakeWireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return err
		}
		if message.Method == "" && permissionPending && string(message.ID) == "99" {
			permissionPending = false
			var permissionResponse acp.PermissionResponse
			if err := json.Unmarshal(message.Result, &permissionResponse); err != nil {
				return err
			}
			if permissionResponse.Outcome.Outcome == "cancelled" {
				continue
			}
			if err := fakeNotify(writer, "session/update", updateParams("agent_message_chunk", map[string]any{"messageId": "permission-answer", "content": map[string]any{"type": "text", "text": "permission resolved"}})); err != nil {
				return err
			}
			if err := fakeResult(writer, promptID, map[string]any{"stopReason": "end_turn"}); err != nil {
				return err
			}
			promptID = nil
			continue
		}
		switch message.Method {
		case "initialize":
			if err := fakeResult(writer, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"agentCapabilities": map[string]any{
					"loadSession": true,
					"sessionCapabilities": map[string]any{
						"resume": map[string]any{},
						"close":  map[string]any{},
					},
				},
				"agentInfo": map[string]any{"name": "fake-agent", "version": "1.0.0"},
			}); err != nil {
				return err
			}
		case "session/new":
			if err := fakeResult(writer, message.ID, map[string]any{"sessionId": "fake-session"}); err != nil {
				return err
			}
		case "session/load", "session/resume":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return err
			}
			if params.SessionID != "fake-session" {
				return fmt.Errorf("unexpected resume ID %q", params.SessionID)
			}
			if message.Method == "session/load" {
				if err := fakeNotify(writer, "session/update", updateParams("agent_message_chunk", map[string]any{
					"messageId": "replay-1",
					"content":   map[string]any{"type": "text", "text": "replayed history"},
				})); err != nil {
					return err
				}
			}
			if err := fakeResult(writer, message.ID, map[string]any{}); err != nil {
				return err
			}
		case "session/prompt":
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return err
			}
			prompt := params.Prompt[0].Text
			promptID = append(json.RawMessage(nil), message.ID...)
			switch prompt {
			case "permission":
				permissionPending = true
				if err := fakeRequest(writer, 99, "session/request_permission", map[string]any{
					"sessionId": "fake-session",
					"toolCall": map[string]any{
						"toolCallId": "tool-1",
						"title":      "Run harmless command?",
					},
					"options": []map[string]any{
						{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
						{"optionId": "no", "name": "Deny", "kind": "reject_once"},
					},
				}); err != nil {
					return err
				}
			case "cancel":
			default:
				if err := fakeNotify(writer, "session/update", updateParams("agent_message_chunk", map[string]any{
					"messageId": "answer-1",
					"content":   map[string]any{"type": "text", "text": "hello from fake"},
				})); err != nil {
					return err
				}
				if err := fakeResult(writer, promptID, map[string]any{"stopReason": "end_turn"}); err != nil {
					return err
				}
				promptID = nil
			}
		case "session/cancel":
			if len(promptID) == 0 {
				return fmt.Errorf("cancel received without pending prompt")
			}
			if err := fakeResult(writer, promptID, map[string]any{"stopReason": "cancelled"}); err != nil {
				return err
			}
			promptID = nil
		case "session/close":
			if err := fakeResult(writer, message.ID, map[string]any{}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected method %q", message.Method)
		}
	}
	return scanner.Err()
}

func updateParams(kind string, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": kind}
	for key, value := range fields {
		update[key] = value
	}
	return map[string]any{"sessionId": "fake-session", "update": update}
}

func fakeResult(writer *bufio.Writer, id json.RawMessage, result any) error {
	return fakeWrite(writer, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fakeRequest(writer *bufio.Writer, id int, method string, params any) error {
	return fakeWrite(writer, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func fakeNotify(writer *bufio.Writer, method string, params any) error {
	return fakeWrite(writer, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func fakeWrite(writer *bufio.Writer, message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
