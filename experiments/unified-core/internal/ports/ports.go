// Package ports contains the protocol-neutral execution seams. ACP, zmx, and
// provider status protocols must terminate in implementations of these types.
package ports

import (
	"context"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

type ChatOpen struct {
	ThreadID        string
	Agent           domain.Agent
	CWD             string
	ProviderSession string
	Events          ChatEvents
}

type ChatEvents interface {
	AssistantText(threadID, turnID, text string)
	BackgroundActivity(threadID string, activity domain.Activity)
	Request(threadID, turnID, providerRef string, kind domain.RequestKind, prompt string, options []domain.RequestOption)
	Raw(threadID, kind string, raw []byte)
}

type ChatSession interface {
	Prompt(context.Context, string, string) (domain.TurnOutcome, error)
	Interrupt(context.Context, string) error
	Answer(context.Context, string, string) error
	Close(context.Context) error
}

type ChatAdapter interface {
	Open(context.Context, ChatOpen) (ChatSession, string, error)
}

type TerminalEntry struct {
	Name      string
	Reachable bool
	DaemonPID int
}

type TerminalOpen struct {
	TerminalID string
	Agent      domain.Agent
	CWD        string
	Command    []string
	ExitPath   string
}

type TerminalAdapter interface {
	Open(context.Context, TerminalOpen) error
	Inventory(context.Context) ([]TerminalEntry, error)
	Terminate(context.Context, string) error
}

type ProviderObservation struct {
	Activity   domain.Activity
	ObservedAt time.Time
	Rule       string
	Raw        []byte
}

type StatusAdapter interface {
	Observe(threadID string, provider domain.Agent, raw []byte) (ProviderObservation, bool)
	Restore(context.Context, string, domain.Agent, string) (ProviderObservation, error)
}
