// Package tui is the picker (ATC-316, ATC-327): the Bubble Tea program
// behind bare `atc` and `atc --remote <target>`. It lists the Spaces of
// every connection — Local and the saved remote machines — in one table,
// then the Terminals of one Space or, through the All Spaces entry
// (ATC-329), of every Space at once, and attaches by handing the caller's
// real TTY to a child process — `zmx attach` locally, an interactive ssh
// remotely — through Bubble Tea's exec facility, which releases the
// renderer before the child starts and restores it after. Terminal bytes
// never pass through the picker or the API.
//
// The picker is an API client and nothing more: it speaks only the public
// /v1 contract through the Client seam, imports no server, store, or
// domain package, and holds remote tokens only in memory. Each connection
// is its own world — its own client, transport, availability, loads, and
// recovery — and every resource is addressed by its connection and its
// server-local ID, so two servers that mint the same ID never collide and
// nothing ever falls back to another machine. Connections are attempted
// on launch without a person (no prompts); what a person must do is shown
// on the Connections screen, whose actions hand the terminal to ssh's own
// prompts through the same exec facility as an attachment.
//
// State loads when a screen opens, when an attachment returns, and on
// demand; observations are polled while the terminal list is visible; a
// connection that stops answering is dimmed and polled for health until
// it answers again. The picker never consumes the event stream.
//
// Children are started only through the exec seam, which waits for them,
// so nothing the picker started outlives Run; Bubble Tea restores the
// terminal on every exit path, panics included.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
)

const (
	reconnectMin    = time.Second
	reconnectMax    = 30 * time.Second
	requestTimeout  = 15 * time.Second
	refreshInterval = 2 * time.Second
	// connectTimeout bounds an unattended connection attempt, which may
	// start a stopped server and wait for its tailnet route; past it the
	// connection is failed with a Retry rather than connecting forever.
	connectTimeout = 2 * time.Minute
)

// Client is the slice of the /v1 API the picker uses; *api.Client
// satisfies it.
type Client interface {
	Health(ctx context.Context) (api.Health, error)
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
	// ClientVersion is shown in the help overlay beside every server's;
	// a mismatch is coloured there and blocks nothing.
	ClientVersion string
	// Connections are the machines the picker opens, in display order:
	// Local first on a plain launch, the one target alone under --remote.
	Connections []Connection
	// Aliases are the SSH configuration's Host aliases Add offers.
	Aliases []string
	// Open makes the connector for an alias Add chose.
	Open func(alias string) Connector
	// Save persists the remote connection names, in order, before every
	// add or removal; nil hides Add and Remove (the single-machine picker).
	Save func(remotes []string) error
}

// Connection names one machine and how to reach it.
type Connection struct {
	Name string
	// Local is the built-in connection: never removed, and connected
	// without a terminal handoff.
	Local     bool
	Connector Connector
}

// Connector is one connection's transport and lifecycle, owned by the
// picker for its whole run.
type Connector interface {
	// Connect attempts the connection without a person: nothing prompts
	// and nothing is changed on the machine. An error wrapping
	// ErrLoginRequired or ErrSetupRequired names what Setup would clear.
	Connect(ctx context.Context) (Session, error)
	// Setup is the interactive flow on the caller's terminal streams:
	// ssh's own prompts, the setup plan, its one confirmation, and the
	// approved changes.
	Setup(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) (Session, error)
	// Close releases this picker's private transport. The server and its
	// work are untouched.
	Close() error
}

// Session is a proven-ready server: its client, and the child that hands
// the TTY to one of its terminals.
type Session struct {
	Client        Client
	ServerVersion string
	// Attach resolves the child that attaches a running terminal, bound
	// to ctx so ending the picker ends the child.
	Attach func(ctx context.Context, terminal api.Terminal) (*exec.Cmd, error)
	// TransportLoss reports whether an attach child's exit means the
	// transport failed and the same terminal should be re-attached once
	// it answers again; nil never reconnects (local mode).
	TransportLoss func(err error) bool
	// Notice is one line the connection wants shown when the session
	// opens — what connecting changed, such as the local server being
	// registered with the supervisor on a first run. Connect runs while
	// the picker owns the terminal, so this is its only voice.
	Notice string
}

// ErrLoginRequired and ErrSetupRequired classify a Connect failure for
// the Connections screen: the first needs ssh's prompts answered, the
// second needs setup changes approved. Either is cleared by Setup.
var (
	ErrLoginRequired = errors.New("login required")
	ErrSetupRequired = errors.New("setup required")
)

// Run runs the picker until the user quits or ctx ends, then closes every
// connection it holds. In-flight requests and children are bound to ctx.
func Run(ctx context.Context, opts Options) error {
	if len(opts.Connections) == 0 {
		return errors.New("tui.Run: at least one connection is required")
	}
	for _, c := range opts.Connections {
		if c.Connector == nil {
			return fmt.Errorf("tui.Run: connection %s has no connector", c.Name)
		}
	}
	names := map[string]bool{}
	for _, c := range opts.Connections {
		if names[c.Name] {
			return fmt.Errorf("tui.Run: connection %s is listed twice", c.Name)
		}
		names[c.Name] = true
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	final, err := tea.NewProgram(newModel(runCtx, opts), tea.WithContext(runCtx)).Run()
	// Every connector the run ever held is closed here, removed ones
	// included: their own close may not have run before the program
	// ended, and Close is idempotent.
	connectors := make([]Connector, 0, len(opts.Connections))
	if m, ok := final.(model); ok {
		for _, c := range m.connections {
			connectors = append(connectors, c.connector)
		}
		connectors = append(connectors, m.retired...)
	} else {
		for _, c := range opts.Connections {
			connectors = append(connectors, c.Connector)
		}
	}
	var closeErr error
	for _, connector := range connectors {
		closeErr = errors.Join(closeErr, connector.Close())
	}
	if errors.Is(err, tea.ErrInterrupted) || (errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil) {
		return closeErr
	}
	return errors.Join(err, closeErr)
}

type screen int

const (
	screenSpaces screen = iota
	screenTerminals
	screenDirectories
	// screenDestination chooses the connection a new Space is created on.
	screenDestination
	// screenSpaceChooser chooses the Space a new Terminal is created in,
	// from the All Spaces view.
	screenSpaceChooser
	screenConnections
	screenAliases
)

// confirmation is the pending delete: what, named for the prompt (a
// Terminal by its label), on which connection, and for a Space how many
// Terminals go with it.
type confirmation struct {
	kind  string // "space", "terminal", or "connection"
	conn  string
	id    string
	name  string
	count int
}

// reconnect is the same-terminal retry after transport loss; while it is
// set the terminal list is modal and only Esc is heard. generation
// invalidates ticks and polls scheduled by an earlier attempt.
type reconnect struct {
	conn       string
	terminal   api.Terminal
	delay      time.Duration
	generation uint64
}

// spaceRef addresses one Space: IDs are unique only within a server.
type spaceRef struct {
	conn, id string
}

// allSpaces is the Spaces table's first row as a selection: the entry
// that opens every Space's Terminals. It is navigation, not a Space — no
// connection owns it, and it never reaches a mutation.
var allSpaces = spaceRef{}

// terminalRef addresses one Terminal the same way.
type terminalRef struct {
	conn, id string
}

type model struct {
	ctx           context.Context
	clientVersion string
	connections   []connection
	// retired are removed connections' connectors, closed again at exit.
	retired []Connector
	aliases []string
	open    func(string) Connector
	save    func([]string) error
	// execProcess, execCommand, and tick are the process and clock
	// seams: tea.ExecProcess, tea.Exec, and tea.Tick in production,
	// scripted in tests.
	execProcess func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	execCommand func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	tick        func(time.Duration, tea.Msg) tea.Cmd
	refreshTick func() tea.Cmd
	polling     bool
	attached    bool

	width, height int
	// dark is whether the terminal background is dark, which picks the
	// palette; assumed until the terminal answers.
	dark    bool
	screen  screen
	loading bool
	// message is the current screen's notice or error, failed telling
	// the two apart for colour; cleared by the next key press.
	message string
	failed  bool
	// seq stamps the current screen's loads and mutations so a result
	// from a superseded request, or one answered after the screen was
	// left, is dropped.
	seq  uint64
	help bool
	// helpScroll is the help overlay's first visible line when it does
	// not fit the body.
	helpScroll int
	confirm    *confirmation

	selectedSpace spaceRef
	// selectedConnection is the row on the Connections and destination
	// screens.
	selectedConnection string
	selectedAlias      string

	// conn owns the Space the terminal and directory screens show. The
	// All Spaces view has neither: allSpaces is set instead, and its rows
	// come from every connection's loaded Spaces and Terminals.
	conn             string
	space            api.Space
	terminals        []api.Terminal
	allSpaces        bool
	selectedTerminal terminalRef
	// chosenSpace is the row on the Space chooser.
	chosenSpace spaceRef
	// searching is the terminal screen's search mode and query its text;
	// outside search mode the query is always empty.
	searching bool
	query     string

	dir         api.DirectoryList
	dirInput    string
	selectedDir string

	reconnect  *reconnect
	generation uint64
}

func newModel(ctx context.Context, opts Options) model {
	m := model{
		ctx:           ctx,
		clientVersion: opts.ClientVersion,
		aliases:       opts.Aliases,
		open:          opts.Open,
		save:          opts.Save,
		dark:          true,
		execProcess:   tea.ExecProcess,
		execCommand:   tea.Exec,
		tick: func(delay time.Duration, msg tea.Msg) tea.Cmd {
			return tea.Tick(delay, func(time.Time) tea.Msg { return msg })
		},
		refreshTick: func() tea.Cmd {
			return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
		},
	}
	for _, c := range opts.Connections {
		m.connections = append(m.connections, connection{name: c.Name, local: c.Local, connector: c.Connector, status: connConnecting})
	}
	if len(m.connections) > 0 {
		m.selectedConnection = m.connections[0].name
	}
	return m
}

// Init has a value receiver and cannot stamp a load, so it asks Update to
// start the connections. It also asks the terminal for its background,
// so the palette can follow it.
func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return startMsg{} }, tea.RequestBackgroundColor, m.refreshTick())
}

type startMsg struct{}
type refreshTickMsg struct{}
type terminalsPolledMsg struct{ terminalsLoadedMsg }
type spacesPolledMsg struct{ spacesLoadedMsg }

// Messages: every load and mutation answers with one of these, stamped
// so a superseded request cannot overwrite a newer screen, and named
// for the connection it belongs to.

type spacesLoadedMsg struct {
	conn      string
	seq       uint64
	spaces    []api.Space
	terminals []api.Terminal
	err       error
}

type terminalsLoadedMsg struct {
	conn      string
	seq       uint64
	spaceID   string
	terminals []api.Terminal
	err       error
}

type directoriesLoadedMsg struct {
	conn string
	seq  uint64
	list api.DirectoryList
	err  error
}

type spaceCreatedMsg struct {
	conn        string
	seq         uint64
	space       api.Space
	terminal    api.Terminal
	spaceErr    error
	terminalErr error
}

type terminalCreatedMsg struct {
	conn     string
	seq      uint64
	terminal api.Terminal
	err      error
}

type deletedMsg struct {
	conn string
	seq  uint64
	kind string
	id   string
	err  error
}

type attachEndedMsg struct {
	conn     string
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
// return from a handoff does this before drawing.
func requestWindowSize() tea.Msg { return tea.RequestWindowSize() }

// fail and notify set the message: a request failure or refusal, shown
// in red, or a neutral notice.
func (m *model) fail(text string)   { m.message, m.failed = text, true }
func (m *model) notify(text string) { m.message, m.failed = text, false }

func (m *model) nextSeq() uint64 {
	m.seq++
	m.loading = true
	return m.seq
}

// current is the connection the Space and directory screens work on.
func (m *model) current() *connection { return m.connection(m.conn) }

func (m *model) client() Client { return m.clientOf(m.conn) }

// clientOf is the named connection's client, or nil with the refusal set
// when the connection is not ready: nothing is ever sent to another
// machine instead.
func (m *model) clientOf(conn string) Client {
	c := m.connection(conn)
	if c == nil || c.status != connReady {
		m.fail(m.unavailable(c))
		return nil
	}
	return c.session.Client
}

func (m *model) unavailable(c *connection) string {
	if c == nil {
		return "the connection was removed"
	}
	text := c.name + " is " + c.statusLabel()
	if c.err != nil {
		text += ": " + firstLine(c.reason())
	}
	return text + " (see connections)"
}

// loadTerminals reloads the terminal screen: the Space's Terminals, or
// for All Spaces every ready connection's Spaces and Terminals.
func (m *model) loadTerminals() tea.Cmd {
	if m.allSpaces {
		return m.loadSpaces()
	}
	client := m.client()
	if client == nil {
		return nil
	}
	seq, ctx, conn, spaceID := m.nextSeq(), m.ctx, m.conn, m.space.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminals, err := client.Terminals(ctx, spaceID)
		return terminalsLoadedMsg{conn: conn, seq: seq, spaceID: spaceID, terminals: terminals, err: err}
	}
}

func (m *model) loadDirectory(path string) tea.Cmd {
	client := m.client()
	if client == nil {
		return nil
	}
	seq, ctx, conn := m.nextSeq(), m.ctx, m.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		list, err := client.Directories(ctx, path)
		return directoriesLoadedMsg{conn: conn, seq: seq, list: list, err: err}
	}
}

// Mutations are stamped like loads: while one is in flight the keys that
// would start another are ignored (a held-down n must not create a
// terminal per repeat), and a result that arrives after the user has
// navigated on is dropped — the resource exists, and the next load
// shows it.

// createSpace creates the Space at dir on the current connection and its
// first shell Terminal; the two failures are reported apart because they
// leave the user in different places.
func (m *model) createSpace(dir string) tea.Cmd {
	client := m.client()
	if client == nil {
		return nil
	}
	seq, ctx, conn := m.nextSeq(), m.ctx, m.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		space, err := client.CreateSpace(ctx, api.SpaceCreateParams{Directory: dir})
		if err != nil {
			return spaceCreatedMsg{conn: conn, seq: seq, spaceErr: err}
		}
		terminal, err := client.CreateTerminal(ctx, api.TerminalCreateParams{SpaceID: space.ID})
		return spaceCreatedMsg{conn: conn, seq: seq, space: space, terminal: terminal, terminalErr: err}
	}
}

func (m *model) createTerminal(space spaceRef) tea.Cmd {
	client := m.clientOf(space.conn)
	if client == nil {
		return nil
	}
	seq, ctx := m.nextSeq(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminal, err := client.CreateTerminal(ctx, api.TerminalCreateParams{SpaceID: space.id})
		return terminalCreatedMsg{conn: space.conn, seq: seq, terminal: terminal, err: err}
	}
}

func (m *model) deleteConfirmed(c confirmation) tea.Cmd {
	owner := m.connection(c.conn)
	if owner == nil || owner.status != connReady {
		m.fail(m.unavailable(owner))
		return nil
	}
	client := owner.session.Client
	seq, ctx := m.nextSeq(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		var err error
		if c.kind == "space" {
			err = client.DeleteSpace(ctx, c.id)
		} else {
			err = client.DeleteTerminal(ctx, c.id)
		}
		return deletedMsg{conn: c.conn, seq: seq, kind: c.kind, id: c.id, err: err}
	}
}

func (m *model) pollTerminal(r reconnect) tea.Cmd {
	client := m.clientOf(r.conn)
	if client == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		terminal, err := client.Terminal(ctx, r.terminal.ID)
		return reconnectPolledMsg{generation: r.generation, terminal: terminal, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Connections are edited in place through pointers; the slice is
	// copied first so the model keeps its value semantics.
	m.connections = slices.Clone(m.connections)
	switch msg := msg.(type) {
	case startMsg:
		cmds := make([]tea.Cmd, 0, len(m.connections))
		for i := range m.connections {
			cmds = append(cmds, m.connect(&m.connections[i]))
		}
		return m, tea.Batch(cmds...)
	case refreshTickMsg:
		next := m.refreshTick()
		if m.canPollTerminals() && m.allSpaces {
			return m, tea.Batch(next, m.pollSpaces())
		}
		c := m.current()
		if !m.canPollTerminals() || m.polling || c == nil || c.status != connReady {
			return m, next
		}
		m.polling = true
		seq, conn, spaceID, client, ctx := m.seq, m.conn, m.space.ID, c.session.Client, m.ctx
		return m, tea.Batch(next, func() tea.Msg {
			ctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			terminals, err := client.Terminals(ctx, spaceID)
			return terminalsPolledMsg{terminalsLoadedMsg{conn: conn, seq: seq, spaceID: spaceID, terminals: terminals, err: err}}
		})
	case terminalsPolledMsg:
		m.polling = false
		if msg.conn != m.conn || msg.seq != m.seq || msg.spaceID != m.space.ID {
			return m, nil
		}
		if msg.err != nil {
			// The list and any notice stay; a transport failure still
			// marks the connection, so its recovery starts now rather
			// than at the next explicit request.
			return m, m.transportFailed(msg.conn, msg.err)
		}
		if m.canPollTerminals() {
			m.setTerminals(msg.terminals)
		}
		return m, nil
	case spacesPolledMsg:
		c := m.connection(msg.conn)
		if c == nil {
			return m, nil
		}
		c.polling = false
		if msg.seq != c.seq {
			return m, nil
		}
		if msg.err != nil {
			return m, m.transportFailed(msg.conn, msg.err)
		}
		if m.canPollTerminals() {
			m.setSpaces(c, msg.spaces, msg.terminals)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.helpScroll = min(m.helpScroll, m.helpScrollLimit())
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if m.screen == screenDirectories {
			m.typeDirectory(strings.TrimSpace(msg.Content))
		}
		return m, nil
	case connectedMsg:
		return m.connected(msg)
	case setupEndedMsg:
		return m.setupEnded(msg)
	case recoverTickMsg:
		c := m.connection(msg.conn)
		if c == nil || c.gen != msg.gen || c.status != connUnavailable {
			return m, nil
		}
		return m, m.checkHealth(c)
	case healthMsg:
		return m.healthChecked(msg)
	case closedMsg:
		return m, nil
	case spacesLoadedMsg:
		c := m.connection(msg.conn)
		if c == nil || msg.seq != c.seq {
			return m, nil
		}
		c.loading = false
		if msg.err != nil {
			return m, m.requestFailed(msg.conn, "loading spaces on "+msg.conn, msg.err)
		}
		m.setSpaces(c, msg.spaces, msg.terminals)
		return m, nil
	case terminalsLoadedMsg:
		if msg.conn != m.conn || msg.seq != m.seq || msg.spaceID != m.space.ID {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			return m, m.requestFailed(msg.conn, "loading terminals", msg.err)
		}
		m.setTerminals(msg.terminals)
		return m, nil
	case directoriesLoadedMsg:
		if msg.conn != m.conn || msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			return m, m.requestFailed(msg.conn, "listing directory", msg.err)
		}
		m.dir, m.dirInput = msg.list, ""
		m.selectedDir = ""
		m.reselectDirectory()
		return m, nil
	case spaceCreatedMsg:
		if msg.conn != m.conn || msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.spaceErr != nil {
			return m, m.requestFailed(msg.conn, "creating space", msg.spaceErr)
		}
		m.selectedSpace = spaceRef{conn: msg.conn, id: msg.space.ID}
		m.enterSpace(msg.conn, msg.space)
		if msg.terminalErr != nil {
			m.fail(m.describe(m.current(), "creating terminal", msg.terminalErr))
			return m, m.loadTerminals()
		}
		m.terminals = append(m.terminals, msg.terminal)
		return m.startAttach(msg.conn, msg.terminal)
	case terminalCreatedMsg:
		// The stamp alone identifies the request: from All Spaces the
		// owning connection is the chosen Space's, not the screen's.
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			return m, tea.Batch(m.requestFailed(msg.conn, "creating terminal", msg.err), m.loadTerminals())
		}
		// The new terminal is the newest of its Space, so it takes the
		// next number there until the list reloads.
		if c := m.connection(msg.conn); m.allSpaces && c != nil {
			c.terminals = append(slices.Clone(c.terminals), msg.terminal)
		} else {
			m.terminals = append(m.terminals, msg.terminal)
		}
		return m.startAttach(msg.conn, msg.terminal)
	case deletedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			return m, m.requestFailed(msg.conn, "deleting "+msg.kind, msg.err)
		}
		if msg.kind == "space" && (m.screen == screenSpaces || (m.conn == msg.conn && m.space.ID == msg.id)) {
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

// Background reads never block an action, interrupt a handoff or
// confirmation, or replace a newer explicit request's result.
func (m model) canPollTerminals() bool {
	return m.screen == screenTerminals && !m.loading && !m.attached && m.reconnect == nil && m.confirm == nil && !m.help
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.help {
		// Movement scrolls the help only while it overflows the body;
		// otherwise any key closes it.
		if limit := m.helpScrollLimit(); limit > 0 {
			switch key {
			case "j", "down":
				m.helpScroll = min(m.helpScroll+1, limit)
				return m, nil
			case "k", "up":
				m.helpScroll = max(m.helpScroll-1, 0)
				return m, nil
			}
		}
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
			m.notify("reconnect cancelled")
			return m, m.loadTerminals()
		}
		return m, nil
	}
	// A message lives until the next key: loads never clear one, so a
	// notice set beside a reload (an attachment's exit, a refused
	// create) survives the reload that follows it.
	m.message = ""
	if key == "?" {
		m.help, m.helpScroll = true, 0
		return m, nil
	}
	switch m.screen {
	case screenSpaces:
		return m.handleSpacesKey(key)
	case screenTerminals:
		return m.handleTerminalsKey(msg)
	case screenSpaceChooser:
		return m.handleSpaceChooserKey(key)
	case screenDirectories:
		return m.handleDirectoryKey(msg)
	case screenDestination:
		return m.handleDestinationKey(key)
	case screenConnections:
		return m.handleConnectionsKey(key)
	case screenAliases:
		return m.handleAliasesKey(key)
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
		if c.kind == "connection" {
			return m, m.removeConnection(c.conn)
		}
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
	case "j", "down":
		m.selectedSpace = moveSelection(m.spaceKeys(), m.selectedSpace, 1)
	case "k", "up":
		m.selectedSpace = moveSelection(m.spaceKeys(), m.selectedSpace, -1)
	case "r":
		return m, m.loadSpaces()
	case "c":
		return m.showConnections()
	case "enter":
		if m.selectedSpace == allSpaces {
			return m.showAllSpaces()
		}
		row, ok := m.findRow(m.selectedSpace)
		if !ok {
			return m, nil
		}
		if !row.ready {
			m.fail(m.unavailable(m.connection(row.conn)))
			return m, nil
		}
		m.enterSpace(row.conn, row.space)
		return m, m.loadTerminals()
	case "n":
		return m.newSpace()
	case "d":
		if m.selectedSpace == allSpaces {
			m.fail("All Spaces is not a Space; open it to delete its Terminals")
			return m, nil
		}
		row, ok := m.findRow(m.selectedSpace)
		if !ok || m.loading {
			return m, nil
		}
		if row.space.IsDefault {
			m.fail("the Default Space cannot be deleted")
			return m, nil
		}
		if !row.ready {
			m.fail(m.unavailable(m.connection(row.conn)))
			return m, nil
		}
		m.confirm = &confirmation{kind: "space", conn: row.conn, id: row.space.ID, name: row.space.Name, count: row.terminals}
	}
	return m, nil
}

// newSpace starts creation: with one connection configured the directory
// browser opens on it at once; with more, the destination is chosen
// first, starting from the selected Space's connection.
func (m model) newSpace() (tea.Model, tea.Cmd) {
	if len(m.connections) == 1 {
		return m.browse(m.connections[0].name)
	}
	m.screen = screenDestination
	m.selectedConnection = m.selectedSpace.conn
	if m.connection(m.selectedConnection) == nil {
		m.selectedConnection = m.connections[0].name
	}
	return m, nil
}

// browse opens the directory browser on the named connection, refusing
// one that is not ready: an unavailable machine is never a destination.
func (m model) browse(conn string) (tea.Model, tea.Cmd) {
	c := m.connection(conn)
	if c == nil || c.status != connReady {
		m.fail(m.unavailable(c))
		return m, nil
	}
	m.conn = conn
	m.screen = screenDirectories
	m.dir, m.dirInput, m.selectedDir, m.message = api.DirectoryList{}, "", "", ""
	return m, m.loadDirectory("")
}

func (m model) handleDestinationKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.selectedConnection = moveSelection(m.connectionNames(), m.selectedConnection, 1)
	case "k", "up":
		m.selectedConnection = moveSelection(m.connectionNames(), m.selectedConnection, -1)
	case "enter":
		return m.browse(m.selectedConnection)
	case "esc", "h":
		return m.showSpaces()
	}
	return m, nil
}

// handleTerminalsKey drives both terminal views — one Space's list and
// All Spaces — which differ only in where n creates.
func (m model) handleTerminalsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.searching {
		return m.handleSearchKey(msg)
	}
	rows := m.results()
	switch key := msg.String(); key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.selectedTerminal = moveSelection(terminalRefs(rows), m.selectedTerminal, 1)
	case "k", "up":
		m.selectedTerminal = moveSelection(terminalRefs(rows), m.selectedTerminal, -1)
	case "r":
		return m, m.loadTerminals()
	case "esc", "h":
		return m.showSpaces()
	case "/":
		m.searching = true
	case "enter":
		return m.attachSelected()
	case "n":
		if m.loading {
			return m, nil
		}
		if m.allSpaces {
			return m.chooseSpace()
		}
		return m, m.createTerminal(spaceRef{conn: m.conn, id: m.space.ID})
	case "d":
		row, ok := m.selectedRow()
		if !ok || m.loading {
			return m, nil
		}
		if !row.ready {
			m.fail(m.unavailable(m.connection(row.conn)))
			return m, nil
		}
		m.confirm = &confirmation{kind: "terminal", conn: row.conn, id: row.terminal.ID, name: m.label(row.conn, row.terminal)}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
		// The number is the row's, as held in the current list: after a
		// delete elsewhere since the last refresh the user still gets the
		// terminal they were looking at. 0 is row ten.
		if m.loading {
			return m, nil
		}
		number := int(key[0] - '0')
		if number == 0 {
			number = 10
		}
		if number > len(rows) {
			m.fail(fmt.Sprintf("no terminal %d", number))
			return m, nil
		}
		return m.startAttach(rows[number-1].conn, rows[number-1].terminal)
	}
	return m, nil
}

// handleSearchKey is the terminal screen's search mode: every printable
// key is query text, so the list's letters and digits act on nothing, and
// each edit selects the best match. '?' is help here as everywhere.
func (m model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching, m.query = false, ""
		m.selectedTerminal = keepSelection(nil, terminalRefs(m.results()), m.selectedTerminal)
		return m, nil
	case "up", "ctrl+p":
		m.selectedTerminal = moveSelection(terminalRefs(m.results()), m.selectedTerminal, -1)
		return m, nil
	case "down", "ctrl+n":
		m.selectedTerminal = moveSelection(terminalRefs(m.results()), m.selectedTerminal, 1)
		return m, nil
	case "enter":
		return m.attachSelected()
	case "backspace":
		if runes := []rune(m.query); len(runes) > 0 {
			m.setQuery(string(runes[:len(runes)-1]))
		}
		return m, nil
	}
	if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper|tea.ModHyper) == 0 {
		m.setQuery(m.query + msg.Text)
	}
	return m, nil
}

func (m *model) setQuery(query string) {
	m.query = query
	m.selectedTerminal = keepSelection(nil, terminalRefs(m.results()), terminalRef{})
}

func (m model) attachSelected() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok || m.loading {
		return m, nil
	}
	return m.startAttach(row.conn, row.terminal)
}

// chooseSpace starts creation from All Spaces, where no one Space is
// implied: the Space is chosen first, starting from the selected
// Terminal's.
func (m model) chooseSpace() (tea.Model, tea.Cmd) {
	m.chosenSpace = spaceRef{}
	if row, ok := m.selectedRow(); ok {
		m.chosenSpace = spaceRef{conn: row.conn, id: row.space.ID}
	}
	m.chosenSpace = keepSelection(nil, spaceRefs(m.rows()), m.chosenSpace)
	m.screen = screenSpaceChooser
	return m, nil
}

func (m model) handleSpaceChooserKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.chosenSpace = moveSelection(spaceRefs(m.rows()), m.chosenSpace, 1)
	case "k", "up":
		m.chosenSpace = moveSelection(spaceRefs(m.rows()), m.chosenSpace, -1)
	case "esc", "h":
		m.screen = screenTerminals
	case "enter":
		row, ok := m.findRow(m.chosenSpace)
		if !ok {
			return m, nil
		}
		if !row.ready {
			m.fail(m.unavailable(m.connection(row.conn)))
			return m, nil
		}
		m.screen = screenTerminals
		return m, m.createTerminal(m.chosenSpace)
	}
	return m, nil
}

// handleDirectoryKey drives the directory picker. Printable input goes
// into the field: a relative text filters the listed entries by prefix;
// a text beginning with '/' is an absolute path Enter navigates to. '.'
// — "this directory", a prefix no visible entry can have — confirms the
// current directory while the field holds a filter. '?' is help here as
// everywhere and never reaches the field; a directory whose name holds
// one is reached by the movement keys and enter, or by pasting its path.
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

func (m *model) enterSpace(conn string, space api.Space) {
	m.conn, m.space, m.allSpaces = conn, space, false
	m.screen = screenTerminals
	m.terminals, m.selectedTerminal, m.message = nil, terminalRef{}, ""
	m.searching, m.query = false, ""
}

// showAllSpaces opens every Space's Terminals as one list. Its rows are
// what the connections last loaded, so they show at once while each
// connection reloads on its own.
func (m model) showAllSpaces() (tea.Model, tea.Cmd) {
	m.enterSpace("", api.Space{})
	m.allSpaces = true
	m.selectedTerminal = keepSelection(nil, terminalRefs(m.results()), terminalRef{})
	return m, m.loadSpaces()
}

func (m model) showSpaces() (tea.Model, tea.Cmd) {
	m.leaveScreen()
	m.screen = screenSpaces
	return m, m.loadSpaces()
}

// leaveScreen retires the current screen's requests: a create or load
// still in flight must not act on the screen the user has moved to.
func (m *model) leaveScreen() {
	m.seq++
	m.loading = false
	m.message = ""
	m.searching, m.query = false, ""
}

// startAttach hands the TTY to terminal's session on the connection that
// owns it. Only a running terminal on a ready connection is attached,
// whatever path led here — a create that settled as exited or
// unreachable is refused with its status, not handed to ssh.
func (m model) startAttach(conn string, terminal api.Terminal) (tea.Model, tea.Cmd) {
	m.selectedTerminal = terminalRef{conn: conn, id: terminal.ID}
	if terminal.Status != api.TerminalRunning {
		m.fail(m.refusal(conn, terminal))
		return m, m.loadTerminals()
	}
	c := m.connection(conn)
	if c == nil || c.status != connReady {
		m.fail(m.unavailable(c))
		return m, nil
	}
	cmd, err := c.session.Attach(m.ctx, terminal)
	if err != nil {
		m.fail("cannot attach: " + err.Error())
		return m, m.loadTerminals()
	}
	m.message = ""
	// A return refresh may still be in flight when the user attaches
	// again. Its result must not redraw stale state after this handoff.
	m.seq++
	m.attached = true
	return m, m.execProcess(cmd, func(err error) tea.Msg {
		return attachEndedMsg{conn: conn, terminal: terminal, err: err}
	})
}

// attachEnded is the return from an attachment. Exit zero is a detach;
// transport loss in remote mode starts the same-terminal retry; any other
// exit is reported and not retried. Every path asks for the terminal size
// again: it may have changed while the child held the TTY. The view the
// attachment left returns in normal mode: a search ends with it.
func (m model) attachEnded(msg attachEndedMsg) (tea.Model, tea.Cmd) {
	m.attached = false
	m.screen = screenTerminals
	m.searching, m.query = false, ""
	m.selectedTerminal = terminalRef{conn: msg.conn, id: msg.terminal.ID}
	c := m.connection(msg.conn)
	switch {
	case msg.err == nil:
		m.message = ""
	case c != nil && c.session.TransportLoss != nil && c.session.TransportLoss(msg.err):
		m.generation++
		m.reconnect = &reconnect{conn: msg.conn, terminal: msg.terminal, delay: reconnectMin, generation: m.generation}
		m.notify("connection lost, reconnecting to " + m.label(msg.conn, msg.terminal))
		return m, tea.Batch(requestWindowSize, m.tick(reconnectMin, reconnectTickMsg{generation: m.generation}))
	default:
		m.fail(fmt.Sprintf("attachment to %s ended: %v", m.label(msg.conn, msg.terminal), msg.err))
	}
	// Keep the last list visible and usable while refreshing on return.
	// A routine detach should neither flash a loading label nor make the
	// next attachment wait for an API round trip. Explicit loads and
	// mutations still use the loading gate; failures still reach the view.
	// All Spaces has exactly that in its background read.
	if m.allSpaces {
		return m, tea.Batch(requestWindowSize, m.pollSpaces())
	}
	refresh := m.loadTerminals()
	m.loading = false
	return m, tea.Batch(requestWindowSize, refresh)
}

func (m model) reconnectPolled(msg reconnectPolledMsg) (tea.Model, tea.Cmd) {
	if m.reconnect == nil || msg.generation != m.reconnect.generation {
		return m, nil
	}
	r := *m.reconnect
	stop := func(message string) (tea.Model, tea.Cmd) {
		m.reconnect = nil
		m.fail(message)
		return m, m.loadTerminals()
	}
	if msg.err != nil {
		var problem *api.Problem
		if errors.As(msg.err, &problem) && (problem.Status == http.StatusNotFound || problem.Status == http.StatusUnauthorized) {
			// The terminal is gone, or the token no longer works: neither
			// heals by waiting.
			return stop(m.describe(m.connection(r.conn), "reconnecting to "+m.label(r.conn, r.terminal), msg.err))
		}
		// Unreachable, or answering with a transient failure: wait
		// longer, up to the cap.
		r.delay = min(r.delay*2, reconnectMax)
		m.reconnect = &r
		return m, m.tick(r.delay, reconnectTickMsg{generation: r.generation})
	}
	if msg.terminal.Status != api.TerminalRunning {
		return stop(m.refusal(r.conn, msg.terminal))
	}
	m.reconnect = nil
	return m.startAttach(r.conn, msg.terminal)
}

// describe renders a request failure for the screen. Route-level 404s
// mean the answering server predates an API the picker needs; name the
// version-skew remedy instead of leaving the user with "Not Found". A
// 401 on a remote means its token was rotated since bootstrap; connecting
// again on the Connections screen issues a new one, so the message says
// so too.
func (m model) describe(c *connection, action string, err error) string {
	var problem *api.Problem
	if !errors.As(err, &problem) || c == nil {
		return action + ": " + err.Error()
	}
	if problem.Status == http.StatusNotFound && problem.Code == api.CodeNotFound {
		if !c.local {
			return fmt.Sprintf("%s: the server on %s (%s) lacks an API this picker needs; update it from connections", action, c.name, c.session.ServerVersion)
		}
		return fmt.Sprintf("%s: server %s lacks an API this picker needs; quit and run `atc server restart`", action, c.session.ServerVersion)
	}
	if !c.local && problem.Status == http.StatusUnauthorized {
		return fmt.Sprintf("%s: the token for %s was rotated; connect again from connections", action, c.name)
	}
	return action + ": " + err.Error()
}

func (m model) refusal(conn string, terminal api.Terminal) string {
	label := m.label(conn, terminal)
	if terminal.Status == api.TerminalExited && terminal.ExitCode != nil {
		return fmt.Sprintf("%s has exited with code %d; only running terminals can be attached", label, *terminal.ExitCode)
	}
	return fmt.Sprintf("%s is %s; only running terminals can be attached", label, terminal.Status)
}

// label is the terminal's row label in the current list — `2:nvim`,
// `4:api` — or its bare name or process when it is not listed (a
// terminal the picker no longer shows).
func (m model) label(conn string, terminal api.Terminal) string {
	ref := terminalRef{conn: conn, id: terminal.ID}
	for i, row := range m.results() {
		if row.ref() == ref {
			return cli.Label(i+1, terminal)
		}
	}
	return cli.DisplayName(terminal)
}

// spaceRow is one line of the Spaces table: a Space with the connection
// that serves it, whether that connection can act on it now, and how many
// Terminals it holds.
type spaceRow struct {
	conn      string
	ready     bool
	space     api.Space
	terminals int
}

// rows is the Spaces table: every connection's Spaces in connection
// order, each connection's in its own order.
func (m model) rows() []spaceRow {
	var rows []spaceRow
	for _, c := range m.connections {
		for _, space := range c.spaces {
			count := 0
			for _, terminal := range c.terminals {
				if terminal.SpaceID == space.ID {
					count++
				}
			}
			rows = append(rows, spaceRow{conn: c.name, ready: c.status == connReady, space: space, terminals: count})
		}
	}
	return rows
}

// spaceKeys are the Spaces table's selectable rows: the All Spaces entry,
// then every Space.
func (m model) spaceKeys() []spaceRef {
	return append([]spaceRef{allSpaces}, spaceRefs(m.rows())...)
}

func (m model) findRow(ref spaceRef) (spaceRow, bool) {
	for _, row := range m.rows() {
		if row.conn == ref.conn && row.space.ID == ref.id {
			return row, true
		}
	}
	return spaceRow{}, false
}

// setSpaces installs a connection's loaded Spaces and Terminals: the
// Default Space first, then newest-first, and the Terminals in number
// order. Every table the connection feeds — Spaces, the Space chooser,
// All Spaces — keeps its selection by reference, or moves it to the
// adjacent row when its resource is gone.
func (m *model) setSpaces(c *connection, spaces []api.Space, terminals []api.Terminal) {
	spaces = append([]api.Space(nil), spaces...)
	sort.SliceStable(spaces, func(i, j int) bool {
		if spaces[i].IsDefault != spaces[j].IsDefault {
			return spaces[i].IsDefault
		}
		return spaces[i].CreatedAt.After(spaces[j].CreatedAt)
	})
	beforeSpaces, beforeTerminals := m.spaceKeys(), terminalRefs(m.results())
	c.spaces, c.terminals = spaces, numberOrder(terminals)
	m.selectedSpace = keepSelection(beforeSpaces, m.spaceKeys(), m.selectedSpace)
	m.chosenSpace = keepSelection(beforeSpaces[1:], spaceRefs(m.rows()), m.chosenSpace)
	if m.allSpaces {
		m.selectedTerminal = keepSelection(beforeTerminals, terminalRefs(m.results()), m.selectedTerminal)
	}
}

// setTerminals installs a Space's loaded terminal list; on first entry
// row one is selected, afterwards the selection is kept by reference or
// moved to the adjacent row.
func (m *model) setTerminals(terminals []api.Terminal) {
	before := terminalRefs(m.results())
	m.terminals = numberOrder(terminals)
	m.selectedTerminal = keepSelection(before, terminalRefs(m.results()), m.selectedTerminal)
}

// numberOrder is a terminal list oldest first, newest at the bottom, so
// within a Space row N is terminal N.
func numberOrder(terminals []api.Terminal) []api.Terminal {
	terminals = append([]api.Terminal(nil), terminals...)
	sort.SliceStable(terminals, func(i, j int) bool {
		return terminals[i].CreatedAt.Before(terminals[j].CreatedAt)
	})
	return terminals
}

// terminalRow is one line of a terminal view: a Terminal with the Space
// and connection that own it, and whether that connection can act on it
// now.
type terminalRow struct {
	conn     string
	ready    bool
	space    api.Space
	terminal api.Terminal
}

func (r terminalRow) ref() terminalRef { return terminalRef{conn: r.conn, id: r.terminal.ID} }

// terminalRows is the terminal screen's list in its normal order: the
// Space's Terminals in number order, or for All Spaces every Space's, the
// Spaces in their table's order.
func (m model) terminalRows() []terminalRow {
	var rows []terminalRow
	if !m.allSpaces {
		c := m.current()
		for _, terminal := range m.terminals {
			rows = append(rows, terminalRow{conn: m.conn, ready: c != nil && c.status == connReady, space: m.space, terminal: terminal})
		}
		return rows
	}
	for _, c := range m.connections {
		for _, space := range c.spaces {
			for _, terminal := range c.terminals {
				if terminal.SpaceID == space.ID {
					rows = append(rows, terminalRow{conn: c.name, ready: c.status == connReady, space: space, terminal: terminal})
				}
			}
		}
	}
	return rows
}

// results are the rows the terminal screen shows and numbers: every row,
// or under a query its matches, best first.
func (m model) results() []terminalRow { return searchRows(m.terminalRows(), m.query) }

func (m model) selectedRow() (terminalRow, bool) {
	for _, row := range m.results() {
		if row.ref() == m.selectedTerminal {
			return row, true
		}
	}
	return terminalRow{}, false
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

// keepSelection keeps selected when it is still listed; otherwise it
// falls to the row that held the same position in the old list (the
// adjacent row after a deletion), clamped, or the first row.
func keepSelection[K comparable](oldKeys, newKeys []K, selected K) K {
	var zero K
	if len(newKeys) == 0 {
		return zero
	}
	index := 0
	for _, key := range newKeys {
		if key == selected {
			return key
		}
	}
	for i, key := range oldKeys {
		if key == selected {
			index = i
		}
	}
	return newKeys[min(index, len(newKeys)-1)]
}

func moveSelection[K comparable](keys []K, selected K, delta int) K {
	var zero K
	if len(keys) == 0 {
		return zero
	}
	index := 0
	for i, key := range keys {
		if key == selected {
			index = i
		}
	}
	return keys[max(0, min(len(keys)-1, index+delta))]
}

func spaceRefs(rows []spaceRow) []spaceRef {
	refs := make([]spaceRef, len(rows))
	for i, row := range rows {
		refs[i] = spaceRef{conn: row.conn, id: row.space.ID}
	}
	return refs
}

func terminalRefs(rows []terminalRow) []terminalRef {
	refs := make([]terminalRef, len(rows))
	for i, row := range rows {
		refs[i] = row.ref()
	}
	return refs
}

func entryNames(entries []api.DirectoryEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return names
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
