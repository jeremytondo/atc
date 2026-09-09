// Package tui is the picker (ATC-316): the Bubble Tea program behind bare
// `atc` and `atc --remote <target>`. It lists Spaces, then a Space's
// Terminals, and attaches by handing the caller's real TTY to a child
// process — `zmx attach` locally, an interactive ssh remotely — through
// Bubble Tea's exec facility, which releases the renderer before the
// child starts and restores it after. Terminal bytes never pass through
// the picker or the API.
//
// The picker is an API client and nothing more: it speaks only the public
// /v1 contract through the Client seam, imports no server, store, or
// domain package, and holds the remote token only in memory. It loads
// state when a screen opens and when an attachment returns, refreshes on
// demand, and never consumes the event stream.
//
// Children are started only through the exec seam, which waits for them,
// so nothing the picker started outlives Run; Bubble Tea restores the
// terminal on every exit path, panics included.
package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
)

const (
	reconnectMin   = time.Second
	reconnectMax   = 30 * time.Second
	requestTimeout = 15 * time.Second
)

// Client is the slice of the /v1 API the picker uses; *api.Client
// satisfies it.
type Client interface {
	Spaces(ctx context.Context) ([]api.Space, error)
	CreateSpace(ctx context.Context, params api.SpaceCreateParams) (api.Space, error)
	DeleteSpace(ctx context.Context, id string) error
	Terminals(ctx context.Context, spaceID string) ([]api.Terminal, error)
	Terminal(ctx context.Context, id string) (api.Terminal, error)
	CreateTerminal(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, error)
	DeleteTerminal(ctx context.Context, id string) error
	Directories(ctx context.Context, path string) (api.DirectoryList, error)
}

// Options wires one picker run.
type Options struct {
	Client Client
	// Target is the ssh target in remote mode; empty means the local
	// server. It is shown in the status line and named in messages.
	Target string
	// ClientVersion and ServerVersion are shown in the status line; a
	// mismatch is flagged there and blocks nothing.
	ClientVersion string
	ServerVersion string
	// Attach resolves the child that hands the TTY to a running terminal:
	// the local zmx handover or the remote ssh channel, bound to ctx so
	// ending the picker ends the child.
	Attach func(ctx context.Context, terminal api.Terminal) (*exec.Cmd, error)
	// TransportLoss reports whether an attach child's exit means the
	// transport failed and the same terminal should be re-attached once
	// it answers again; nil never reconnects (local mode).
	TransportLoss func(err error) bool
}

// Run runs the picker until the user quits or ctx ends. In-flight
// requests and children are bound to ctx.
func Run(ctx context.Context, opts Options) error {
	if opts.Client == nil || opts.Attach == nil {
		return errors.New("tui.Run: Client and Attach are required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := tea.NewProgram(newModel(runCtx, opts), tea.WithContext(runCtx)).Run()
	if errors.Is(err, tea.ErrInterrupted) || (errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil) {
		return nil
	}
	return err
}

type screen int

const (
	screenSpaces screen = iota
	screenTerminals
	screenDirectories
)

// confirmation is the pending delete: what, named for the prompt (a
// Terminal by its label), and for a Space how many Terminals go with it.
type confirmation struct {
	kind  string // "space" or "terminal"
	id    string
	name  string
	count int
}

// reconnect is the same-terminal retry after transport loss; while it is
// set the terminal list is modal and only Esc is heard. generation
// invalidates ticks and polls scheduled by an earlier attempt.
type reconnect struct {
	terminal   api.Terminal
	delay      time.Duration
	generation uint64
}

type model struct {
	ctx           context.Context
	client        Client
	target        string
	clientVersion string
	serverVersion string
	attach        func(context.Context, api.Terminal) (*exec.Cmd, error)
	transportLoss func(error) bool
	// execProcess and tick are the process and clock seams:
	// tea.ExecProcess and tea.Tick in production, scripted in tests.
	execProcess func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	tick        func(time.Duration, uint64) tea.Cmd

	height  int
	screen  screen
	loading bool
	// message is the current screen's notice or error; cleared by the
	// next key press.
	message string
	// seq stamps loads so a result from a superseded request is dropped.
	seq     uint64
	help    bool
	confirm *confirmation

	spaces         []api.Space
	terminalCounts map[string]int
	selectedSpace  string

	space            api.Space
	terminals        []api.Terminal
	selectedTerminal string

	dir         api.DirectoryList
	dirInput    string
	selectedDir string

	reconnect  *reconnect
	generation uint64
}

func newModel(ctx context.Context, opts Options) model {
	return model{
		ctx:           ctx,
		client:        opts.Client,
		target:        opts.Target,
		clientVersion: opts.ClientVersion,
		serverVersion: opts.ServerVersion,
		attach:        opts.Attach,
		transportLoss: opts.TransportLoss,
		execProcess:   tea.ExecProcess,
		tick: func(delay time.Duration, generation uint64) tea.Cmd {
			return tea.Tick(delay, func(time.Time) tea.Msg { return reconnectTickMsg{generation: generation} })
		},
	}
}

// Init has a value receiver and cannot stamp a load, so it asks Update to
// start the first one.
func (m model) Init() tea.Cmd {
	return func() tea.Msg { return startMsg{} }
}

type startMsg struct{}

// Messages: every load and mutation answers with one of these, stamped
// so a superseded request cannot overwrite a newer screen.

type spacesLoadedMsg struct {
	seq       uint64
	spaces    []api.Space
	terminals []api.Terminal
	err       error
}

type terminalsLoadedMsg struct {
	seq       uint64
	spaceID   string
	terminals []api.Terminal
	err       error
}

type directoriesLoadedMsg struct {
	seq  uint64
	list api.DirectoryList
	err  error
}

type spaceCreatedMsg struct {
	seq         uint64
	space       api.Space
	terminal    api.Terminal
	spaceErr    error
	terminalErr error
}

type terminalCreatedMsg struct {
	seq      uint64
	terminal api.Terminal
	err      error
}

type deletedMsg struct {
	seq  uint64
	kind string
	id   string
	err  error
}

type attachEndedMsg struct {
	terminal api.Terminal
	err      error
}

type reconnectTickMsg struct{ generation uint64 }

type reconnectPolledMsg struct {
	generation uint64
	terminal   api.Terminal
	err        error
}

// requestWindowSize asks Bubble Tea to re-read the terminal size — every
// return from an attachment does this before drawing.
func requestWindowSize() tea.Msg { return tea.RequestWindowSize() }

func (m *model) nextSeq() uint64 {
	m.seq++
	m.loading = true
	return m.seq
}

func (m *model) loadSpaces() tea.Cmd {
	seq, client, ctx := m.nextSeq(), m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		spaces, err := client.Spaces(ctx)
		if err != nil {
			return spacesLoadedMsg{seq: seq, err: err}
		}
		terminals, err := client.Terminals(ctx, "")
		if err != nil {
			return spacesLoadedMsg{seq: seq, err: err}
		}
		return spacesLoadedMsg{seq: seq, spaces: spaces, terminals: terminals}
	}
}

func (m *model) loadTerminals() tea.Cmd {
	seq, client, ctx, spaceID := m.nextSeq(), m.client, m.ctx, m.space.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminals, err := client.Terminals(ctx, spaceID)
		return terminalsLoadedMsg{seq: seq, spaceID: spaceID, terminals: terminals, err: err}
	}
}

func (m *model) loadDirectory(path string) tea.Cmd {
	seq, client, ctx := m.nextSeq(), m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		list, err := client.Directories(ctx, path)
		return directoriesLoadedMsg{seq: seq, list: list, err: err}
	}
}

// Mutations are stamped like loads: while one is in flight the keys that
// would start another are ignored (a held-down n must not create a
// terminal per repeat), and a result that arrives after the user has
// navigated on is dropped — the resource exists, and the next load
// shows it.

// createSpace creates the Space at dir and its first shell Terminal; the
// two failures are reported apart because they leave the user in
// different places.
func (m *model) createSpace(dir string) tea.Cmd {
	seq, client, ctx := m.nextSeq(), m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		space, err := client.CreateSpace(ctx, api.SpaceCreateParams{Directory: dir})
		if err != nil {
			return spaceCreatedMsg{seq: seq, spaceErr: err}
		}
		terminal, err := client.CreateTerminal(ctx, api.TerminalCreateParams{SpaceID: space.ID})
		return spaceCreatedMsg{seq: seq, space: space, terminal: terminal, terminalErr: err}
	}
}

func (m *model) createTerminal() tea.Cmd {
	seq, client, ctx, spaceID := m.nextSeq(), m.client, m.ctx, m.space.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminal, err := client.CreateTerminal(ctx, api.TerminalCreateParams{SpaceID: spaceID})
		return terminalCreatedMsg{seq: seq, terminal: terminal, err: err}
	}
}

func (m *model) deleteConfirmed(c confirmation) tea.Cmd {
	seq, client, ctx := m.nextSeq(), m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		var err error
		if c.kind == "space" {
			err = client.DeleteSpace(ctx, c.id)
		} else {
			err = client.DeleteTerminal(ctx, c.id)
		}
		return deletedMsg{seq: seq, kind: c.kind, id: c.id, err: err}
	}
}

func (m *model) pollTerminal(r reconnect) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminal, err := client.Terminal(ctx, r.terminal.ID)
		return reconnectPolledMsg{generation: r.generation, terminal: terminal, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case startMsg:
		return m, m.loadSpaces()
	case tea.WindowSizeMsg:
		m.height = msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if m.screen == screenDirectories {
			m.typeDirectory(strings.TrimSpace(msg.Content))
		}
		return m, nil
	case spacesLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = m.describe("loading spaces", msg.err)
			return m, nil
		}
		m.setSpaces(msg.spaces, msg.terminals)
		return m, nil
	case terminalsLoadedMsg:
		if msg.seq != m.seq || msg.spaceID != m.space.ID {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = m.describe("loading terminals", msg.err)
			return m, nil
		}
		m.setTerminals(msg.terminals)
		return m, nil
	case directoriesLoadedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = m.describe("listing directory", msg.err)
			return m, nil
		}
		m.dir, m.dirInput = msg.list, ""
		m.selectedDir = ""
		m.reselectDirectory()
		return m, nil
	case spaceCreatedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.spaceErr != nil {
			m.message = m.describe("creating space", msg.spaceErr)
			return m, nil
		}
		m.selectedSpace = msg.space.ID
		m.enterSpace(msg.space)
		if msg.terminalErr != nil {
			m.message = m.describe("creating terminal", msg.terminalErr)
			return m, m.loadTerminals()
		}
		m.terminals = append(m.terminals, msg.terminal)
		return m.startAttach(msg.terminal)
	case terminalCreatedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = m.describe("creating terminal", msg.err)
			return m, m.loadTerminals()
		}
		// The new terminal is the newest, so it takes the next number
		// until the list reloads.
		m.terminals = append(m.terminals, msg.terminal)
		return m.startAttach(msg.terminal)
	case deletedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.message = m.describe("deleting "+msg.kind, msg.err)
			return m, nil
		}
		if msg.kind == "space" && (m.screen == screenSpaces || m.space.ID == msg.id) {
			return m.showSpaces()
		}
		if msg.kind == "space" {
			return m, m.loadSpaces()
		}
		return m, m.loadTerminals()
	case attachEndedMsg:
		return m.attachEnded(msg)
	case reconnectTickMsg:
		if m.reconnect == nil || msg.generation != m.reconnect.generation {
			return m, nil
		}
		return m, m.pollTerminal(*m.reconnect)
	case reconnectPolledMsg:
		return m.reconnectPolled(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if m.confirm != nil {
		return m.handleConfirmKey(key)
	}
	if m.reconnect != nil {
		if key == "esc" {
			m.reconnect = nil
			m.generation++
			m.message = "reconnect cancelled"
			return m, m.loadTerminals()
		}
		return m, nil
	}
	// A message lives until the next key: loads never clear one, so a
	// notice set beside a reload (an attachment's exit, a refused
	// create) survives the reload that follows it.
	m.message = ""
	switch m.screen {
	case screenSpaces:
		return m.handleSpacesKey(key)
	case screenTerminals:
		return m.handleTerminalsKey(key)
	case screenDirectories:
		return m.handleDirectoryKey(msg)
	}
	return m, nil
}

func (m model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		if m.loading {
			return m, nil
		}
		c := *m.confirm
		m.confirm = nil
		return m, m.deleteConfirmed(c)
	case "esc", "n":
		m.confirm = nil
	}
	return m, nil
}

func (m model) handleSpacesKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "j", "down":
		m.selectedSpace = moveSelection(spaceIDs(m.spaces), m.selectedSpace, 1)
	case "k", "up":
		m.selectedSpace = moveSelection(spaceIDs(m.spaces), m.selectedSpace, -1)
	case "r":
		return m, m.loadSpaces()
	case "enter":
		if space, ok := m.findSpace(m.selectedSpace); ok {
			m.enterSpace(space)
			return m, m.loadTerminals()
		}
	case "n":
		m.screen = screenDirectories
		m.dir, m.dirInput, m.selectedDir, m.message = api.DirectoryList{}, "", "", ""
		return m, m.loadDirectory("")
	case "d":
		space, ok := m.findSpace(m.selectedSpace)
		if !ok || m.loading {
			return m, nil
		}
		if space.IsDefault {
			m.message = "the Default Space cannot be deleted"
			return m, nil
		}
		m.confirm = &confirmation{kind: "space", id: space.ID, name: space.Name, count: m.terminalCounts[space.ID]}
	}
	return m, nil
}

func (m model) handleTerminalsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case "j", "down":
		m.selectedTerminal = moveSelection(terminalIDs(m.terminals), m.selectedTerminal, 1)
	case "k", "up":
		m.selectedTerminal = moveSelection(terminalIDs(m.terminals), m.selectedTerminal, -1)
	case "r":
		return m, m.loadTerminals()
	case "esc", "h":
		return m.showSpaces()
	case "enter":
		if terminal, ok := m.findTerminal(m.selectedTerminal); ok && !m.loading {
			return m.startAttach(terminal)
		}
	case "n":
		if !m.loading {
			return m, m.createTerminal()
		}
	case "d":
		if terminal, ok := m.findTerminal(m.selectedTerminal); ok && !m.loading {
			m.confirm = &confirmation{kind: "terminal", id: terminal.ID, name: m.label(terminal)}
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// The number is the row's, as held in the current list: after a
		// delete elsewhere since the last refresh the user still gets the
		// terminal they were looking at.
		if m.loading {
			return m, nil
		}
		number := int(key[0] - '0')
		if number > len(m.terminals) {
			m.message = fmt.Sprintf("no terminal %d", number)
			return m, nil
		}
		return m.startAttach(m.terminals[number-1])
	}
	return m, nil
}

// handleDirectoryKey drives the directory picker. Printable input goes
// into the field: a relative text filters the listed entries by prefix;
// a text beginning with '/' is an absolute path Enter navigates to. '.'
// — "this directory", a prefix no visible entry can have — confirms the
// current directory while the field holds a filter.
func (m model) handleDirectoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	absolute := strings.HasPrefix(m.dirInput, "/")
	switch msg.String() {
	case "esc":
		if m.dirInput != "" {
			m.dirInput = ""
			m.reselectDirectory()
			return m, nil
		}
		return m.showSpaces()
	case "up", "ctrl+p":
		m.selectedDir = moveSelection(entryNames(m.filteredEntries()), m.selectedDir, -1)
		return m, nil
	case "down", "ctrl+n":
		m.selectedDir = moveSelection(entryNames(m.filteredEntries()), m.selectedDir, 1)
		return m, nil
	case "enter":
		if absolute {
			return m, m.loadDirectory(m.dirInput)
		}
		for _, entry := range m.filteredEntries() {
			if entry.Name == m.selectedDir {
				return m, m.loadDirectory(entry.Path)
			}
		}
		return m, nil
	case "backspace":
		if m.dirInput == "" {
			if m.dir.Parent != nil {
				return m, m.loadDirectory(*m.dir.Parent)
			}
			return m, nil
		}
		runes := []rune(m.dirInput)
		m.dirInput = string(runes[:len(runes)-1])
		m.reselectDirectory()
		return m, nil
	case "ctrl+r":
		if absolute {
			return m, m.loadDirectory(m.dirInput)
		}
		return m, m.loadDirectory(m.dir.Path)
	case ".":
		if !absolute && m.dir.Path != "" && !m.loading {
			return m, m.createSpace(m.dir.Path)
		}
	}
	if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper|tea.ModHyper) == 0 {
		m.typeDirectory(msg.Text)
	}
	return m, nil
}

// typeDirectory appends text to the field; an absolute path replaces a
// filter, which is how a pasted path lands whole.
func (m *model) typeDirectory(text string) {
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(m.dirInput, "/") {
		m.dirInput = text
	} else {
		m.dirInput += text
	}
	m.reselectDirectory()
}

func (m *model) enterSpace(space api.Space) {
	m.space = space
	m.screen = screenTerminals
	m.terminals, m.selectedTerminal, m.message = nil, "", ""
}

func (m model) showSpaces() (tea.Model, tea.Cmd) {
	m.screen = screenSpaces
	m.message = ""
	return m, m.loadSpaces()
}

// startAttach hands the TTY to terminal's session. Only a running
// terminal is attached, whatever path led here — a create that settled
// as exited or unreachable is refused with its status, not handed to ssh.
func (m model) startAttach(terminal api.Terminal) (tea.Model, tea.Cmd) {
	m.selectedTerminal = terminal.ID
	if terminal.Status != api.TerminalRunning {
		m.message = m.refusal(terminal)
		return m, m.loadTerminals()
	}
	cmd, err := m.attach(m.ctx, terminal)
	if err != nil {
		m.message = "cannot attach: " + err.Error()
		return m, m.loadTerminals()
	}
	m.message = ""
	return m, m.execProcess(cmd, func(err error) tea.Msg {
		return attachEndedMsg{terminal: terminal, err: err}
	})
}

// attachEnded is the return from an attachment. Exit zero is a detach;
// transport loss in remote mode starts the same-terminal retry; any other
// exit is reported and not retried. Every path asks for the terminal size
// again: it may have changed while the child held the TTY.
func (m model) attachEnded(msg attachEndedMsg) (tea.Model, tea.Cmd) {
	m.screen = screenTerminals
	m.selectedTerminal = msg.terminal.ID
	switch {
	case msg.err == nil:
		m.message = ""
	case m.transportLoss != nil && m.transportLoss(msg.err):
		m.generation++
		m.reconnect = &reconnect{terminal: msg.terminal, delay: reconnectMin, generation: m.generation}
		m.message = "connection lost, reconnecting to " + m.label(msg.terminal)
		return m, tea.Batch(requestWindowSize, m.tick(reconnectMin, m.generation))
	default:
		m.message = fmt.Sprintf("attachment to %s ended: %v", m.label(msg.terminal), msg.err)
	}
	return m, tea.Batch(requestWindowSize, m.loadTerminals())
}

func (m model) reconnectPolled(msg reconnectPolledMsg) (tea.Model, tea.Cmd) {
	if m.reconnect == nil || msg.generation != m.reconnect.generation {
		return m, nil
	}
	r := *m.reconnect
	stop := func(message string) (tea.Model, tea.Cmd) {
		m.reconnect = nil
		m.message = message
		return m, m.loadTerminals()
	}
	if msg.err != nil {
		var problem *api.Problem
		if errors.As(msg.err, &problem) && (problem.Status == http.StatusNotFound || problem.Status == http.StatusUnauthorized) {
			// The terminal is gone, or the token no longer works: neither
			// heals by waiting.
			return stop(m.describe("reconnecting to "+m.label(r.terminal), msg.err))
		}
		// Unreachable, or answering with a transient failure: wait
		// longer, up to the cap.
		r.delay = min(r.delay*2, reconnectMax)
		m.reconnect = &r
		return m, m.tick(r.delay, r.generation)
	}
	if msg.terminal.Status != api.TerminalRunning {
		return stop(m.refusal(msg.terminal))
	}
	m.reconnect = nil
	return m.startAttach(msg.terminal)
}

// describe renders a request failure for the screen. A 401 in remote
// mode means the remote token was rotated since the bootstrap; nothing
// but a relaunch fixes that, so the message says so.
func (m model) describe(action string, err error) string {
	var problem *api.Problem
	if m.target != "" && errors.As(err, &problem) && problem.Status == http.StatusUnauthorized {
		return fmt.Sprintf("%s: the remote token was rotated; relaunch atc --remote %s", action, m.target)
	}
	return action + ": " + err.Error()
}

func (m model) refusal(terminal api.Terminal) string {
	label := m.label(terminal)
	if terminal.Status == api.TerminalExited && terminal.ExitCode != nil {
		return fmt.Sprintf("%s has exited with code %d; only running terminals can be attached", label, *terminal.ExitCode)
	}
	return fmt.Sprintf("%s is %s; only running terminals can be attached", label, terminal.Status)
}

// label is the terminal's row label in the current list — `2:nvim`,
// `4:api` — or its bare name or process when it is not listed (a
// terminal the picker no longer shows).
func (m model) label(terminal api.Terminal) string {
	for i, listed := range m.terminals {
		if listed.ID == terminal.ID {
			return cli.Label(i+1, terminal)
		}
	}
	return cli.DisplayName(terminal)
}

// setSpaces installs a loaded space list: the Default Space first, then
// newest-first, with the selection kept by ID or moved to the adjacent
// row when its Space is gone.
func (m *model) setSpaces(spaces []api.Space, terminals []api.Terminal) {
	spaces = append([]api.Space(nil), spaces...)
	sort.SliceStable(spaces, func(i, j int) bool {
		if spaces[i].IsDefault != spaces[j].IsDefault {
			return spaces[i].IsDefault
		}
		return spaces[i].CreatedAt.After(spaces[j].CreatedAt)
	})
	counts := map[string]int{}
	for _, terminal := range terminals {
		counts[terminal.SpaceID]++
	}
	m.selectedSpace = keepSelection(spaceIDs(m.spaces), spaceIDs(spaces), m.selectedSpace)
	m.spaces, m.terminalCounts = spaces, counts
}

// setTerminals installs a loaded terminal list in number order — oldest
// first, newest at the bottom, so row N is terminal N; on first entry
// row one is selected, afterwards the selection is kept by ID or moved
// to the adjacent row.
func (m *model) setTerminals(terminals []api.Terminal) {
	terminals = append([]api.Terminal(nil), terminals...)
	sort.SliceStable(terminals, func(i, j int) bool {
		return terminals[i].CreatedAt.Before(terminals[j].CreatedAt)
	})
	m.selectedTerminal = keepSelection(terminalIDs(m.terminals), terminalIDs(terminals), m.selectedTerminal)
	m.terminals = terminals
}

// filteredEntries is the listing under the current filter; an absolute
// path in the field leaves the listing unfiltered.
func (m model) filteredEntries() []api.DirectoryEntry {
	if m.dirInput == "" || strings.HasPrefix(m.dirInput, "/") {
		return m.dir.Entries
	}
	prefix := strings.ToLower(m.dirInput)
	var entries []api.DirectoryEntry
	for _, entry := range m.dir.Entries {
		if strings.HasPrefix(strings.ToLower(entry.Name), prefix) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (m *model) reselectDirectory() {
	names := entryNames(m.filteredEntries())
	m.selectedDir = keepSelection(nil, names, m.selectedDir)
}

func (m model) findSpace(id string) (api.Space, bool) {
	for _, space := range m.spaces {
		if space.ID == id {
			return space, true
		}
	}
	return api.Space{}, false
}

func (m model) findTerminal(id string) (api.Terminal, bool) {
	for _, terminal := range m.terminals {
		if terminal.ID == id {
			return terminal, true
		}
	}
	return api.Terminal{}, false
}

// keepSelection keeps selected when it is still listed; otherwise it
// falls to the row that held the same position in the old list (the
// adjacent row after a deletion), clamped, or the first row.
func keepSelection(oldIDs, newIDs []string, selected string) string {
	if len(newIDs) == 0 {
		return ""
	}
	index := 0
	for _, id := range newIDs {
		if id == selected {
			return id
		}
	}
	for i, id := range oldIDs {
		if id == selected {
			index = i
		}
	}
	return newIDs[min(index, len(newIDs)-1)]
}

func moveSelection(ids []string, selected string, delta int) string {
	if len(ids) == 0 {
		return ""
	}
	index := 0
	for i, id := range ids {
		if id == selected {
			index = i
		}
	}
	return ids[max(0, min(len(ids)-1, index+delta))]
}

func spaceIDs(spaces []api.Space) []string {
	ids := make([]string, len(spaces))
	for i, space := range spaces {
		ids[i] = space.ID
	}
	return ids
}

func terminalIDs(terminals []api.Terminal) []string {
	ids := make([]string, len(terminals))
	for i, terminal := range terminals {
		ids[i] = terminal.ID
	}
	return ids
}

func entryNames(entries []api.DirectoryEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return names
}
