package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/provider"
)

type capturedRequest struct {
	turnID      string
	providerRef string
	kind        domain.RequestKind
	options     []domain.RequestOption
}

type captureEvents struct {
	mu         sync.Mutex
	requests   chan capturedRequest
	assistant  chan string
	activities chan domain.Activity
	rawFrames  int
}

func newCaptureEvents() *captureEvents {
	return &captureEvents{
		requests: make(chan capturedRequest, 4), assistant: make(chan string, 4),
		activities: make(chan domain.Activity, 8),
	}
}

func (c *captureEvents) AssistantText(_ string, _ string, text string) { c.assistant <- text }
func (c *captureEvents) BackgroundActivity(_ string, activity domain.Activity) {
	c.activities <- activity
}
func (c *captureEvents) Request(_ string, turnID, providerRef string, kind domain.RequestKind, _ string, options []domain.RequestOption) {
	c.requests <- capturedRequest{turnID: turnID, providerRef: providerRef, kind: kind, options: options}
}
func (c *captureEvents) Raw(_ string, _ string, _ []byte) {
	c.mu.Lock()
	c.rawFrames++
	c.mu.Unlock()
}

func TestACPCreatePromptPermissionAndNormalizedEvents(t *testing.T) {
	events := newCaptureEvents()
	adapter := helperAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opened, identity, err := adapter.Open(ctx, ports.ChatOpen{
		ThreadID: "thread", Agent: domain.AgentClaude, CWD: t.TempDir(), Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity != "helper-session" {
		t.Fatalf("identity = %s", identity)
	}
	outcome := make(chan promptResult, 1)
	go func() {
		result, promptErr := opened.Prompt(ctx, "turn-1", "hello")
		outcome <- promptResult{result, promptErr}
	}()
	request := <-events.requests
	if request.turnID != "turn-1" || request.kind != domain.RequestApproval {
		t.Fatalf("request = %#v", request)
	}
	if len(request.options) != 2 || request.options[0].ID != "option_1" || strings.Contains(request.options[0].ID, "provider") {
		t.Fatalf("normalized options = %#v", request.options)
	}
	if err := opened.Answer(ctx, request.providerRef, request.options[0].ID); err != nil {
		t.Fatal(err)
	}
	if text := <-events.assistant; text != "hello from helper" {
		t.Fatalf("assistant text = %q", text)
	}
	if activity := <-events.activities; activity != domain.ActivityWorking {
		t.Fatalf("tool start = %s", activity)
	}
	if activity := <-events.activities; activity != domain.ActivityIdle {
		t.Fatalf("tool end = %s", activity)
	}
	result := <-outcome
	if result.err != nil || result.outcome != domain.TurnCompleted {
		t.Fatalf("prompt result = %#v", result)
	}
	if err := opened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if events.rawFrames == 0 {
		t.Fatal("raw diagnostics were not captured")
	}
}

func TestACPExactLoadHasNoCreateFallback(t *testing.T) {
	adapter := helperAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opened, identity, err := adapter.Open(ctx, ports.ChatOpen{
		ThreadID: "thread", Agent: domain.AgentCodex, CWD: t.TempDir(),
		ProviderSession: "saved-session", Events: newCaptureEvents(),
	})
	if err != nil || identity != "saved-session" {
		t.Fatalf("exact load = %s, %v", identity, err)
	}
	if err := opened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Open(ctx, ports.ChatOpen{
		ThreadID: "thread", Agent: domain.AgentCodex, CWD: t.TempDir(),
		ProviderSession: "missing-session", Events: newCaptureEvents(),
	}); err == nil || !strings.Contains(err.Error(), "load exact ACP session") {
		t.Fatalf("missing exact load = %v", err)
	}
}

type promptResult struct {
	outcome domain.TurnOutcome
	err     error
}

func helperAdapter() *Adapter {
	command := Command{Path: os.Args[0], Args: []string{"-test.run=TestACPHelperProcess", "--"}, Env: []string{"ATC_ACP_HELPER=1"}}
	return New(Config{
		Commands: map[domain.Agent]Command{domain.AgentClaude: command, domain.AgentCodex: command},
		Models: map[domain.Agent]string{
			domain.AgentClaude: provider.ClaudeCheapModel,
			domain.AgentCodex:  provider.CodexCheapModel,
		},
		Efforts: map[domain.Agent]string{
			domain.AgentClaude: provider.CheapEffort,
			domain.AgentCodex:  provider.CheapEffort,
		},
	})
}

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("ATC_ACP_HELPER") != "1" {
		return
	}
	if err := runHelper(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runHelper() error {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var promptID json.RawMessage
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
			Result json.RawMessage            `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return err
		}
		switch message.Method {
		case "initialize":
			var capabilities map[string]any
			if err := json.Unmarshal(message.Params["clientCapabilities"], &capabilities); err != nil || len(capabilities) != 0 {
				return fmt.Errorf("client execution capabilities were not empty: %s", message.Params["clientCapabilities"])
			}
			respond(encoder, message.ID, map[string]any{
				"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo": map[string]string{"name": "helper", "version": "1"},
			})
		case "session/new":
			respond(encoder, message.ID, map[string]string{"sessionId": "helper-session"})
		case "session/load":
			var sessionID string
			_ = json.Unmarshal(message.Params["sessionId"], &sessionID)
			if sessionID == "missing-session" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32000, "message": "not found"}})
				continue
			}
			respond(encoder, message.ID, map[string]any{})
		case "session/set_config_option":
			var configID, value string
			_ = json.Unmarshal(message.Params["configId"], &configID)
			_ = json.Unmarshal(message.Params["value"], &value)
			respond(encoder, message.ID, map[string]any{
				"configOptions": []map[string]string{{"id": configID, "currentValue": value}},
			})
		case "session/prompt":
			promptID = append(json.RawMessage(nil), message.ID...)
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": "provider-permission", "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "helper-session", "toolCall": map[string]string{"toolCallId": "provider-tool", "title": "Edit file"},
					"options": []map[string]string{
						{"optionId": "provider-allow", "name": "Allow", "kind": "allow_once"},
						{"optionId": "provider-deny", "name": "Deny", "kind": "reject_once"},
					},
				},
			})
		case "session/close":
			respond(encoder, message.ID, map[string]any{})
			return nil
		default:
			if len(message.ID) > 0 && len(message.Result) > 0 && string(message.ID) == `"provider-permission"` {
				notifyUpdate(encoder, "tool_call", "in_progress", nil)
				notifyUpdate(encoder, "agent_message_chunk", "", map[string]string{"type": "text", "text": "hello from helper"})
				notifyUpdate(encoder, "tool_call_update", "completed", nil)
				respond(encoder, promptID, map[string]string{"stopReason": "end_turn"})
			}
		}
	}
	return scanner.Err()
}

func respond(encoder *json.Encoder, id json.RawMessage, result any) {
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func notifyUpdate(encoder *json.Encoder, kind, status string, content any) {
	update := map[string]any{"sessionUpdate": kind, "toolCallId": "provider-tool", "status": status}
	if content != nil {
		update["content"] = content
	}
	_ = encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": "helper-session", "update": update},
	})
}
