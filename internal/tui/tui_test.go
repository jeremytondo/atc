package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// fakeExec is the process seam: it records every child the picker asked
// for and answers each with the next scripted exit.
type fakeExec struct {
	commands [][]string
	envs     [][]string
	exits    []error
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

type fakeExit int

func (e fakeExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

func transportLoss(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 255
}

// fakeClock records the reconnect delays and fires each tick immediately
// when its Cmd runs — no wall-clock sleeps.
type fakeClock struct{ delays []time.Duration }

func (c *fakeClock) tick(delay time.Duration, generation uint64) tea.Cmd {
	c.delays = append(c.delays, delay)
	return func() tea.Msg { return reconnectTickMsg{generation: generation} }
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
		{ID: "term-old", Process: "zsh", SpaceID: "spce-work", Status: api.TerminalRunning, CreatedAt: t0},
		{ID: "term-dead", Process: "nvim", SpaceID: "spce-work", Status: api.TerminalExited, ExitCode: &exitOne, CreatedAt: t0.Add(time.Minute)},
		{ID: "term-new", Name: "api", Process: "codex", SpaceID: "spce-work", Status: api.TerminalRunning, CreatedAt: t0.Add(time.Hour)},
		{ID: "term-play", Name: "play", Process: "zsh", SpaceID: "spce-play", Status: api.TerminalRunning, CreatedAt: t0},
	}
)

type harness struct {
	t      *testing.T
	m      model
	client *fakeClient
	exec   *fakeExec
	clock  *fakeClock
}

func newHarness(t *testing.T, target string) *harness {
	t.Helper()
	home := api.DirectoryList{Path: "/home/u", Parent: ptr("/home"), Entries: []api.DirectoryEntry{
		{Name: "Code", Path: "/home/u/Code"}, {Name: "play", Path: "/home/u/play"}, {Name: "work", Path: "/home/u/work"}}}
	client := &fakeClient{
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
	h := &harness{t: t, client: client, exec: &fakeExec{}, clock: &fakeClock{}}
	h.m = newModel(context.Background(), Options{
		Client: client, Target: target, ClientVersion: "v1", ServerVersion: "v1",
		Attach: func(_ context.Context, terminal api.Terminal) (*exec.Cmd, error) {
			cmd := exec.Command("/bin/attach", terminal.ID)
			cmd.Env = []string{"TERM=xterm"}
			return cmd, nil
		},
	})
	if target != "" {
		h.m.transportLoss = transportLoss
	}
	h.m.execProcess = h.exec.exec
	h.m.tick = h.clock.tick
	return h
}

func ptr(s string) *string { return &s }

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

func (h *harness) open() {
	h.t.Helper()
	for _, msg := range drain(h.m.Init()) {
		h.run(msg)
	}
}

func isWindowSizeRequest(msg tea.Msg) bool {
	return reflect.TypeOf(msg) == reflect.TypeOf(tea.RequestWindowSize())
}

func TestSpaceListOrderSelectionAndCounts(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	if diff := cmp.Diff([]string{"spce-home", "spce-play", "spce-work"}, spaceIDs(h.m.spaces)); diff != "" {
		t.Errorf("Default first, then newest-first (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]int{"spce-work": 3, "spce-play": 1}, h.m.terminalCounts); diff != "" {
		t.Errorf("terminal counts (-want +got):\n%s", diff)
	}
	if h.m.selectedSpace != "spce-home" || h.m.screen != screenSpaces || h.m.message != "" {
		t.Fatalf("initial = selected %q screen %v message %q", h.m.selectedSpace, h.m.screen, h.m.message)
	}
	h.key("j")
	h.key("j")
	h.key("j") // clamps
	if h.m.selectedSpace != "spce-work" {
		t.Errorf("after j j j: %q", h.m.selectedSpace)
	}
	h.key("k")
	if h.m.selectedSpace != "spce-play" {
		t.Errorf("after k: %q", h.m.selectedSpace)
	}
	// Selection survives a refresh by ID, and falls to the adjacent row
	// when its Space disappears.
	h.client.spaces = []api.Space{spaces[0], spaces[1]}
	h.key("r")
	if h.m.selectedSpace != "spce-work" {
		t.Errorf("after the selected space vanished: %q, want the adjacent row", h.m.selectedSpace)
	}
	if !strings.Contains(h.m.View().Content, "> work") || !strings.Contains(h.m.View().Content, "Default (default)") {
		t.Errorf("view:\n%s", h.m.View().Content)
	}
	if strings.Contains(h.m.View().Content, "version mismatch") {
		t.Error("matching versions flagged as a mismatch")
	}
	h.m.serverVersion = "v2"
	if !strings.Contains(h.m.View().Content, "version mismatch") {
		t.Error("differing versions not flagged")
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
	if h.m.screen != screenTerminals || h.m.space.ID != "spce-work" {
		t.Fatalf("enter = screen %v space %q", h.m.screen, h.m.space.ID)
	}
	if diff := cmp.Diff([]string{"term-old", "term-dead", "term-new"}, terminalIDs(h.m.terminals)); diff != "" {
		t.Errorf("number order (-want +got):\n%s", diff)
	}
	if h.m.selectedTerminal != "term-old" {
		t.Errorf("row one preselected: %q", h.m.selectedTerminal)
	}
	view := h.m.View().Content
	for _, row := range []string{"> 1:zsh", "  2:nvim", "  3:api"} {
		if !strings.Contains(view, row) {
			t.Errorf("view lacks %q:\n%s", row, view)
		}
	}
	h.key("j")
	h.key("enter")
	if !strings.Contains(h.m.message, "2:nvim has exited with code 1") || len(h.exec.commands) != 0 {
		t.Errorf("exited terminal: message %q, exec %v", h.m.message, h.exec.commands)
	}
	if !strings.Contains(h.m.View().Content, "exited (1)") {
		t.Errorf("view:\n%s", h.m.View().Content)
	}
	h.key("esc")
	if h.m.screen != screenSpaces || h.m.selectedSpace != "spce-work" {
		t.Errorf("esc = screen %v selected %q", h.m.screen, h.m.selectedSpace)
	}
	h.key("enter")
	h.key("h")
	if h.m.screen != screenSpaces {
		t.Errorf("h did not go back")
	}
}

func TestRequestFailureShowsErrorAndKeepsNavigation(t *testing.T) {
	h := newHarness(t, "ws")
	h.open()
	h.client.err = errors.New("dial tcp: connection refused")
	h.key("r")
	if !strings.Contains(h.m.message, "loading spaces: dial tcp") || len(h.m.spaces) != 3 {
		t.Fatalf("failure = message %q spaces %d", h.m.message, len(h.m.spaces))
	}
	h.key("j")
	if h.m.selectedSpace != "spce-play" {
		t.Error("navigation blocked by the error")
	}
	h.client.err = &api.Problem{Status: http.StatusUnauthorized, Code: api.CodeUnauthorized}
	h.key("r")
	if !strings.Contains(h.m.message, "token was rotated") || !strings.Contains(h.m.message, "atc --remote ws") {
		t.Errorf("401 in remote mode: %q", h.m.message)
	}
	h.client.err = nil
	h.key("r")
	if h.m.message != "" {
		t.Errorf("retry left the message: %q", h.m.message)
	}
	if msgs := h.send(keyPress("q")); len(msgs) != 1 || reflect.TypeOf(msgs[0]) != reflect.TypeOf(tea.QuitMsg{}) {
		t.Errorf("q = %v, want quit", msgs)
	}
	if msgs := h.send(keyPress("ctrl+c")); len(msgs) != 1 || reflect.TypeOf(msgs[0]) != reflect.TypeOf(tea.QuitMsg{}) {
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
	want := &confirmation{kind: "space", id: "spce-work", name: "work", count: 3}
	if diff := cmp.Diff(want, h.m.confirm, cmp.AllowUnexported(confirmation{})); diff != "" {
		t.Fatalf("confirmation (-want +got):\n%s", diff)
	}
	if !strings.Contains(h.m.View().Content, "delete space work and its 3 terminals?") {
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
	if h.m.screen != screenSpaces || h.m.selectedSpace != "spce-play" {
		t.Errorf("after delete: screen %v selected %q", h.m.screen, h.m.selectedSpace)
	}

	// Terminal delete names the terminal and reloads the list.
	h.key("enter") // play
	h.key("d")
	if h.m.confirm == nil || h.m.confirm.kind != "terminal" || h.m.confirm.name != "1:play" {
		t.Fatalf("terminal confirmation = %+v", h.m.confirm)
	}
	if !strings.Contains(h.m.View().Content, "delete terminal 1:play?") {
		t.Errorf("view:\n%s", h.m.View().Content)
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
	if h.m.screen != screenDirectories || h.m.dir.Path != "/home/u" || h.m.selectedDir != "Code" {
		t.Fatalf("n = screen %v dir %q selected %q", h.m.screen, h.m.dir.Path, h.m.selectedDir)
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
	if h.m.screen != screenTerminals || h.m.space.ID != "spce-new01" || h.m.selectedTerminal != "term-new02" || h.m.selectedSpace != "spce-new01" {
		t.Errorf("after create = screen %v space %q selected %q selectedSpace %q", h.m.screen, h.m.space.ID, h.m.selectedTerminal, h.m.selectedSpace)
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
	if h.m.height != 40 || h.m.selectedTerminal != "term-old" {
		t.Errorf("after resize = height %d selected %q", h.m.height, h.m.selectedTerminal)
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
	h.client.createErr = errors.New("boom")
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
	if h.m.reconnect == nil || !strings.Contains(h.m.message, "connection lost, reconnecting to 1:play") || !strings.Contains(h.m.View().Content, "waiting for 1:play") {
		t.Fatalf("loss = reconnect %v message %q", h.m.reconnect, h.m.message)
	}
	// The retry is modal: list keys are ignored until it ends.
	h.key("j")
	if h.m.selectedTerminal != "term-play" || h.m.reconnect == nil {
		t.Fatalf("keys during reconnect moved the selection")
	}
	var tick tea.Msg
	for _, msg := range ended {
		if isWindowSizeRequest(msg) {
			continue
		}
		tick = msg
	}
	if tick == nil {
		t.Fatal("no reconnect tick scheduled")
	}
	poll := h.send(tick) // poll 1 fails
	tick2 := h.send(poll[0])
	poll = h.send(tick2[0]) // poll 2 fails
	h.client.err = nil
	tick3 := h.send(poll[0])
	poll = h.send(tick3[0]) // poll 3 answers running
	if diff := cmp.Diff([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, h.clock.delays); diff != "" {
		t.Errorf("backoff (-want +got):\n%s", diff)
	}
	h.send(poll[0])
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-play"}, {"/bin/attach", "term-play"}}, h.exec.commands); diff != "" {
		t.Errorf("re-attach (-want +got):\n%s", diff)
	}
	if h.m.screen != screenTerminals || h.m.reconnect != nil {
		t.Errorf("after re-attach = screen %v reconnect %v", h.m.screen, h.m.reconnect)
	}

	// Backoff caps at thirty seconds.
	h.clock.delays = nil
	h.exec.exits = []error{fakeExit(255)}
	h.client.err = errors.New("down")
	next := h.send(h.send(keyPress("enter"))[0])
	for range 8 {
		var tick tea.Msg
		for _, msg := range next {
			if !isWindowSizeRequest(msg) {
				tick = msg
			}
		}
		next = h.send(h.send(tick)[0])
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
	for _, msg := range next {
		if !isWindowSizeRequest(msg) {
			h.run(msg)
		}
	}
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
		}, "token was rotated"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "ws")
			h.open()
			h.key("j")
			h.key("enter")
			h.exec.exits = []error{fakeExit(255)}
			ended := h.send(h.send(keyPress("enter"))[0])
			tc.mutate(h.client)
			for _, msg := range ended {
				if !isWindowSizeRequest(msg) {
					h.run(msg)
				}
			}
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
	if !h.m.help || !strings.Contains(h.m.View().Content, "enter attach") || !strings.Contains(h.m.View().Content, "1-9 attach by number") {
		t.Fatalf("help = %v view:\n%s", h.m.help, h.m.View().Content)
	}
	h.key("1")
	if h.m.help || h.m.selectedSpace != "spce-home" || len(h.exec.commands) != 0 {
		t.Error("closing key was also applied to the list")
	}
}

// Digits attach by row number on the terminals screen only (ATC-317).
func TestDigitAttachesByRowNumber(t *testing.T) {
	h := newHarness(t, "")
	h.open()
	// Inert on the spaces screen.
	h.key("1")
	if len(h.exec.commands) != 0 || h.m.screen != screenSpaces || h.m.selectedSpace != "spce-home" || h.m.message != "" {
		t.Fatalf("digit on spaces = exec %v screen %v selected %q message %q", h.exec.commands, h.m.screen, h.m.selectedSpace, h.m.message)
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
