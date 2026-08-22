package portal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"
)

const (
	keyCtrlC = 3
	keyCtrlN = 14
	keyCtrlR = 18
	keyCtrlU = 21
)

type tuiBackend interface {
	Location() string
	ListSessions() ([]string, error)
	Attach(name string) error
}

type hostedBackend struct {
	app *App
}

func (b hostedBackend) Location() string {
	return "private namespace: " + b.app.zmxDir
}

func (b hostedBackend) ListSessions() ([]string, error) {
	return b.app.selectableSessions()
}

func (b hostedBackend) Attach(name string) error {
	return b.app.switchTo(name)
}

func (a *App) tui() error {
	if os.Getenv("ZMX_SESSION") != managerSession {
		return fmt.Errorf("tui is internal and must run inside zmx session %q", managerSession)
	}
	return a.runTUI(hostedBackend{app: a}, a.markCleanManagerExit)
}

func (a *App) markCleanManagerExit() error {
	if err := os.WriteFile(a.managerExitMarker(), []byte("clean\n"), 0o600); err != nil {
		return fmt.Errorf("write manager exit marker: %w", err)
	}
	return nil
}

func (a *App) runTUI(backend tuiBackend, onQuit func() error) error {
	if !term.IsTerminal(int(a.in.Fd())) || !term.IsTerminal(int(a.out.Fd())) {
		return errors.New("tui needs an interactive terminal")
	}

	sessions, err := backend.ListSessions()
	if err != nil {
		return err
	}

	state, err := term.MakeRaw(int(a.in.Fd()))
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	raw := true
	restore := func() {
		if raw {
			_ = term.Restore(int(a.in.Fd()), state)
			raw = false
		}
	}
	defer func() {
		restore()
		fmt.Fprint(a.out, "\x1b[?25h\x1b[0m\r\n")
	}()

	query := ""
	selected := 0
	status := ""

	for {
		matches := filterSessions(sessions, query)
		selected = clampSelection(selected, len(matches))
		render(a.out, backend.Location(), query, matches, selected, status)
		status = ""

		key, err := readKey(a.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("manager input closed")
			}
			return err
		}

		switch key {
		case "up":
			if selected > 0 {
				selected--
			}
		case "down":
			if selected+1 < len(matches) {
				selected++
			}
		case "enter":
			if len(matches) == 0 {
				status = "No match. Ctrl-N creates a session from the search text."
				continue
			}
			sessions, status, state, raw, err = a.attachAndRefresh(
				backend,
				matches[selected],
				restore,
			)
			if err != nil {
				return err
			}
			selected = 0
		case "ctrl-n":
			name := strings.TrimSpace(query)
			if name == "" {
				status = "Type a new session name, then press Ctrl-N."
				continue
			}
			sessions, status, state, raw, err = a.attachAndRefresh(backend, name, restore)
			if err != nil {
				return err
			}
			query = ""
			selected = 0
		case "ctrl-r":
			if sessions, err = backend.ListSessions(); err != nil {
				status = err.Error()
			} else {
				status = "Session list refreshed."
			}
			selected = 0
		case "ctrl-u":
			query = ""
			selected = 0
		case "backspace":
			if len(query) > 0 {
				query = query[:len(query)-1]
				selected = 0
			}
		case "q":
			if query == "" {
				if onQuit != nil {
					return onQuit()
				}
				return nil
			}
			query += key
		case "ctrl-c":
			if onQuit != nil {
				return onQuit()
			}
			return nil
		default:
			if len(key) == 1 && unicode.IsPrint(rune(key[0])) {
				query += key
				selected = 0
			}
		}
	}
}

func (a *App) attachAndRefresh(
	backend tuiBackend,
	name string,
	restore func(),
) (sessions []string, status string, state *term.State, raw bool, err error) {
	restore()
	attachErr := backend.Attach(name)
	sessions, listErr := backend.ListSessions()

	state, err = term.MakeRaw(int(a.in.Fd()))
	raw = err == nil
	if err != nil {
		return nil, "", state, raw, fmt.Errorf("restore manager terminal mode: %w", err)
	}
	if attachErr != nil {
		status = attachErr.Error()
	}
	if listErr != nil {
		status = listErr.Error()
	}
	return sessions, status, state, raw, nil
}

func (a *App) selectableSessions() ([]string, error) {
	all, err := a.sessions()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(all))
	for _, session := range all {
		if session != managerSession {
			result = append(result, session)
		}
	}
	return result, nil
}

func filterSessions(sessions []string, query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]string(nil), sessions...)
	}
	result := make([]string, 0, len(sessions))
	for _, session := range sessions {
		if strings.Contains(strings.ToLower(session), query) {
			result = append(result, session)
		}
	}
	return result
}

func clampSelection(selected, count int) int {
	if count == 0 || selected < 0 {
		return 0
	}
	if selected >= count {
		return count - 1
	}
	return selected
}

func render(out io.Writer, location, query string, sessions []string, selected int, status string) {
	fmt.Fprint(out, "\x1b[?25l\x1b[2J\x1b[H")
	fmt.Fprintln(out, "portal · zmx session switcher\r")
	fmt.Fprintf(out, "%s\r\n\r\n", location)
	fmt.Fprintf(out, "Search: %s_\r\n\r\n", query)
	if len(sessions) == 0 {
		fmt.Fprintln(out, "  No matching sessions.\r")
	} else {
		for index, session := range sessions {
			prefix := "  "
			if index == selected {
				prefix = "› "
			}
			fmt.Fprintf(out, "%s%s\r\n", prefix, session)
		}
	}
	if status != "" {
		fmt.Fprintf(out, "\r\n%s\r\n", status)
	}
	fmt.Fprintln(out, "\r\n↑/↓ select · Enter switch · Ctrl-N create · Ctrl-R refresh\r")
	fmt.Fprintln(out, "Ctrl-U clear search · q on empty search quit · Ctrl-\\ in a session returns here\r")
}

func readKey(in io.Reader) (string, error) {
	var first [1]byte
	if _, err := io.ReadFull(in, first[:]); err != nil {
		return "", err
	}
	switch first[0] {
	case '\r', '\n':
		return "enter", nil
	case 8, 127:
		return "backspace", nil
	case keyCtrlC:
		return "ctrl-c", nil
	case keyCtrlN:
		return "ctrl-n", nil
	case keyCtrlR:
		return "ctrl-r", nil
	case keyCtrlU:
		return "ctrl-u", nil
	case 27:
		var rest [2]byte
		if _, err := io.ReadFull(in, rest[:]); err != nil {
			return "", err
		}
		if rest[0] == '[' && rest[1] == 'A' {
			return "up", nil
		}
		if rest[0] == '[' && rest[1] == 'B' {
			return "down", nil
		}
		return "unknown", nil
	default:
		return string(first[0]), nil
	}
}
