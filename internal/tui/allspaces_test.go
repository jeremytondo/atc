package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// All Spaces and search (ATC-329). Every connection in the multi harness
// serves the same Space and Terminal IDs, so each test here is also a
// test that a row's connection, not its ID, decides what an action
// reaches.

var compareRefs = cmp.AllowUnexported(terminalRef{}, spaceRef{})

func resultRefs(m model) []terminalRef { return terminalRefs(m.results()) }

// selectedLine is the styled view line carrying the selection.
func selectedLine(m model) string {
	for _, line := range strings.Split(m.View().Content, "\n") {
		if highlighted(line) {
			return line
		}
	}
	return ""
}

// perConnection is one connection's rows in All Spaces order: its Spaces
// as the Spaces table lists them (Default, which is empty, then play,
// then work), each Space's Terminals oldest first.
func perConnection(conn string) []terminalRef {
	return []terminalRef{{conn, "term-play"}, {conn, "term-old"}, {conn, "term-dead"}, {conn, "term-new"}}
}

func TestAllSpacesListsEveryConnectionAndRoutesByOwner(t *testing.T) {
	h := newMultiHarness(t, "ws", "devbox")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.key("enter") // All Spaces is where the picker opens
	if h.m.screen != screenTerminals || !h.m.allSpaces {
		t.Fatalf("enter = screen %v all %v", h.m.screen, h.m.allSpaces)
	}
	want := append(append(perConnection("Local"), perConnection("ws")...), perConnection("devbox")...)
	if diff := cmp.Diff(want, resultRefs(h.m), compareRefs); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if h.m.selectedTerminal != (terminalRef{"Local", "term-play"}) {
		t.Errorf("row one preselected: %v", h.m.selectedTerminal)
	}
	view := plain(h.m)
	for _, row := range []string{
		`#\s+NAME\s+SPACE\s+CONNECTION\s+STATUS\s+DIRECTORY`,
		`1\s+play\s+play\s+Local\s+running\s+/home/u/play`,
		`3\s+nvim\s+work\s+Local\s+exited \(1\)\s+/home/u/work`,
		`8\s+api\s+work\s+ws\s+running\s+/home/u/work/api`,
		`12\s+api\s+work\s+devbox\s+running`,
	} {
		if !matches(row, view) {
			t.Errorf("view lacks %q:\n%s", row, view)
		}
	}

	// Row five is ws's term-play, the ID row one has on Local: enter, the
	// digits, and delete all reach ws.
	for range 4 {
		h.key("j")
	}
	h.exec.exits = []error{nil, nil, nil}
	h.key("enter")
	h.key("6")
	h.key("0") // row ten: devbox's term-old
	if h.m.selectedTerminal != (terminalRef{"devbox", "term-old"}) {
		t.Errorf("after 0: selected %v", h.m.selectedTerminal)
	}
	attached := map[string][]string{}
	for name, c := range h.connectors {
		attached[name] = c.attached
	}
	if diff := cmp.Diff(map[string][]string{"Local": nil, "ws": {"term-play", "term-old"}, "devbox": {"term-old"}}, attached); diff != "" {
		t.Errorf("attachments by connection (-want +got):\n%s", diff)
	}
	h.key("d")
	if want := (&confirmation{kind: "terminal", conn: "devbox", id: "term-old", name: "10:zsh"}); !cmp.Equal(want, h.m.confirm, cmp.AllowUnexported(confirmation{})) {
		t.Fatalf("confirmation = %+v", h.m.confirm)
	}
	h.key("y")
	if diff := cmp.Diff([]string{"terminal:term-old"}, h.connectors["devbox"].client.deleted); diff != "" {
		t.Errorf("devbox deletions (-want +got):\n%s", diff)
	}
	if len(h.connectors["Local"].client.deleted)+len(h.connectors["ws"].client.deleted) != 0 {
		t.Error("a delete reached a connection that does not own the row")
	}
	// The same refusals as a Space's list: by status, and past the end.
	h.key("3")
	if !strings.Contains(h.m.message, "3:nvim has exited with code 1") || len(h.exec.commands) != 3 {
		t.Errorf("3 = message %q exec %v", h.m.message, h.exec.commands)
	}
	h.client.terminals = h.client.terminals[:1]
	h.key("r")
	h.key("0")
	if h.m.message != "no terminal 10" || len(h.exec.commands) != 3 {
		t.Errorf("0 with nine rows = message %q exec %v", h.m.message, h.exec.commands)
	}
	h.key("esc")
	if h.m.screen != screenSpaces || h.m.selectedSpace != allSpaces {
		t.Errorf("esc = screen %v selected %v", h.m.screen, h.m.selectedSpace)
	}
}

// One connection that needs a person, and one that stops answering,
// leave the others usable: the first has no rows, the second keeps its
// last-known rows dimmed and refuses every action on them.
func TestAllSpacesMixedAvailability(t *testing.T) {
	h := newMultiHarness(t, "ws", "devbox")
	h.connectors["devbox"].connectErr = errors.New("ssh: connect to host devbox: timed out")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.key("enter")
	if diff := cmp.Diff(append(perConnection("Local"), perConnection("ws")...), resultRefs(h.m), compareRefs); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if lines(h.m)[1] != "spaces › all spaces  devbox: failed" {
		t.Errorf("breadcrumb %q", lines(h.m)[1])
	}
	ws := h.connectors["ws"]
	ws.client.err = errors.New("dial tcp: no route to host")
	h.key("r")
	if c := h.conn("ws"); c.status != connUnavailable || len(resultRefs(h.m)) != 8 {
		t.Fatalf("after the failed load: status %v rows %d", c.status, len(resultRefs(h.m)))
	}
	st := newStyles(true)
	const green = "38;5;42" // the running status's colour, under any background
	if line := rawLine(h.m, "/home/u/work/api"); !strings.Contains(line, st.good.Render("running")) {
		t.Errorf("Local row dimmed: %q", line)
	}
	for range 4 {
		h.key("j") // ws's term-play
	}
	if line := selectedLine(h.m); !matches(`5\s+play\s+play\s+ws\s+running`, ansi.Strip(line)) || strings.Contains(line, green) {
		t.Errorf("ws row not dimmed: %q", line)
	}
	h.key("enter")
	if !strings.Contains(h.m.message, "ws is unavailable") || len(h.exec.commands) != 0 {
		t.Errorf("enter on a dimmed row = message %q exec %v", h.m.message, h.exec.commands)
	}
	h.key("d")
	if h.m.confirm != nil || !strings.Contains(h.m.message, "ws is unavailable") {
		t.Errorf("d on a dimmed row = confirm %v message %q", h.m.confirm, h.m.message)
	}
	h.key("n")
	h.key("enter") // the chooser opens on the row's Space, which ws cannot serve
	if h.m.screen != screenSpaceChooser || !strings.Contains(h.m.message, "ws is unavailable") || len(ws.client.createdTerm) != 0 {
		t.Errorf("unavailable destination = screen %v message %q created %v", h.m.screen, h.m.message, ws.client.createdTerm)
	}
	h.key("esc")
	h.exec.exits = []error{nil}
	h.key("1")
	if diff := cmp.Diff([]string{"term-play"}, h.connectors["Local"].attached); diff != "" {
		t.Errorf("Local attachments (-want +got):\n%s", diff)
	}
	// Recovery is the connection's own: its rows come back usable.
	ws.client.err = nil
	h.fire()
	h.key("r") // Local's attachment moved the selection; find ws's row again
	for range 4 {
		h.key("j")
	}
	if line := selectedLine(h.m); h.conn("ws").status != connReady || !matches(`5\s+play\s+play\s+ws`, ansi.Strip(line)) || !strings.Contains(line, green) {
		t.Errorf("after recovery: status %v row %q", h.conn("ws").status, line)
	}
}

// The background read and the stale-answer protection are each
// connection's own: a poll changes the list without a loading label and
// keeps the selection by identity, a slow connection is not asked twice,
// and an answer from a superseded load or a removed connection restores
// nothing.
func TestAllSpacesRefreshAndLateAnswers(t *testing.T) {
	h := newMultiHarness(t, "ws")
	h.open()
	h.key("enter")
	for range 5 {
		h.key("j") // ws's term-old
	}
	ws := h.connectors["ws"]
	ws.client.terminals = append([]api.Terminal{{ID: "term-first", Process: "top", SpaceID: "spce-play", Status: api.TerminalRunning, CreatedAt: t0.Add(-time.Hour)}}, ws.client.terminals...)
	polls := h.send(refreshTickMsg{})
	if len(polls) != 2 || h.m.anyLoading() {
		t.Fatalf("tick = %d reads, loading %v", len(polls), h.m.anyLoading())
	}
	if again := h.send(refreshTickMsg{}); len(again) != 0 {
		t.Errorf("connections still answering were polled again: %v", again)
	}
	for _, msg := range polls {
		h.run(msg)
	}
	if got := resultRefs(h.m); len(got) != 9 || got[4] != (terminalRef{"ws", "term-first"}) || h.m.selectedTerminal != (terminalRef{"ws", "term-old"}) {
		t.Errorf("after the poll: rows %v selected %v", got, h.m.selectedTerminal)
	}
	// A failed poll is quiet, and marks only its own connection.
	ws.client.err = errors.New("dial tcp: no route to host")
	h.run(refreshTickMsg{})
	if h.conn("ws").status != connUnavailable || h.conn("Local").status != connReady || h.m.message != "" || len(resultRefs(h.m)) != 9 {
		t.Errorf("failed poll = ws %v Local %v message %q rows %d", h.conn("ws").status, h.conn("Local").status, h.m.message, len(resultRefs(h.m)))
	}
	ws.client.err = nil
	h.fire()

	// A load superseded by a newer one is dropped.
	stale := h.send(keyPress("r"))
	h.key("r")
	for _, msg := range stale {
		loaded := msg.(spacesLoadedMsg)
		loaded.terminals = nil
		h.run(loaded)
	}
	if len(resultRefs(h.m)) != 9 {
		t.Errorf("a superseded load replaced the rows: %v", resultRefs(h.m))
	}
	// So is one that answers after its connection was removed.
	late := h.send(keyPress("r"))
	h.key("esc")
	h.key("c")
	h.key("j")
	h.key("d")
	h.key("y")
	for _, msg := range late {
		h.run(msg)
	}
	h.key("esc")
	h.key("enter")
	if diff := cmp.Diff(perConnection("Local"), resultRefs(h.m), compareRefs); diff != "" {
		t.Errorf("rows after removing ws (-want +got):\n%s", diff)
	}
}

// n in All Spaces asks which Space: the Spaces table's rows in its order,
// opened on the selected Terminal's Space without moving it. The shell is
// created on the chosen Space's connection and attached at once, and the
// return is to All Spaces with the new Terminal selected.
func TestAllSpacesCreateChoosesSpace(t *testing.T) {
	h := newMultiHarness(t, "ws")
	h.open()
	h.run(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.key("enter")
	for range 5 {
		h.key("j") // ws's term-old, in work
	}
	h.key("n")
	if h.m.screen != screenSpaceChooser || h.m.chosenSpace != (spaceRef{"ws", "spce-work"}) {
		t.Fatalf("n = screen %v chosen %v", h.m.screen, h.m.chosenSpace)
	}
	view := lines(h.m)
	want := []string{"NAME     CONNECTION", "Default  Local", "play     Local", "work     Local", "Default  ws", "play     ws", "work     ws"}
	if diff := cmp.Diff(want, view[3:10]); diff != "" {
		t.Errorf("chooser (-want +got):\n%s", diff)
	}
	if !highlighted(rawLine(h.m, "work     ws")) {
		t.Errorf("the selected Terminal's Space not highlighted:\n%s", h.m.View().Content)
	}
	h.key("esc")
	if h.m.screen != screenTerminals || !h.m.allSpaces || len(h.connectors["ws"].client.createdTerm) != 0 {
		t.Fatalf("esc = screen %v all %v", h.m.screen, h.m.allSpaces)
	}
	h.key("n")
	h.key("k") // ws's play
	h.exec.exits = []error{nil}
	h.key("enter")
	ws, local := h.connectors["ws"], h.connectors["Local"]
	if diff := cmp.Diff([]api.TerminalCreateParams{{SpaceID: "spce-play"}}, ws.client.createdTerm); diff != "" {
		t.Errorf("ws creates (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"term-new01"}, ws.attached); diff != "" {
		t.Errorf("ws attachments (-want +got):\n%s", diff)
	}
	if len(local.client.createdTerm) != 0 || len(local.attached) != 0 {
		t.Errorf("Local saw create %v attach %v", local.client.createdTerm, local.attached)
	}
	if h.m.screen != screenTerminals || !h.m.allSpaces || h.m.selectedTerminal != (terminalRef{"ws", "term-new01"}) {
		t.Errorf("return = screen %v all %v selected %v", h.m.screen, h.m.allSpaces, h.m.selectedTerminal)
	}
	// The new shell is the newest of ws's play: row six, after term-play.
	if got := resultRefs(h.m); len(got) != 9 || got[5] != (terminalRef{"ws", "term-new01"}) {
		t.Errorf("rows after create: %v", got)
	}
	// A Space's own list still creates in place.
	h.key("esc")
	h.key("j")
	h.key("j")
	h.key("enter") // Local's play
	h.exec.exits = []error{nil}
	h.key("n")
	if diff := cmp.Diff([]api.TerminalCreateParams{{SpaceID: "spce-play"}}, local.client.createdTerm); diff != "" || h.m.screen != screenTerminals || h.m.allSpaces {
		t.Errorf("n in a Space = screen %v all %v creates (-want +got):\n%s", h.m.screen, h.m.allSpaces, diff)
	}
}

// searchFixture is Local and a remote named workstation, with names
// chosen so ranking and cross-field matching are visible: on workstation,
// `app-installer` is older than `api`, and play holds a `codex`.
func searchFixture(t *testing.T) *harness {
	t.Helper()
	h := newMultiHarness(t, "workstation")
	h.connectors["workstation"].client.terminals = []api.Terminal{
		{ID: "term-inst", Name: "app-installer", SpaceID: "spce-work", Status: api.TerminalRunning, CreatedAt: t0},
		{ID: "term-api", Name: "api", SpaceID: "spce-work", Status: api.TerminalRunning, CreatedAt: t0.Add(time.Minute)},
		{ID: "term-gone", Name: "apiary", SpaceID: "spce-work", Status: api.TerminalExited, ExitCode: &exitOne, CreatedAt: t0.Add(2 * time.Minute)},
		{ID: "term-codex", Process: "codex", SpaceID: "spce-play", Status: api.TerminalRunning, CreatedAt: t0},
	}
	h.open()
	h.run(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

func (h *harness) typeText(text string) {
	h.t.Helper()
	for _, r := range text {
		h.key(string(r))
	}
}

func TestSearchAllSpaces(t *testing.T) {
	h := searchFixture(t)
	h.key("enter")
	h.key("/")
	// Every printable key is query text: no quit, create, delete, or
	// attach by number.
	h.typeText("qnd01")
	if h.m.query != "qnd01" || h.m.screen != screenTerminals || h.m.confirm != nil || len(h.exec.commands) != 0 || len(h.client.createdTerm) != 0 {
		t.Fatalf("typed keys acted on the list: query %q screen %v confirm %v exec %v", h.m.query, h.m.screen, h.m.confirm, h.exec.commands)
	}
	if len(resultRefs(h.m)) != 0 || !strings.Contains(plain(h.m), "no terminals match") {
		t.Errorf("no matches:\n%s", plain(h.m))
	}
	h.key("enter")
	if len(h.exec.commands) != 0 {
		t.Errorf("enter with no matches attached %v", h.exec.commands)
	}
	for range 5 {
		h.key("backspace")
	}
	h.key("backspace") // an empty field stays in search mode
	if !h.m.searching || len(resultRefs(h.m)) != 8 {
		t.Fatalf("cleared field = searching %v rows %d", h.m.searching, len(resultRefs(h.m)))
	}

	// Terms match across fields in any order, case ignored and characters
	// skipped: the connection by `WORK`, the Terminal by `cdx`.
	h.typeText("cdx WORK")
	if diff := cmp.Diff([]terminalRef{{"workstation", "term-codex"}}, resultRefs(h.m), compareRefs); diff != "" {
		t.Errorf("`cdx WORK` (-want +got):\n%s", diff)
	}
	if !matches(`1\s+codex\s+play\s+workstation`, plain(h.m)) {
		t.Errorf("the match is not result one:\n%s", plain(h.m))
	}
	for range 8 {
		h.key("backspace")
	}
	// Best first: the exact name, then the longer one that starts with
	// it, then the scattered letters — whatever their normal order — and
	// numbered as listed. Local's own `api` ties with workstation's and
	// keeps its place ahead of it.
	h.typeText("api")
	want := []terminalRef{{"Local", "term-new"}, {"workstation", "term-api"}, {"workstation", "term-gone"}, {"workstation", "term-inst"}}
	if diff := cmp.Diff(want, resultRefs(h.m), compareRefs); diff != "" {
		t.Errorf("`api` (-want +got):\n%s", diff)
	}
	if view := plain(h.m); !matches(`2\s+api\s+work\s+workstation`, view) || !matches(`4\s+app-installer`, view) {
		t.Errorf("results not renumbered:\n%s", view)
	}
	if h.m.selectedTerminal != want[0] {
		t.Errorf("best match not selected: %v", h.m.selectedTerminal)
	}
	h.key("ctrl+n")
	h.key("ctrl+n")
	h.key("ctrl+n")
	h.key("ctrl+n") // clamps
	h.key("ctrl+p")
	if h.m.selectedTerminal != want[2] {
		t.Errorf("after ctrl+n ×4, ctrl+p: %v", h.m.selectedTerminal)
	}
	// Enter is subject to the same checks: an exited Terminal is refused
	// and the search goes on.
	h.key("enter")
	if !strings.Contains(h.m.message, "3:apiary has exited with code 1") || !h.m.searching || len(h.exec.commands) != 0 {
		t.Errorf("enter on an exited match = message %q searching %v exec %v", h.m.message, h.m.searching, h.exec.commands)
	}
	h.key("ctrl+p")
	h.exec.exits = []error{nil}
	h.key("enter")
	if diff := cmp.Diff([]string{"term-api"}, h.connectors["workstation"].attached); diff != "" || len(h.connectors["Local"].attached) != 0 {
		t.Errorf("workstation attachments (-want +got):\n%s", diff)
	}
	// The return is to All Spaces in normal mode, the query gone, the
	// attached Terminal selected in the full list.
	if h.m.screen != screenTerminals || !h.m.allSpaces || h.m.searching || h.m.query != "" || h.m.selectedTerminal != want[1] || len(resultRefs(h.m)) != 8 {
		t.Errorf("return = screen %v all %v searching %v query %q selected %v rows %d", h.m.screen, h.m.allSpaces, h.m.searching, h.m.query, h.m.selectedTerminal, len(resultRefs(h.m)))
	}
	// Esc leaves the search, keeping the list and the selected match;
	// the next esc leaves the list.
	h.key("/")
	h.typeText("codex")
	h.key("esc")
	if h.m.screen != screenTerminals || h.m.searching || h.m.query != "" || h.m.selectedTerminal != (terminalRef{"workstation", "term-codex"}) || len(resultRefs(h.m)) != 8 {
		t.Errorf("esc = screen %v searching %v query %q selected %v", h.m.screen, h.m.searching, h.m.query, h.m.selectedTerminal)
	}
	h.key("esc")
	if h.m.screen != screenSpaces {
		t.Errorf("second esc = screen %v", h.m.screen)
	}
}

// A Space's own list searches and numbers the same way, and 0 is its
// tenth row.
func TestSearchAndTenthRowInOneSpace(t *testing.T) {
	h := newHarness(t, "")
	for i := range 8 {
		h.client.terminals = append(h.client.terminals, api.Terminal{ID: "term-more" + string(rune('a'+i)), Process: "htop", SpaceID: "spce-work", Status: api.TerminalRunning, CreatedAt: t0.Add(time.Duration(i+2) * time.Hour)})
	}
	h.open()
	for range 3 {
		h.key("j")
	}
	h.key("enter") // work: zsh, nvim, api, then eight htops
	h.exec.exits = []error{nil}
	h.key("0")
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-moreg"}}, h.exec.commands); diff != "" {
		t.Errorf("0 (-want +got):\n%s", diff)
	}
	h.key("/")
	h.key("0")
	if h.m.query != "0" || len(h.exec.commands) != 1 {
		t.Errorf("0 while searching = query %q exec %v", h.m.query, h.exec.commands)
	}
	h.key("backspace")
	h.typeText("nv")
	if diff := cmp.Diff([]string{"term-dead"}, resultIDs(h.m)); diff != "" {
		t.Errorf("`nv` (-want +got):\n%s", diff)
	}
	if !matches(`1\s+nvim\s+exited`, plain(h.m)) {
		t.Errorf("the match is not result one:\n%s", plain(h.m))
	}
	h.key("backspace")
	h.key("backspace")
	// The Space and connection names match here too, as in All Spaces.
	h.typeText("local api")
	if diff := cmp.Diff([]string{"term-new"}, resultIDs(h.m)); diff != "" {
		t.Errorf("`local api` (-want +got):\n%s", diff)
	}
	h.exec.exits = []error{nil}
	h.key("enter")
	if diff := cmp.Diff([][]string{{"/bin/attach", "term-moreg"}, {"/bin/attach", "term-new"}}, h.exec.commands); diff != "" {
		t.Errorf("enter (-want +got):\n%s", diff)
	}
	if h.m.searching || h.m.query != "" || h.m.allSpaces || h.m.selectedTerminal.id != "term-new" || len(resultIDs(h.m)) != 11 {
		t.Errorf("return = searching %v query %q all %v selected %v rows %d", h.m.searching, h.m.query, h.m.allSpaces, h.m.selectedTerminal, len(resultIDs(h.m)))
	}
	// A refresh during a search keeps the selected match by identity.
	h.key("/")
	h.typeText("htop")
	h.key("ctrl+n")
	selected := h.m.selectedTerminal
	h.client.terminals = h.client.terminals[:len(h.client.terminals)-1]
	h.run(refreshTickMsg{})
	if h.m.selectedTerminal != selected || len(resultIDs(h.m)) != 7 {
		t.Errorf("after a poll: selected %v (was %v) rows %d", h.m.selectedTerminal, selected, len(resultIDs(h.m)))
	}
}

// A transport loss from All Spaces retries the same Terminal on the same
// connection, whatever the list was filtered to.
func TestAllSpacesTransportLossReconnectsOnOwner(t *testing.T) {
	h := searchFixture(t)
	h.key("enter")
	h.key("/")
	h.typeText("codex")
	ws := h.connectors["workstation"]
	h.exec.exits = []error{fakeExit(255), nil}
	ended := h.send(keyPress("enter"))
	ws.client.err = errors.New("dial tcp: no route to host")
	h.send(ended[0])
	if h.m.reconnect == nil || h.m.reconnect.conn != "workstation" || !strings.Contains(h.m.message, "reconnecting to 5:codex") {
		t.Fatalf("loss = reconnect %+v message %q", h.m.reconnect, h.m.message)
	}
	h.fire() // unreachable
	ws.client.err = nil
	h.fire() // answers: re-attached
	if diff := cmp.Diff([]string{"term-codex", "term-codex"}, ws.attached); diff != "" || ws.client.polls != 2 || h.client.polls != 0 {
		t.Errorf("re-attach: polls ws %d Local %d (-want +got):\n%s", ws.client.polls, h.client.polls, diff)
	}
	if h.m.reconnect != nil || !h.m.allSpaces || h.m.searching {
		t.Errorf("after re-attach = reconnect %v all %v searching %v", h.m.reconnect, h.m.allSpaces, h.m.searching)
	}
}
