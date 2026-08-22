package play

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	maxEvents     = 500
	pollEvery     = 400 * time.Millisecond
	terminalEvery = 2 * time.Second
	defaultBase   = "http://127.0.0.1:7332"
)

type Config struct {
	BaseURL string
	Client  *Client
	Input   *os.File
	Output  *os.File
}

func Run(ctx context.Context, config Config) error {
	base := config.BaseURL
	if base == "" {
		base = defaultBase
	}
	client := config.Client
	if client == nil {
		var err error
		client, err = NewClient(base, nil)
		if err != nil {
			return err
		}
	}
	model := newModel(ctx, client, base)
	options := []tea.ProgramOption{tea.WithContext(ctx), tea.WithAltScreen()}
	if config.Input != nil {
		options = append(options, tea.WithInput(config.Input))
	}
	if config.Output != nil {
		options = append(options, tea.WithOutput(config.Output))
	}
	_, err := tea.NewProgram(model, options...).Run()
	return err
}

type screenMode int

const (
	modeBrowse screenMode = iota
	modeCreate
	modePrompt
	modeAnswer
)

type model struct {
	ctx     context.Context
	client  *Client
	baseURL string
	width   int
	height  int

	threads    []Thread
	terminals  map[string]Terminal
	events     []Event
	requests   []PendingRequest
	selected   int
	cursor     uint64
	connected  bool
	everOnline bool
	lastError  string
	status     string
	mode       screenMode
	busy       bool

	createKind  string
	createAgent string
	createFocus int
	cwdInput    textinput.Model
	promptInput textarea.Model
	answerInput textinput.Model
	request     int
	option      int

	lastTerminalSync   time.Time
	preferredSelection string
}

type syncMsg struct {
	threads          []Thread
	terminals        []Terminal
	terminalsChecked bool
	terminalsOK      bool
	events           []Event
	requests         []PendingRequest
	requestFor       string
	requestsOK       bool
	warning          error
	err              error
}

type pollMsg struct{}

type actionMsg struct {
	message  string
	selectID string
	err      error
}

type terminalOpenedMsg struct {
	terminal Terminal
	err      error
}

func newModel(ctx context.Context, client *Client, baseURL string) model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cwdInput := textinput.New()
	cwdInput.Prompt = ""
	cwdInput.SetValue(cwd)
	cwdInput.CharLimit = 4096

	promptInput := textarea.New()
	promptInput.Placeholder = "Ask the agent…"
	promptInput.ShowLineNumbers = false
	promptInput.SetWidth(72)
	promptInput.SetHeight(8)

	answerInput := textinput.New()
	answerInput.Prompt = "> "
	answerInput.Placeholder = "Type an answer"
	answerInput.CharLimit = 4096

	return model{
		ctx: ctx, client: client, baseURL: baseURL,
		terminals: make(map[string]Terminal), createKind: "chat", createAgent: "claude",
		cwdInput: cwdInput, promptInput: promptInput, answerInput: answerInput,
	}
}

func (m model) Init() tea.Cmd {
	return m.syncCmd()
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.promptInput.SetWidth(max(30, min(78, message.Width-10)))
		return m, nil
	case syncMsg:
		m.busy = false
		if message.err != nil {
			m.connected = false
			m.lastError = message.err.Error()
			return m, schedulePoll()
		}
		wasOffline := m.everOnline && !m.connected
		selectedID := m.selectedThreadID()
		if m.preferredSelection != "" {
			selectedID = m.preferredSelection
		}
		m.threads = message.threads
		if message.terminalsChecked {
			m.lastTerminalSync = time.Now()
		}
		if message.terminalsOK {
			m.terminals = make(map[string]Terminal, len(message.terminals))
			for _, terminal := range message.terminals {
				m.terminals[terminal.ThreadID] = terminal
			}
		}
		m.restoreSelection(selectedID)
		m.preferredSelection = ""
		if message.requestsOK && message.requestFor == m.selectedThreadID() {
			m.requests = message.requests
		}
		caughtUp := m.appendEvents(message.events)
		m.connected = true
		m.everOnline = true
		m.lastError = ""
		if message.warning != nil {
			m.status = "partial refresh: " + message.warning.Error()
		}
		if wasOffline {
			m.status = fmt.Sprintf("reconnected • caught up %d event(s)", caughtUp)
		}
		return m, schedulePoll()
	case pollMsg:
		if m.busy {
			return m, schedulePoll()
		}
		return m, m.syncCmd()
	case actionMsg:
		m.busy = true
		if message.err != nil {
			m.status = "error: " + message.err.Error()
		} else {
			m.status = message.message
			if message.selectID != "" {
				m.preferredSelection = message.selectID
			}
		}
		return m, m.syncCmd()
	case terminalOpenedMsg:
		m.busy = false
		if message.err != nil {
			m.status = "error: " + message.err.Error()
			return m, m.syncCmd()
		}
		m.status = "zmx session ready; in another terminal: make attach TERMINAL=" + message.terminal.ID
		return m, m.syncCmd()
	}

	key, ok := message.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch m.mode {
	case modeCreate:
		return m.updateCreate(key)
	case modePrompt:
		return m.updatePrompt(key)
	case modeAnswer:
		return m.updateAnswer(key)
	default:
		return m.updateBrowse(key)
	}
}

func (m model) updateBrowse(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.requests = nil
			return m, m.syncCmd()
		}
	case "down", "j":
		if m.selected+1 < len(m.threads) {
			m.selected++
			m.requests = nil
			return m, m.syncCmd()
		}
	case "n":
		m.mode = modeCreate
		m.createFocus = 0
		m.cwdInput.Blur()
		m.status = ""
	case "p":
		thread := m.selectedThread()
		if thread == nil || thread.Kind != "chat" {
			m.status = "select a chat thread to send a prompt"
			break
		}
		if thread.ActiveTurn != nil {
			m.status = "the selected thread already has a foreground turn"
			break
		}
		m.mode = modePrompt
		m.promptInput.Reset()
		m.promptInput.Focus()
		return m, textarea.Blink
	case "a":
		if len(m.requests) == 0 {
			m.status = "the selected thread has no pending requests"
			break
		}
		m.mode = modeAnswer
		m.request, m.option = 0, 0
		m.answerInput.SetValue("")
		if len(m.requests[0].Options) == 0 {
			m.answerInput.Focus()
			return m, textinput.Blink
		}
	case "i":
		thread := m.selectedThread()
		if thread == nil || thread.ActiveTurn == nil {
			m.status = "the selected thread has no foreground turn"
			break
		}
		m.busy = true
		return m, m.interruptCmd(thread.ID, thread.ActiveTurn.ID)
	case "enter", "o":
		thread := m.selectedThread()
		if thread == nil || thread.Kind != "tui" {
			m.status = "select a TUI thread to open its terminal"
			break
		}
		m.busy = true
		return m, m.openTerminalCmd(thread.ID)
	case "r":
		m.busy = true
		return m, m.syncCmd()
	}
	return m, nil
}

func (m model) updateCreate(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.mode = modeBrowse
		m.cwdInput.Blur()
		return m, nil
	case "tab", "shift+tab":
		delta := 1
		if key.String() == "shift+tab" {
			delta = -1
		}
		m.createFocus = (m.createFocus + delta + 3) % 3
		if m.createFocus == 2 {
			m.cwdInput.Focus()
			return m, textinput.Blink
		}
		m.cwdInput.Blur()
		return m, nil
	case "left", "right", "h", "l":
		if m.createFocus == 0 {
			m.createKind = toggle(m.createKind, "chat", "tui")
		}
		if m.createFocus == 1 {
			m.createAgent = toggle(m.createAgent, "claude", "codex")
		}
		return m, nil
	case "enter":
		cwd := strings.TrimSpace(m.cwdInput.Value())
		if cwd == "" {
			m.status = "working directory is required"
			return m, nil
		}
		m.mode = modeBrowse
		m.cwdInput.Blur()
		m.busy = true
		return m, m.createThreadCmd(m.createKind, m.createAgent, cwd)
	}
	if m.createFocus == 2 {
		var command tea.Cmd
		m.cwdInput, command = m.cwdInput.Update(key)
		return m, command
	}
	return m, nil
}

func (m model) updatePrompt(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.mode = modeBrowse
		m.promptInput.Blur()
		return m, nil
	case "ctrl+s":
		thread := m.selectedThread()
		text := strings.TrimSpace(m.promptInput.Value())
		if thread == nil || thread.Kind != "chat" {
			m.mode = modeBrowse
			m.status = "selected chat thread disappeared"
			return m, nil
		}
		if text == "" {
			m.status = "prompt text is required"
			return m, nil
		}
		m.mode = modeBrowse
		m.promptInput.Blur()
		m.busy = true
		return m, m.promptCmd(thread.ID, text)
	}
	var command tea.Cmd
	m.promptInput, command = m.promptInput.Update(key)
	return m, command
}

func (m model) updateAnswer(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.requests) == 0 {
		m.mode = modeBrowse
		return m, nil
	}
	request := m.requests[m.request]
	switch key.String() {
	case "esc":
		m.mode = modeBrowse
		m.answerInput.Blur()
		return m, nil
	case "tab", "shift+tab":
		delta := 1
		if key.String() == "shift+tab" {
			delta = -1
		}
		m.request = (m.request + delta + len(m.requests)) % len(m.requests)
		m.option = 0
		m.answerInput.SetValue("")
		if len(m.requests[m.request].Options) == 0 {
			m.answerInput.Focus()
			return m, textinput.Blink
		}
		m.answerInput.Blur()
		return m, nil
	case "left", "up", "h", "k":
		if len(request.Options) > 0 {
			m.option = (m.option - 1 + len(request.Options)) % len(request.Options)
		}
		return m, nil
	case "right", "down", "j", "l":
		if len(request.Options) > 0 {
			m.option = (m.option + 1) % len(request.Options)
		}
		return m, nil
	case "enter":
		optionID := ""
		answer := strings.TrimSpace(m.answerInput.Value())
		if len(request.Options) > 0 {
			optionID = request.Options[m.option].ID
		} else if answer == "" {
			m.status = "answer text is required"
			return m, nil
		}
		m.mode = modeBrowse
		m.answerInput.Blur()
		m.busy = true
		return m, m.answerCmd(request.ThreadID, request.ID, optionID, answer)
	}
	if len(request.Options) == 0 {
		var command tea.Cmd
		m.answerInput, command = m.answerInput.Update(key)
		return m, command
	}
	return m, nil
}

func (m model) View() string {
	if m.mode == modeCreate {
		return m.createView()
	}
	if m.mode == modePrompt {
		return m.promptView()
	}
	if m.mode == modeAnswer {
		return m.answerView()
	}
	return m.browseView()
}

func (m model) browseView() string {
	width := max(72, m.width)
	height := max(18, m.height)
	leftWidth := min(48, max(32, width/3))
	rightWidth := max(36, width-leftWidth-3)
	bodyHeight := max(8, height-5)

	connection := successStyle.Render("connected")
	if !m.connected {
		connection = warningStyle.Render("reconnecting")
	}
	header := titleStyle.Render("ATC unified play") + "  " + mutedStyle.Render(m.baseURL) + "  " + connection
	if m.busy {
		header += "  " + accentStyle.Render("syncing")
	}

	left := m.threadList(leftWidth, bodyHeight)
	right := m.threadDetail(rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, panelStyle.Width(leftWidth).Height(bodyHeight).Render(left), " ", panelStyle.Width(rightWidth).Height(bodyHeight).Render(right))
	status := m.status
	if status == "" && m.lastError != "" {
		status = "server unavailable: " + m.lastError
	}
	footer := mutedStyle.Render("↑/↓ select  n new  p ACP prompt  a answer  i interrupt  enter/o create zmx session  r refresh  q quit")
	if status != "" {
		footer = truncate(status, width) + "\n" + footer
	}
	return header + "\n" + body + "\n" + footer
}

func (m model) threadList(width, height int) string {
	lines := []string{sectionStyle.Render(fmt.Sprintf("Threads (%d)", len(m.threads)))}
	if len(m.threads) == 0 {
		lines = append(lines, "", mutedStyle.Render("No threads. Press n to create one."))
		return strings.Join(lines, "\n")
	}
	perThread := 3
	visible := max(1, (height-2)/perThread)
	start := max(0, m.selected-visible+1)
	for index := start; index < len(m.threads) && index < start+visible; index++ {
		thread := m.threads[index]
		marker := "  "
		style := lipgloss.NewStyle()
		if index == m.selected {
			marker = accentStyle.Render("› ")
			style = selectedStyle
		}
		identity := fmt.Sprintf("%s%s/%s  %s", marker, kindLabel(thread.Kind), thread.Agent, shortID(thread.ID))
		lifecycle := threadLifecycle(thread, m.terminals[thread.ID])
		cwd := filepath.Base(thread.CWD)
		lines = append(lines, style.Render(truncate(identity, width-2)), "  "+truncate(lifecycle, width-4), mutedStyle.Render("  "+truncate(cwd, width-4)))
	}
	return strings.Join(lines, "\n")
}

func (m model) threadDetail(width, height int) string {
	thread := m.selectedThread()
	if thread == nil {
		return sectionStyle.Render("Activity") + "\n\n" + mutedStyle.Render("Select or create a thread.")
	}
	terminal := m.terminals[thread.ID]
	lines := []string{
		sectionStyle.Render(thread.Agent + " " + kindLabel(thread.Kind) + " • " + shortID(thread.ID)),
		truncate(thread.CWD, width-2),
		threadLifecycle(*thread, terminal),
	}
	if terminal.ID != "" {
		detail := "terminal " + shortID(terminal.ID) + " • " + terminal.Lifecycle
		if terminal.Reachable {
			detail += " • reachable"
		} else {
			detail += " • unreachable"
		}
		if terminal.Reason != "" {
			detail += " • " + terminal.Reason
		}
		lines = append(lines, truncate(detail, width-2))
		if terminal.Reachable {
			lines = append(lines, truncate("attach elsewhere: make attach TERMINAL="+terminal.ID, width-2))
		}
	}
	if thread.LastTurn != nil {
		last := "last turn • " + thread.LastTurn.Outcome
		if thread.LastTurn.Error != "" {
			last += " • " + thread.LastTurn.Error
		}
		lines = append(lines, truncate(last, width-2))
	}
	if len(m.requests) > 0 {
		lines = append(lines, warningStyle.Render(fmt.Sprintf("%d pending request(s) • press a", len(m.requests))))
	}
	lines = append(lines, "", sectionStyle.Render("Normalized events"))
	events := m.eventsForThread(thread.ID)
	available := max(2, height-len(lines)-1)
	if len(events) == 0 {
		lines = append(lines, mutedStyle.Render("No events received for this thread."))
		return strings.Join(lines, "\n")
	}
	start := max(0, len(events)-available)
	for _, event := range events[start:] {
		lines = append(lines, truncate(formatEvent(event), width-2))
	}
	return strings.Join(lines, "\n")
}

func (m model) createView() string {
	fields := []string{
		choiceLine("Experiment", kindLabel(m.createKind), m.createFocus == 0),
		choiceLine("Agent", m.createAgent, m.createFocus == 1),
		fieldLabel("Working directory", m.createFocus == 2) + "\n" + inputStyle.Render(m.cwdInput.View()),
	}
	content := titleStyle.Render("Create Thread") + "\n\n" + strings.Join(fields, "\n\n") + "\n\n" + mutedStyle.Render("tab selects • ←/→ changes • enter creates • esc cancels")
	return modalStyle.Width(min(82, max(50, m.width-8))).Render(content)
}

func (m model) promptView() string {
	thread := m.selectedThread()
	identity := "chat thread"
	if thread != nil {
		identity = thread.Agent + " chat • " + shortID(thread.ID)
	}
	content := titleStyle.Render("Send Prompt") + "  " + mutedStyle.Render(identity) + "\n\n" + m.promptInput.View() + "\n" + mutedStyle.Render("ctrl+s sends • esc cancels")
	return modalStyle.Width(min(88, max(50, m.width-8))).Render(content)
}

func (m model) answerView() string {
	if len(m.requests) == 0 {
		return "No pending requests"
	}
	request := m.requests[m.request]
	content := titleStyle.Render(fmt.Sprintf("Answer %s %d/%d", request.Kind, m.request+1, len(m.requests))) + "\n\n" + request.Prompt + "\n\n"
	if len(request.Options) > 0 {
		options := make([]string, 0, len(request.Options))
		for index, option := range request.Options {
			label := option.Label
			if index == m.option {
				label = selectedStyle.Render("› " + label)
			} else {
				label = "  " + label
			}
			options = append(options, label)
		}
		content += strings.Join(options, "\n") + "\n\n" + mutedStyle.Render("↑/↓ chooses • enter answers • tab changes request • esc cancels")
	} else {
		content += inputStyle.Render(m.answerInput.View()) + "\n\n" + mutedStyle.Render("enter answers • tab changes request • esc cancels")
	}
	return modalStyle.Width(min(88, max(50, m.width-8))).Render(content)
}

func (m model) syncCmd() tea.Cmd {
	cursor := m.cursor
	selectedID := m.selectedThreadID()
	if m.preferredSelection != "" {
		selectedID = m.preferredSelection
	}
	terminalDue := time.Since(m.lastTerminalSync) >= terminalEvery
	return func() tea.Msg {
		threads, err := m.client.Threads(m.ctx)
		if err != nil {
			return syncMsg{err: err}
		}
		var terminals []Terminal
		terminalsOK := false
		var warning error
		if terminalDue {
			terminals, err = m.client.Terminals(m.ctx)
			if err != nil {
				warning = fmt.Errorf("terminal state unavailable: %w", err)
			} else {
				terminalsOK = true
			}
		}
		events, err := m.client.Events(m.ctx, cursor)
		if err != nil {
			return syncMsg{err: err}
		}
		selected := findThread(threads, selectedID)
		if selected == nil && len(threads) > 0 {
			selected = &threads[0]
		}
		var requests []PendingRequest
		requestsOK := false
		requestFor := ""
		if selected != nil && selected.Kind == "chat" {
			requestFor = selected.ID
			requests, err = m.client.Requests(m.ctx, selected.ID)
			if err != nil {
				warning = errors.Join(warning, fmt.Errorf("requests unavailable: %w", err))
			} else {
				requestsOK = true
			}
		}
		return syncMsg{
			threads: threads, terminals: terminals, terminalsChecked: terminalDue, terminalsOK: terminalsOK,
			events: events, requests: requests, requestFor: requestFor, requestsOK: requestsOK,
			warning: warning,
		}
	}
}

func (m model) createThreadCmd(kind, agent, cwd string) tea.Cmd {
	return func() tea.Msg {
		thread, err := m.client.CreateThread(m.ctx, kind, agent, cwd)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{
			message:  "created " + thread.Agent + " " + thread.Kind + " " + shortID(thread.ID),
			selectID: thread.ID,
		}
	}
}

func (m model) promptCmd(threadID, prompt string) tea.Cmd {
	return func() tea.Msg {
		turn, err := m.client.Prompt(m.ctx, threadID, prompt)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "started foreground turn " + shortID(turn.ID)}
	}
}

func (m model) answerCmd(threadID, requestID, optionID, answer string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.Answer(m.ctx, threadID, requestID, optionID, answer)
		return actionMsg{message: "answered request " + shortID(requestID), err: err}
	}
}

func (m model) interruptCmd(threadID, turnID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.Interrupt(m.ctx, threadID, turnID)
		return actionMsg{message: "requested foreground interruption; background work may continue", err: err}
	}
}

func (m model) openTerminalCmd(threadID string) tea.Cmd {
	return func() tea.Msg {
		terminal, err := m.client.OpenTerminal(m.ctx, threadID)
		return terminalOpenedMsg{terminal: terminal, err: err}
	}
}

func schedulePoll() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m *model) appendEvents(events []Event) int {
	appended := 0
	for _, event := range events {
		if event.Sequence <= m.cursor {
			continue
		}
		m.events = append(m.events, event)
		m.cursor = event.Sequence
		appended++
	}
	if len(m.events) > maxEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-maxEvents:]...)
	}
	return appended
}

func (m *model) restoreSelection(id string) {
	if len(m.threads) == 0 {
		m.selected = 0
		m.requests = nil
		return
	}
	for index := range m.threads {
		if m.threads[index].ID == id {
			m.selected = index
			return
		}
	}
	m.selected = min(m.selected, len(m.threads)-1)
}

func (m model) selectedThread() *Thread {
	if m.selected < 0 || m.selected >= len(m.threads) {
		return nil
	}
	return &m.threads[m.selected]
}

func (m model) selectedThreadID() string {
	thread := m.selectedThread()
	if thread == nil {
		return ""
	}
	return thread.ID
}

func (m model) eventsForThread(threadID string) []Event {
	result := make([]Event, 0)
	for _, event := range m.events {
		if event.ThreadID == threadID {
			result = append(result, event)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}

func findThread(threads []Thread, id string) *Thread {
	for index := range threads {
		if threads[index].ID == id {
			return &threads[index]
		}
	}
	return nil
}

func threadLifecycle(thread Thread, terminal Terminal) string {
	parts := []string{thread.Activity}
	if thread.ActiveTurn != nil {
		parts = append(parts, "foreground:"+thread.ActiveTurn.State)
	} else {
		parts = append(parts, "foreground:none")
	}
	parts = append(parts, "background:"+thread.BackgroundActivity)
	if thread.PendingRequestCount > 0 {
		parts = append(parts, fmt.Sprintf("pending:%d", thread.PendingRequestCount))
	} else {
		parts = append(parts, "pending:0")
	}
	if thread.Kind == "tui" {
		terminalState := "terminal:none"
		if terminal.ID != "" {
			terminalState = "terminal:" + terminal.Lifecycle
			if !terminal.Reachable {
				terminalState += "/unreachable"
			}
		}
		parts = append(parts, terminalState)
	}
	return strings.Join(parts, " • ")
}

func formatEvent(event Event) string {
	detail := event.Text
	if event.Activity != "" {
		detail = event.Activity
	}
	if event.Turn != nil {
		detail = event.Turn.State
		if event.Turn.Outcome != "" {
			detail += "/" + event.Turn.Outcome
		}
		if event.Turn.Error != "" {
			detail += " • " + event.Turn.Error
		}
	}
	if event.Request != nil {
		detail = event.Request.Kind + " • " + event.Request.Prompt
	}
	if event.Terminal != nil {
		detail = event.Terminal.Lifecycle
		if event.Terminal.Reachable {
			detail += "/reachable"
		} else {
			detail += "/unreachable"
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	line := fmt.Sprintf("%d  %s", event.Sequence, event.Type)
	if detail != "" {
		line += "  " + detail
	}
	return line
}

func choiceLine(label, value string, focused bool) string {
	return fieldLabel(label, focused) + "  " + selectedStyle.Render("[ ‹ "+value+" › ]")
}

func fieldLabel(label string, focused bool) string {
	if focused {
		return accentStyle.Render("› " + label)
	}
	return "  " + label
}

func toggle(value, first, second string) string {
	if value == first {
		return second
	}
	return first
}

func kindLabel(kind string) string {
	if kind == "chat" {
		return "ACP"
	}
	if kind == "tui" {
		return "TUI/zmx"
	}
	return kind
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	sectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75"))
	accentStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
	modalStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	inputStyle    = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
)
