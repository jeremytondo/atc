package acp

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
)

func TestRealACP(t *testing.T) {
	if os.Getenv("ATC_UNIFIED_ACP_SMOKE") != "1" {
		t.Skip("set ATC_UNIFIED_ACP_SMOKE=1 to call the real official adapters")
	}
	for _, provider := range []domain.Agent{domain.AgentClaude, domain.AgentCodex} {
		t.Run(string(provider), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			events := &realEvents{}
			session, identity, err := New(Config{Stderr: os.Stderr}).Open(ctx, ports.ChatOpen{
				ThreadID: string(provider), Agent: provider, CWD: t.TempDir(), Events: events,
			})
			if err != nil {
				t.Fatal(err)
			}
			events.session = session
			outcome, err := session.Prompt(ctx, "smoke-turn", "Reply with exactly ATC_UNIFIED_OK and do not use tools.")
			if err != nil || outcome != domain.TurnCompleted {
				t.Fatalf("prompt = %s, %v", outcome, err)
			}
			if !strings.Contains(events.text(), "ATC_UNIFIED_OK") {
				t.Fatalf("assistant response = %q", events.text())
			}
			if identity == "" {
				t.Fatal("adapter returned no exact provider identity")
			}
			if err := session.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type realEvents struct {
	mu      sync.Mutex
	session ports.ChatSession
	content strings.Builder
}

func (r *realEvents) AssistantText(_, _ string, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content.WriteString(text)
}
func (*realEvents) BackgroundActivity(string, domain.Activity) {}
func (r *realEvents) Request(_ string, _ string, reference string, _ domain.RequestKind, _ string, options []domain.RequestOption) {
	if r.session != nil && len(options) > 0 {
		_ = r.session.Answer(context.Background(), reference, options[0].ID)
	}
}
func (*realEvents) Raw(string, string, []byte) {}

func (r *realEvents) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.content.String()
}
