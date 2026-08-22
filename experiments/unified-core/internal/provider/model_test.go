package provider

import (
	"strings"
	"testing"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

func TestForbiddenModelFamiliesFailClosed(t *testing.T) {
	for _, test := range []struct {
		agent     domain.Agent
		model     string
		forbidden string
	}{
		{agent: domain.AgentClaude, model: "FABLE", forbidden: "fable"},
		{agent: domain.AgentCodex, model: "gpt-5.6-sol", forbidden: "sol"},
	} {
		if err := ValidateSelection(test.agent, test.model, CheapEffort); err == nil || !strings.Contains(strings.ToLower(err.Error()), test.forbidden) {
			t.Fatalf("ValidateSelection(%s, %q) = %v", test.agent, test.model, err)
		}
	}
}

func TestCheapSelectionsAreAccepted(t *testing.T) {
	if err := ValidateSelection(domain.AgentClaude, ClaudeCheapModel, CheapEffort); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSelection(domain.AgentCodex, CodexCheapModel, CheapEffort); err != nil {
		t.Fatal(err)
	}
}
