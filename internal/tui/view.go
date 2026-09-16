package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/cli"
)

// Rendering is one rounded frame filling the terminal: a title bar naming
// the machine when there is one and the program otherwise, a breadcrumb
// that names the connection wherever a screen works on one, the current
// screen's body, a message line, and a footer of only the keys the screen
// does not otherwise hint at.
// The help overlay is the complete key reference and the only place the
// versions appear. Colour carries meaning through lipgloss styles, which
// the program's colour profile degrades for NO_COLOR and dumb terminals.
// The frame is drawn by hand because a lipgloss border cannot carry the
// title and the rules; the picker stays a launcher, with no layers.

const (
	// defaultWidth and defaultHeight stand in until the first
	// WindowSizeMsg.
	defaultWidth  = 80
	defaultHeight = 24
	// frameLines is what the frame takes beyond the body: the top
	// border, breadcrumb, two rules, message, footer, and bottom border.
	frameLines = 7
	// minWidth keeps the frame drawable: borders, their padding, and a
	// few columns of text. A terminal smaller than this or than the
	// frame plus one body line is drawn at the minimum and clipped.
	minWidth = 8
	// maxNameWidth caps the name column so a long name cannot push the
	// directory off the row.
	maxNameWidth = 24
	// flex marks the column that takes what the fixed ones leave.
	flex     = -1
	gap      = "  "
	ellipsis = "…"
)

// styles is the palette, picked for the terminal background. Chrome is
// dim, the selection is a muted background so its text stays readable,
// and status colours are ANSI-256 values chosen per background; the
// renderer's colour profile downsamples or drops them.
type styles struct {
	dim, good, bad, warn, selected, bold lipgloss.Style
}

func newStyles(dark bool) styles {
	pick := lipgloss.LightDark(dark)
	return styles{
		dim:      lipgloss.NewStyle().Faint(true),
		good:     lipgloss.NewStyle().Foreground(pick(lipgloss.Color("28"), lipgloss.Color("42"))),
		bad:      lipgloss.NewStyle().Foreground(pick(lipgloss.Color("160"), lipgloss.Color("203"))),
		warn:     lipgloss.NewStyle().Foreground(pick(lipgloss.Color("130"), lipgloss.Color("220"))),
		selected: lipgloss.NewStyle().Background(pick(lipgloss.Color("254"), lipgloss.Color("237"))),
		bold:     lipgloss.NewStyle().Bold(true),
	}
}

// layout is the frame's geometry for the terminal size: inner is the
// text columns between the borders and their padding, body the lines
// between the rules.
type layout struct {
	inner, body int
}

func (m model) layout() layout {
	width, height := m.width, m.height
	if width <= 0 {
		width = defaultWidth
	}
	if height <= 0 {
		height = defaultHeight
	}
	return layout{inner: max(width, minWidth) - 4, body: max(height, frameLines+1) - frameLines}
}

func (m model) View() tea.View {
	st := newStyles(m.dark)
	l := m.layout()
	f := frame{dim: st.dim, inner: l.inner}
	note := ""
	if m.anyLoading() {
		note = "loading…"
	}
	lines := make([]string, 0, l.body+frameLines)
	lines = append(lines, f.top(m.title(), note), f.line(m.breadcrumb(st)), f.rule())
	body := m.body(st, l)
	for i := range l.body {
		text := ""
		if i < len(body) {
			text = body[i]
		}
		lines = append(lines, f.line(text))
	}
	lines = append(lines, f.rule(), f.line(m.messageLine(st)), f.line(m.footer(st)), f.bottom())
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// frame draws the border. Every line is fitted to the inner width —
// truncated, never wrapped — so the right edge lines up.
type frame struct {
	dim   lipgloss.Style
	inner int
}

// top is the title bar: the title at the left, the note (a request in
// flight) at the right, the border filling between. The note is fitted
// first so a narrow terminal loses title before it loses the note.
func (f frame) top(title, note string) string {
	b := lipgloss.RoundedBorder()
	span := f.inner + 2 // between the corners
	if note != "" {
		note = ansi.Truncate(" "+note+" ", span, ellipsis)
	}
	title = ansi.Truncate(" "+title+" ", span-ansi.StringWidth(note), ellipsis)
	fill := span - ansi.StringWidth(title) - ansi.StringWidth(note)
	return f.dim.Render(b.TopLeft) + title + f.dim.Render(strings.Repeat(b.Top, fill)+note+b.TopRight)
}

func (f frame) rule() string {
	b := lipgloss.RoundedBorder()
	return f.dim.Render(b.MiddleLeft + strings.Repeat(b.Top, f.inner+2) + b.MiddleRight)
}

func (f frame) bottom() string {
	b := lipgloss.RoundedBorder()
	return f.dim.Render(b.BottomLeft + strings.Repeat(b.Bottom, f.inner+2) + b.BottomRight)
}

func (f frame) line(text string) string {
	b := lipgloss.RoundedBorder()
	return f.dim.Render(b.Left) + " " + fit(text, f.inner) + " " + f.dim.Render(b.Right)
}

// fit truncates text to width and pads it there.
func fit(text string, width int) string {
	text = ansi.Truncate(text, width, ellipsis)
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

// anyLoading reports a request in flight on the current screen or a
// Space load on any connection.
func (m model) anyLoading() bool {
	if m.loading {
		return true
	}
	for _, c := range m.connections {
		if c.loading {
			return true
		}
	}
	return false
}

func (m model) title() string {
	if len(m.connections) == 1 {
		return safeText(m.connections[0].name)
	}
	return "atc"
}

func (m model) breadcrumb(st styles) string {
	parent := st.dim.Render("spaces › ")
	switch {
	case m.help:
		return "help"
	case m.screen == screenTerminals:
		return parent + st.dim.Render(safeText(m.conn)+" › ") + safeText(m.space.Name) + gap + st.dim.Render(safeText(m.space.Directory))
	case m.screen == screenDirectories:
		return parent + st.dim.Render(safeText(m.conn)+" › ") + "new space" + gap + safeText(m.pathField()) + "▏"
	case m.screen == screenDestination:
		return parent + "new space" + gap + st.dim.Render("choose a connection")
	case m.screen == screenConnections:
		return "connections"
	case m.screen == screenAliases:
		return st.dim.Render("connections › ") + "add" + gap + st.dim.Render("ssh config aliases")
	}
	return "spaces" + m.connectionSummary(st)
}

// connectionSummary is the Spaces breadcrumb's account of every
// connection that is not ready, so a machine with nothing listed is not
// simply absent.
func (m model) connectionSummary(st styles) string {
	var parts []string
	for _, c := range m.connections {
		if c.status != connReady {
			parts = append(parts, safeText(c.name)+": "+c.statusLabel())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return gap + st.dim.Render(strings.Join(parts, " · "))
}

// pathField is the directory browser's field: the current directory
// with the filter typed after it, or the absolute path typed over it.
func (m model) pathField() string {
	switch {
	case strings.HasPrefix(m.dirInput, "/"):
		return m.dirInput
	case m.dir.Path == "/":
		return "/" + m.dirInput
	}
	return m.dir.Path + "/" + m.dirInput
}

func (m model) body(st styles, l layout) []string {
	switch {
	case m.help:
		return m.helpBody(st, l.body)
	case m.confirm != nil:
		return m.confirmBody(st, l)
	case m.reconnect != nil:
		return []string{fmt.Sprintf("connection lost; waiting for %s to answer again (next check in %s)", safeText(m.label(m.reconnect.terminal)), m.reconnect.delay)}
	}
	switch m.screen {
	case screenSpaces:
		return m.spacesBody(st, l)
	case screenTerminals:
		return m.terminalsBody(st, l)
	case screenDirectories:
		return m.directoriesBody(st, l)
	case screenDestination:
		return m.destinationBody(st, l)
	case screenConnections:
		return m.connectionsBody(st, l)
	case screenAliases:
		return m.aliasesBody(st, l)
	}
	return nil
}

// A list screen is a table: a dim header row, then the rows windowed to
// the body so the selected one is always on screen. Each cell is
// truncated to its column, so a row never wraps, and the flex column
// takes whatever the fixed ones leave.
type cell struct {
	text  string
	style lipgloss.Style
}

type table struct {
	st     styles
	widths []int
	inner  int
}

func newTable(st styles, inner int, widths ...int) table {
	fixed := len(gap) * (len(widths) - 1)
	for _, w := range widths {
		if w != flex {
			fixed += w
		}
	}
	for i, w := range widths {
		if w == flex {
			widths[i] = max(0, inner-fixed)
		}
	}
	return table{st: st, widths: widths, inner: inner}
}

func (t table) header(titles ...string) string {
	cells := make([]cell, len(titles))
	for i, title := range titles {
		cells[i] = cell{text: title, style: t.st.dim}
	}
	return t.row(cells, false)
}

// row lays cells out at their columns and fills the line to the inner
// width. A selected row is one background-coloured bar: every cell, gap,
// and pad carries the highlight beneath its own style.
func (t table) row(cells []cell, selected bool) string {
	base := lipgloss.NewStyle()
	if selected {
		base = t.st.selected
	}
	var b strings.Builder
	used := 0
	for i, c := range cells {
		if i > 0 {
			b.WriteString(styled(base, gap))
			used += len(gap)
		}
		text := ansi.Truncate(c.text, t.widths[i], ellipsis)
		b.WriteString(styled(c.style.Inherit(base), text))
		b.WriteString(styled(base, strings.Repeat(" ", max(0, t.widths[i]-ansi.StringWidth(text)))))
		used += t.widths[i]
	}
	b.WriteString(styled(base, strings.Repeat(" ", max(0, t.inner-used))))
	return b.String()
}

// styled is Render without the escape sequences an empty string earns.
func styled(style lipgloss.Style, text string) string {
	if text == "" {
		return ""
	}
	return style.Render(text)
}

// listBody is a header (when there is room for one above a row), then
// rows or the empty-state notice.
func listBody(st styles, height int, header string, rows []string, selected int, empty string) []string {
	var lines []string
	if height >= 2 {
		lines = append(lines, header)
	}
	if len(rows) == 0 {
		return append(lines, st.dim.Render(empty))
	}
	return append(lines, window(rows, selected, max(1, height-len(lines)))...)
}

// window is the slice of rows that fits, moved so the selection shows.
func window(rows []string, selected, visible int) []string {
	start := 0
	if selected >= visible {
		start = selected - visible + 1
	}
	return rows[start:min(len(rows), start+visible)]
}

// spacesBody is the one table of every connection's Spaces. A row whose
// connection is not ready is dimmed whole: still there, not usable.
func (m model) spacesBody(st styles, l layout) []string {
	rows := m.rows()
	connWidth, nameWidth, tagWidth := len("CONNECTION"), len("NAME"), 0
	for _, row := range rows {
		connWidth = max(connWidth, ansi.StringWidth(safeText(row.conn)))
		nameWidth = max(nameWidth, ansi.StringWidth(safeText(row.space.Name)))
		if row.space.IsDefault {
			tagWidth = len("default")
		}
	}
	t := newTable(st, l.inner, min(nameWidth, maxNameWidth), min(connWidth, maxNameWidth), len("TERMINALS"), flex, tagWidth)
	lines := make([]string, len(rows))
	selected := max(0, slices.Index(spaceRefs(rows), m.selectedSpace))
	for i, row := range rows {
		tag := ""
		if row.space.IsDefault {
			tag = "default"
		}
		style := lipgloss.NewStyle()
		if !row.ready {
			style = st.dim
		}
		lines[i] = t.row([]cell{
			{text: safeText(row.space.Name), style: style},
			{text: safeText(row.conn), style: style},
			{text: strconv.Itoa(m.connection(row.conn).counts[row.space.ID]), style: style},
			{text: safeText(row.space.Directory), style: style},
			{text: tag, style: st.dim},
		}, i == selected)
	}
	empty := "no spaces"
	if m.anyLoading() {
		empty = "loading spaces…"
	}
	return listBody(st, l.body, t.header("NAME", "CONNECTION", "TERMINALS", "DIRECTORY", ""), lines, selected, empty)
}

// connectionsBody is the table of connections with their state, then the
// selected one's full reason — a setup plan runs to several lines.
func (m model) connectionsBody(st styles, l layout) []string {
	nameWidth, statusWidth, serverWidth := len("NAME"), len("STATUS"), len("SERVER")
	for i := range m.connections {
		c := &m.connections[i]
		nameWidth = max(nameWidth, ansi.StringWidth(safeText(c.name)))
		statusWidth = max(statusWidth, ansi.StringWidth(c.statusLabel()))
		serverWidth = max(serverWidth, ansi.StringWidth(safeText(c.session.ServerVersion)))
	}
	t := newTable(st, l.inner, min(nameWidth, maxNameWidth), statusWidth, serverWidth, flex)
	rows := make([]string, len(m.connections))
	selected := max(0, slices.Index(m.connectionNames(), m.selectedConnection))
	var detail []string
	for i := range m.connections {
		c := &m.connections[i]
		rows[i] = t.row([]cell{
			{text: safeText(c.name)},
			{text: c.statusLabel(), style: connectionStyle(st, c)},
			{text: safeText(c.session.ServerVersion), style: st.dim},
			{text: safeText(firstLine(c.reason()))},
		}, i == selected)
		if i == selected && c.err != nil {
			for _, line := range strings.Split(strings.TrimRight(c.reason(), "\n"), "\n") {
				detail = append(detail, connectionStyle(st, c).Render(safeText(line)))
			}
		}
	}
	// The table keeps at least its header and the selected row; the
	// detail takes what is left below a blank line.
	height := l.body
	detail = detail[:min(len(detail), max(0, height-2))]
	if len(detail) > 0 {
		height -= len(detail) + 1
	}
	lines := listBody(st, height, t.header("NAME", "STATUS", "SERVER", "DETAIL"), rows, selected, "no connections")
	if len(detail) > 0 {
		for len(lines) < height {
			lines = append(lines, "")
		}
		lines = append(append(lines, ""), detail...)
	}
	return lines
}

func connectionStyle(st styles, c *connection) lipgloss.Style {
	switch c.status {
	case connReady:
		return st.good
	case connFailed:
		return st.bad
	case connUnavailable:
		return st.warn
	}
	return st.dim
}

// destinationBody chooses the connection a new Space is created on.
func (m model) destinationBody(st styles, l layout) []string {
	nameWidth := len("NAME")
	for _, c := range m.connections {
		nameWidth = max(nameWidth, ansi.StringWidth(safeText(c.name)))
	}
	t := newTable(st, l.inner, min(nameWidth, maxNameWidth), flex)
	rows := make([]string, len(m.connections))
	selected := max(0, slices.Index(m.connectionNames(), m.selectedConnection))
	for i := range m.connections {
		c := &m.connections[i]
		rows[i] = t.row([]cell{{text: safeText(c.name)}, {text: c.statusLabel(), style: connectionStyle(st, c)}}, i == selected)
	}
	return listBody(st, l.body, t.header("NAME", "STATUS"), rows, selected, "no connections")
}

func (m model) aliasesBody(st styles, l layout) []string {
	aliases := m.availableAliases()
	t := newTable(st, l.inner, flex)
	rows := make([]string, len(aliases))
	selected := max(0, slices.Index(aliases, m.selectedAlias))
	for i, alias := range aliases {
		rows[i] = t.row([]cell{{text: safeText(alias)}}, i == selected)
	}
	return listBody(st, l.body, t.header("HOST ALIAS"), rows, selected, "no further Host aliases in your ssh configuration")
}

func (m model) terminalsBody(st styles, l layout) []string {
	numberWidth, nameWidth, statusWidth := 1, len("NAME"), len("STATUS")
	for i, terminal := range m.terminals {
		numberWidth = max(numberWidth, len(strconv.Itoa(i+1)))
		nameWidth = max(nameWidth, ansi.StringWidth(safeText(cli.DisplayName(terminal))))
		statusWidth = max(statusWidth, len(statusLabel(terminal)))
	}
	t := newTable(st, l.inner, numberWidth, min(nameWidth, maxNameWidth), statusWidth, flex)
	rows := make([]string, len(m.terminals))
	selected := max(0, slices.Index(terminalIDs(m.terminals), m.selectedTerminal))
	for i, terminal := range m.terminals {
		rows[i] = t.row([]cell{
			{text: strconv.Itoa(i + 1)},
			{text: safeText(cli.DisplayName(terminal))},
			{text: statusLabel(terminal), style: statusStyle(st, terminal)},
			{text: safeText(terminal.Directory)},
		}, i == selected)
	}
	empty := "no terminals — press n to create a shell"
	if m.loading {
		empty = "loading terminals…"
	}
	return listBody(st, l.body, t.header("#", "NAME", "STATUS", "DIRECTORY"), rows, selected, empty)
}

func (m model) directoriesBody(st styles, l layout) []string {
	entries := m.filteredEntries()
	height := l.body
	var notice []string
	if m.dir.Truncated {
		notice = []string{st.dim.Render("(listing truncated at the server's cap)")}
		height--
	}
	if len(entries) == 0 {
		empty := "no subdirectories"
		switch {
		case m.loading:
			empty = "loading…"
		case m.dirInput != "" && !strings.HasPrefix(m.dirInput, "/"):
			empty = "no subdirectories match"
		}
		return append([]string{st.dim.Render(empty)}, notice...)
	}
	t := newTable(st, l.inner, flex)
	rows := make([]string, len(entries))
	selected := max(0, slices.Index(entryNames(entries), m.selectedDir))
	for i, entry := range entries {
		rows[i] = t.row([]cell{{text: safeText(entry.Name) + "/"}}, i == selected)
	}
	return append(window(rows, selected, max(1, height)), notice...)
}

func (m model) confirmBody(st styles, l layout) []string {
	c := m.confirm
	text := "delete terminal " + st.bold.Render(safeText(c.name)) + "?"
	switch c.kind {
	case "space":
		text = fmt.Sprintf("delete space %s and its %d %s on %s?", st.bold.Render(safeText(c.name)), c.count, plural(c.count, "terminal"), safeText(c.conn))
	case "connection":
		text = fmt.Sprintf("remove connection %s from this picker? (nothing on it is changed)", st.bold.Render(safeText(c.name)))
	}
	return strings.Split(lipgloss.Place(l.inner, l.body, lipgloss.Center, lipgloss.Center, text), "\n")
}

// The help overlay: every bound key, grouped by the screen that hears
// it, and the version pair last. It replaces the body and scrolls when
// the body is shorter. Kept by hand beside the key handlers in tui.go.
var helpKeys = []string{
	"everywhere",
	"  ?          help          ctrl+c    quit",
	"",
	"spaces",
	"  ↑/↓ j/k    move          enter     open",
	"  n          new space     d         delete",
	"  c          connections   r         refresh",
	"  q          quit",
	"",
	"terminals",
	"  ↑/↓ j/k    move          enter     attach",
	"  1-9        attach by #   ctrl-\\    detach",
	"  n          new shell     d         delete",
	"  esc h      back          r         refresh",
	"  q          quit",
	"",
	"new space",
	"  ↑/↓ j/k    move          enter     choose connection",
	"  type       filter, or an absolute path",
	"  ↑/↓        move          ctrl+p/n  move",
	"  enter      descend       backspace up",
	"  .          choose        ctrl+r    refresh",
	"  esc        clear the field, then back",
	"",
	"connections",
	"  ↑/↓ j/k    move          enter     connect, retry, or update",
	"  a          add           d         remove",
	"  r          retry all     esc h     back",
	"  q          quit",
	"",
	"add",
	"  ↑/↓ j/k    move          enter     add and set up",
	"  esc h      back",
	"",
	"confirm",
	"  y          confirm       n esc     cancel",
	"",
	"reconnecting",
	"  esc        cancel, back to terminals",
	"",
}

// helpLines is the key reference and then the versions: the client's,
// and each connected server's, coloured when it differs.
func (m model) helpLines(st styles) []string {
	lines := append([]string(nil), helpKeys...)
	lines = append(lines, st.dim.Render("client "+safeText(m.clientVersion)))
	for _, c := range m.connections {
		if c.session.ServerVersion == "" {
			continue
		}
		style := st.dim
		if c.session.ServerVersion != m.clientVersion {
			style = st.warn
		}
		lines = append(lines, style.Render(safeText(c.name)+" server "+safeText(c.session.ServerVersion)))
	}
	return lines
}

// helpScrollLimit is how far the help can scroll: zero when it fits.
func (m model) helpScrollLimit() int {
	return max(0, len(m.helpLines(styles{}))-m.layout().body)
}

func (m model) helpBody(st styles, height int) []string {
	lines := m.helpLines(st)
	start := min(m.helpScroll, m.helpScrollLimit())
	return lines[start:min(len(lines), start+height)]
}

// messageLine is the message, red for a failure; while the help
// overflows the body it is instead the scroll hint, so nothing is cut
// off silently.
func (m model) messageLine(st styles) string {
	if m.help && m.helpScrollLimit() > 0 {
		return st.dim.Render(fmt.Sprintf("↑/↓ j/k scroll · line %d of %d", min(m.helpScroll, m.helpScrollLimit())+1, len(m.helpLines(st))))
	}
	if m.message == "" {
		return ""
	}
	if m.failed {
		return st.bad.Render(safeText(m.message))
	}
	return safeText(m.message)
}

// The footer names only the keys the screen does not otherwise hint at;
// movement, enter, r, and the digits are left to the help overlay.
type binding struct{ key, verb string }

var (
	spacesFooter       = []binding{{"n", "new"}, {"d", "delete"}, {"c", "connections"}, {"?", "help"}, {"q", "quit"}}
	terminalsFooter    = []binding{{"n", "new"}, {"d", "delete"}, {"esc", "back"}, {"?", "help"}, {"q", "quit"}}
	directoriesFooter  = []binding{{".", "choose this directory"}, {"esc", "back"}, {"?", "help"}}
	destinationFooter  = []binding{{"esc", "back"}, {"?", "help"}}
	aliasesFooter      = []binding{{"esc", "back"}, {"?", "help"}}
	confirmFooter      = []binding{{"y", "confirm"}, {"n/esc", "cancel"}}
	reconnectingFooter = []binding{{"esc", "cancel"}}
	helpFooter         = []binding{{"any key", "to close"}}
)

// connectionsFooter names the action enter performs on the selected
// connection, when there is one.
func (m model) connectionsFooter() []binding {
	var keys []binding
	if c := m.connection(m.selectedConnection); c != nil && c.action() != "" {
		keys = append(keys, binding{"enter", c.action()})
	}
	if m.save != nil {
		keys = append(keys, binding{"a", "add"}, binding{"d", "remove"})
	}
	return append(keys, binding{"esc", "back"}, binding{"?", "help"}, binding{"q", "quit"})
}

func (m model) footer(st styles) string {
	var keys []binding
	switch {
	case m.help:
		keys = helpFooter
	case m.confirm != nil:
		keys = confirmFooter
	case m.reconnect != nil:
		keys = reconnectingFooter
	case m.screen == screenSpaces:
		keys = spacesFooter
	case m.screen == screenTerminals:
		keys = terminalsFooter
	case m.screen == screenDirectories:
		keys = directoriesFooter
	case m.screen == screenDestination:
		keys = destinationFooter
	case m.screen == screenConnections:
		keys = m.connectionsFooter()
	case m.screen == screenAliases:
		keys = aliasesFooter
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.key + " " + st.dim.Render(k.verb)
	}
	return " " + strings.Join(parts, "   ")
}

func statusLabel(terminal api.Terminal) string {
	if terminal.Status == api.TerminalExited && terminal.ExitCode != nil {
		return fmt.Sprintf("exited (%d)", *terminal.ExitCode)
	}
	return safeText(string(terminal.Status))
}

func statusStyle(st styles, terminal api.Terminal) lipgloss.Style {
	switch terminal.Status {
	case api.TerminalRunning:
		return st.good
	case api.TerminalExited:
		return st.bad
	case api.TerminalUnreachable, api.TerminalMissing:
		return st.warn
	}
	return lipgloss.NewStyle()
}

func plural(count int, noun string) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}

// safeText keeps server-supplied names from carrying control sequences
// into the renderer.
func safeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, value)
}
