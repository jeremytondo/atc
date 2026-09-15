package tui

// Connections (ATC-327): each machine the picker shows is a connection
// with its own client, transport, availability, loaded Spaces, and
// recovery. Attempts and recoveries are stamped with a generation so a
// late answer from a superseded attempt, or from a connection since
// removed, changes nothing. Nothing here prompts: the unattended attempt
// fails with what a person must do, and the person does it from the
// Connections screen, which hands the terminal to the connector's
// interactive setup the way an attachment hands it to ssh.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jeremytondo/atc/internal/api"
)

type connStatus int

const (
	// connConnecting is an unattended attempt in flight.
	connConnecting connStatus = iota
	// connSettingUp is the interactive setup holding the terminal.
	connSettingUp
	connReady
	// connUnavailable is a connection that stopped answering after it was
	// ready: its Spaces stay listed but dimmed while its health is polled.
	connUnavailable
	// connFailed needs a person: the remedy names the action.
	connFailed
)

type connection struct {
	name      string
	local     bool
	connector Connector
	status    connStatus
	// err is why the connection is not ready, as shown on the Connections
	// screen; it may run to several lines (a setup plan). Wrapping
	// ErrLoginRequired or ErrSetupRequired names the action that clears it.
	err error
	// session is valid once ready; an unavailable connection keeps it for
	// the health polls and the reload that follows recovery.
	session Session
	spaces  []api.Space
	counts  map[string]int
	loading bool
	// seq stamps this connection's space loads and gen its attempts and
	// recovery ticks; both are drawn from the model's one counter, so a
	// connection removed and added again never matches an old answer.
	seq   uint64
	gen   uint64
	delay time.Duration
}

func (c *connection) statusLabel() string {
	switch c.status {
	case connConnecting:
		return "connecting…"
	case connSettingUp:
		return "setting up…"
	case connReady:
		return "ready"
	case connUnavailable:
		return "unavailable"
	}
	switch c.action() {
	case "connect":
		return "needs login"
	case "update":
		return "needs update"
	}
	return "failed"
}

// action is the verb enter performs on this connection, "" when there is
// nothing to do. Local has no interactive setup, so its only remedy is
// another attempt.
func (c *connection) action() string {
	switch {
	case c.status == connUnavailable, c.status == connFailed && c.local:
		return "retry"
	case c.status != connFailed:
		return ""
	case errors.Is(c.err, ErrLoginRequired):
		return "connect"
	case errors.Is(c.err, ErrSetupRequired):
		return "update"
	}
	return "retry"
}

func (c *connection) reason() string {
	if c.err == nil {
		return ""
	}
	return c.err.Error()
}

// failed records why the connection needs a person.
func (c *connection) failed(err error) {
	c.status, c.err = connFailed, err
}

func (m *model) connection(name string) *connection {
	for i := range m.connections {
		if m.connections[i].name == name {
			return &m.connections[i]
		}
	}
	return nil
}

func (m model) connectionNames() []string {
	names := make([]string, len(m.connections))
	for i, c := range m.connections {
		names[i] = c.name
	}
	return names
}

func (m model) remoteNames() []string {
	var names []string
	for _, c := range m.connections {
		if !c.local {
			names = append(names, c.name)
		}
	}
	return names
}

type connectedMsg struct {
	conn    string
	gen     uint64
	session Session
	err     error
}

type setupEndedMsg struct {
	conn    string
	gen     uint64
	session Session
	err     error
}

type recoverTickMsg struct {
	conn string
	gen  uint64
}

type healthMsg struct {
	conn string
	gen  uint64
	err  error
}

type closedMsg struct{}

// stamp starts a new attempt on c: whatever an earlier one answers is
// dropped.
func (m *model) stamp(c *connection) uint64 {
	m.generation++
	c.gen = m.generation
	return c.gen
}

// connect is the unattended attempt, bounded so a machine that accepts
// the connection and then hangs ends in a Retry, not in "connecting…".
func (m *model) connect(c *connection) tea.Cmd {
	gen := m.stamp(c)
	c.status, c.err = connConnecting, nil
	name, connector, ctx := c.name, c.connector, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		session, err := connector.Connect(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("no answer within %s: %w", connectTimeout, err)
		}
		return connectedMsg{conn: name, gen: gen, session: session, err: err}
	}
}

func (m model) connected(msg connectedMsg) (tea.Model, tea.Cmd) {
	c := m.connection(msg.conn)
	if c == nil || c.gen != msg.gen {
		return m, nil
	}
	if msg.err != nil {
		c.failed(msg.err)
		return m, nil
	}
	return m, m.becameReady(c, msg.session)
}

// becameReady installs a session and reloads what the screens show of
// the connection: its Spaces, and its Terminals when they are on screen.
func (m *model) becameReady(c *connection, session Session) tea.Cmd {
	c.status, c.err, c.session, c.delay = connReady, nil, session, 0
	cmds := []tea.Cmd{m.loadConnectionSpaces(c)}
	if m.screen == screenTerminals && m.conn == c.name && m.reconnect == nil {
		cmds = append(cmds, m.loadTerminals())
	}
	return tea.Batch(cmds...)
}

// setupCommand runs a connector's interactive setup on the terminal
// Bubble Tea releases for it, the way an attachment runs ssh.
type setupCommand struct {
	ctx     context.Context
	setup   func(context.Context, io.Reader, io.Writer, io.Writer) (Session, error)
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	session Session
}

func (c *setupCommand) SetStdin(r io.Reader)  { c.stdin = r }
func (c *setupCommand) SetStdout(w io.Writer) { c.stdout = w }
func (c *setupCommand) SetStderr(w io.Writer) { c.stderr = w }

func (c *setupCommand) Run() error {
	session, err := c.setup(c.ctx, c.stdin, c.stdout, c.stderr)
	c.session = session
	return err
}

// startSetup hands the terminal to c's interactive setup. Local has none:
// its remedy is the unattended attempt again. Bubble Tea's event loop
// waits for the handoff, so nothing else runs until it returns.
func (m *model) startSetup(c *connection) tea.Cmd {
	if c.local {
		return m.connect(c)
	}
	gen := m.stamp(c)
	c.status, c.err = connSettingUp, nil
	name := c.name
	cmd := &setupCommand{ctx: m.ctx, setup: c.connector.Setup}
	return m.execCommand(cmd, func(err error) tea.Msg {
		return setupEndedMsg{conn: name, gen: gen, session: cmd.session, err: err}
	})
}

func (m model) setupEnded(msg setupEndedMsg) (tea.Model, tea.Cmd) {
	c := m.connection(msg.conn)
	if c == nil || c.gen != msg.gen {
		return m, requestWindowSize
	}
	if msg.err != nil {
		c.failed(msg.err)
		return m, requestWindowSize
	}
	return m, tea.Batch(requestWindowSize, m.becameReady(c, msg.session))
}

// transportFailed marks a ready connection unavailable after a request
// that never reached its server, and starts polling its health with the
// same backoff as a lost attachment. Nothing else changes: the loaded
// Spaces stay listed, dimmed.
func (m *model) transportFailed(name string, err error) tea.Cmd {
	c := m.connection(name)
	if c == nil || c.status != connReady {
		return nil
	}
	var problem *api.Problem
	if errors.As(err, &problem) || errors.Is(err, context.Canceled) {
		return nil
	}
	c.status, c.err, c.delay = connUnavailable, err, reconnectMin
	return m.tick(c.delay, recoverTickMsg{conn: c.name, gen: m.stamp(c)})
}

// requestFailed reports a request failure on a connection: the message
// names the action, and the connection's state follows the failure — a
// transport failure starts recovery, a rejected token or a missing API
// needs a person.
func (m *model) requestFailed(name, action string, err error) tea.Cmd {
	c := m.connection(name)
	m.fail(m.describe(c, action, err))
	if c == nil || c.status != connReady {
		return nil
	}
	var problem *api.Problem
	if !errors.As(err, &problem) {
		return m.transportFailed(name, err)
	}
	if c.local {
		return nil
	}
	switch {
	case problem.Status == http.StatusUnauthorized:
		c.failed(fmt.Errorf("%w: the token was rotated since this picker connected; connecting again issues a new one", ErrLoginRequired))
	case problem.Status == http.StatusNotFound && problem.Code == api.CodeNotFound, problem.Code == api.CodeProtocolMismatch:
		c.failed(fmt.Errorf("%w: the server lacks an API this picker needs; updating it runs this picker's release", ErrSetupRequired))
	}
	return nil
}

func (m *model) checkHealth(c *connection) tea.Cmd {
	name, gen, client, ctx := c.name, c.gen, c.session.Client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		_, err := client.Health(ctx)
		return healthMsg{conn: name, gen: gen, err: err}
	}
}

func (m model) healthChecked(msg healthMsg) (tea.Model, tea.Cmd) {
	c := m.connection(msg.conn)
	if c == nil || c.gen != msg.gen || c.status != connUnavailable {
		return m, nil
	}
	if msg.err == nil {
		return m, m.becameReady(c, c.session)
	}
	var problem *api.Problem
	if errors.As(msg.err, &problem) {
		// The server answers but refuses this picker: waiting heals
		// nothing, a person does.
		c.status = connReady
		return m, m.requestFailed(c.name, "checking "+c.name, msg.err)
	}
	c.err = msg.err
	c.delay = min(c.delay*2, reconnectMax)
	return m, m.tick(c.delay, recoverTickMsg{conn: c.name, gen: c.gen})
}

// loadSpaces reloads every ready connection's Spaces.
func (m *model) loadSpaces() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.connections {
		if m.connections[i].status == connReady {
			cmds = append(cmds, m.loadConnectionSpaces(&m.connections[i]))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) loadConnectionSpaces(c *connection) tea.Cmd {
	m.generation++
	c.seq = m.generation
	c.loading = true
	name, seq, client, ctx := c.name, c.seq, c.session.Client, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		spaces, err := client.Spaces(ctx)
		if err != nil {
			return spacesLoadedMsg{conn: name, seq: seq, err: err}
		}
		terminals, err := client.Terminals(ctx, "")
		if err != nil {
			return spacesLoadedMsg{conn: name, seq: seq, err: err}
		}
		return spacesLoadedMsg{conn: name, seq: seq, spaces: spaces, terminals: terminals}
	}
}

func (m model) showConnections() (tea.Model, tea.Cmd) {
	m.leaveScreen()
	m.screen = screenConnections
	if m.connection(m.selectedConnection) == nil {
		m.selectedConnection = m.connections[0].name
	}
	return m, nil
}

func (m model) handleConnectionsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.selectedConnection = moveSelection(m.connectionNames(), m.selectedConnection, 1)
	case "k", "up":
		m.selectedConnection = moveSelection(m.connectionNames(), m.selectedConnection, -1)
	case "esc", "h":
		return m.showSpaces()
	case "enter":
		c := m.connection(m.selectedConnection)
		if c == nil || c.action() == "" {
			return m, nil
		}
		return m, m.startSetup(c)
	case "r":
		// Every connection that is not ready gets another unattended
		// attempt, except the one a person is working on.
		var cmds []tea.Cmd
		for i := range m.connections {
			c := &m.connections[i]
			if c.status == connFailed || c.status == connUnavailable {
				cmds = append(cmds, m.connect(c))
			}
		}
		return m, tea.Batch(cmds...)
	case "a":
		if m.save == nil {
			m.fail("this picker was opened on one machine; run plain `atc` to manage connections")
			return m, nil
		}
		m.selectedAlias = keepSelection(nil, m.availableAliases(), "")
		m.screen = screenAliases
	case "d":
		c := m.connection(m.selectedConnection)
		if c == nil {
			return m, nil
		}
		if c.local {
			m.fail("Local cannot be removed")
			return m, nil
		}
		if m.save == nil {
			m.fail("this picker was opened on one machine; run plain `atc` to manage connections")
			return m, nil
		}
		m.confirm = &confirmation{kind: "connection", conn: c.name, name: c.name}
	}
	return m, nil
}

// availableAliases are the ssh configuration's Host aliases not yet
// connections.
func (m model) availableAliases() []string {
	var available []string
	saved := m.connectionNames()
	for _, alias := range m.aliases {
		if !slices.Contains(saved, alias) {
			available = append(available, alias)
		}
	}
	return available
}

func (m model) handleAliasesKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.selectedAlias = moveSelection(m.availableAliases(), m.selectedAlias, 1)
	case "k", "up":
		m.selectedAlias = moveSelection(m.availableAliases(), m.selectedAlias, -1)
	case "esc", "h":
		m.screen = screenConnections
	case "enter":
		if m.selectedAlias == "" {
			return m, nil
		}
		return m.addConnection(m.selectedAlias)
	}
	return m, nil
}

// addConnection saves the alias and runs its interactive setup. An alias
// already saved is selected, never duplicated. The choice is saved before
// setup runs, so a failed setup leaves the connection listed with its
// reason and a Retry; a choice that cannot be saved is not made.
func (m model) addConnection(alias string) (tea.Model, tea.Cmd) {
	m.screen = screenConnections
	if m.connection(alias) != nil {
		m.selectedConnection = alias
		m.notify(alias + " is already a connection")
		return m, nil
	}
	if err := m.save(append(m.remoteNames(), alias)); err != nil {
		m.fail("saving connections: " + err.Error())
		return m, nil
	}
	m.selectedConnection = alias
	m.connections = append(m.connections, connection{name: alias, connector: m.open(alias), status: connFailed, err: errors.New("not connected yet")})
	return m, m.startSetup(m.connection(alias))
}

// removeConnection forgets a saved remote: its rows leave the table and
// its private transport closes. The machine, its server, and its
// Terminals are untouched. A removal that cannot be saved is not made.
func (m *model) removeConnection(name string) tea.Cmd {
	index := slices.Index(m.connectionNames(), name)
	if index < 0 {
		return nil
	}
	if err := m.save(slices.DeleteFunc(m.remoteNames(), func(remote string) bool { return remote == name })); err != nil {
		m.fail("saving connections: " + err.Error())
		return nil
	}
	connector := m.connections[index].connector
	m.connections = slices.Delete(m.connections, index, index+1)
	m.retired = append(m.retired, connector)
	m.selectedConnection = keepSelection(nil, m.connectionNames(), m.selectedConnection)
	if m.selectedSpace.conn == name {
		m.selectedSpace = keepSelection(nil, spaceRefs(m.rows()), spaceRef{})
	}
	m.notify("removed " + name + "; nothing on it was changed")
	return func() tea.Msg {
		_ = connector.Close()
		return closedMsg{}
	}
}
