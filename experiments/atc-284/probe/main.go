// Command probe provides the narrow app-server probes needed by ATC-284.
// It connects to the user's existing Codex control socket and never starts,
// stops, or configures the shared server.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const usage = `Usage:
  ./scripts/atc-284 doctor
  ./scripts/atc-284 listen
  ./scripts/atc-284 bind [directory]
  ./scripts/atc-284 create [directory]
  ./scripts/atc-284 rollout THREAD_ID

Options:
  --socket PATH  Override the Codex control socket (for isolated testing).

Commands:
  doctor  Confirm that the shared socket accepts a local client.
  listen  Print thread/started and thread/status/changed until Ctrl-C.
  bind    Launch plain codex and test cwd-and-timing terminal binding.
  create  Create an empty thread and print only its thread ID.
  rollout Find local rollout files for a thread ID.`

const bindingWindow = 5 * time.Second

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type client struct {
	websocket *webSocket
	nextID    int
}

type webSocket struct {
	conn   net.Conn
	reader *bufio.Reader
}

type startedThread struct {
	ID         string
	CWD        string
	ObservedAt time.Time
}

type bindingCandidate struct {
	ThreadID string
	CWD      string
	Elapsed  time.Duration
}

type bindingResult struct {
	Bound        bool
	Candidates   []bindingCandidate
	ObserveError string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "atc-284:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("atc-284", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socketOverride := flags.String("socket", "", "Codex app-server control socket")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	remaining := flags.Args()
	if len(remaining) == 0 || remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h" {
		fmt.Println(usage)
		return nil
	}
	if remaining[0] == "rollout" {
		if len(remaining) != 2 {
			return errors.New("rollout requires one thread ID")
		}
		return findRollouts(remaining[1])
	}

	socketPath, err := resolveSocket(*socketOverride)
	if err != nil {
		return err
	}
	c, err := connect(socketPath)
	if err != nil {
		return fmt.Errorf("connect to %s: %w\nStart or reconnect Codex Desktop, then try again", socketPath, err)
	}
	defer func() { _ = c.websocket.conn.Close() }()
	if err := c.initialize(); err != nil {
		return err
	}

	switch remaining[0] {
	case "doctor":
		if len(remaining) != 1 {
			return errors.New("doctor takes no arguments")
		}
		fmt.Printf("PASS: shared app-server accepted a local client at %s\n", socketPath)
		return nil
	case "listen":
		if len(remaining) != 1 {
			return errors.New("listen takes no arguments")
		}
		return c.listen()
	case "bind":
		if len(remaining) > 2 {
			return errors.New("bind accepts at most one directory")
		}
		directory := "."
		if len(remaining) == 2 {
			directory = remaining[1]
		}
		return c.bindLaunch(directory)
	case "create":
		if len(remaining) > 2 {
			return errors.New("create accepts at most one directory")
		}
		directory := "."
		if len(remaining) == 2 {
			directory = remaining[1]
		}
		return c.createThread(directory)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", remaining[0], usage)
	}
}

func resolveSocket(override string) (string, error) {
	if override != "" {
		return strings.TrimPrefix(override, "unix://"), nil
	}
	codexHome, err := resolveCodexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock"), nil
}

func resolveCodexHome() (string, error) {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		codexHome = filepath.Join(userHome, ".codex")
	}
	absoluteHome, err := filepath.Abs(codexHome)
	if err != nil {
		return "", fmt.Errorf("resolve CODEX_HOME: %w", err)
	}
	return absoluteHome, nil
}

func connect(socketPath string) (*client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	websocket, err := upgrade(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &client{websocket: websocket}, nil
}

func (c *client) initialize() error {
	_, err := c.request("initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "atc_284_probe",
			"title":   "ATC-284 shared app-server probe",
			"version": "0.1.0",
		},
		"capabilities": map[string]bool{"experimentalApi": true},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.send(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (c *client) request(method string, params map[string]any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}
	for {
		payload, err := c.websocket.readText()
		if err != nil {
			return nil, err
		}
		var incoming message
		if err := json.Unmarshal(payload, &incoming); err != nil {
			return nil, fmt.Errorf("decode app-server response: %w", err)
		}
		if len(incoming.ID) > 0 && incoming.Method != "" {
			if err := c.send(map[string]any{
				"id": incoming.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "ATC-284 probe does not handle server requests",
				},
			}); err != nil {
				return nil, fmt.Errorf("reject server request %s: %w", incoming.Method, err)
			}
			continue
		}
		if !sameID(incoming.ID, id) || incoming.Method != "" {
			continue
		}
		if incoming.Error != nil {
			return nil, fmt.Errorf("%s failed (%d): %s", method, incoming.Error.Code, incoming.Error.Message)
		}
		return incoming.Result, nil
	}
}

func (c *client) send(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.websocket.writeFrame(0x1, payload)
}

func sameID(raw json.RawMessage, want int) bool {
	var got int
	if json.Unmarshal(raw, &got) == nil {
		return got == want
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text == fmt.Sprint(want)
}

func (c *client) listen() error {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	go func() {
		<-interrupts
		_ = c.websocket.conn.Close()
	}()

	fmt.Fprintln(os.Stderr, "Listening for thread/started and thread/status/changed. Press Ctrl-C to stop.")
	for {
		payload, err := c.websocket.readText()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var incoming message
		if err := json.Unmarshal(payload, &incoming); err != nil {
			fmt.Fprintf(os.Stderr, "Ignored invalid event: %v\n", err)
			continue
		}
		switch incoming.Method {
		case "thread/started":
			printThreadStarted(incoming.Params)
		case "thread/status/changed":
			printStatusChanged(incoming.Params)
		}
	}
}

func (c *client) bindLaunch(directory string) error {
	absoluteDirectory, err := resolveDirectory(directory)
	if err != nil {
		return err
	}
	reportPath := filepath.Join(os.TempDir(), fmt.Sprintf("atc-284-bind-%d.log", os.Getpid()))
	fmt.Fprintf(os.Stderr, "ATC-284 binding probe: launching plain codex in %s\n", absoluteDirectory)
	fmt.Fprintf(os.Stderr, "Wait %s before the first prompt. Evidence: %s\n", bindingWindow+time.Second, reportPath)

	events := make(chan startedThread)
	readErrors := make(chan error, 1)
	go c.readStartedThreads(events, readErrors)

	command := exec.Command("codex")
	command.Dir = absoluteDirectory
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = os.Environ()

	startedAt := time.Now()
	if err := command.Start(); err != nil {
		return fmt.Errorf("launch plain codex: %w", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()

	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	drainDone := make(chan struct{})
	defer close(drainDone)
	go func() {
		for {
			select {
			case <-interrupts:
				// The foreground Codex child receives terminal signals directly.
			case <-drainDone:
				return
			}
		}
	}()

	result := observeBinding(absoluteDirectory, startedAt, bindingWindow, events, readErrors)
	_ = c.websocket.conn.Close()
	report := formatBindingResult(absoluteDirectory, bindingWindow, result)
	if err := os.WriteFile(reportPath, []byte(report), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Could not save binding evidence: %v\n", err)
	}

	childErr := <-childDone
	fmt.Printf("\n%s", report)
	fmt.Printf("Evidence saved to %s\n", reportPath)
	if childErr != nil {
		fmt.Printf("Codex exited with: %v\n", childErr)
	}
	if !result.Bound {
		return errors.New("terminal left unbound; see binding result above")
	}
	return nil
}

func (c *client) readStartedThreads(events chan<- startedThread, readErrors chan<- error) {
	defer close(events)
	for {
		payload, err := c.websocket.readText()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				readErrors <- err
			}
			return
		}
		var incoming message
		if err := json.Unmarshal(payload, &incoming); err != nil {
			continue
		}
		if len(incoming.ID) > 0 && incoming.Method != "" {
			_ = c.send(map[string]any{
				"id": incoming.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "ATC-284 probe does not handle server requests",
				},
			})
			continue
		}
		if incoming.Method != "thread/started" {
			continue
		}
		started, ok := decodeStartedThread(incoming.Params)
		if !ok {
			continue
		}
		started.ObservedAt = time.Now()
		events <- started
	}
}

func observeBinding(directory string, startedAt time.Time, window time.Duration, events <-chan startedThread, readErrors <-chan error) bindingResult {
	timer := time.NewTimer(window)
	defer timer.Stop()
	result := bindingResult{}
	for {
		select {
		case started, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if started.ObservedAt.Before(startedAt) || !sameDirectory(directory, started.CWD) {
				continue
			}
			result.Candidates = append(result.Candidates, bindingCandidate{
				ThreadID: started.ID,
				CWD:      started.CWD,
				Elapsed:  started.ObservedAt.Sub(startedAt),
			})
		case err := <-readErrors:
			if err != nil {
				result.ObserveError = err.Error()
			}
			result.Bound = false
			return result
		case <-timer.C:
			result.Bound = len(result.Candidates) == 1 && result.ObserveError == ""
			return result
		}
	}
}

func formatBindingResult(directory string, window time.Duration, result bindingResult) string {
	var output strings.Builder
	fmt.Fprintln(&output, "ATC-284 launch-then-observe result")
	fmt.Fprintf(&output, "cwd: %s\n", directory)
	fmt.Fprintf(&output, "window: %s\n", window)
	for i, candidate := range result.Candidates {
		fmt.Fprintf(&output, "candidate %d: thread=%s elapsed=%s cwd=%s\n",
			i+1, candidate.ThreadID, candidate.Elapsed.Round(time.Millisecond), candidate.CWD)
	}
	switch {
	case result.ObserveError != "":
		fmt.Fprintf(&output, "FAIL CLOSED: observer error: %s\n", result.ObserveError)
	case len(result.Candidates) == 0:
		fmt.Fprintln(&output, "FAIL CLOSED: no matching thread/started")
	case len(result.Candidates) > 1:
		fmt.Fprintf(&output, "FAIL CLOSED: %d matching thread/started events\n", len(result.Candidates))
	default:
		fmt.Fprintf(&output, "PASS: bound thread=%s\n", result.Candidates[0].ThreadID)
	}
	return output.String()
}

func decodeStartedThread(raw json.RawMessage) (startedThread, bool) {
	var params struct {
		Thread struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Thread.ID == "" || params.Thread.CWD == "" {
		return startedThread{}, false
	}
	return startedThread{ID: params.Thread.ID, CWD: params.Thread.CWD}, true
}

func sameDirectory(want, got string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(absolute)
	}
	return canonical(want) == canonical(got)
}

func upgrade(conn net.Conn) (*webSocket, error) {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("create websocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := "GET / HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, fmt.Errorf("send websocket upgrade: %w", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("read websocket upgrade: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("websocket upgrade returned %s", response.Status)
	}
	wantAccept := websocketAccept(key)
	if got := response.Header.Get("Sec-WebSocket-Accept"); got != wantAccept {
		return nil, fmt.Errorf("websocket upgrade returned invalid accept header")
	}
	return &webSocket{conn: conn, reader: reader}, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (w *webSocket) writeFrame(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header = append(header, mask...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%len(mask)]
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	_, err := w.conn.Write(masked)
	return err
}

func (w *webSocket) readText() ([]byte, error) {
	var message []byte
	for {
		opcode, final, payload, err := w.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x0:
			if message == nil {
				return nil, errors.New("unexpected websocket continuation frame")
			}
			message = append(message, payload...)
			if final {
				return message, nil
			}
		case 0x1:
			message = append(message[:0], payload...)
			if final {
				return message, nil
			}
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := w.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA:
		default:
			return nil, fmt.Errorf("unexpected websocket opcode %d", opcode)
		}
	}
}

func (w *webSocket) readFrame() (opcode byte, final bool, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(w.reader, header); err != nil {
		return 0, false, nil, err
	}
	final = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(w.reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(w.reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}
	if length > 8*1024*1024 {
		return 0, false, nil, fmt.Errorf("websocket frame is too large: %d bytes", length)
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(w.reader, mask); err != nil {
			return 0, false, nil, err
		}
	}
	payload = make([]byte, int(length))
	if _, err := io.ReadFull(w.reader, payload); err != nil {
		return 0, false, nil, err
	}
	for i := range payload {
		if masked {
			payload[i] ^= mask[i%len(mask)]
		}
	}
	return opcode, final, payload, nil
}

func printThreadStarted(raw json.RawMessage) {
	var params struct {
		Thread struct {
			ID     string         `json:"id"`
			CWD    string         `json:"cwd"`
			Status map[string]any `json:"status"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	fmt.Printf("%s  thread/started         thread=%s  cwd=%s  status=%s\n",
		timestamp(), params.Thread.ID, params.Thread.CWD, statusType(params.Thread.Status))
}

func printStatusChanged(raw json.RawMessage) {
	var params struct {
		ThreadID string         `json:"threadId"`
		Status   map[string]any `json:"status"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	flags := activeFlags(params.Status)
	suffix := ""
	if len(flags) > 0 {
		suffix = "  flags=" + strings.Join(flags, ",")
	}
	fmt.Printf("%s  thread/status/changed  thread=%s  status=%s%s\n",
		timestamp(), params.ThreadID, statusType(params.Status), suffix)
}

func statusType(status map[string]any) string {
	value, _ := status["type"].(string)
	if value == "" {
		return "unknown"
	}
	return value
}

func activeFlags(status map[string]any) []string {
	values, _ := status["activeFlags"].([]any)
	flags := make([]string, 0, len(values))
	for _, value := range values {
		if flag, ok := value.(string); ok {
			flags = append(flags, flag)
		}
	}
	sort.Strings(flags)
	return flags
}

func timestamp() string {
	return time.Now().Format("15:04:05.000")
}

func (c *client) createThread(directory string) error {
	absoluteDirectory, err := resolveDirectory(directory)
	if err != nil {
		return err
	}
	result, err := c.request("thread/start", map[string]any{"cwd": absoluteDirectory})
	if err != nil {
		return err
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &started); err != nil {
		return fmt.Errorf("decode thread/start result: %w", err)
	}
	if started.Thread.ID == "" {
		return errors.New("thread/start returned no thread ID")
	}
	fmt.Println(started.Thread.ID)
	return nil
}

func resolveDirectory(directory string) (string, error) {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	info, err := os.Stat(absoluteDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", absoluteDirectory)
	}
	return absoluteDirectory, nil
}

func findRollouts(threadID string) error {
	if strings.TrimSpace(threadID) == "" || strings.ContainsAny(threadID, `/\\`) {
		return errors.New("thread ID must be a non-empty filename-safe value")
	}
	codexHome, err := resolveCodexHome()
	if err != nil {
		return err
	}
	sessionsDirectory := filepath.Join(codexHome, "sessions")
	var matches []string
	err = filepath.WalkDir(sessionsDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), threadID) && strings.HasSuffix(entry.Name(), ".jsonl") {
			matches = append(matches, path)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		fmt.Printf("MISSING: no sessions directory at %s\n", sessionsDirectory)
		return nil
	}
	if err != nil {
		return fmt.Errorf("search rollout files: %w", err)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		fmt.Printf("MISSING: no rollout file found for thread %s\n", threadID)
		return nil
	}
	for _, match := range matches {
		fmt.Println(match)
	}
	return nil
}
