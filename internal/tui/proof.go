// Package tui contains the client-side terminal UI mechanics shared by the
// ATC launcher. ATC-287 starts with a deliberately small proof screen: it is
// production code for connection and attachment handoff, not the ATC-286
// product interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/remote"
)

const (
	reconnectMin    = time.Second
	reconnectMax    = 30 * time.Second
	snapshotTimeout = 10 * time.Second
)

type terminalClient interface {
	Terminals(context.Context, string) ([]api.Terminal, error)
}

// ProofOptions wires the narrow ATC-287 screen. LocalClient and Attacher are
// required in local mode; SSH and Target are required in remote mode.
type ProofOptions struct {
	LocalClient terminalClient
	Attacher    cli.SessionAttacher
	SSH         *remote.SSH
	Target      string
	Input       io.Reader
	Output      io.Writer
}

// RunProof runs the reusable handoff/recovery mechanics behind the hidden
// ATC-287 command. It leaves ordinary CLI commands independent of Bubble Tea.
func RunProof(ctx context.Context, opts ProofOptions) error {
	runCtx, cancel := context.WithCancel(ctx)
	model, err := newProofModel(runCtx, opts)
	if err != nil {
		cancel()
		return err
	}
	programOptions := []tea.ProgramOption{tea.WithContext(runCtx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	// Bubble Tea deliberately does not wait for long-running Cmd goroutines.
	// Cancel their SSH/API work before inspecting the final model so a quit
	// during connection setup cannot create an unowned forward afterward.
	cancel()
	if opts.SSH != nil {
		if closeErr := opts.SSH.Close(); err == nil {
			err = closeErr
		}
	}
	if finished, ok := final.(proofModel); ok && finished.session != nil {
		if closeErr := finished.session.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

type proofModel struct {
	ctx      context.Context
	client   terminalClient
	attacher cli.SessionAttacher
	ssh      *remote.SSH
	target   string
	session  *remote.Session

	terminals []api.Terminal
	selected  string
	width     int
	height    int
	state     string
	stale     bool
	err       error

	pendingAttach string
	retryDelay    time.Duration
	generation    uint64
	refreshSeq    uint64
	retryCanceled bool
	remeasured    bool
	attempt       *connectAttempt

	retryCommand func(time.Duration, uint64) tea.Cmd
	now          func() time.Time
}

func newProofModel(ctx context.Context, opts ProofOptions) (proofModel, error) {
	m := proofModel{
		ctx: ctx, client: opts.LocalClient, attacher: opts.Attacher,
		ssh: opts.SSH, target: opts.Target, retryDelay: reconnectMin, refreshSeq: 1,
		state: "loading", remeasured: true,
	}
	m.retryCommand = func(delay time.Duration, generation uint64) tea.Cmd {
		return tea.Tick(delay, func(time.Time) tea.Msg { return retryMsg{generation: generation} })
	}
	m.now = time.Now
	if opts.Target == "" {
		if opts.LocalClient == nil || opts.Attacher == nil {
			return proofModel{}, errors.New("local TUI proof requires an API client and terminal attacher")
		}
		return m, nil
	}
	if opts.SSH == nil {
		return proofModel{}, errors.New("remote TUI proof requires an SSH transport")
	}
	m.client = nil
	m.state = "connecting"
	m.attempt = newConnectAttempt(ctx)
	return m, nil
}

func (m proofModel) Init() tea.Cmd {
	if m.target != "" {
		return m.connectCmd(m.attempt)
	}
	return m.refreshCmd(m.refreshSeq)
}

type refreshMsg struct {
	seq       uint64
	terminals []api.Terminal
	err       error
}

type connectMsg struct {
	attempt   *connectAttempt
	session   *remote.Session
	client    terminalClient
	terminals []api.Terminal
	err       error
}

type controlEndedMsg struct {
	session *remote.Session
	err     error
	stable  bool
}

type attachmentEndedMsg struct {
	id  string
	err error
}

type retryMsg struct{ generation uint64 }

type connectAttempt struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newConnectAttempt(parent context.Context) *connectAttempt {
	ctx, cancel := context.WithCancel(parent)
	return &connectAttempt{ctx: ctx, cancel: cancel}
}

func (m proofModel) refreshCmd(seq uint64) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		terminals, err := loadSnapshot(m.ctx, client)
		return refreshMsg{seq: seq, terminals: terminals, err: err}
	}
}

func (m *proofModel) beginConnect() tea.Cmd {
	if m.attempt != nil {
		m.attempt.cancel()
	}
	m.attempt = newConnectAttempt(m.ctx)
	return m.connectCmd(m.attempt)
}

func (m proofModel) connectCmd(attempt *connectAttempt) tea.Cmd {
	return func() tea.Msg {
		session, err := m.ssh.Connect(attempt.ctx, m.target)
		if err != nil {
			return connectMsg{attempt: attempt, err: err}
		}
		terminals, err := loadSnapshot(attempt.ctx, session.Client())
		if err != nil {
			_ = session.Close()
			return connectMsg{attempt: attempt, err: fmt.Errorf("loading remote terminal snapshot: %w", err)}
		}
		return connectMsg{attempt: attempt, session: session, client: session.Client(), terminals: terminals}
	}
}

func loadSnapshot(ctx context.Context, client terminalClient) ([]api.Terminal, error) {
	requestCtx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	return client.Terminals(requestCtx, "")
}

func watchControl(session *remote.Session, connectedAt time.Time, now func() time.Time) tea.Cmd {
	return func() tea.Msg {
		<-session.Done()
		return controlEndedMsg{
			session: session, err: session.WaitError(),
			stable: now().Sub(connectedAt) >= reconnectMin,
		}
	}
}

func closeControl(session *remote.Session) tea.Cmd {
	return func() tea.Msg {
		_ = session.Close()
		return nil
	}
}

func (m proofModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.remeasured = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.state == "connecting" || m.state == "reconnecting" {
				if m.attempt != nil {
					m.attempt.cancel()
					m.attempt = nil
				}
				m.generation++
				m.retryCanceled = true
				m.pendingAttach = ""
				m.state = "disconnected"
				m.err = errors.New("reconnect canceled; press r to retry")
			}
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			return m, nil
		case "k", "up":
			m.moveSelection(-1)
			return m, nil
		case "r":
			m.retryCanceled = false
			m.err = nil
			if m.target != "" && m.session == nil {
				m.generation++
				m.state = "connecting"
				return m, m.beginConnect()
			}
			m.refreshSeq++
			m.state = "refreshing"
			return m, m.refreshCmd(m.refreshSeq)
		case "enter":
			return m.startSelectedAttachment()
		}
	case refreshMsg:
		if msg.seq != m.refreshSeq {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			m.stale = len(m.terminals) > 0
			if m.target != "" {
				return m.loseControl(msg.err)
			}
			m.state = "error"
			return m, nil
		}
		m.setSnapshot(msg.terminals)
		m.state, m.stale, m.err = "connected", false, nil
		return m, nil
	case connectMsg:
		if msg.attempt != m.attempt || m.retryCanceled {
			if msg.session != nil {
				return m, closeControl(msg.session)
			}
			return m, nil
		}
		if msg.err != nil {
			msg.attempt.cancel()
			m.attempt = nil
			if remote.IsStable(msg.err) {
				m.state, m.err = "configuration error", msg.err
				return m, nil
			}
			return m.scheduleReconnect(msg.err, nil)
		}
		m.attempt = nil
		var commands []tea.Cmd
		if m.session != nil && m.session != msg.session {
			commands = append(commands, closeControl(m.session))
		}
		m.session, m.client = msg.session, msg.client
		m.refreshSeq++
		m.setSnapshot(msg.terminals)
		m.state, m.stale, m.err = "connected", false, nil
		m.retryCanceled = false
		if msg.session != nil {
			commands = append(commands, watchControl(msg.session, m.now(), m.now))
		}
		if m.pendingAttach != "" {
			id := m.pendingAttach
			m.pendingAttach = ""
			terminal, ok := m.terminal(id)
			if !ok || terminal.Status != api.TerminalRunning {
				m.err = fmt.Errorf("interrupted terminal %s is no longer running", id)
			} else if cmd, err := m.attachmentCmd(terminal); err != nil {
				m.err = err
			} else {
				m.state = "attaching " + id
				commands = append(commands, execAttachment(cmd, id))
			}
		}
		return m, tea.Batch(commands...)
	case controlEndedMsg:
		if msg.session != m.session {
			return m, nil
		}
		if msg.stable {
			m.retryDelay = reconnectMin
		}
		return m.scheduleReconnect(fmt.Errorf("SSH control connection lost: %w", msg.err), msg.session)
	case retryMsg:
		if msg.generation != m.generation || m.retryCanceled {
			return m, nil
		}
		m.state = "connecting"
		return m, m.beginConnect()
	case attachmentEndedMsg:
		m.remeasured = false
		if msg.err == nil {
			m.state, m.err = "connected", nil
			return m, requestWindowSize
		}
		if m.target != "" && remote.IsTransportFailure(msg.err) {
			m.pendingAttach = msg.id
			model, cmd := m.scheduleReconnect(fmt.Errorf("attachment transport failed: %w", msg.err), m.session)
			return model, tea.Batch(cmd, requestWindowSize)
		}
		m.state, m.err = "connected", fmt.Errorf("attachment ended: %w", msg.err)
		return m, requestWindowSize
	}
	return m, nil
}

func (m proofModel) startSelectedAttachment() (tea.Model, tea.Cmd) {
	if m.stale || m.state != "connected" {
		m.err = errors.New("attachment is unavailable while control state is stale or disconnected")
		return m, nil
	}
	terminal, ok := m.terminal(m.selected)
	if !ok {
		m.err = errors.New("no terminal selected")
		return m, nil
	}
	if terminal.Status != api.TerminalRunning {
		m.err = fmt.Errorf("terminal %s is %s, not running", terminal.ID, terminal.Status)
		return m, nil
	}
	cmd, err := m.attachmentCmd(terminal)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.state, m.err = "attaching "+terminal.ID, nil
	return m, execAttachment(cmd, terminal.ID)
}

func (m proofModel) attachmentCmd(terminal api.Terminal) (*exec.Cmd, error) {
	if m.target != "" {
		return m.ssh.AttachmentCommand(m.target, terminal.ID)
	}
	return cli.PrepareAttach(terminal, m.attacher)
}

func execAttachment(cmd *exec.Cmd, id string) tea.Cmd {
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return attachmentEndedMsg{id: id, err: err}
	})
}

func requestWindowSize() tea.Msg { return tea.RequestWindowSize() }

func (m proofModel) loseControl(err error) (tea.Model, tea.Cmd) {
	return m.scheduleReconnect(err, m.session)
}

func (m proofModel) scheduleReconnect(err error, old *remote.Session) (tea.Model, tea.Cmd) {
	if m.attempt != nil {
		m.attempt.cancel()
		m.attempt = nil
	}
	m.session, m.client = nil, nil
	m.refreshSeq++
	m.stale, m.err, m.state = len(m.terminals) > 0, err, "reconnecting"
	m.retryCanceled = false
	m.generation++
	generation, delay := m.generation, m.retryDelay
	m.retryDelay = min(m.retryDelay*2, reconnectMax)
	commands := []tea.Cmd{m.retryCommand(delay, generation)}
	if old != nil {
		commands = append(commands, closeControl(old))
	}
	return m, tea.Batch(commands...)
}

func (m *proofModel) setSnapshot(terminals []api.Terminal) {
	terminals = append([]api.Terminal(nil), terminals...)
	sort.SliceStable(terminals, func(i, j int) bool {
		return terminals[i].CreatedAt.After(terminals[j].CreatedAt)
	})
	m.terminals = terminals
	if _, ok := m.terminal(m.selected); ok {
		return
	}
	if len(terminals) == 0 {
		m.selected = ""
	} else {
		m.selected = terminals[0].ID
	}
}

func (m proofModel) terminal(id string) (api.Terminal, bool) {
	for _, terminal := range m.terminals {
		if terminal.ID == id {
			return terminal, true
		}
	}
	return api.Terminal{}, false
}

func (m *proofModel) moveSelection(delta int) {
	if len(m.terminals) == 0 {
		return
	}
	index := 0
	for i, terminal := range m.terminals {
		if terminal.ID == m.selected {
			index = i
			break
		}
	}
	index = max(0, min(len(m.terminals)-1, index+delta))
	m.selected = m.terminals[index].ID
}

func (m proofModel) View() tea.View {
	target := "local"
	if m.target != "" {
		target = "ssh:" + safeText(m.target)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ATC-287 TUI handoff proof  target=%s  state=%s  size=%dx%d", target, safeText(m.state), m.width, m.height)
	if m.stale {
		b.WriteString("  STALE")
	}
	if !m.remeasured {
		b.WriteString("  remeasuring")
	}
	b.WriteString("\n\n")
	if len(m.terminals) == 0 {
		b.WriteString("No terminals in the current snapshot.\n")
	}
	for _, terminal := range m.terminals {
		marker := "  "
		if terminal.ID == m.selected {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%-12s %-14s %s\n", marker,
			safeText(terminal.ID), safeText(string(terminal.Status)), safeText(terminal.Name))
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\n%s\n", safeText(m.err.Error()))
	}
	b.WriteString("\n j/k select  enter attach  r refresh/reconnect  esc cancel retry  q quit\n")
	view := tea.NewView(b.String())
	view.AltScreen = true
	return view
}

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return '�'
		}
		return char
	}, value)
}
