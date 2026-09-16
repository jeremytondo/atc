package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// fakeClient answers from scripted state and records mutations. Every
// method takes the listing error first so request failures are one knob.
type fakeClient struct {
	err         error
	spaces      []api.Space
	terminals   []api.Terminal
	directories map[string]api.DirectoryList
	dirErr      map[string]error
	created     []api.SpaceCreateParams
	createdTerm []api.TerminalCreateParams
	deleted     []string
	createErr   error
	// createStatus overrides the status a created terminal reports.
	createStatus api.TerminalStatus
	nextID       int
	polls        int
	healths      int
}

func (c *fakeClient) Health(context.Context) (api.Health, error) {
	c.healths++
	if c.err != nil {
		return api.Health{}, c.err
	}
	return api.Health{Status: "ok"}, nil
}

func (c *fakeClient) Spaces(context.Context) ([]api.Space, error) {
	return append([]api.Space(nil), c.spaces...), c.err
}

func (c *fakeClient) CreateSpace(_ context.Context, params api.SpaceCreateParams) (api.Space, error) {
	c.created = append(c.created, params)
	if c.createErr != nil {
		return api.Space{}, c.createErr
	}
	c.nextID++
	space := api.Space{ID: fmt.Sprintf("spce-new%02d", c.nextID), Name: "new", Directory: params.Directory, CreatedAt: time.Now()}
	c.spaces = append(c.spaces, space)
	return space, nil
}

func (c *fakeClient) DeleteSpace(_ context.Context, id string) error {
	c.deleted = append(c.deleted, "space:"+id)
	return c.err
}

func (c *fakeClient) Terminals(_ context.Context, spaceID string) ([]api.Terminal, error) {
	var out []api.Terminal
	for _, terminal := range c.terminals {
		if spaceID == "" || terminal.SpaceID == spaceID {
			out = append(out, terminal)
		}
	}
	return out, c.err
}

func (c *fakeClient) Terminal(_ context.Context, id string) (api.Terminal, error) {
	c.polls++
	if c.err != nil {
		return api.Terminal{}, c.err
	}
	for _, terminal := range c.terminals {
		if terminal.ID == id {
			return terminal, nil
		}
	}
	return api.Terminal{}, &api.Problem{Status: http.StatusNotFound, Code: api.CodeTerminalNotFound, Detail: "terminal not found"}
}

func (c *fakeClient) CreateTerminal(_ context.Context, params api.TerminalCreateParams) (api.Terminal, error) {
	c.createdTerm = append(c.createdTerm, params)
	if c.createErr != nil {
		return api.Terminal{}, c.createErr
	}
	c.nextID++
	terminal := api.Terminal{ID: fmt.Sprintf("term-new%02d", c.nextID), Process: "shell", SpaceID: params.SpaceID, Status: api.TerminalRunning, CreatedAt: time.Now()}
	if c.createStatus != "" {
		terminal.Status = c.createStatus
	}
	c.terminals = append(c.terminals, terminal)
	return terminal, nil
}

func (c *fakeClient) DeleteTerminal(_ context.Context, id string) error {
	c.deleted = append(c.deleted, "terminal:"+id)
	return c.err
}

func (c *fakeClient) Directories(_ context.Context, path string) (api.DirectoryList, error) {
	if err := c.dirErr[path]; err != nil {
		return api.DirectoryList{}, err
	}
	list, ok := c.directories[path]
	if !ok {
		return api.DirectoryList{}, &api.Problem{Status: http.StatusUnprocessableEntity, Code: api.CodeDirectoryInvalid, Detail: "invalid directory: " + path}
	}
	return list, nil
}

// fakeConnector plays one machine's transport: Connect and Setup answer
// with the scripted session or failure and count their calls, Setup
// writes what a guided setup would to the released terminal, and Close
// records the release.
type fakeConnector struct {
	name       string
	remote     bool
	client     *fakeClient
	version    string
	connectErr error
	setupErr   error
	setupSays  string
	notice     string
	connects   int
	setups     int
	closes     int
	attached   []string
}

func (c *fakeConnector) Connect(ctx context.Context) (Session, error) {
	c.connects++
	if _, bounded := ctx.Deadline(); !bounded {
		panic("an unattended attempt was not bounded")
	}
	if c.connectErr != nil {
		return Session{}, c.connectErr
	}
	return c.session(), nil
}

func (c *fakeConnector) Setup(_ context.Context, _ io.Reader, stdout, _ io.Writer) (Session, error) {
	c.setups++
	_, _ = io.WriteString(stdout, c.setupSays)
	if c.setupErr != nil {
		return Session{}, c.setupErr
	}
	return c.session(), nil
}

func (c *fakeConnector) Close() error {
	c.closes++
	return nil
}

func (c *fakeConnector) session() Session {
	s := Session{Client: c.client, ServerVersion: c.version, Attach: func(_ context.Context, terminal api.Terminal) (*exec.Cmd, error) {
		c.attached = append(c.attached, terminal.ID)
		cmd := exec.Command("/bin/attach", terminal.ID)
		cmd.Env = []string{"TERM=xterm"}
		return cmd, nil
	}}
	if c.remote {
		s.TransportLoss = transportLoss
	}
	s.Notice = c.notice
	return s
}

// fakeExec is the process seam: it records every child the picker asked
// for and answers each with the next scripted exit, and runs every
// interactive setup handed the terminal, keeping what it printed there.
type fakeExec struct {
	commands [][]string
	envs     [][]string
	exits    []error
	setups   int
	terminal strings.Builder
}

func (f *fakeExec) exec(cmd *exec.Cmd, done tea.ExecCallback) tea.Cmd {
	f.commands = append(f.commands, cmd.Args)
	f.envs = append(f.envs, cmd.Env)
	var err error
	if len(f.exits) > 0 {
		err, f.exits = f.exits[0], f.exits[1:]
	}
	return func() tea.Msg { return done(err) }
}

func (f *fakeExec) execCommand(c tea.ExecCommand, done tea.ExecCallback) tea.Cmd {
	f.setups++
	c.SetStdin(strings.NewReader(""))
	c.SetStdout(&f.terminal)
	c.SetStderr(io.Discard)
	err := c.Run()
	return func() tea.Msg { return done(err) }
}

type fakeExit int

func (e fakeExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

func transportLoss(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 255
}

// fakeClock records every scheduled delay and holds the message for the
// test to fire — no wall-clock sleeps, and no tick fires on its own.
type fakeClock struct {
	delays  []time.Duration
	pending []tea.Msg
}

func (c *fakeClock) tick(delay time.Duration, msg tea.Msg) tea.Cmd {
	c.delays = append(c.delays, delay)
	c.pending = append(c.pending, msg)
	return nil
}

var (
	t0     = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	spaces = []api.Space{
		{ID: "spce-work", Name: "work", Directory: "/home/u/work", CreatedAt: t0.Add(time.Hour)},
		{ID: "spce-home", Name: "Default", Directory: "/home/u", IsDefault: true, CreatedAt: t0},
		{ID: "spce-play", Name: "play", Directory: "/home/u/play", CreatedAt: t0.Add(2 * time.Hour)},
	}
	exitOne   = 1
	terminals = []api.Terminal{
		{ID: "term-old", Process: "zsh", SpaceID: "spce-work", Directory: "/home/u/work", Status: api.TerminalRunning, CreatedAt: t0},
		{ID: "term-dead", Process: "nvim", SpaceID: "spce-work", Directory: "/home/u/work", Status: api.TerminalExited, ExitCode: &exitOne, CreatedAt: t0.Add(time.Minute)},
		{ID: "term-new", Name: "api", Process: "codex", SpaceID: "spce-work", Directory: "/home/u/work/api", Status: api.TerminalRunning, CreatedAt: t0.Add(time.Hour)},
		{ID: "term-play", Name: "play", Process: "zsh", SpaceID: "spce-play", Directory: "/home/u/play", Status: api.TerminalRunning, CreatedAt: t0},
	}
)

func newFakeClient() *fakeClient {
	home := api.DirectoryList{Path: "/home/u", Parent: ptr("/home"), Entries: []api.DirectoryEntry{
		{Name: "Code", Path: "/home/u/Code"}, {Name: "play", Path: "/home/u/play"}, {Name: "work", Path: "/home/u/work"}}}
	return &fakeClient{
		spaces:    append([]api.Space(nil), spaces...),
		terminals: append([]api.Terminal(nil), terminals...),
		directories: map[string]api.DirectoryList{
			"":             home,
			"/home/u":      home,
			"/home/u/Code": {Path: "/home/u/Code", Parent: ptr("/home/u"), Entries: []api.DirectoryEntry{{Name: "atc", Path: "/home/u/Code/atc"}}},
			"/home":        {Path: "/home", Parent: ptr("/"), Entries: []api.DirectoryEntry{{Name: "u", Path: "/home/u"}}},
			"/":            {Path: "/", Entries: []api.DirectoryEntry{{Name: "home", Path: "/home"}}},
			"/home/u/.cfg": {Path: "/home/u/.cfg", Parent: ptr("/home/u"), Entries: []api.DirectoryEntry{}},
		},
	}
}

type harness struct {
	t          *testing.T
	m          model
	client     *fakeClient // the first connection's
	connectors map[string]*fakeConnector
	exec       *fakeExec
	clock      *fakeClock
	saves      [][]string
}

// newHarness is the single-machine picker: Local alone, or one remote
// target as `atc --remote` opens it — no Add, no Remove.
func newHarness(t *testing.T, target string) *harness {
	t.Helper()
	if target == "" {
		return build(t, Options{ClientVersion: "v1", Connections: []Connection{{Name: "Local", Local: true}}})
	}
	return build(t, Options{ClientVersion: "v1", Connections: []Connection{{Name: target}}})
}

// newMultiHarness is the plain launch: Local and the saved remotes, with
// the Host aliases Add may offer.
func newMultiHarness(t *testing.T, remotes ...string) *harness {
	t.Helper()
	connections := []Connection{{Name: "Local", Local: true}}
	for _, remote := range remotes {
		connections = append(connections, Connection{Name: remote})
	}
	h := build(t, Options{ClientVersion: "v1", Connections: connections, Aliases: append(append([]string(nil), remotes...), "build", "tablet")})
	h.m.open = func(alias string) Connector {
		c := &fakeConnector{name: alias, remote: true, client: newFakeClient(), version: "v1"}
		h.connectors[alias] = c
		return c
	}
	h.m.save = func(remotes []string) error {
		h.saves = append(h.saves, append([]string(nil), remotes...))
		return nil
	}
	return h
}

func build(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{t: t, connectors: map[string]*fakeConnector{}, exec: &fakeExec{}, clock: &fakeClock{}}
	for i := range opts.Connections {
		c := &fakeConnector{name: opts.Connections[i].Name, remote: !opts.Connections[i].Local, client: newFakeClient(), version: "v1"}
		h.connectors[c.name] = c
		opts.Connections[i].Connector = c
		if i == 0 {
			h.client = c.client
		}
	}
	h.m = newModel(context.Background(), opts)
	h.m.execProcess = h.exec.exec
	h.m.execCommand = h.exec.execCommand
	h.m.tick = h.clock.tick
	// Periodic refreshes are delivered explicitly by the tests that need them.
	h.m.refreshTick = func() tea.Cmd { return nil }
	return h
}

func ptr(s string) *string { return &s }

func (h *harness) conn(name string) *connection {
	h.t.Helper()
	c := h.m.connection(name)
	if c == nil {
		h.t.Fatalf("no connection %q", name)
	}
	return c
}

// run applies msg and then every message its command produces, batches
// flattened, so a test reads like the program: key, load, result.
func (h *harness) run(msg tea.Msg) {
	h.t.Helper()
	updated, cmd := h.m.Update(msg)
	h.m = updated.(model)
	for _, produced := range drain(cmd) {
		h.run(produced)
	}
}

// send applies one message and returns the messages its command would
// produce, without applying them.
func (h *harness) send(msg tea.Msg) []tea.Msg {
	h.t.Helper()
	updated, cmd := h.m.Update(msg)
	h.m = updated.(model)
	return drain(cmd)
}

// fire delivers the oldest scheduled tick and everything it leads to.
func (h *harness) fire() {
	h.t.Helper()
	if len(h.clock.pending) == 0 {
		h.t.Fatal("no tick scheduled")
	}
	msg := h.clock.pending[0]
	h.clock.pending = h.clock.pending[1:]
	h.run(msg)
}

func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, inner := range batch {
			out = append(out, drain(inner)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func (h *harness) key(name string) {
	h.t.Helper()
	h.run(keyPress(name))
}

func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

// run on a model returned by an Update-style method: apply nothing, the
// model is already updated, then drain its command.
func (h *harness) runModel(m tea.Model, cmd tea.Cmd) {
	h.t.Helper()
	h.m = m.(model)
	for _, produced := range drain(cmd) {
		h.run(produced)
	}
}

func (h *harness) open() {
	h.t.Helper()
	for _, msg := range drain(h.m.Init()) {
		h.run(msg)
	}
}

func isWindowSizeRequest(msg tea.Msg) bool {
	return reflect.TypeOf(msg) == reflect.TypeOf(tea.RequestWindowSize())
}

// plain is the view without its styling, for text assertions.
func plain(m model) string { return ansi.Strip(m.View().Content) }

// lines are the view's lines with the border and styling removed, so a
// footer or breadcrumb can be compared whole.
func lines(m model) []string {
	raw := strings.Split(plain(m), "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		out[i] = strings.TrimSpace(strings.Trim(line, "│"))
	}
	return out
}

// rawLine is the styled view line whose text contains needle.
func rawLine(m model, needle string) string {
	for _, line := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(ansi.Strip(line), needle) {
			return line
		}
	}
	return ""
}

// highlighted reports whether line carries the selection's background.
var background = regexp.MustCompile(`\x1b\[(\d+;)*48;5;\d+m`)

func highlighted(line string) bool { return background.MatchString(line) }

// dimmedRow reports whether the Spaces row for conn/name is rendered
// faint: its name cell carries the attribute, selected or not.
func dimmedRow(m model, conn, name string) bool {
	row := regexp.MustCompile(`^│ ` + conn + `\s+` + name + `\s`)
	for _, line := range strings.Split(m.View().Content, "\n") {
		if row.MatchString(ansi.Strip(line)) {
			return regexp.MustCompile(`\x1b\[2[;\d]*m` + name).MatchString(line)
		}
	}
	return false
}

func matches(pattern, text string) bool { return regexp.MustCompile(pattern).MatchString(text) }

// rowKeys are the Spaces table's rows as connection/ID pairs.
func rowKeys(m model) []string {
	var keys []string
	for _, row := range m.rows() {
		keys = append(keys, row.conn+"/"+row.space.ID)
	}
	return keys
}

func TestSpaceListOrderSelectionAndCounts(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	if diff := cmp.Diff([]string{"Local/spce-home", "Local/spce-play", "Local/spce-work"}, rowKeys(h.m)); diff != "" {
		t.Errorf("Default first, then newest-first (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]int{"spce-work": 3, "spce-play": 1}, h.conn("Local").counts); diff != "" {
		t.Errorf("terminal counts (-want +got):\n%s", diff)
	}
	if h.m.selectedSpace != (spaceRef{"Local", "spce-home"}) || h.m.screen != screenSpaces || h.m.message != "" {
		t.Fatalf("initial = selected %v screen %v message %q", h.m.selectedSpace, h.m.screen, h.m.message)
	}
	h.key("j")
	h.key("j")
	h.key("j") // clamps
	if h.m.selectedSpace.id != "spce-work" {
		t.Errorf("after j j j: %v", h.m.selectedSpace)
	}
	h.key("k")
	if h.m.selectedSpace.id != "spce-play" {
		t.Errorf("after k: %v", h.m.selectedSpace)
	}
	// Selection survives a refresh by ID, and falls to the adjacent row
	// when its Space disappears.
	h.client.spaces = []api.Space{spaces[0], spaces[1]}
	h.key("r")
	if h.m.selectedSpace.id != "spce-work" {
		t.Errorf("after the selected space vanished: %v, want the adjacent row", h.m.selectedSpace)
	}
	if !highlighted(rawLine(h.m, "work")) || highlighted(rawLine(h.m, "Default")) {
		t.Errorf("selection not the highlighted row:\n%s", h.m.View().Content)
	}
	if view := plain(h.m); !matches(`CONNECTION\s+NAME\s+TERMINALS\s+DIRECTORY`, view) || !matches(`Local\s+Default\s+0\s+/home/u\s+default`, view) || !matches(`Local\s+work\s+3\s+/home/u/work`, view) {
		t.Errorf("view:\n%s", view)
	}
}

// The terminal list is in number order — creation order, oldest first —
// with row one selected on entry, and each row labelled by its number
// and name or process (ATC-317).
func TestTerminalListNumberOrderAndBack(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("j")
	h.key("j") // work
	h.key("enter")
	if h.m.screen != screenTerminals || h.m.space.ID != "spce-work" || h.m.conn != "Local" {
		t.Fatalf("enter = screen %v space %q conn %q", h.m.screen, h.m.space.ID, h.m.conn)
	}
	if diff := cmp.Diff([]string{"term-old", "term-dead", "term-new"}, terminalIDs(h.m.terminals)); diff != "" {
		t.Errorf("number order (-want +got):\n%s", diff)
	}
	if h.m.selectedTerminal != "term-old" {
		t.Errorf("row one preselected: %q", h.m.selectedTerminal)
	}
	view := plain(h.m)
	for _, row := range []string{`#\s+NAME\s+STATUS\s+DIRECTORY`, `1\s+zsh\s+running\s+/home/u/work`, `2\s+nvim\s+exited \(1\)\s+/home/u/work`, `3\s+api\s+running\s+/home/u/work/api`} {
		if !matches(row, view) {
			t.Errorf("view lacks %q:\n%s", row, view)
		}
	}
	if !highlighted(rawLine(h.m, "zsh")) || highlighted(rawLine(h.m, "nvim")) {
		t.Errorf("row one not the highlighted row:\n%s", h.m.View().Content)
	}
	h.key("j")
	h.key("enter")
	if !strings.Contains(h.m.message, "2:nvim has exited with code 1") || len(h.exec.commands) != 0 {
		t.Errorf("exited terminal: message %q, exec %v", h.m.message, h.exec.commands)
	}
	if !strings.Contains(plain(h.m), "exited (1)") {
		t.Errorf("view:\n%s", plain(h.m))
	}
	h.key("esc")
	if h.m.screen != screenSpaces || h.m.selectedSpace.id != "spce-work" {
		t.Errorf("esc = screen %v selected %v", h.m.screen, h.m.selectedSpace)
	}
	h.key("enter")
	h.key("h")
	if h.m.screen != screenSpaces {
		t.Errorf("h did not go back")
	}
}

func TestDirectoryRefreshWhileTerminalListVisible(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("j")
	h.key("j")
	h.key("enter")
	selected := h.m.selectedTerminal
	h.client.terminals[0].Directory = "/home/u/other-worktree"
	msgs := h.send(refreshTickMsg{})
	if h.m.loading || !h.m.polling {
		t.Fatal("background refresh must leave actions available")
	}
	if extra := h.send(refreshTickMsg{}); len(extra) != 0 {
		t.Fatal("overlapping background requests")
	}
	for _, msg := range msgs {
		h.run(msg)
	}
	if h.m.selectedTerminal != selected || !strings.Contains(plain(h.m), "/home/u/other-worktree") {
		t.Fatalf("directory refresh lost selection or path:\n%s", plain(h.m))
	}
	// A failed poll preserves both the list and any user-facing notice,
	// and is the moment the connection is known to be unavailable: its
	// recovery starts here, not at the next key.
	before := plain(h.m)
	h.client.err = errors.New("offline")
	h.run(refreshTickMsg{})
	if diff := cmp.Diff(before, plain(h.m)); diff != "" {
		t.Fatalf("failed background poll changed the view:\n%s", diff)
	}
	if h.conn("Local").status != connUnavailable || len(h.clock.pending) != 1 {
		t.Fatalf("failed poll: status %v ticks %v", h.conn("Local").status, h.clock.pending)
	}
	h.client.err = nil
	h.fire()
	if h.conn("Local").status != connReady {
		t.Fatalf("health answered but the connection stayed %v", h.conn("Local").status)
	}
	// A delayed response cannot overwrite an explicit refresh or a new screen.
	h.client.terminals[0].Directory = "/obsolete"
	stale := h.send(refreshTickMsg{})
	h.client.terminals[0].Directory = "/current"
	h.key("r")
	for _, msg := range stale {
		h.run(msg)
	}
	if !strings.Contains(plain(h.m), "/current") || strings.Contains(plain(h.m), "/obsolete") {
		t.Fatalf("stale background result applied:\n%s", plain(h.m))
	}
	for _, state := range []string{"confirmation", "help", "attachment", "spaces"} {
		t.Run(state, func(t *testing.T) {
			m := h.m
			switch state {
			case "confirmation":
				m.confirm = &confirmation{}
			case "help":
				m.help = true
			case "attachment":
				m.attached = true
			case "spaces":
				m.screen = screenSpaces
			}
			_, cmd := m.Update(refreshTickMsg{})
			if msgs := drain(cmd); len(msgs) != 0 {
				t.Fatalf("polled during %s: %v", state, msgs)
			}
		})
	}
}

func TestRequestFailureShowsErrorAndKeepsNavigation(t *testing.T) {
	h := newHarness(t, "ws")
	h.open()
	h.client.err = errors.New("dial tcp: connection refused")
	h.key("r")
	if !strings.Contains(h.m.message, "loading spaces on ws: dial tcp") || len(h.m.rows()) != 3 {
		t.Fatalf("failure = message %q rows %d", h.m.message, len(h.m.rows()))
	}
	h.key("j")
	if h.m.selectedSpace.id != "spce-play" {
		t.Error("navigation blocked by the error")
	}
	h.client.err = nil
	h.fire() // the health poll answers: ready again
	h.client.err = &api.Problem{Status: http.StatusUnauthorized, Code: api.CodeUnauthorized}
	h.key("r")
	if !strings.Contains(h.m.message, "token for ws was rotated") || !strings.Contains(h.m.message, "connect again from connections") {
		t.Errorf("401 in remote mode: %q", h.m.message)
	}
	if c := h.conn("ws"); c.status != connFailed || c.action() != "connect" {
		t.Errorf("401 left ws %v/%q, want failed with login", c.status, c.action())
	}
	h.client.err = nil
	h.key("c")
	h.key("r") // retry all: ws reconnects
	h.key("esc")
	h.client.err = &api.Problem{Status: http.StatusNotFound, Code: api.CodeNotFound}
	h.key("r")
	if !strings.Contains(h.m.message, "the server on ws (v1) lacks an API") || !strings.Contains(h.m.message, "update it from connections") {
		t.Errorf("route 404 in remote mode: %q", h.m.message)
	}
	if c := h.conn("ws"); c.status != connFailed || c.action() != "update" {
		t.Errorf("404 left ws %v/%q, want failed with update", c.status, c.action())
	}
	local := newHarness(t, "")
	local.open()
	local.client.err = &api.Problem{Status: http.StatusNotFound, Code: api.CodeNotFound}
	local.key("r")
	if !strings.Contains(local.m.message, "server v1 lacks an API") || !strings.Contains(local.m.message, "`atc server restart`") {
		t.Errorf("route 404 in local mode: %q", local.m.message)
	}
	if local.conn("Local").status != connReady {
		t.Error("a problem answer marked Local not ready")
	}
	local.client.err = nil
	local.key("r")
	if local.m.message != "" {
		t.Errorf("retry left the message: %q", local.m.message)
	}
	if msgs := local.send(keyPress("q")); len(msgs) != 1 || reflect.TypeOf(msgs[0]) != reflect.TypeOf(tea.QuitMsg{}) {
		t.Errorf("q = %v, want quit", msgs)
	}
	if msgs := local.send(keyPress("ctrl+c")); len(msgs) != 1 || reflect.TypeOf(msgs[0]) != reflect.TypeOf(tea.QuitMsg{}) {
		t.Errorf("ctrl+c = %v, want quit", msgs)
	}
}

func TestDeleteConfirmations(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("d")
	if h.m.confirm != nil || !strings.Contains(h.m.message, "Default Space cannot be deleted") || len(h.client.deleted) != 0 {
		t.Fatalf("Default delete: confirm %v message %q deleted %v", h.m.confirm, h.m.message, h.client.deleted)
	}
	h.key("j")
	h.key("j") // work, three terminals
	h.key("d")
	want := &confirmation{kind: "space", conn: "Local", id: "spce-work", name: "work", count: 3}
	if diff := cmp.Diff(want, h.m.confirm, cmp.AllowUnexported(confirmation{})); diff != "" {
		t.Fatalf("confirmation (-want +got):\n%s", diff)
	}
	if !strings.Contains(plain(h.m), "delete space work and its 3 terminals on Local?") || !strings.Contains(h.m.View().Content, newStyles(true).bold.Render("work")) {
		t.Errorf("view:\n%s", h.m.View().Content)
	}
	h.key("n")
	if h.m.confirm != nil || len(h.client.deleted) != 0 {
		t.Fatal("n did not cancel")
	}
	h.key("d")
	h.key("esc")
	if h.m.confirm != nil {
		t.Fatal("esc did not cancel")
	}
	h.key("d")
	h.client.spaces = []api.Space{spaces[1], spaces[2]}
	h.key("y")
	if diff := cmp.Diff([]string{"space:spce-work"}, h.client.deleted); diff != "" {
		t.Errorf("deleted (-want +got):\n%s", diff)
	}
	if h.m.screen != screenSpaces || h.m.selectedSpace.id != "spce-play" {
		t.Errorf("after delete: screen %v selected %v", h.m.screen, h.m.selectedSpace)
	}

	// Terminal delete names the terminal and reloads the list.
	h.key("enter") // play
	h.key("d")
	if h.m.confirm == nil || h.m.confirm.kind != "terminal" || h.m.confirm.name != "1:play" || h.m.confirm.conn != "Local" {
		t.Fatalf("terminal confirmation = %+v", h.m.confirm)
	}
	if !strings.Contains(plain(h.m), "delete terminal 1:play?") {
		t.Errorf("view:\n%s", plain(h.m))
	}
	h.client.terminals = terminals[:3]
	h.key("y")
	if diff := cmp.Diff([]string{"space:spce-work", "terminal:term-play"}, h.client.deleted); diff != "" {
		t.Errorf("deleted (-want +got):\n%s", diff)
	}
	if h.m.screen != screenTerminals || len(h.m.terminals) != 0 || h.m.selectedTerminal != "" {
		t.Errorf("after terminal delete: screen %v terminals %d selected %q", h.m.screen, len(h.m.terminals), h.m.selectedTerminal)
	}
}

func TestDirectoryPickerNavigationFilteringAndCreate(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("n")
	if h.m.screen != screenDirectories || h.m.dir.Path != "/home/u" || h.m.selectedDir != "Code" || h.m.conn != "Local" {
		t.Fatalf("n = screen %v dir %q selected %q conn %q", h.m.screen, h.m.dir.Path, h.m.selectedDir, h.m.conn)
	}
	// Typing filters case-insensitively by prefix and moves the
	// selection into the filtered set.
	h.key("down")
	h.key("P")
	if got := entryNames(h.m.filteredEntries()); !cmp.Equal(got, []string{"play"}) || h.m.selectedDir != "play" {
		t.Errorf("filter P = %v selected %q", got, h.m.selectedDir)
	}
	h.key("backspace")
	if h.m.dirInput != "" || len(h.m.filteredEntries()) != 3 {
		t.Errorf("backspace on a filter = input %q entries %d", h.m.dirInput, len(h.m.filteredEntries()))
	}
	// Enter descends; backspace on an empty filter ascends.
	h.key("c")
	h.key("enter")
	if h.m.dir.Path != "/home/u/Code" || h.m.dirInput != "" || h.m.selectedDir != "atc" {
		t.Errorf("descend = dir %q input %q selected %q", h.m.dir.Path, h.m.dirInput, h.m.selectedDir)
	}
	h.key("backspace")
	h.key("backspace")
	if h.m.dir.Path != "/home" {
		t.Errorf("ascend twice = %q", h.m.dir.Path)
	}
	h.key("backspace")
	h.key("backspace") // at the root: nothing to ascend to
	if h.m.dir.Path != "/" {
		t.Errorf("root = %q", h.m.dir.Path)
	}
	// An absolute path in the field: enter goes there, hidden or not.
	h.run(tea.PasteMsg{Content: "/home/u/.cfg\n"})
	if h.m.dirInput != "/home/u/.cfg" {
		t.Errorf("paste = %q", h.m.dirInput)
	}
	h.key("enter")
	if h.m.dir.Path != "/home/u/.cfg" || h.m.dirInput != "" {
		t.Errorf("absolute enter = dir %q input %q", h.m.dir.Path, h.m.dirInput)
	}
	// A bad absolute path keeps the field so it can be fixed.
	h.key("/")
	for _, r := range "nope" {
		h.key(string(r))
	}
	h.key("enter")
	if !strings.Contains(h.m.message, "listing directory") || h.m.dirInput != "/nope" || h.m.dir.Path != "/home/u/.cfg" {
		t.Errorf("bad path = message %q input %q dir %q", h.m.message, h.m.dirInput, h.m.dir.Path)
	}
	h.key("esc")
	if h.m.dirInput != "" || h.m.screen != screenDirectories {
		t.Errorf("esc with input = %q screen %v", h.m.dirInput, h.m.screen)
	}
	// '.' confirms the current directory: the Space is created with the
	// directory alone, its shell Terminal follows, and the picker attaches.
	h.key(".")
	if diff := cmp.Diff([]api.SpaceCreateParams{{Directory: "/home/u/.cfg"}}, h.client.created); diff != "" {
		t.Errorf("created spaces (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]api.TerminalCreateParams{{SpaceID: "spce-new01"}}, h.client.createdTerm); diff != "" {
		t.Errorf("created terminals (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-new02"}}, h.exec.commands); diff != "" {
		t.Errorf("attached (-want +got):\n%s", diff)
	}
	if h.m.screen != screenTerminals || h.m.space.ID != "spce-new01" || h.m.selectedTerminal != "term-new02" || h.m.selectedSpace != (spaceRef{"Local", "spce-new01"}) {
		t.Errorf("after create = screen %v space %q selected %q selectedSpace %v", h.m.screen, h.m.space.ID, h.m.selectedTerminal, h.m.selectedSpace)
	}
	// esc with an empty field leaves the picker.
	h.key("esc")
	h.key("n")
	h.key("esc")
	if h.m.screen != screenSpaces {
		t.Errorf("esc from the picker = %v", h.m.screen)
	}
}

func TestDirectoryPickerKeepsUserOnValidationError(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("n")
	h.client.createErr = &api.Problem{Status: http.StatusUnprocessableEntity, Code: api.CodeSpaceDirectoryInvalid, Detail: "invalid space directory"}
	h.key(".")
	if h.m.screen != screenDirectories || !strings.Contains(h.m.message, "creating space: invalid space directory") || len(h.exec.commands) != 0 {
		t.Errorf("validation error = screen %v message %q exec %v", h.m.screen, h.m.message, h.exec.commands)
	}
	// ctrl+r retries the listing; a listing failure keeps the screen.
	h.client.dirErr = map[string]error{"/home/u": errors.New("boom")}
	h.key("ctrl+r")
	if !strings.Contains(h.m.message, "listing directory: boom") || h.m.dir.Path != "/home/u" {
		t.Errorf("ctrl+r failure = message %q dir %q", h.m.message, h.m.dir.Path)
	}
}

func TestAttachDetachAndReturnSelection(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("j")
	h.key("j")
	h.key("enter") // work; row one, term-old, selected
	h.exec.exits = []error{nil}
	msgs := h.send(keyPress("enter"))
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-old"}}, h.exec.commands); diff != "" {
		t.Fatalf("exec (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"TERM=xterm"}}, h.exec.envs); diff != "" {
		t.Errorf("child environment (-want +got):\n%s", diff)
	}
	// The child's exit arrives; the return remeasures before redrawing
	// and reloads the same Space with the same Terminal selected.
	ended := h.send(msgs[0])
	if h.m.screen != screenTerminals || h.m.selectedTerminal != "term-old" || h.m.message != "" {
		t.Fatalf("detach = screen %v selected %q message %q", h.m.screen, h.m.selectedTerminal, h.m.message)
	}
	var sawResize bool
	for _, msg := range ended {
		if isWindowSizeRequest(msg) {
			sawResize = true
			continue
		}
		h.run(msg)
	}
	if !sawResize {
		t.Error("return did not request a remeasure")
	}
	h.run(tea.WindowSizeMsg{Width: 100, Height: 40})
	if h.m.width != 100 || h.m.height != 40 || h.m.selectedTerminal != "term-old" {
		t.Errorf("after resize = %dx%d selected %q", h.m.width, h.m.height, h.m.selectedTerminal)
	}
	// The selected Terminal is gone on return: the adjacent row.
	h.client.terminals = terminals[1:]
	h.exec.exits = []error{nil}
	h.key("enter")
	if h.m.selectedTerminal != "term-dead" {
		t.Errorf("vanished selection fell to %q", h.m.selectedTerminal)
	}

	// A non-zero, non-transport exit is reported, naming the terminal by
	// its label, and never retried; exit 255 in local mode is just such
	// an exit.
	h.key("j") // term-new, now row two
	for _, exit := range []error{fakeExit(1), fakeExit(255)} {
		h.exec.exits = []error{exit}
		before := len(h.exec.commands)
		h.key("enter")
		if len(h.exec.commands) != before+1 || h.m.screen != screenTerminals || !strings.Contains(h.m.message, "attachment to 2:api ended: "+exit.Error()) {
			t.Errorf("exit %v = commands %d screen %v message %q", exit, len(h.exec.commands), h.m.screen, h.m.message)
		}
	}
	if len(h.clock.delays) != 0 {
		t.Errorf("local mode scheduled reconnects: %v", h.clock.delays)
	}
}

func TestDetachRefreshKeepsViewAndAllowsImmediateAttach(t *testing.T) {
	for _, target := range []string{"", "workstation"} {
		t.Run("target="+target, func(t *testing.T) {
			h := newHarness(t, target)
			h.open()
			h.key("j")
			h.key("j")
			h.key("enter") // work
			before := h.m.View().Content
			ended := h.send(keyPress("enter"))
			refresh := h.send(ended[0])
			if diff := cmp.Diff(before, h.m.View().Content); diff != "" {
				t.Fatalf("view while return refresh is pending (-want +got):\n%s", diff)
			}

			// Reattach before the API reply arrives. The old reply must
			// not replace the selection or report an obsolete failure.
			ended = h.send(keyPress("3"))
			if diff := cmp.Diff([][]string{{"/bin/attach", "term-old"}, {"/bin/attach", "term-new"}}, h.exec.commands); diff != "" {
				t.Fatalf("immediate reattach (-want +got):\n%s", diff)
			}
			for _, msg := range refresh {
				if loaded, ok := msg.(terminalsLoadedMsg); ok {
					loaded.err = errors.New("obsolete refresh")
					h.run(loaded)
				}
			}
			if h.m.selectedTerminal != "term-new" || h.m.message != "" {
				t.Fatalf("obsolete refresh changed selection %q or message %q", h.m.selectedTerminal, h.m.message)
			}

			// The next return still refreshes and exposes real failures.
			h.client.err = errors.New("refresh unavailable")
			h.run(ended[0])
			if h.m.loading || !strings.Contains(h.m.message, "loading terminals: refresh unavailable") {
				t.Errorf("failed refresh: loading %v message %q", h.m.loading, h.m.message)
			}
		})
	}
}

func TestCreateTerminalAttachesImmediately(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("j")
	h.key("j")
	h.key("enter")
	h.exec.exits = []error{nil}
	h.key("n")
	if diff := cmp.Diff([]api.TerminalCreateParams{{SpaceID: "spce-work"}}, h.client.createdTerm); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-new01"}}, h.exec.commands); diff != "" {
		t.Errorf("exec (-want +got):\n%s", diff)
	}
	if last := h.m.terminals[len(h.m.terminals)-1].ID; h.m.selectedTerminal != "term-new01" || last != "term-new01" {
		t.Errorf("after create: selected %q last %q", h.m.selectedTerminal, last)
	}
	h.client.createErr = &api.Problem{Status: http.StatusInternalServerError, Code: "internal", Detail: "boom"}
	h.key("n")
	if !strings.Contains(h.m.message, "creating terminal: boom") || len(h.exec.commands) != 1 {
		t.Errorf("create failure = %q exec %v", h.m.message, h.exec.commands)
	}
	// A create that settled without running is refused with its status,
	// never handed to the attach child.
	h.client.createErr = nil
	h.client.createStatus = api.TerminalUnreachable
	h.key("n")
	if !strings.Contains(h.m.message, "5:shell is unreachable") || len(h.exec.commands) != 1 {
		t.Errorf("unreachable create = %q exec %v", h.m.message, h.exec.commands)
	}
	// While a create is in flight, another n is ignored.
	h.client.createStatus = ""
	pending := h.send(keyPress("n"))
	if more := h.send(keyPress("n")); len(more) != 0 || len(h.client.createdTerm) != 4 {
		t.Errorf("second n during create = %v, created %d", more, len(h.client.createdTerm))
	}
	for _, msg := range pending {
		h.run(msg)
	}
	if len(h.exec.commands) != 2 {
		t.Errorf("exec after the pending create = %v", h.exec.commands)
	}
}

// A create still in flight when the user leaves the screen must not act
// on where they went: no attach, no navigation.
// What connecting changed — the local server registered on a first run —
// is shown on the message line once the connection is ready, since the
// connector had no terminal to say it on.
func TestSessionNoticeIsShownWhenConnected(t *testing.T) {
	h := newMultiHarness(t, "ws")
	h.connectors["Local"].notice = "registered atc.server; undo with `atc server uninstall`"
	h.open()
	if h.m.message != "registered atc.server; undo with `atc server uninstall`" || h.m.failed {
		t.Errorf("message after connecting = %q (failed %v)", h.m.message, h.m.failed)
	}
	view := lines(h.m)
	if view[len(view)-3] != "registered atc.server; undo with `atc server uninstall`" {
		t.Errorf("message line %q", view[len(view)-3])
	}
}

func TestCreateAnsweredAfterLeavingScreenIsDropped(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("j")
	h.key("j")
	h.key("enter") // work
	pending := h.send(keyPress("n"))
	h.key("esc")
	if h.m.screen != screenSpaces {
		t.Fatalf("esc during a create = screen %v", h.m.screen)
	}
	for _, msg := range pending {
		h.run(msg)
	}
	if h.m.screen != screenSpaces || len(h.exec.commands) != 0 || h.m.loading {
		t.Errorf("late create = screen %v exec %v loading %v", h.m.screen, h.exec.commands, h.m.loading)
	}
	h.key("n")
	pending = h.send(keyPress("."))
	h.key("esc")
	for _, msg := range pending {
		h.run(msg)
	}
	if h.m.screen != screenSpaces || len(h.exec.commands) != 0 {
		t.Errorf("late space create = screen %v exec %v", h.m.screen, h.exec.commands)
	}
}

func TestTransportLossReconnectsSameTerminal(t *testing.T) {
	h := newHarness(t, "ws")
	h.open()
	h.key("j") // play
	h.key("enter")
	if h.m.selectedTerminal != "term-play" {
		t.Fatalf("selected %q", h.m.selectedTerminal)
	}
	// The transport drops; the API is unreachable for two polls, then
	// answers with the Terminal still running.
	h.exec.exits = []error{fakeExit(255), nil}
	h.client.err = errors.New("dial tcp: no route to host")
	msgs := h.send(keyPress("enter"))
	ended := h.send(msgs[0])
	if h.m.reconnect == nil || !strings.Contains(h.m.message, "connection lost, reconnecting to 1:play") || !strings.Contains(plain(h.m), "waiting for 1:play") {
		t.Fatalf("loss = reconnect %v message %q", h.m.reconnect, h.m.message)
	}
	if view := lines(h.m); view[len(view)-2] != "esc cancel" {
		t.Errorf("reconnecting footer = %q", view[len(view)-2])
	}
	for _, msg := range ended {
		if !isWindowSizeRequest(msg) {
			t.Errorf("unexpected message on loss: %v", msg)
		}
	}
	// The retry is modal: list keys are ignored until it ends.
	h.key("j")
	if h.m.selectedTerminal != "term-play" || h.m.reconnect == nil {
		t.Fatalf("keys during reconnect moved the selection")
	}
	h.fire() // poll 1 fails
	h.fire() // poll 2 fails
	h.client.err = nil
	h.fire() // poll 3 answers running: re-attached
	if diff := cmp.Diff([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, h.clock.delays); diff != "" {
		t.Errorf("backoff (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-play"}, {"/bin/attach", "term-play"}}, h.exec.commands); diff != "" {
		t.Errorf("re-attach (-want +got):\n%s", diff)
	}
	if h.m.screen != screenTerminals || h.m.reconnect != nil || len(h.clock.pending) != 0 {
		t.Errorf("after re-attach = screen %v reconnect %v pending %v", h.m.screen, h.m.reconnect, h.clock.pending)
	}

	// Backoff caps at thirty seconds.
	h.clock.delays = nil
	h.exec.exits = []error{fakeExit(255)}
	h.client.err = errors.New("down")
	h.run(h.send(keyPress("enter"))[0])
	for range 8 {
		h.fire()
	}
	if got := h.clock.delays[len(h.clock.delays)-1]; got != reconnectMax || h.clock.delays[5] != reconnectMax {
		t.Errorf("backoff did not cap at %s: %v", reconnectMax, h.clock.delays)
	}
	// Esc cancels: the terminal list returns and the pending tick is dead.
	h.client.err = nil
	h.key("esc")
	if h.m.screen != screenTerminals || h.m.reconnect != nil {
		t.Fatalf("esc = screen %v reconnect %v", h.m.screen, h.m.reconnect)
	}
	polls := h.client.polls
	h.fire()
	if h.client.polls != polls || len(h.exec.commands) != 3 {
		t.Errorf("cancelled retry still polled or attached: polls %d exec %v", h.client.polls, h.exec.commands)
	}
}

func TestReconnectStopsWhenTerminalIsNotRunningOrGone(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*fakeClient)
		want   string
	}{
		"exited": {func(c *fakeClient) {
			c.terminals[3].Status, c.terminals[3].ExitCode = api.TerminalExited, &exitOne
		}, "1:play has exited with code 1"},
		"gone": {func(c *fakeClient) { c.terminals = c.terminals[:3] }, "terminal not found"},
		"token rotated": {func(c *fakeClient) {
			c.err = &api.Problem{Status: http.StatusUnauthorized, Code: api.CodeUnauthorized}
		}, "token for ws was rotated"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "ws")
			h.open()
			h.key("j")
			h.key("enter")
			h.exec.exits = []error{fakeExit(255)}
			h.run(h.send(keyPress("enter"))[0])
			tc.mutate(h.client)
			h.fire()
			if h.m.screen != screenTerminals || h.m.reconnect != nil || !strings.Contains(h.m.message, tc.want) || len(h.exec.commands) != 1 {
				t.Errorf("stop = screen %v reconnect %v message %q exec %v", h.m.screen, h.m.reconnect, h.m.message, h.exec.commands)
			}
		})
	}
}

func TestHelpOverlay(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.key("?")
	if !h.m.help || !matches(`enter\s+attach`, plain(h.m)) || !matches(`1-9\s+attach by #`, plain(h.m)) {
		t.Fatalf("help = %v view:\n%s", h.m.help, plain(h.m))
	}
	h.key("1")
	if h.m.help || h.m.selectedSpace.id != "spce-home" || len(h.exec.commands) != 0 {
		t.Error("closing key was also applied to the list")
	}
}

// Digits attach by row number on the terminals screen only (ATC-317).
func TestDigitAttachesByRowNumber(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	// Inert on the spaces screen.
	h.key("1")
	if len(h.exec.commands) != 0 || h.m.screen != screenSpaces || h.m.selectedSpace.id != "spce-home" || h.m.message != "" {
		t.Fatalf("digit on spaces = exec %v screen %v selected %v message %q", h.exec.commands, h.m.screen, h.m.selectedSpace, h.m.message)
	}
	// Filter text in the directory picker.
	h.key("n")
	h.key("1")
	if h.m.dirInput != "1" || len(h.exec.commands) != 0 {
		t.Fatalf("digit in the directory picker = input %q exec %v", h.m.dirInput, h.exec.commands)
	}
	h.key("esc")
	h.key("esc")
	h.key("j")
	h.key("j")
	h.key("enter") // work: 1:zsh 2:nvim 3:api

	h.exec.exits = []error{nil}
	h.key("3")
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-new"}}, h.exec.commands); diff != "" {
		t.Errorf("3 (-want +got):\n%s", diff)
	}
	if h.m.selectedTerminal != "term-new" {
		t.Errorf("after 3: selected %q", h.m.selectedTerminal)
	}
	h.key("7")
	if len(h.exec.commands) != 1 || h.m.message != "no terminal 7" {
		t.Errorf("7 = exec %v message %q", h.exec.commands, h.m.message)
	}
	h.key("2")
	if len(h.exec.commands) != 1 || !strings.Contains(h.m.message, "2:nvim has exited with code 1") {
		t.Errorf("2 = exec %v message %q", h.exec.commands, h.m.message)
	}
	// Ignored while a load is in flight, like enter.
	pending := h.send(keyPress("r"))
	if more := h.send(keyPress("1")); len(more) != 0 || len(h.exec.commands) != 1 {
		t.Errorf("digit while loading = %v exec %v", more, h.exec.commands)
	}
	for _, msg := range pending {
		h.run(msg)
	}
	// The row's terminal, as listed: a delete elsewhere since the last
	// refresh does not renumber what the user sees.
	h.client.terminals = terminals[1:]
	h.exec.exits = []error{nil}
	h.key("1")
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-new"}, {"/bin/attach", "term-old"}}, h.exec.commands); diff != "" {
		t.Errorf("1 after a stale delete (-want +got):\n%s", diff)
	}
	// A confirmation prompt does not hear digits.
	h.key("d")
	h.key("1")
	if h.m.confirm == nil || len(h.exec.commands) != 2 {
		t.Errorf("digit in confirmation = confirm %v exec %v", h.m.confirm, h.exec.commands)
	}
}

// The frame fills the terminal at any size: every line is exactly the
// terminal's width, rows truncate rather than wrap, the selection stays
// on screen, and a request in flight shows in the title bar.
func TestFrameFitsTerminalAndTruncatesRows(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 52, Height: 12})
	raw := strings.Split(h.m.View().Content, "\n")
	if len(raw) != 12 {
		t.Fatalf("%d lines, want 12:\n%s", len(raw), plain(h.m))
	}
	for i, line := range raw {
		if ansi.StringWidth(line) != 52 {
			t.Errorf("line %d is %d wide: %q", i, ansi.StringWidth(line), ansi.Strip(line))
		}
	}
	view := lines(h.m)
	if !strings.HasPrefix(view[0], "╭ Local ─") || !strings.HasSuffix(view[0], "╮") || !strings.HasPrefix(view[11], "╰") {
		t.Errorf("frame:\n%s", plain(h.m))
	}
	if !strings.Contains(plain(h.m), "/home/…") {
		t.Errorf("long directory not truncated:\n%s", plain(h.m))
	}
	// Two body lines: the header and one row, moved to the selection.
	h.run(tea.WindowSizeMsg{Width: 40, Height: 9})
	h.key("j")
	h.key("j") // work
	if view := plain(h.m); !strings.Contains(view, "work") || strings.Contains(view, "play") || !strings.Contains(view, "NAME") {
		t.Errorf("window at height 9:\n%s", view)
	}
	h.send(keyPress("r"))
	if view := lines(h.m); !h.m.anyLoading() || !strings.HasSuffix(view[0], " loading… ╮") {
		t.Errorf("loading indicator: %q", view[0])
	}
	// Narrower than the title and the note together: the note wins,
	// and nothing overflows.
	for _, width := range []int{8, 12, 16} {
		h.run(tea.WindowSizeMsg{Width: width, Height: 8})
		for i, line := range strings.Split(h.m.View().Content, "\n") {
			if ansi.StringWidth(line) != width {
				t.Errorf("width %d line %d is %d wide: %q", width, i, ansi.StringWidth(line), ansi.Strip(line))
			}
		}
	}
	if view := lines(h.m); !strings.Contains(view[0], "loading…") {
		t.Errorf("note lost at width 16: %q", view[0])
	}
}

// Each screen's breadcrumb and footer, exactly; movement, enter, r,
// and the digits never appear in a footer. The terminal and directory
// screens name the connection they work on.
func TestBreadcrumbsAndFooters(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 80, Height: 40})
	check := func(breadcrumb, footer string) {
		t.Helper()
		view := lines(h.m)
		if view[1] != breadcrumb || view[len(view)-2] != footer {
			t.Errorf("breadcrumb %q footer %q, want %q and %q", view[1], view[len(view)-2], breadcrumb, footer)
		}
		for _, obvious := range []string{"j/k", "enter open", "enter attach", "r refresh", "1-9"} {
			if strings.Contains(footer, obvious) {
				t.Errorf("footer %q names %q", footer, obvious)
			}
		}
	}
	check("spaces", "n new   d delete   c connections   ? help   q quit")
	h.key("j")
	h.key("d")
	check("spaces", "y confirm   n/esc cancel")
	h.key("esc")
	h.key("j")
	h.key("enter")
	check("spaces › Local › work  /home/u/work", "n new   d delete   esc back   ? help   q quit")
	h.key("esc")
	h.key("n")
	h.key("C")
	check("spaces › Local › new space  /home/u/C▏", ". choose this directory   esc back   ? help")
	h.key("esc")
	h.key("esc")
	h.key("c")
	check("connections", "esc back   ? help   q quit")
	h.key("?")
	check("help", "any key to close")
}

// Status cells are coloured by status, failures in red and notices
// plain, and the palette follows the terminal background.
func TestStatusAndMessageColours(t *testing.T) {
	h := newHarness(t, "")
	h.client.terminals = append(h.client.terminals, api.Terminal{ID: "term-lost", Process: "ssh", SpaceID: "spce-work", Status: api.TerminalUnreachable, CreatedAt: t0.Add(2 * time.Hour)})
	h.open()
	h.key("j")
	h.key("j")
	h.key("enter")
	st := newStyles(true)
	for _, want := range []string{st.good.Render("running"), st.bad.Render("exited (1)"), st.warn.Render("unreachable")} {
		if !strings.Contains(h.m.View().Content, want) {
			t.Errorf("view lacks %q:\n%s", want, h.m.View().Content)
		}
	}
	h.client.err = errors.New("boom")
	h.key("r")
	if !strings.Contains(h.m.View().Content, st.bad.Render("loading terminals: boom")) {
		t.Errorf("failure not red:\n%s", h.m.View().Content)
	}
	h.m.notify("plain notice")
	if line := rawLine(h.m, "plain notice"); strings.Contains(line, st.bad.Render("plain notice")) || !strings.Contains(line, " plain notice ") {
		t.Errorf("notice styled: %q", line)
	}
	h.run(tea.BackgroundColorMsg{Color: color.White})
	if h.m.dark || !strings.Contains(h.m.View().Content, newStyles(false).warn.Render("unreachable")) {
		t.Errorf("light palette not applied (dark %v):\n%s", h.m.dark, h.m.View().Content)
	}
}

// The help overlay lists every key and is the only place the versions
// show: the client's, then each connected server's, dim when equal and
// yellow when different. On a short terminal it scrolls with the
// movement keys instead of being cut off.
func TestHelpOverlayVersionsAndScroll(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 80, Height: 50})
	h.key("?")
	view := strings.Join(lines(h.m), "\n")
	for _, group := range []string{
		`everywhere\n.*\?\s+help\s+ctrl\+c\s+quit`,
		`spaces\n.*↑/↓ j/k\s+move\s+enter\s+open\n.*n\s+new space\s+d\s+delete\n.*c\s+connections\s+r\s+refresh\n.*q\s+quit`,
		`terminals\n.*↑/↓ j/k\s+move\s+enter\s+attach\n.*1-9\s+attach by #\s+ctrl-\\\s+detach\n.*n\s+new shell\s+d\s+delete\n.*esc h\s+back\s+r\s+refresh\n.*q\s+quit`,
		`new space\n.*↑/↓ j/k\s+move\s+enter\s+choose connection\n.*type\s+filter, or an absolute path\n.*↑/↓\s+move\s+ctrl\+p/n\s+move\n.*enter\s+descend\s+backspace up\n.*\.\s+choose\s+ctrl\+r\s+refresh\n.*esc\s+clear the field, then back`,
		`connections\n.*↑/↓ j/k\s+move\s+enter\s+connect, retry, or update\n.*a\s+add\s+d\s+remove\n.*r\s+retry all\s+esc h\s+back\n.*q\s+quit`,
		`add\n.*↑/↓ j/k\s+move\s+enter\s+add and set up\n.*esc h\s+back`,
		`confirm\n.*y\s+confirm\s+n esc\s+cancel`,
		`reconnecting\n.*esc\s+cancel, back to terminals`,
	} {
		if !matches(group, view) {
			t.Errorf("help lacks %q:\n%s", group, view)
		}
	}
	st := newStyles(true)
	if !strings.Contains(h.m.View().Content, st.dim.Render("client v1")) || !strings.Contains(h.m.View().Content, st.dim.Render("Local server v1")) {
		t.Errorf("equal versions not dim:\n%s", h.m.View().Content)
	}
	h.key("esc")
	h.conn("Local").session.ServerVersion = "v2"
	if strings.Contains(plain(h.m), "v2") || strings.Contains(plain(h.m), "v1") {
		t.Errorf("version outside help:\n%s", plain(h.m))
	}
	h.key("?")
	if !strings.Contains(h.m.View().Content, st.warn.Render("Local server v2")) {
		t.Errorf("mismatch not yellow:\n%s", h.m.View().Content)
	}
	h.key("x")
	remote := newHarness(t, "devbox")
	remote.open()
	remote.run(tea.WindowSizeMsg{Width: 80, Height: 50})
	remote.key("?")
	if view := lines(remote.m); !strings.HasPrefix(view[0], "╭ devbox ") || !strings.Contains(plain(remote.m), "devbox server v1") {
		t.Errorf("remote help:\n%s", plain(remote.m))
	}

	// Five body lines: the help scrolls, the message line says so, and
	// any key but movement still closes it.
	total := len(h.m.helpLines(styles{}))
	h.run(tea.WindowSizeMsg{Width: 80, Height: 12})
	h.key("?")
	body := func() []string { return lines(h.m)[3:8] }
	if got := body(); got[0] != "everywhere" || lines(h.m)[9] != fmt.Sprintf("↑/↓ j/k scroll · line 1 of %d", total) || lines(h.m)[10] != "any key to close" {
		t.Fatalf("short help = body %q message %q footer %q", got, lines(h.m)[9], lines(h.m)[10])
	}
	h.key("j")
	if got := body(); !h.m.help || !strings.HasPrefix(got[0], "?          help") {
		t.Errorf("after j: help %v body %q", h.m.help, got)
	}
	for range 60 {
		h.key("down")
	}
	if got := body(); got[4] != "Local server v2" {
		t.Errorf("scrolled to the end: %q", got)
	}
	h.key("k")
	if got := body(); got[4] != "client v1" || !h.m.help {
		t.Errorf("after k: %q", got)
	}
	// Growing the terminal clamps the offset so k answers at once.
	h.run(tea.WindowSizeMsg{Width: 80, Height: 30})
	if h.m.helpScroll != h.m.helpScrollLimit() || lines(h.m)[25] != "Local server v2" {
		t.Errorf("after growing: scroll %d limit %d line 25 %q", h.m.helpScroll, h.m.helpScrollLimit(), lines(h.m)[25])
	}
	h.run(tea.WindowSizeMsg{Width: 80, Height: 12})
	h.key("enter")
	if h.m.help || h.m.screen != screenSpaces || len(h.exec.commands) != 0 {
		t.Error("closing key was also applied to the list")
	}
	// Reopening starts at the top.
	h.key("?")
	if got := body(); got[0] != "everywhere" {
		t.Errorf("reopened help: %q", got)
	}
}

// Every connection is attempted on launch and loads on its own: one
// that needs a person leaves the others' Spaces in the table, the same
// IDs on two servers stay two rows, and actions reach only the row's
// own server (ATC-327).
func TestConnectionsLoadIndependentlyAndRouteByConnection(t *testing.T) {
	h := newMultiHarness(t, "ws", "devbox")
	h.connectors["devbox"].connectErr = fmt.Errorf("%w: ssh to devbox: Permission denied (publickey)", ErrLoginRequired)
	h.open()
	for _, name := range []string{"Local", "ws", "devbox"} {
		if h.connectors[name].connects != 1 {
			t.Errorf("%s attempted %d times on launch", name, h.connectors[name].connects)
		}
	}
	want := []string{"Local/spce-home", "Local/spce-play", "Local/spce-work", "ws/spce-home", "ws/spce-play", "ws/spce-work"}
	if diff := cmp.Diff(want, rowKeys(h.m)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if h.m.screen != screenSpaces || h.m.selectedSpace != (spaceRef{"Local", "spce-home"}) {
		t.Errorf("landed on screen %v selected %v", h.m.screen, h.m.selectedSpace)
	}
	if view := lines(h.m); !strings.HasPrefix(view[0], "╭ atc ─") || view[1] != "spaces  devbox: needs login" {
		t.Errorf("title %q breadcrumb %q", view[0], view[1])
	}
	if !matches(`ws\s+work\s+3\s+/home/u/work`, plain(h.m)) {
		t.Errorf("view:\n%s", plain(h.m))
	}
	// Down through Local's rows into ws's: the same Space ID, the other
	// server. Its Terminals come from ws, its header names ws.
	for range 5 {
		h.key("j")
	}
	if h.m.selectedSpace != (spaceRef{"ws", "spce-work"}) {
		t.Fatalf("selected %v", h.m.selectedSpace)
	}
	h.connectors["ws"].client.terminals[0].Directory = "/on/ws"
	h.key("enter")
	if h.m.screen != screenTerminals || h.m.conn != "ws" || !strings.Contains(plain(h.m), "/on/ws") || lines(h.m)[1] != "spaces › ws › work  /home/u/work" {
		t.Errorf("ws terminals = screen %v conn %q breadcrumb %q:\n%s", h.m.screen, h.m.conn, lines(h.m)[1], plain(h.m))
	}
	// New shell, attach, and delete all reach ws, never Local.
	h.exec.exits = []error{nil}
	h.key("n")
	h.key("d")
	h.key("y")
	ws, local := h.connectors["ws"], h.connectors["Local"]
	if len(ws.client.createdTerm) != 1 || len(ws.attached) != 1 || len(ws.client.deleted) != 1 {
		t.Errorf("ws saw create %d attach %d delete %d", len(ws.client.createdTerm), len(ws.attached), len(ws.client.deleted))
	}
	if len(local.client.createdTerm) != 0 || len(local.attached) != 0 || len(local.client.deleted) != 0 {
		t.Errorf("Local saw create %d attach %d delete %d", len(local.client.createdTerm), len(local.attached), len(local.client.deleted))
	}
	h.key("esc")
	h.key("d") // delete ws's work
	if h.m.confirm == nil || h.m.confirm.conn != "ws" || !strings.Contains(plain(h.m), "on ws?") {
		t.Fatalf("confirmation = %+v", h.m.confirm)
	}
	h.key("y")
	if diff := cmp.Diff([]string{"terminal:term-new01", "space:spce-work"}, ws.client.deleted); diff != "" {
		t.Errorf("ws deletions (-want +got):\n%s", diff)
	}
	if len(local.client.deleted) != 0 {
		t.Errorf("Local deletions: %v", local.client.deleted)
	}
}

// A connection that stops answering keeps its Spaces listed but dimmed
// and unusable while its health is polled with backoff; when it answers
// again its rows are usable and reloaded. Nothing falls back to another
// connection meanwhile.
func TestUnavailableConnectionDimsSpacesAndRecovers(t *testing.T) {
	h := newMultiHarness(t, "ws")
	h.open()
	ws := h.connectors["ws"]
	ws.client.err = errors.New("dial tcp: no route to host")
	h.key("r")
	c := h.conn("ws")
	if c.status != connUnavailable || !strings.Contains(h.m.message, "loading spaces on ws: dial tcp") {
		t.Fatalf("after the failed load: %v %q", c.status, h.m.message)
	}
	if !dimmedRow(h.m, "ws", "work") || !dimmedRow(h.m, "ws", "Default") || dimmedRow(h.m, "Local", "work") {
		t.Errorf("ws rows not dimmed:\n%s", h.m.View().Content)
	}
	for range 4 {
		h.key("j") // ws/play
	}
	h.key("enter")
	if h.m.screen != screenSpaces || !strings.Contains(h.m.message, "ws is unavailable: dial tcp: no route to host (see connections)") {
		t.Errorf("enter on a dimmed row = screen %v message %q", h.m.screen, h.m.message)
	}
	h.key("d")
	if h.m.confirm != nil || !strings.Contains(h.m.message, "ws is unavailable") {
		t.Errorf("d on a dimmed row = confirm %v message %q", h.m.confirm, h.m.message)
	}
	if len(h.connectors["Local"].client.deleted) != 0 {
		t.Error("a refused action fell back to Local")
	}
	// Health polls back off like a lost attachment, then the recovered
	// connection reloads its Spaces.
	h.fire()
	h.fire()
	if diff := cmp.Diff([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, h.clock.delays); diff != "" {
		t.Errorf("backoff (-want +got):\n%s", diff)
	}
	ws.client.err = nil
	ws.client.spaces = ws.client.spaces[:2]
	h.fire()
	if h.conn("ws").status != connReady || len(h.m.rows()) != 5 || dimmedRow(h.m, "ws", "work") {
		t.Errorf("after recovery: status %v rows %d:\n%s", h.conn("ws").status, len(h.m.rows()), h.m.View().Content)
	}
	if ws.client.healths != 3 {
		t.Errorf("health polled %d times", ws.client.healths)
	}
	h.key("enter")
	if h.m.screen != screenTerminals || h.m.conn != "ws" {
		t.Errorf("recovered row not usable: screen %v conn %q", h.m.screen, h.m.conn)
	}
	// A server that answers but refuses this picker is not waited for.
	h.key("esc")
	ws.client.err = errors.New("timeout")
	h.key("r")
	ws.client.err = &api.Problem{Status: http.StatusUnauthorized, Code: api.CodeUnauthorized}
	h.fire()
	if c := h.conn("ws"); c.status != connFailed || c.action() != "connect" || len(h.clock.pending) != 0 {
		t.Errorf("refused health = %v/%q pending %v", c.status, c.action(), h.clock.pending)
	}
}

// The Connections screen shows each connection's state and the action
// that clears it. Add offers only the ssh configuration's aliases not
// yet saved, saves the choice before running setup, and a failed setup
// leaves the connection listed with its reason and a Retry; setup holds
// the terminal and prints there. Remove forgets the connection and
// closes its transport, and nothing that answers afterwards revives it.
func TestConnectionsScreenAddRetryAndRemove(t *testing.T) {
	h := newMultiHarness(t, "ws")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.key("c")
	if h.m.screen != screenConnections || h.m.selectedConnection != "Local" {
		t.Fatalf("c = screen %v selected %q", h.m.screen, h.m.selectedConnection)
	}
	if view := plain(h.m); !matches(`NAME\s+STATUS\s+SERVER\s+DETAIL`, view) || !matches(`Local\s+ready\s+v1`, view) || !matches(`ws\s+ready\s+v1`, view) {
		t.Errorf("view:\n%s", view)
	}
	if h.m.message != "" || h.send(keyPress("enter")) != nil {
		t.Errorf("enter on a ready connection did something: %q", h.m.message)
	}
	h.key("a")
	if h.m.screen != screenAliases || !cmp.Equal(h.m.availableAliases(), []string{"build", "tablet"}) || h.m.selectedAlias != "build" {
		t.Fatalf("a = screen %v aliases %v selected %q", h.m.screen, h.m.availableAliases(), h.m.selectedAlias)
	}
	if lines(h.m)[1] != "connections › add  ssh config aliases" {
		t.Errorf("breadcrumb %q", lines(h.m)[1])
	}
	// Setup for the new alias fails; it stays saved with its reason.
	pending := &fakeConnector{name: "build", remote: true, client: newFakeClient(), version: "v1"}
	h.m.open = func(alias string) Connector { h.connectors[alias] = pending; return pending }
	pending.setupErr = errors.New("ssh to build failed (exit 255): Connection refused")
	pending.setupSays = "build (linux/amd64) needs setup before it can be used:\n"
	h.key("enter")
	if diff := cmp.Diff([][]string{{"ws", "build"}}, h.saves); diff != "" {
		t.Errorf("saved (-want +got):\n%s", diff)
	}
	if h.exec.setups != 1 || pending.setups != 1 || !strings.Contains(h.exec.terminal.String(), "needs setup") {
		t.Errorf("setup handoff: exec %d connector %d terminal %q", h.exec.setups, pending.setups, h.exec.terminal.String())
	}
	c := h.conn("build")
	if h.m.screen != screenConnections || h.m.selectedConnection != "build" || c.status != connFailed || c.action() != "retry" {
		t.Fatalf("after a failed setup: screen %v selected %q status %v action %q", h.m.screen, h.m.selectedConnection, c.status, c.action())
	}
	view := lines(h.m)
	if !matches(`build\s+failed\s+ssh to build failed`, strings.Join(view, "\n")) || view[len(view)-2] != "enter retry   a add   d remove   esc back   ? help   q quit" {
		t.Errorf("failed connection view:\n%s", strings.Join(view, "\n"))
	}
	// Adding the same alias again selects it, never duplicates it; a
	// choice that cannot be saved is not made.
	h.key("a")
	if !cmp.Equal(h.m.availableAliases(), []string{"tablet"}) {
		t.Errorf("aliases offered again: %v", h.m.availableAliases())
	}
	h.key("esc")
	if got, _ := h.m.addConnection("build"); len(got.(model).connections) != 3 {
		t.Errorf("re-adding build made %d connections", len(got.(model).connections))
	}
	save := h.m.save
	h.m.save = func([]string) error { return errors.New("disk full") }
	h.key("a")
	h.key("enter")
	if len(h.m.connections) != 3 || pending.setups != 1 || !strings.Contains(h.m.message, "saving connections: disk full") {
		t.Errorf("unsaved add = connections %d setups %d message %q", len(h.m.connections), pending.setups, h.m.message)
	}
	h.m.save = save
	// Retry runs setup again; success lists its Spaces.
	pending.setupErr = nil
	h.key("enter")
	if c := h.conn("build"); c.status != connReady || pending.setups != 2 || len(h.m.rows()) != 9 {
		t.Errorf("after retry: status %v setups %d rows %d", c.status, pending.setups, len(h.m.rows()))
	}
	if diff := cmp.Diff([]string{"Local", "ws", "build"}, h.m.connectionNames()); diff != "" {
		t.Errorf("connections (-want +got):\n%s", diff)
	}
	// Local cannot be removed; a remote can, without touching it.
	h.key("k")
	h.key("k")
	h.key("d")
	if h.m.confirm != nil || h.m.message != "Local cannot be removed" {
		t.Errorf("d on Local = confirm %v message %q", h.m.confirm, h.m.message)
	}
	h.key("j")
	h.key("d")
	if h.m.confirm == nil || h.m.confirm.kind != "connection" || !strings.Contains(plain(h.m), "remove connection ws from this picker? (nothing on it is changed)") {
		t.Fatalf("confirm = %+v:\n%s", h.m.confirm, plain(h.m))
	}
	ws := h.connectors["ws"]
	late := h.send(keyPress("y"))
	if diff := cmp.Diff([]string{"Local", "build"}, h.m.connectionNames()); diff != "" {
		t.Errorf("after remove (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"build"}, h.saves[len(h.saves)-1]); diff != "" {
		t.Errorf("saved after remove (-want +got):\n%s", diff)
	}
	for _, msg := range late {
		h.run(msg)
	}
	if ws.closes != 1 || len(ws.client.deleted) != 0 || h.m.message != "removed ws; nothing on it was changed" {
		t.Errorf("remove: closes %d deleted %v message %q", ws.closes, ws.client.deleted, h.m.message)
	}
	if len(h.m.retired) != 1 {
		t.Errorf("removed connector not kept for closing at exit: %d", len(h.m.retired))
	}
	// Answers from the removed connection change nothing, and cannot
	// reach a connection of the same name added afterwards.
	stale := spacesLoadedMsg{conn: "ws", seq: h.conn("Local").seq, err: errors.New("stale")}
	h.run(stale)
	h.run(connectedMsg{conn: "ws", gen: 2, session: ws.session()})
	h.run(healthMsg{conn: "ws", gen: 2})
	if len(h.m.rows()) != 6 || len(h.m.connections) != 2 {
		t.Errorf("late answers revived ws: rows %d connections %d", len(h.m.rows()), len(h.m.connections))
	}
	h.m.open = func(alias string) Connector {
		c := &fakeConnector{name: alias, remote: true, client: newFakeClient(), version: "v1"}
		h.connectors[alias] = c
		return c
	}
	h.runModel(h.m.addConnection("ws"))
	if c := h.conn("ws"); c.status != connReady || c.seq <= stale.seq {
		t.Fatalf("re-added ws: status %v seq %d", c.status, c.seq)
	}
	h.run(stale)
	if c := h.conn("ws"); c.status != connReady || c.err != nil {
		t.Errorf("the removed connection's answer reached the re-added one: %v %v", c.status, c.err)
	}
	h.key("esc")
	if h.m.screen != screenSpaces || h.m.selectedSpace.conn == "ws" {
		t.Errorf("back = screen %v selected %v", h.m.screen, h.m.selectedSpace)
	}
}

// A needs-update connection shows its plan whole, and enter runs the
// setup that applies it after its own confirmation; retry all is the
// unattended attempt again.
func TestConnectionsNeedingSetupOrLogin(t *testing.T) {
	h := newMultiHarness(t, "ws", "devbox")
	plan := fmt.Errorf("%w:\nws (linux/amd64) needs setup before it can be used:\n  atc: v0.1.0 at /home/u/.local/bin/atc; update it", ErrSetupRequired)
	h.connectors["ws"].connectErr = plan
	h.connectors["devbox"].connectErr = fmt.Errorf("%w: ssh to devbox: Permission denied", ErrLoginRequired)
	h.open()
	h.run(tea.WindowSizeMsg{Width: 80, Height: 24})
	if lines(h.m)[1] != "spaces  ws: needs update · devbox: needs login" {
		t.Errorf("breadcrumb %q", lines(h.m)[1])
	}
	h.key("c")
	h.key("j")
	view := lines(h.m)
	if !matches(`ws\s+needs update\s+setup required:`, strings.Join(view, "\n")) || !strings.Contains(strings.Join(view, "\n"), "atc: v0.1.0 at /home/u/.local/bin/atc; update it") || view[len(view)-2] != "enter update   a add   d remove   esc back   ? help   q quit" {
		t.Errorf("needs-update view:\n%s", strings.Join(view, "\n"))
	}
	h.key("j")
	if view := lines(h.m); view[len(view)-2] != "enter connect   a add   d remove   esc back   ? help   q quit" {
		t.Errorf("needs-login footer %q", view[len(view)-2])
	}
	// Retry all attempts both without a person; ws now connects.
	h.connectors["ws"].connectErr = nil
	h.key("r")
	if h.connectors["ws"].connects != 2 || h.connectors["devbox"].connects != 2 || h.conn("ws").status != connReady || h.conn("devbox").status != connFailed {
		t.Errorf("retry all: ws %d/%v devbox %d/%v", h.connectors["ws"].connects, h.conn("ws").status, h.connectors["devbox"].connects, h.conn("devbox").status)
	}
	if h.exec.setups != 0 {
		t.Error("retry all handed the terminal over")
	}
	// Connect hands the terminal to devbox's setup; while it holds it no
	// second handoff starts.
	h.connectors["devbox"].setupSays = "devbox password: "
	h.key("enter")
	if h.exec.setups != 1 || h.connectors["devbox"].setups != 1 || h.conn("devbox").status != connReady || !strings.Contains(h.exec.terminal.String(), "password") {
		t.Errorf("connect: handoffs %d setups %d status %v terminal %q", h.exec.setups, h.connectors["devbox"].setups, h.conn("devbox").status, h.exec.terminal.String())
	}
	if len(h.m.rows()) != 9 {
		t.Errorf("rows after every connection came up: %d", len(h.m.rows()))
	}
}

// With more than one connection, a new Space first chooses its
// connection, starting from the selected Space's; an unavailable one is
// refused; the browser and the creation then use only the chosen one.
func TestNewSpaceChoosesConnection(t *testing.T) {
	h := newMultiHarness(t, "ws", "devbox")
	h.connectors["devbox"].connectErr = errors.New("ssh to devbox failed (exit 255): Connection refused")
	h.open()
	for range 4 {
		h.key("j") // ws/play
	}
	h.key("n")
	if h.m.screen != screenDestination || h.m.selectedConnection != "ws" || lines(h.m)[1] != "spaces › new space  choose a connection" {
		t.Fatalf("n = screen %v selected %q breadcrumb %q", h.m.screen, h.m.selectedConnection, lines(h.m)[1])
	}
	if view := plain(h.m); !matches(`Local\s+ready`, view) || !matches(`devbox\s+failed`, view) {
		t.Errorf("destination view:\n%s", view)
	}
	h.key("j") // devbox
	h.key("enter")
	if h.m.screen != screenDestination || !strings.Contains(h.m.message, "devbox is failed: ssh to devbox failed") {
		t.Errorf("enter on a failed destination = screen %v message %q", h.m.screen, h.m.message)
	}
	h.key("k")
	h.key("enter")
	if h.m.screen != screenDirectories || h.m.conn != "ws" || lines(h.m)[1] != "spaces › ws › new space  /home/u/▏" {
		t.Fatalf("enter on ws = screen %v conn %q breadcrumb %q", h.m.screen, h.m.conn, lines(h.m)[1])
	}
	h.exec.exits = []error{nil}
	h.key(".")
	ws, local := h.connectors["ws"], h.connectors["Local"]
	if diff := cmp.Diff([]api.SpaceCreateParams{{Directory: "/home/u"}}, ws.client.created); diff != "" {
		t.Errorf("ws created (-want +got):\n%s", diff)
	}
	if len(local.client.created) != 0 || len(ws.attached) != 1 || h.m.selectedSpace != (spaceRef{"ws", "spce-new01"}) || lines(h.m)[1] != "spaces › ws › new  /home/u" {
		t.Errorf("create = Local created %d ws attached %d selected %v breadcrumb %q", len(local.client.created), len(ws.attached), h.m.selectedSpace, lines(h.m)[1])
	}
	// The chooser's esc returns to Spaces.
	h.key("esc")
	h.key("n")
	h.key("esc")
	if h.m.screen != screenSpaces {
		t.Errorf("esc from the chooser = %v", h.m.screen)
	}
}

// The single-machine picker (`atc --remote`) shows its one connection
// and manages none: no Add, no Remove.
func TestSingleMachinePickerManagesNoConnections(t *testing.T) {
	h := newHarness(t, "ws")
	h.open()
	if view := lines(h.m); !strings.HasPrefix(view[0], "╭ ws ─") || view[1] != "spaces" {
		t.Errorf("title %q breadcrumb %q", view[0], view[1])
	}
	h.key("n")
	if h.m.screen != screenDirectories || h.m.conn != "ws" {
		t.Errorf("n with one connection = screen %v conn %q", h.m.screen, h.m.conn)
	}
	h.key("esc")
	h.key("c")
	if view := lines(h.m); view[len(view)-2] != "esc back   ? help   q quit" {
		t.Errorf("footer %q", view[len(view)-2])
	}
	h.key("a")
	if h.m.screen != screenConnections || !strings.Contains(h.m.message, "run plain `atc` to manage connections") {
		t.Errorf("a = screen %v message %q", h.m.screen, h.m.message)
	}
	h.key("d")
	if h.m.confirm != nil || !strings.Contains(h.m.message, "run plain `atc`") {
		t.Errorf("d = confirm %v message %q", h.m.confirm, h.m.message)
	}
}
