package acp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/provider"
)

func TestRealACP(t *testing.T) {
	if os.Getenv("ATC_UNIFIED_ACP_SMOKE") != "1" {
		t.Skip("set ATC_UNIFIED_ACP_SMOKE=1 to call the real official adapters")
	}
	for _, agent := range []domain.Agent{domain.AgentClaude, domain.AgentCodex} {
		t.Run(string(agent), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			dir := t.TempDir()
			events := newRealEvents()
			session, identity, err := realAdapter().Open(ctx, ports.ChatOpen{
				ThreadID: string(agent), Agent: agent, CWD: dir, Events: events,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close(context.Background()) })
			if identity == "" {
				t.Fatal("adapter returned no exact provider identity")
			}

			marker := "ATC_RESTART_" + strings.ToUpper(string(agent))
			allowPath := filepath.Join(dir, "allowed.txt")
			allowPrompt := fmt.Sprintf("Remember the marker %s. Use one tool to write exactly ALLOWED to %s, then reply ALLOW_DONE.", marker, allowPath)
			var allowRequest, denyRequest chan struct{}
			var permissionURL string
			if agent == domain.AgentCodex {
				allowRequest = make(chan struct{}, 1)
				denyRequest = make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/allow" {
						select {
						case allowRequest <- struct{}{}:
						default:
						}
					}
					if request.URL.Path == "/deny" {
						select {
						case denyRequest <- struct{}{}:
						default:
						}
					}
				}))
				t.Cleanup(server.Close)
				permissionURL = server.URL
				allowPrompt = fmt.Sprintf("Remember the marker %s. Use a shell tool to run: curl -fsS -X POST %s/allow . The sandbox attempt should fail; then request elevated permission and retry it once. Reply ALLOW_DONE after the retry.", marker, permissionURL)
			}
			runPermissionTurn(t, ctx, events, session, "allow", allowPrompt, true)
			if agent == domain.AgentCodex {
				select {
				case <-allowRequest:
				default:
					t.Fatal("allowed network request did not reach the test server")
				}
			} else {
				contents, err := os.ReadFile(allowPath)
				if err != nil || strings.TrimSpace(string(contents)) != "ALLOWED" {
					t.Fatalf("allowed tool result = %q, %v", contents, err)
				}
			}

			denyPath := filepath.Join(dir, "denied.txt")
			denyPrompt := fmt.Sprintf("Attempt exactly once to write DENIED to %s. If permission is rejected, do not retry and reply DENY_DONE.", denyPath)
			if agent == domain.AgentCodex {
				denyPrompt = fmt.Sprintf("Use a shell tool to run: curl -fsS -X POST %s/deny . The sandbox attempt should fail; then request elevated permission to retry it. If permission is rejected, do not retry and reply DENY_DONE.", permissionURL)
			}
			runPermissionTurn(t, ctx, events, session, "deny", denyPrompt, false)
			if agent == domain.AgentCodex {
				select {
				case <-denyRequest:
					t.Fatal("denied network request reached the test server")
				default:
				}
			} else if _, err := os.Stat(denyPath); !os.IsNotExist(err) {
				t.Fatalf("denied tool created a file: %v", err)
			}

			runCancellationTurn(t, ctx, events, session)
			if err := session.Close(ctx); err != nil {
				t.Fatal(err)
			}

			reloadedEvents := newRealEvents()
			reloaded, loadedIdentity, err := realAdapter().Open(ctx, ports.ChatOpen{
				ThreadID: string(agent), Agent: agent, CWD: dir,
				ProviderSession: identity, Events: reloadedEvents,
			})
			if err != nil {
				t.Fatalf("load exact session: %v", err)
			}
			t.Cleanup(func() { _ = reloaded.Close(context.Background()) })
			if loadedIdentity != identity {
				t.Fatalf("loaded identity = %q, want %q", loadedIdentity, identity)
			}
			outcome, err := reloaded.Prompt(ctx, "resume", "Reply with only the marker I asked you to remember earlier.")
			if err != nil || outcome != domain.TurnCompleted {
				t.Fatalf("resume prompt = %s, %v", outcome, err)
			}
			if !strings.Contains(reloadedEvents.text(), marker) {
				t.Fatalf("resumed assistant response = %q, want %q", reloadedEvents.text(), marker)
			}
		})
	}
}

func realAdapter() *Adapter {
	return New(Config{
		Models: map[domain.Agent]string{
			domain.AgentClaude: provider.ClaudeCheapModel,
			domain.AgentCodex:  provider.CodexCheapModel,
		},
		Efforts: map[domain.Agent]string{
			domain.AgentClaude: provider.CheapEffort,
			domain.AgentCodex:  provider.CheapEffort,
		},
		Stderr: os.Stderr,
	})
}

func runPermissionTurn(t *testing.T, ctx context.Context, events *realEvents, session ports.ChatSession, turnID, prompt string, allow bool) {
	t.Helper()
	events.clearText()
	result := make(chan realPromptResult, 1)
	go func() {
		outcome, err := session.Prompt(ctx, turnID, prompt)
		result <- realPromptResult{outcome: outcome, err: err}
	}()
	answered := 0
	for {
		select {
		case request := <-events.requests:
			option, ok := permissionOption(request.options, allow)
			if !ok {
				t.Fatalf("no matching permission option in %#v", request.options)
			}
			if err := session.Answer(ctx, request.providerRef, option.ID); err != nil {
				t.Fatal(err)
			}
			answered++
		case promptResult := <-result:
			if promptResult.err != nil || promptResult.outcome != domain.TurnCompleted {
				t.Fatalf("%s prompt = %s, %v", turnID, promptResult.outcome, promptResult.err)
			}
			if answered == 0 {
				t.Fatalf("%s prompt completed without exercising permission; assistant=%q", turnID, events.text())
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func runCancellationTurn(t *testing.T, ctx context.Context, events *realEvents, session ports.ChatSession) {
	t.Helper()
	events.drainActivity()
	result := make(chan realPromptResult, 1)
	go func() {
		outcome, err := session.Prompt(ctx, "cancel", "Use a shell tool to run the exact command sleep 30, then reply CANCEL_DONE.")
		result <- realPromptResult{outcome: outcome, err: err}
	}()
	for {
		select {
		case request := <-events.requests:
			option, ok := permissionOption(request.options, true)
			if !ok {
				t.Fatalf("no allow option in %#v", request.options)
			}
			if err := session.Answer(ctx, request.providerRef, option.ID); err != nil {
				t.Fatal(err)
			}
		case activity := <-events.activities:
			if activity != domain.ActivityWorking {
				continue
			}
			if err := session.Interrupt(ctx, "cancel"); err != nil {
				t.Fatal(err)
			}
			promptResult := <-result
			if promptResult.err != nil || promptResult.outcome != domain.TurnInterrupted {
				t.Fatalf("cancel prompt = %s, %v", promptResult.outcome, promptResult.err)
			}
			return
		case promptResult := <-result:
			t.Fatalf("cancel prompt finished before interruption: %s, %v", promptResult.outcome, promptResult.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func permissionOption(options []domain.RequestOption, allow bool) (domain.RequestOption, bool) {
	needles := []string{"deny", "reject"}
	if allow {
		needles = []string{"allow", "approve"}
	}
	for _, option := range options {
		label := strings.ToLower(option.Label)
		for _, needle := range needles {
			if strings.Contains(label, needle) {
				return option, true
			}
		}
	}
	return domain.RequestOption{}, false
}

type realPromptResult struct {
	outcome domain.TurnOutcome
	err     error
}

type realRequest struct {
	providerRef string
	options     []domain.RequestOption
}

type realEvents struct {
	mu         sync.Mutex
	content    strings.Builder
	requests   chan realRequest
	activities chan domain.Activity
}

func newRealEvents() *realEvents {
	return &realEvents{requests: make(chan realRequest, 16), activities: make(chan domain.Activity, 32)}
}

func (r *realEvents) AssistantText(_, _ string, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content.WriteString(text)
}
func (r *realEvents) BackgroundActivity(_ string, activity domain.Activity) {
	r.activities <- activity
}
func (r *realEvents) Request(_ string, _ string, reference string, _ domain.RequestKind, _ string, options []domain.RequestOption) {
	r.requests <- realRequest{providerRef: reference, options: options}
}
func (*realEvents) Raw(string, string, []byte) {}

func (r *realEvents) clearText() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content.Reset()
}

func (r *realEvents) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.content.String()
}

func (r *realEvents) drainActivity() {
	for {
		select {
		case <-r.activities:
		default:
			return
		}
	}
}
