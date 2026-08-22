// Package provider owns provider-specific execution choices that must stay
// consistent across chat and terminal transports. Model policy is validated at
// the boundary so a forbidden selection cannot reach a provider process.
package provider

import (
	"fmt"
	"strings"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

const (
	ClaudeCheapModel = "haiku"
	CodexCheapModel  = "gpt-5.6-luna"
	CheapEffort      = "low"
)

func ValidateSelection(agent domain.Agent, model, effort string) error {
	if model == "" {
		return fmt.Errorf("%s model is required", agent)
	}
	lower := strings.ToLower(model)
	switch agent {
	case domain.AgentClaude:
		if strings.Contains(lower, "fable") {
			return fmt.Errorf("Claude Fable is forbidden")
		}
	case domain.AgentCodex:
		if strings.Contains(lower, "sol") {
			return fmt.Errorf("Codex Sol is forbidden")
		}
	default:
		return fmt.Errorf("unsupported agent %s", agent)
	}
	if effort == "" {
		return fmt.Errorf("%s effort is required", agent)
	}
	return nil
}
