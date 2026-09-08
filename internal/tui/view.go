package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/jeremytondo/atc/internal/api"
)

// Rendering is plain text: a status line, the current screen's rows with
// the selection marked, the current message, and the key hints. Rows
// scroll so the selection stays visible. No styling library — the picker
// is a launcher, not a dashboard.

const (
	marker   = "> "
	noMarker = "  "
	// chromeLines is what the status line, message, and hints occupy.
	chromeLines = 5
)

func (m model) View() tea.View {
	var b strings.Builder
	b.WriteString(m.statusLine())
	b.WriteString("\n\n")
	switch {
	case m.help:
		b.WriteString(helpText())
	case m.confirm != nil:
		b.WriteString(m.confirmText())
	case m.reconnect != nil:
		b.WriteString(m.reconnectingView())
	default:
		switch m.screen {
		case screenSpaces:
			b.WriteString(m.spacesView())
		case screenTerminals:
			b.WriteString(m.terminalsView())
		case screenDirectories:
			b.WriteString(m.directoriesView())
		}
	}
	b.WriteString("\n")
	if m.message != "" {
		b.WriteString(safeText(m.message))
	}
	b.WriteString("\n")
	b.WriteString(m.hints())
	view := tea.NewView(b.String())
	view.AltScreen = true
	return view
}

func (m model) statusLine() string {
	target := "local"
	if m.target != "" {
		target = safeText(m.target)
	}
	versions := fmt.Sprintf("client %s · server %s", m.clientVersion, m.serverVersion)
	if m.target != "" {
		versions = fmt.Sprintf("local %s · remote %s", m.clientVersion, m.serverVersion)
	}
	line := fmt.Sprintf("atc  target: %s  %s", target, versions)
	if m.clientVersion != m.serverVersion {
		line += "  (version mismatch)"
	}
	if m.loading {
		line += "  loading…"
	}
	return line
}

func (m model) spacesView() string {
	if len(m.spaces) == 0 {
		if m.loading {
			return "loading spaces…\n"
		}
		return "no spaces\n"
	}
	rows := make([]string, len(m.spaces))
	selected := 0
	for i, space := range m.spaces {
		if space.ID == m.selectedSpace {
			selected = i
		}
		name := space.Name
		if space.IsDefault {
			name += " (default)"
		}
		count := m.terminalCounts[space.ID]
		rows[i] = fmt.Sprintf("%-24s %2d %s  %s", safeText(name), count, plural(count, "terminal"), safeText(space.Directory))
	}
	return m.rowsView(rows, selected)
}

func (m model) terminalsView() string {
	header := fmt.Sprintf("space %s  %s\n\n", safeText(m.space.Name), safeText(m.space.Directory))
	if len(m.terminals) == 0 {
		if m.loading {
			return header + "loading terminals…\n"
		}
		return header + "no terminals — press n to create a shell\n"
	}
	rows := make([]string, len(m.terminals))
	selected := 0
	for i, terminal := range m.terminals {
		if terminal.ID == m.selectedTerminal {
			selected = i
		}
		rows[i] = fmt.Sprintf("%-24s %s", safeText(terminal.Name), statusLabel(terminal))
	}
	return header + m.rowsView(rows, selected)
}

func (m model) directoriesView() string {
	var field string
	switch {
	case strings.HasPrefix(m.dirInput, "/"):
		field = m.dirInput
	case m.dir.Path == "/":
		field = "/" + m.dirInput
	default:
		field = m.dir.Path + "/" + m.dirInput
	}
	header := "create space in: " + safeText(field) + "▏\n\n"
	entries := m.filteredEntries()
	if len(entries) == 0 {
		if m.loading {
			return header + "loading…\n"
		}
		if m.dirInput != "" && !strings.HasPrefix(m.dirInput, "/") {
			return header + "no subdirectories match\n"
		}
		return header + "no subdirectories\n"
	}
	rows := make([]string, len(entries))
	selected := 0
	for i, entry := range entries {
		if entry.Name == m.selectedDir {
			selected = i
		}
		rows[i] = safeText(entry.Name) + "/"
	}
	body := header + m.rowsView(rows, selected)
	if m.dir.Truncated {
		body += "(listing truncated at the server's cap)\n"
	}
	return body
}

func (m model) reconnectingView() string {
	return fmt.Sprintf("connection lost; waiting for %s to answer again (next check in %s)\n", safeText(m.reconnect.terminal.Name), m.reconnect.delay)
}

func (m model) confirmText() string {
	c := m.confirm
	if c.kind == "space" {
		return fmt.Sprintf("delete space %s and its %d %s?  y / n\n", safeText(c.name), c.count, plural(c.count, "terminal"))
	}
	return fmt.Sprintf("delete terminal %s?  y / n\n", safeText(c.name))
}

// rowsView renders rows with the selection marked, windowed to the
// available height so the selected row is always on screen.
func (m model) rowsView(rows []string, selected int) string {
	visible := len(rows)
	if m.height > 0 {
		visible = max(1, m.height-chromeLines-2)
	}
	start := 0
	if selected >= visible {
		start = selected - visible + 1
	}
	end := min(len(rows), start+visible)
	var b strings.Builder
	for i := start; i < end; i++ {
		if i == selected {
			b.WriteString(marker)
		} else {
			b.WriteString(noMarker)
		}
		b.WriteString(rows[i])
		b.WriteString("\n")
	}
	return b.String()
}

// The key map, written once: the footer shows the current screen's line
// and the help overlay shows them all.
const (
	spacesKeys       = "j/k move  enter open  n new space  d delete  r refresh  ? help  q quit"
	terminalsKeys    = "j/k move  enter attach (ctrl-\\ detaches)  n new shell  d delete  esc/h back  r refresh  ? help  q quit"
	directoriesKeys  = "type to filter, or /an/absolute/path  ↑/↓ move  enter descend  backspace up  . choose this directory  esc back  ctrl+r refresh"
	reconnectingKeys = "esc cancel and return to the terminal list"
)

func (m model) hints() string {
	switch {
	case m.help:
		return "any key to close"
	case m.confirm != nil:
		return "y confirm  n/esc cancel"
	case m.reconnect != nil:
		return reconnectingKeys
	}
	switch m.screen {
	case screenSpaces:
		return spacesKeys
	case screenTerminals:
		return terminalsKeys
	case screenDirectories:
		return directoriesKeys
	}
	return ""
}

func helpText() string {
	return "keys\n\n" +
		"  spaces        " + spacesKeys + "\n" +
		"  terminals     " + terminalsKeys + "\n" +
		"  new space     " + directoriesKeys + "\n" +
		"  reconnecting  " + reconnectingKeys + "\n"
}

func statusLabel(terminal api.Terminal) string {
	if terminal.Status == api.TerminalExited && terminal.ExitCode != nil {
		return fmt.Sprintf("exited (%d)", *terminal.ExitCode)
	}
	return string(terminal.Status)
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
