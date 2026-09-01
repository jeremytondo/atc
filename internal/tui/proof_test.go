package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/remote"
)

type fakeTerminalClient struct {
	terminals []api.Terminal
	err       error
}

func (c fakeTerminalClient) Terminals(context.Context, string) ([]api.Terminal, error) {
	return append([]api.Terminal(nil), c.terminals...), c.err
}

type fakeAttacher struct{}

func (fakeAttacher) Preflight() error { return nil }
func (fakeAttacher) AttachCommand(id string) (string, []string, []string, error) {
	return "/bin/true", []string{"true", id}, os.Environ(), nil
}

type fakeExit int

func (e fakeExit) Error() string { return "exit" }
func (e fakeExit) ExitCode() int { return int(e) }

func TestSelectionSurvivesRefreshAttachmentAndRemeasure(t *testing.T) {
	older := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	first := api.Terminal{ID: "term-first", Name: "First", Status: api.TerminalRunning, CreatedAt: newer}
	second := api.Terminal{ID: "term-second", Name: "Second", Status: api.TerminalRunning, CreatedAt: older}
	m, err := newProofModel(context.Background(), ProofOptions{
		LocalClient: fakeTerminalClient{}, Attacher: fakeAttacher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := m.Update(refreshMsg{terminals: []api.Terminal{second, first}})
	m = updated.(proofModel)
	if got := terminalIDs(m.terminals); !cmp.Equal(got, []string{"term-first", "term-second"}) {
		t.Fatalf("newest-first snapshot = %v", got)
	}
	m.moveSelection(1)
	if m.selected != "term-second" {
		t.Fatalf("selected = %q", m.selected)
	}

	updated, attachCmd := m.startSelectedAttachment()
	m = updated.(proofModel)
	if attachCmd == nil || m.state != "attaching term-second" {
		t.Fatalf("attach = state %q cmd %v", m.state, attachCmd)
	}
	updated, resizeCmd := m.Update(attachmentEndedMsg{id: "term-second"})
	m = updated.(proofModel)
	if m.selected != "term-second" || m.remeasured || resizeCmd == nil {
		t.Fatalf("after detach = selected %q remeasured %v resizeCmd %v", m.selected, m.remeasured, resizeCmd)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 132, Height: 43})
	m = updated.(proofModel)
	if m.selected != "term-second" || !m.remeasured || m.width != 132 || m.height != 43 {
		t.Fatalf("after remeasure = selected %q size %dx%d remeasured %v", m.selected, m.width, m.height, m.remeasured)
	}

	second.Name = "Renamed after refresh"
	updated, _ = m.Update(refreshMsg{terminals: []api.Terminal{first, second}})
	m = updated.(proofModel)
	if m.selected != "term-second" {
		t.Fatalf("refresh lost stable-ID selection: %q", m.selected)
	}
	updated, _ = m.Update(refreshMsg{terminals: []api.Terminal{first}})
	m = updated.(proofModel)
	if m.selected != "term-first" {
		t.Fatalf("removed selection fallback = %q", m.selected)
	}
}

func TestTransportFailureReconnectsAndRetriesSameTerminal(t *testing.T) {
	terminal := api.Terminal{ID: "term-retry", Name: "Retry", Status: api.TerminalRunning}
	ssh, err := remote.NewSSH("test")
	if err != nil {
		t.Skip(err)
	}
	m := proofModel{
		ctx: context.Background(), ssh: ssh, target: "workstation",
		terminals: []api.Terminal{terminal}, selected: terminal.ID,
		state: "connected", retryDelay: reconnectMin, remeasured: true,
		retryCommand: func(time.Duration, uint64) tea.Cmd { return func() tea.Msg { return nil } },
	}
	updated, cmd := m.Update(attachmentEndedMsg{id: terminal.ID, err: fakeExit(255)})
	m = updated.(proofModel)
	if m.pendingAttach != terminal.ID || m.state != "reconnecting" || !m.stale || cmd == nil {
		t.Fatalf("transport failure = pending %q state %q stale %v cmd %v", m.pendingAttach, m.state, m.stale, cmd)
	}
	generation := m.generation

	updated, retryCmd := m.Update(connectMsg{
		generation: generation,
		client:     fakeTerminalClient{terminals: []api.Terminal{terminal}}, terminals: []api.Terminal{terminal},
	})
	m = updated.(proofModel)
	if m.pendingAttach != "" || m.selected != terminal.ID || m.state != "attaching "+terminal.ID || retryCmd == nil {
		t.Fatalf("reconnected retry = pending %q selected %q state %q cmd %v", m.pendingAttach, m.selected, m.state, retryCmd)
	}
	if m.generation != generation {
		t.Errorf("successful reconnect changed generation: %d -> %d", generation, m.generation)
	}
}

func TestReconnectCancellationAndStableFailureKeepSnapshot(t *testing.T) {
	terminal := api.Terminal{ID: "term-kept", Status: api.TerminalRunning}
	m := proofModel{
		ctx: context.Background(), target: "host", terminals: []api.Terminal{terminal}, selected: terminal.ID,
		state: "reconnecting", stale: true, generation: 3, retryDelay: reconnectMin,
		retryCommand: func(time.Duration, uint64) tea.Cmd { return nil },
	}
	escape := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	updated, _ := m.Update(escape)
	m = updated.(proofModel)
	if !m.retryCanceled || m.state != "disconnected" || m.selected != terminal.ID || len(m.terminals) != 1 {
		t.Fatalf("cancel = canceled %v state %q selected %q terminals %d", m.retryCanceled, m.state, m.selected, len(m.terminals))
	}
	updated, cmd := m.Update(retryMsg{generation: 3})
	if cmd != nil || updated.(proofModel).state != "disconnected" {
		t.Fatal("canceled retry still connected")
	}

	stable := &remote.StableError{Err: errors.New("version mismatch")}
	m.retryCanceled = false
	updated, cmd = m.Update(connectMsg{generation: m.generation, err: stable})
	m = updated.(proofModel)
	if cmd != nil || m.state != "configuration error" || !strings.Contains(m.err.Error(), "version mismatch") || m.selected != terminal.ID {
		t.Fatalf("stable failure = state %q err %v selected %q cmd %v", m.state, m.err, m.selected, cmd)
	}
}

func TestStaleStateRefusesMutationButKeepsNavigation(t *testing.T) {
	m := proofModel{
		terminals: []api.Terminal{
			{ID: "term-a", Status: api.TerminalRunning},
			{ID: "term-b", Status: api.TerminalRunning},
		},
		selected: "term-a", state: "reconnecting", stale: true,
	}
	updated, cmd := m.startSelectedAttachment()
	m = updated.(proofModel)
	if cmd != nil || !strings.Contains(m.err.Error(), "unavailable") {
		t.Fatalf("stale attach = %v, %v", cmd, m.err)
	}
	m.moveSelection(1)
	if m.selected != "term-b" {
		t.Fatalf("stale navigation selected %q", m.selected)
	}
}

func terminalIDs(terminals []api.Terminal) []string {
	ids := make([]string, len(terminals))
	for i, terminal := range terminals {
		ids[i] = terminal.ID
	}
	return ids
}
