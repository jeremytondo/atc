package status

import (
	"testing"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

func TestIdentifyAuthoritativeTUIThreadTransitions(t *testing.T) {
	registry := New(nil)
	tests := []struct {
		name     string
		provider domain.Agent
		raw      string
		identity string
		cause    string
		ok       bool
	}{
		{
			name: "Claude startup", provider: domain.AgentClaude,
			raw:      `{"hook_event_name":"SessionStart","session_id":"claude-one","source":"startup"}`,
			identity: "claude-one", cause: "startup", ok: true,
		},
		{
			name: "Claude in-process resume", provider: domain.AgentClaude,
			raw:      `{"hook_event_name":"SessionStart","session_id":"claude-two","source":"resume"}`,
			identity: "claude-two", cause: "resume", ok: true,
		},
		{
			name: "Claude ordinary status does not switch", provider: domain.AgentClaude,
			raw: `{"hook_event_name":"UserPromptSubmit","session_id":"delayed-other"}`,
		},
		{
			name: "Codex correlated management response", provider: domain.AgentCodex,
			raw:      `{"method":"thread/started","atcExactRoot":"codex-one","atcThreadTransition":"fork"}`,
			identity: "codex-one", cause: "fork", ok: true,
		},
		{
			name: "Codex global broadcast does not switch", provider: domain.AgentCodex,
			raw: `{"method":"thread/started","params":{"thread":{"id":"unrelated"}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, ok := registry.Identify(test.provider, []byte(test.raw))
			if ok != test.ok || observation.Identity != test.identity || observation.Cause != test.cause {
				t.Fatalf("observation = %#v, %v", observation, ok)
			}
		})
	}
}
